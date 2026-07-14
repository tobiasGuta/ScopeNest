package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

type Store struct {
	root        string
	metaPath    string
	lockPath    string
	lockTimeout time.Duration
	mu          sync.Mutex
	// FaultInject is used by durability tests to simulate an interrupted write.
	FaultInject func(point string) error
}

const CurrentVersion = 2

type ReferenceIssue struct {
	Kind        string `json:"kind"`
	OwnerID     string `json:"ownerId"`
	ReferenceID string `json:"referenceId"`
}

func New(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve application data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "containers"), 0700); err != nil {
		return nil, fmt.Errorf("create application data directory: %w", err)
	}
	return &Store{
		root:        abs,
		metaPath:    filepath.Join(abs, "containers.json"),
		lockPath:    filepath.Join(abs, "containers.lock"),
		lockTimeout: 5 * time.Second,
	}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) ProfilePath(id string) (string, error) {
	if err := security.ValidateID(id); err != nil {
		return "", err
	}
	return security.ResolveWithin(s.root, filepath.Join(s.root, "containers", id, "profile"))
}

func (s *Store) Load() (model.Database, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireFileLock(s.lockPath, s.lockTimeout)
	if err != nil {
		return model.Database{}, err
	}
	defer lock.Release()
	return s.loadUnlocked()
}

func (s *Store) loadUnlocked() (model.Database, error) {
	db := model.Database{Version: CurrentVersion, Containers: []model.Container{}, ProxyProfiles: []model.ProxyProfile{}, Certificates: []model.Certificate{}, EnvironmentTemplates: []model.EnvironmentTemplate{}}
	data, err := os.ReadFile(s.metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return db, nil
	}
	if err != nil {
		return db, fmt.Errorf("read container metadata: %w", err)
	}

	// Fast parse to check version
	var peek struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return db, fmt.Errorf("decode container metadata: %w", err)
	}

	if peek.Version < 1 || peek.Version > CurrentVersion {
		return db, fmt.Errorf("unsupported metadata version %d", peek.Version)
	}

	if err := json.Unmarshal(data, &db); err != nil {
		return db, fmt.Errorf("decode container metadata: %w", err)
	}

	if db.Containers == nil {
		db.Containers = []model.Container{}
	}
	if db.ProxyProfiles == nil {
		db.ProxyProfiles = []model.ProxyProfile{}
	}
	if db.Certificates == nil {
		db.Certificates = []model.Certificate{}
	}
	if db.EnvironmentTemplates == nil {
		db.EnvironmentTemplates = []model.EnvironmentTemplate{}
	}

	for i := range db.Containers {
		container := &db.Containers[i]

		// V1 -> V2 Migration for in-memory
		if peek.Version == 1 {
			if container.NetworkMode == "" {
				container.NetworkMode = "direct"
			}
		}

		switch container.State {
		case "":
			if container.Running {
				container.State = model.StateRunning
			} else {
				container.State = model.StateStopped
			}
		case model.StateStopped:
			container.Running = false
		case model.StateLaunching:
			container.Running = false
		case model.StateRunning:
			container.Running = true
		default:
			return db, fmt.Errorf("container %q has invalid lifecycle state %q", container.ID, container.State)
		}
	}

	for i := range db.ProxyProfiles {
		if db.ProxyProfiles[i].CertificateIDs == nil {
			db.ProxyProfiles[i].CertificateIDs = []string{}
		}
	}

	for i := range db.Certificates {
		certificate := &db.Certificates[i]
		if certificate.TrustState == "" {
			if certificate.Trusted {
				certificate.TrustState = model.CertificateTrustTrusted
			} else {
				certificate.TrustState = model.CertificateTrustUntrusted
			}
		}
		ack := certificate.ManualTrustAcknowledgment
		if ack != nil && (ack.CertificateID != certificate.ID || ack.SHA256Fingerprint != certificate.SHA256Fingerprint || ack.Platform != "linux") {
			certificate.ManualTrustAcknowledgment = nil
			certificate.TrustState = model.CertificateTrustUntrusted
			certificate.Trusted = false
		} else if ack != nil {
			certificate.TrustState = model.CertificateTrustManualAcknowledgedUnverified
			certificate.Trusted = false
			certificate.InstalledByScopeNest = false
		} else if certificate.TrustState == model.CertificateTrustManualAcknowledgedUnverified {
			certificate.TrustState = model.CertificateTrustUntrusted
			certificate.Trusted = false
		}
	}

	for i := range db.EnvironmentTemplates {
		if db.EnvironmentTemplates[i].CertificateIDs == nil {
			db.EnvironmentTemplates[i].CertificateIDs = []string{}
		}
	}

	db.Version = CurrentVersion
	return db, nil
}

func (s *Store) Migrate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireFileLock(s.lockPath, s.lockTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()

	data, err := os.ReadFile(s.metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read container metadata: %w", err)
	}

	var peek struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return fmt.Errorf("decode container metadata: %w", err)
	}
	if peek.Version < 1 || peek.Version > CurrentVersion {
		return fmt.Errorf("unsupported metadata version %d", peek.Version)
	}

	if peek.Version == 1 {
		// Create exactly one durable backup of the original v1 bytes.
		backupPath := filepath.Join(s.root, "containers.v1.backup.json")
		if _, err := os.Stat(backupPath); errors.Is(err, os.ErrNotExist) {
			backup, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create version 1 backup: %w", err)
			}
			if err == nil {
				if _, err := backup.Write(data); err != nil {
					backup.Close()
					return fmt.Errorf("write version 1 backup: %w", err)
				}
				if err := backup.Sync(); err != nil {
					backup.Close()
					return fmt.Errorf("sync version 1 backup: %w", err)
				}
				if err := backup.Close(); err != nil {
					return fmt.Errorf("close version 1 backup: %w", err)
				}
				if err := syncParent(backupPath); err != nil {
					return fmt.Errorf("sync version 1 backup directory: %w", err)
				}
			}
		}

		// Perform read and atomic write (loadUnlocked handles the logical migration)
		db, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if err := s.writeAtomic(db); err != nil {
			return fmt.Errorf("commit migrated database: %w", err)
		}
	}

	return nil
}

func (s *Store) Update(fn func(*model.Database) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireFileLock(s.lockPath, s.lockTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	db, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	if err := fn(&db); err != nil {
		return err
	}
	return s.writeAtomic(db)
}

func (s *Store) writeAtomic(db model.Database) error {
	if s.FaultInject != nil {
		if err := s.FaultInject("before-atomic-write"); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("encode container metadata: %w", err)
	}
	tmp, err := os.CreateTemp(s.root, ".containers-*.tmp")
	if err != nil {
		return fmt.Errorf("create metadata temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect metadata temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write metadata temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync metadata temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close metadata temporary file: %w", err)
	}
	if s.FaultInject != nil {
		if err := s.FaultInject("before-atomic-replace"); err != nil {
			return err
		}
	}
	if err := atomicReplace(tmpName, s.metaPath); err != nil {
		return fmt.Errorf("replace container metadata: %w", err)
	}
	return nil
}

// BrokenReferences reports persisted links to resources that do not exist.
// It never mutates or deletes user data.
func BrokenReferences(db model.Database) []ReferenceIssue {
	proxyIDs := make(map[string]bool, len(db.ProxyProfiles))
	certificateIDs := make(map[string]bool, len(db.Certificates))
	templateIDs := make(map[string]bool, len(db.EnvironmentTemplates))
	for _, proxy := range db.ProxyProfiles {
		proxyIDs[proxy.ID] = true
	}
	for _, certificate := range db.Certificates {
		certificateIDs[certificate.ID] = true
	}
	for _, template := range db.EnvironmentTemplates {
		templateIDs[template.ID] = true
	}
	issues := []ReferenceIssue{}
	for _, container := range db.Containers {
		if container.ProxyProfileID != "" && !proxyIDs[container.ProxyProfileID] {
			issues = append(issues, ReferenceIssue{Kind: "missing_proxy_profile", OwnerID: container.ID, ReferenceID: container.ProxyProfileID})
		}
		if container.EnvironmentTemplateID != "" && !templateIDs[container.EnvironmentTemplateID] {
			issues = append(issues, ReferenceIssue{Kind: "missing_environment_template", OwnerID: container.ID, ReferenceID: container.EnvironmentTemplateID})
		}
	}
	for _, proxy := range db.ProxyProfiles {
		for _, certificateID := range proxy.CertificateIDs {
			if !certificateIDs[certificateID] {
				issues = append(issues, ReferenceIssue{Kind: "missing_certificate", OwnerID: proxy.ID, ReferenceID: certificateID})
			}
		}
	}
	for _, template := range db.EnvironmentTemplates {
		if template.ProxyProfileID != "" && !proxyIDs[template.ProxyProfileID] {
			issues = append(issues, ReferenceIssue{Kind: "missing_proxy_profile", OwnerID: template.ID, ReferenceID: template.ProxyProfileID})
		}
		for _, certificateID := range template.CertificateIDs {
			if !certificateIDs[certificateID] {
				issues = append(issues, ReferenceIssue{Kind: "missing_certificate", OwnerID: template.ID, ReferenceID: certificateID})
			}
		}
	}
	return issues
}

func (s *Store) EnsureProfile(id string) (string, error) {
	profile, err := s.ProfilePath(id)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(profile, 0700); err != nil {
		return "", fmt.Errorf("create container profile: %w", err)
	}
	return profile, nil
}

func (s *Store) RemoveContainerDirectory(id string) error {
	profile, err := s.ProfilePath(id)
	if err != nil {
		return err
	}
	dir := filepath.Dir(profile)
	if _, err := security.ResolveWithin(filepath.Join(s.root, "containers"), dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (s *Store) ProfileInUse(id string) (bool, error) {
	profile, err := s.ProfilePath(id)
	if err != nil {
		return false, err
	}
	// Chromium creates these user-data-root markers while a profile instance owns it.
	for _, marker := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		path, err := security.ResolveWithin(s.root, filepath.Join(profile, marker))
		if err != nil {
			return false, err
		}
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect browser profile lock: %w", err)
		}
	}
	return false, nil
}

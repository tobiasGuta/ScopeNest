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
	db := model.Database{Version: 1, Containers: []model.Container{}}
	data, err := os.ReadFile(s.metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return db, nil
	}
	if err != nil {
		return db, fmt.Errorf("read container metadata: %w", err)
	}
	if err := json.Unmarshal(data, &db); err != nil {
		return db, fmt.Errorf("decode container metadata: %w", err)
	}
	if db.Version != 1 {
		return db, fmt.Errorf("unsupported metadata version %d", db.Version)
	}
	if db.Containers == nil {
		db.Containers = []model.Container{}
	}
	for i := range db.Containers {
		container := &db.Containers[i]
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
	return db, nil
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
	if err := atomicReplace(tmpName, s.metaPath); err != nil {
		return fmt.Errorf("replace container metadata: %w", err)
	}
	return nil
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

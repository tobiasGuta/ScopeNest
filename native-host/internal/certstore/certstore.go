package certstore

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/security"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

const MaxCertificateSize = 128 * 1024

var (
	ErrInvalidBase64       = errors.New("invalid base64 encoding")
	ErrTooLarge            = errors.New("certificate exceeds size limit")
	ErrNoCertificate       = errors.New("no certificate found")
	ErrMultipleCerts       = errors.New("multiple certificates found")
	ErrPrivateKey          = errors.New("private key found")
	ErrNotCA               = errors.New("certificate is not a CA")
	ErrSizeMismatch        = errors.New("certificate size mismatch")
	ErrMalformed           = errors.New("malformed certificate")
	ErrFingerprintMismatch = errors.New("certificate fingerprint mismatch")
)

type TrustStore interface {
	Scope() string
	Supported() bool
	Install(der []byte, fingerprint string) (alreadyTrusted bool, err error)
	Remove(der []byte, fingerprint string) error
}

type Manager struct {
	dbStore     *store.Store
	certDir     string
	Trust       TrustStore
	FaultInject func(point string) error
}

func NewManager(dbStore *store.Store, trust TrustStore) *Manager {
	return &Manager{
		dbStore: dbStore,
		certDir: filepath.Join(dbStore.Root(), "resources", "certificates"),
		Trust:   trust,
	}
}

func (m *Manager) checkFault(point string) error {
	if m.FaultInject != nil {
		return m.FaultInject(point)
	}
	return nil
}

type StagedCertificate struct {
	TempDir string
	Model   model.Certificate
	DER     []byte
}

type ResourceIssue struct {
	Kind          string `json:"kind"`
	CertificateID string `json:"certificateId,omitempty"`
	Path          string `json:"path"`
}

func (m *Manager) Import(displayName string, base64Content string, expectedSize int) (*StagedCertificate, error) {
	if expectedSize < 0 || expectedSize > MaxCertificateSize {
		return nil, ErrTooLarge
	}
	if len(base64Content) > base64.StdEncoding.EncodedLen(MaxCertificateSize)+2 {
		return nil, ErrTooLarge
	}
	raw, err := base64.StdEncoding.DecodeString(base64Content)
	if err != nil {
		return nil, ErrInvalidBase64
	}
	if len(raw) != expectedSize {
		return nil, fmt.Errorf("%w: expected %d, got %d", ErrSizeMismatch, expectedSize, len(raw))
	}
	if len(raw) > MaxCertificateSize {
		return nil, ErrTooLarge
	}
	if len(raw) == 0 {
		return nil, ErrNoCertificate
	}

	der, sourceFormat, err := extractSingleCertificate(raw)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if len(cert.Raw) != len(der) {
		return nil, errors.New("certificate contains trailing bytes or multiple certificates")
	}
	// Use exactly the normalized bytes for fingerprinting
	der = cert.Raw

	if !cert.IsCA {
		return nil, ErrNotCA
	}

	hash := sha256.Sum256(der)
	fingerprint := strings.ToUpper(hex.EncodeToString(hash[:]))
	var formattedFingerprint []string
	for i := 0; i < len(fingerprint); i += 2 {
		formattedFingerprint = append(formattedFingerprint, fingerprint[i:i+2])
	}
	fingerprintStr := strings.Join(formattedFingerprint, ":")

	id, err := security.NewID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	modelCert := model.Certificate{
		ID:                     id,
		DisplayName:            strings.TrimSpace(displayName),
		SHA256Fingerprint:      fingerprintStr,
		Subject:                cert.Subject.String(),
		Issuer:                 cert.Issuer.String(),
		SerialNumber:           cert.SerialNumber.String(),
		NotBefore:              cert.NotBefore,
		NotAfter:               cert.NotAfter,
		IsCertificateAuthority: cert.IsCA,
		KeyUsage:               keyUsageStrings(cert.KeyUsage),
		SourceFormat:           sourceFormat,
		Trusted:                false,
		TrustScope:             m.Trust.Scope(),
		InstalledByScopeNest:   false,
		TrustState:             model.CertificateTrustUntrusted,
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	if err := os.MkdirAll(m.certDir, 0700); err != nil {
		return nil, fmt.Errorf("create certificates directory: %w", err)
	}
	if err := os.Chmod(m.certDir, 0700); err != nil {
		return nil, fmt.Errorf("protect certificates directory: %w", err)
	}
	tempDir, err := os.MkdirTemp(m.certDir, ".scopenest-cert-import-*")
	if err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}

	if err := os.Chmod(tempDir, 0700); err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("protect staging directory: %w", err)
	}

	certPath := filepath.Join(tempDir, "certificate.der")
	file, err := os.OpenFile(certPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("create staged certificate: %w", err)
	}
	if _, err := file.Write(der); err != nil {
		file.Close()
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("write staged certificate: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sync staged certificate: %w", err)
	}
	if err := m.checkFault("after-file-sync"); err != nil {
		file.Close()
		os.RemoveAll(tempDir)
		return nil, err
	}
	if err := file.Close(); err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("close staged certificate: %w", err)
	}
	if err := syncDirectory(tempDir); err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("sync staging directory: %w", err)
	}
	if err := m.checkFault("after-staging-directory-sync"); err != nil {
		os.RemoveAll(tempDir)
		return nil, err
	}

	return &StagedCertificate{TempDir: tempDir, Model: modelCert, DER: der}, nil
}

func (m *Manager) CommitImport(staged *StagedCertificate) (model.Certificate, error) {
	var finalCert model.Certificate
	if staged == nil {
		return model.Certificate{}, errors.New("staged certificate is required")
	}
	managedTemp, err := security.ResolveWithin(m.certDir, staged.TempDir)
	if err != nil || filepath.Dir(managedTemp) != m.certDir || !strings.HasPrefix(filepath.Base(managedTemp), ".scopenest-cert-import-") {
		return model.Certificate{}, errors.New("staging directory is outside the managed certificate directory")
	}
	finalDir := filepath.Join(m.certDir, staged.Model.ID)
	placed := false
	err = m.dbStore.Update(func(db *model.Database) error {
		if err := m.checkFault("before-commit"); err != nil {
			return err
		}

		for _, c := range db.Certificates {
			if c.SHA256Fingerprint == staged.Model.SHA256Fingerprint {
				return fmt.Errorf("certificate already exists in library")
			}
		}

		if err := m.checkFault("before-rename"); err != nil {
			return err
		}

		if err := placeDirectoryAtomic(managedTemp, finalDir); err != nil {
			return fmt.Errorf("commit certificate directory: %w", err)
		}
		placed = true

		if err := m.checkFault("after-rename"); err != nil {
			// Simulate failure after rename but before DB update
			return err
		}

		db.Certificates = append(db.Certificates, staged.Model)
		finalCert = staged.Model
		return nil
	})

	if err != nil {
		if placed {
			if rollbackErr := os.RemoveAll(finalDir); rollbackErr != nil {
				return model.Certificate{}, fmt.Errorf("%v; rollback certificate directory: %w", err, rollbackErr)
			}
			_ = syncDirectory(m.certDir)
		}
		return model.Certificate{}, err
	}
	return finalCert, nil
}

func (m *Manager) CleanupStaging() {
	entries, err := os.ReadDir(m.certDir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() && strings.HasPrefix(name, ".scopenest-cert-import-") {
			info, err := entry.Info()
			// Only delete if older than 1 hour to prevent races with active imports
			if err == nil && now.Sub(info.ModTime()) > time.Hour {
				// Safety check: ensure it only contains exactly one expected file
				// or is completely empty before wiping it automatically.
				dirPath := filepath.Join(m.certDir, name)
				contents, cErr := os.ReadDir(dirPath)
				if cErr == nil && len(contents) <= 1 {
					if len(contents) == 1 && contents[0].Name() == "certificate.der" {
						_ = os.RemoveAll(dirPath)
					} else if len(contents) == 0 {
						_ = os.RemoveAll(dirPath)
					}
				}
			}
		}
	}
}

func (m *Manager) ReadCertificate(id string) ([]byte, error) {
	if err := security.ValidateID(id); err != nil {
		return nil, err
	}
	path := filepath.Join(m.certDir, id, "certificate.der")
	path, err := security.ResolveWithin(m.certDir, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func FingerprintDER(der []byte) (string, error) {
	certificate, err := x509.ParseCertificate(der)
	if err != nil || !bytes.Equal(certificate.Raw, der) {
		return "", ErrMalformed
	}
	hash := sha256.Sum256(certificate.Raw)
	hexFingerprint := strings.ToUpper(hex.EncodeToString(hash[:]))
	parts := make([]string, 0, len(hexFingerprint)/2)
	for i := 0; i < len(hexFingerprint); i += 2 {
		parts = append(parts, hexFingerprint[i:i+2])
	}
	return strings.Join(parts, ":"), nil
}

func (m *Manager) ReadCertificateVerified(id, expectedFingerprint string) ([]byte, error) {
	der, err := m.ReadCertificate(id)
	if err != nil {
		return nil, err
	}
	fingerprint, err := FingerprintDER(der)
	if err != nil {
		return nil, err
	}
	if fingerprint != expectedFingerprint {
		return nil, ErrFingerprintMismatch
	}
	return der, nil
}

// AuditResources reports missing and orphaned resources without deleting them.
func (m *Manager) AuditResources() ([]ResourceIssue, error) {
	db, err := m.dbStore.Load()
	if err != nil {
		return nil, err
	}
	issues := []ResourceIssue{}
	known := make(map[string]bool, len(db.Certificates))
	for _, certificate := range db.Certificates {
		known[certificate.ID] = true
		path := filepath.Join(m.certDir, certificate.ID, "certificate.der")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			issues = append(issues, ResourceIssue{Kind: "missing_der", CertificateID: certificate.ID, Path: path})
		} else if err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(m.certDir)
	if errors.Is(err, os.ErrNotExist) {
		return issues, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".scopenest-cert-import-") {
			continue
		}
		if !known[entry.Name()] {
			issues = append(issues, ResourceIssue{Kind: "orphan_resource", CertificateID: entry.Name(), Path: filepath.Join(m.certDir, entry.Name())})
		}
	}
	return issues, nil
}

func extractSingleCertificate(raw []byte) ([]byte, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, "", ErrNoCertificate
	}
	// First, try PEM
	block, rest := pem.Decode(raw)
	if block != nil {
		var certDER []byte
		for {
			if strings.Contains(block.Type, "PRIVATE KEY") {
				return nil, "", ErrPrivateKey
			}
			if block.Type == "CERTIFICATE" {
				if certDER != nil {
					return nil, "", ErrMultipleCerts
				}
				certDER = block.Bytes
			} else {
				// Reject any non-certificate blocks in PEM (like parameters or keys)
				return nil, "", fmt.Errorf("unsupported PEM block type: %s", block.Type)
			}
			rest = bytes.TrimSpace(rest)
			if len(rest) == 0 {
				break
			}
			block, rest = pem.Decode(rest)
			if block == nil && len(rest) > 0 {
				// We had some trailing garbage that wasn't a valid PEM block
				return nil, "", errors.New("trailing data after PEM blocks")
			}
		}
		if certDER == nil {
			return nil, "", ErrNoCertificate
		}
		return certDER, "PEM", nil
	}

	if bytes.Contains(raw, []byte("-----BEGIN")) {
		return nil, "", ErrMalformed
	}

	// Reject DER private-key formats explicitly before treating the input as a certificate.
	if _, err := x509.ParsePKCS1PrivateKey(raw); err == nil {
		return nil, "", ErrPrivateKey
	}
	if key, err := x509.ParsePKCS8PrivateKey(raw); err == nil && key != nil {
		return nil, "", ErrPrivateKey
	}
	lower := bytes.ToLower(raw)
	if bytes.Contains(lower, []byte("private key")) {
		return nil, "", ErrPrivateKey
	}

	if certificates, err := x509.ParseCertificates(raw); err == nil && len(certificates) != 1 {
		return nil, "", ErrMultipleCerts
	}
	return raw, "DER", nil
}

func keyUsageStrings(usage x509.KeyUsage) []string {
	var result []string
	if usage&x509.KeyUsageDigitalSignature != 0 {
		result = append(result, "DigitalSignature")
	}
	if usage&x509.KeyUsageContentCommitment != 0 {
		result = append(result, "ContentCommitment")
	}
	if usage&x509.KeyUsageKeyEncipherment != 0 {
		result = append(result, "KeyEncipherment")
	}
	if usage&x509.KeyUsageDataEncipherment != 0 {
		result = append(result, "DataEncipherment")
	}
	if usage&x509.KeyUsageKeyAgreement != 0 {
		result = append(result, "KeyAgreement")
	}
	if usage&x509.KeyUsageCertSign != 0 {
		result = append(result, "CertSign")
	}
	if usage&x509.KeyUsageCRLSign != 0 {
		result = append(result, "CRLSign")
	}
	if usage&x509.KeyUsageEncipherOnly != 0 {
		result = append(result, "EncipherOnly")
	}
	if usage&x509.KeyUsageDecipherOnly != 0 {
		result = append(result, "DecipherOnly")
	}
	return result
}

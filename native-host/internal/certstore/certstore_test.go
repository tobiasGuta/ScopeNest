package certstore

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

type mockTrustStore struct {
	installed bool
	removed   bool
}

func testManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	dbStore, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(dbStore, &mockTrustStore{}), dbStore
}

func importBytes(t *testing.T, manager *Manager, raw []byte) (*StagedCertificate, error) {
	t.Helper()
	return manager.Import("Test CA", base64.StdEncoding.EncodeToString(raw), len(raw))
}

func (m *mockTrustStore) Scope() string   { return "mock" }
func (m *mockTrustStore) Supported() bool { return true }
func (m *mockTrustStore) Verify([]byte, string) (bool, error) {
	return m.installed && !m.removed, nil
}
func (m *mockTrustStore) Install(der []byte, fingerprint string) (bool, error) {
	m.installed = true
	m.removed = false
	return false, nil
}
func (m *mockTrustStore) Remove([]byte, string) error {
	m.removed = true
	m.installed = false
	return nil
}

func generateTestCert(t *testing.T, isCA bool) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Test Org"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}

	return derBytes, priv
}

func TestExtractSingleCertificate(t *testing.T) {
	caDER, priv := generateTestCert(t, true)

	// Valid DER
	der, fmtType, err := extractSingleCertificate(caDER)
	if err != nil || fmtType != "DER" || !bytes.Equal(der, caDER) {
		t.Fatalf("Failed to extract valid DER: %v", err)
	}

	// Valid PEM
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	der, fmtType, err = extractSingleCertificate(caPEM)
	if err != nil || fmtType != "PEM" || !bytes.Equal(der, caDER) {
		t.Fatalf("Failed to extract valid PEM: %v", err)
	}

	// Trailing bytes after PEM
	invalidPEM := append(caPEM, []byte("trailing garbage")...)
	_, _, err = extractSingleCertificate(invalidPEM)
	if err == nil {
		t.Fatal("Expected error for trailing bytes after PEM")
	}

	// Multiple certs
	multiPEM := append(caPEM, caPEM...)
	_, _, err = extractSingleCertificate(multiPEM)
	if err != ErrMultipleCerts {
		t.Fatalf("Expected ErrMultipleCerts, got %v", err)
	}

	// Private key included
	privDER := x509.MarshalPKCS1PrivateKey(priv)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER})
	withPrivPEM := append(caPEM, privPEM...)
	_, _, err = extractSingleCertificate(withPrivPEM)
	if err != ErrPrivateKey {
		t.Fatalf("Expected ErrPrivateKey, got %v", err)
	}
}

func TestManager_Import_Valid(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "metadata.json")
	dbStore, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	manager := NewManager(dbStore, &mockTrustStore{})
	caDER, _ := generateTestCert(t, true)

	caBase64 := base64.StdEncoding.EncodeToString(caDER)
	staged, err := manager.Import("Test CA", caBase64, len(caDER))
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	if staged.Model.DisplayName != "Test CA" {
		t.Errorf("Expected display name Test CA, got %s", staged.Model.DisplayName)
	}
	if !staged.Model.IsCertificateAuthority {
		t.Errorf("Expected IsCertificateAuthority = true")
	}

	finalCert, err := manager.CommitImport(staged)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if finalCert.ID != staged.Model.ID {
		t.Errorf("ID mismatch after commit")
	}
}

func TestManager_Import_NotCA(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "metadata.json")
	dbStore, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	manager := NewManager(dbStore, &mockTrustStore{})
	notCADER, _ := generateTestCert(t, false)

	notCABase64 := base64.StdEncoding.EncodeToString(notCADER)
	_, err = manager.Import("Not CA", notCABase64, len(notCADER))
	if err != ErrNotCA {
		t.Fatalf("Expected ErrNotCA, got %v", err)
	}
}

func TestManager_Import_TrailingDER(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "metadata.json")
	dbStore, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	manager := NewManager(dbStore, &mockTrustStore{})
	caDER, _ := generateTestCert(t, true)
	trailingDER := append(caDER, []byte("trailing bytes")...)

	caBase64 := base64.StdEncoding.EncodeToString(trailingDER)
	_, err = manager.Import("Trailing DER", caBase64, len(trailingDER))
	if err == nil || !(strings.Contains(err.Error(), "trailing bytes") || strings.Contains(err.Error(), "trailing data")) {
		t.Fatalf("Expected trailing bytes error, got %v", err)
	}
}

func TestManager_Import_Faults(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "metadata.json")
	dbStore, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	manager := NewManager(dbStore, &mockTrustStore{})
	caDER, _ := generateTestCert(t, true)
	caBase64 := base64.StdEncoding.EncodeToString(caDER)

	manager.FaultInject = func(point string) error {
		if point == "before-rename" {
			return errors.New("simulated failure before rename")
		}
		return nil
	}

	staged, err := manager.Import("Test CA", caBase64, len(caDER))
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Mocking commit fault
	_, err = manager.CommitImport(staged)

	if err == nil || !strings.Contains(err.Error(), "simulated failure before rename") {
		t.Fatalf("Expected simulated failure, got %v", err)
	}
}

func TestCertificateParserRejectsEmptyInput(t *testing.T) {
	manager, _ := testManager(t)
	if _, err := manager.Import("empty", "", 0); !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsInvalidBase64(t *testing.T) {
	manager, _ := testManager(t)
	if _, err := manager.Import("bad", "%%%", 2); !errors.Is(err, ErrInvalidBase64) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsDecodedSizeMismatch(t *testing.T) {
	manager, _ := testManager(t)
	if _, err := manager.Import("bad", base64.StdEncoding.EncodeToString([]byte("x")), 2); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsOversizedInputBeforeDecode(t *testing.T) {
	manager, _ := testManager(t)
	oversized := make([]byte, MaxCertificateSize+1)
	if _, err := importBytes(t, manager, oversized); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsMalformedDER(t *testing.T) {
	manager, _ := testManager(t)
	if _, err := importBytes(t, manager, []byte{0x30, 0x01, 0xff}); err == nil {
		t.Fatal("malformed DER was accepted")
	}
}

func TestCertificateParserRejectsMalformedPEM(t *testing.T) {
	manager, _ := testManager(t)
	if _, err := importBytes(t, manager, []byte("-----BEGIN CERTIFICATE-----\nnot-base64\n-----END CERTIFICATE-----")); !errors.Is(err, ErrMalformed) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsPKCS1PrivateKey(t *testing.T) {
	manager, _ := testManager(t)
	_, key := generateTestCert(t, true)
	if _, err := importBytes(t, manager, x509.MarshalPKCS1PrivateKey(key)); !errors.Is(err, ErrPrivateKey) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsPKCS8PrivateKey(t *testing.T) {
	manager, _ := testManager(t)
	_, key := generateTestCert(t, true)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importBytes(t, manager, der); !errors.Is(err, ErrPrivateKey) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsEncryptedPrivateKey(t *testing.T) {
	manager, _ := testManager(t)
	_, key := generateTestCert(t, true)
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), []byte("secret"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importBytes(t, manager, pem.EncodeToMemory(block)); !errors.Is(err, ErrPrivateKey) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsCertificatePlusPrivateKey(t *testing.T) {
	manager, _ := testManager(t)
	der, key := generateTestCert(t, true)
	raw := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})...)
	if _, err := importBytes(t, manager, raw); !errors.Is(err, ErrPrivateKey) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsMultipleCertificates(t *testing.T) {
	manager, _ := testManager(t)
	der, _ := generateTestCert(t, true)
	raw := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	if _, err := importBytes(t, manager, raw); !errors.Is(err, ErrMultipleCerts) {
		t.Fatalf("got %v", err)
	}
}

func TestCertificateParserRejectsExtraPEMBlocks(t *testing.T) {
	manager, _ := testManager(t)
	der, _ := generateTestCert(t, true)
	raw := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PARAMETERS", Bytes: []byte{1}})...)
	if _, err := importBytes(t, manager, raw); err == nil {
		t.Fatal("extra PEM block was accepted")
	}
}

func TestCertificateParserRejectsDERTrailingBytes(t *testing.T) {
	manager, _ := testManager(t)
	der, _ := generateTestCert(t, true)
	if _, err := importBytes(t, manager, append(der, 0)); err == nil {
		t.Fatal("DER trailing byte was accepted")
	}
}

func TestCertificateParserRejectsPFXPKCS12(t *testing.T) {
	manager, _ := testManager(t)
	// Minimal DER PFX v3 with a data ContentInfo containing an empty AuthenticatedSafe.
	pfx := []byte{0x30, 0x16, 0x02, 0x01, 0x03, 0x30, 0x11, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x07, 0x01, 0xa0, 0x04, 0x04, 0x02, 0x30, 0x00}
	if _, err := importBytes(t, manager, pfx); err == nil {
		t.Fatal("PKCS#12/PFX input was accepted")
	}
}

func TestCertificateParserRejectsNonCA(t *testing.T) {
	manager, _ := testManager(t)
	der, _ := generateTestCert(t, false)
	if _, err := importBytes(t, manager, der); !errors.Is(err, ErrNotCA) {
		t.Fatalf("got %v", err)
	}
}

func TestFingerprintUsesNormalizedDERBytes(t *testing.T) {
	manager, _ := testManager(t)
	der, _ := generateTestCert(t, true)
	staged, err := importBytes(t, manager, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := FingerprintDER(der)
	if staged.Model.SHA256Fingerprint != want {
		t.Fatalf("got %s want %s", staged.Model.SHA256Fingerprint, want)
	}
	stored, err := os.ReadFile(filepath.Join(staged.TempDir, "certificate.der"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, der) {
		t.Fatal("staged bytes were not normalized DER")
	}
}

func TestDuplicateFingerprintCheckRunsUnderCrossProcessLock(t *testing.T) {
	root := t.TempDir()
	firstStore, _ := store.New(root)
	secondStore, _ := store.New(root)
	first := NewManager(firstStore, &mockTrustStore{})
	second := NewManager(secondStore, &mockTrustStore{})
	der, _ := generateTestCert(t, true)
	a, _ := importBytes(t, first, der)
	b, _ := importBytes(t, second, der)
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	go func() { defer wg.Done(); _, err := first.CommitImport(a); results <- err }()
	go func() { defer wg.Done(); _, err := second.CommitImport(b); results <- err }()
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successful duplicate commits", successes)
	}
}

func TestImportCreatesManagedStagingDirectoryWithRestrictivePermissions(t *testing.T) {
	manager, _ := testManager(t)
	der, _ := generateTestCert(t, true)
	staged, err := importBytes(t, manager, der)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(staged.TempDir) != manager.certDir {
		t.Fatalf("staging escaped managed directory: %s", staged.TempDir)
	}
	for _, path := range []string{manager.certDir, staged.TempDir, filepath.Join(staged.TempDir, "certificate.der")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		// Windows reports synthesized mode bits; access is constrained by the
		// current-user application-data ACL inherited by the managed directory.
		if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			t.Fatalf("permissions too broad on %s: %o", path, info.Mode().Perm())
		}
	}
}

func TestImportSynchronizesFileAndStagingDirectory(t *testing.T) {
	manager, _ := testManager(t)
	points := []string{}
	manager.FaultInject = func(point string) error { points = append(points, point); return nil }
	der, _ := generateTestCert(t, true)
	if _, err := importBytes(t, manager, der); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(points, ",")
	if !strings.Contains(joined, "after-file-sync") || !strings.Contains(joined, "after-staging-directory-sync") {
		t.Fatalf("sync points not reached: %v", points)
	}
}

func TestCommitImportAtomicallyPlacesDirectoryAndCommitsMetadata(t *testing.T) {
	manager, dbStore := testManager(t)
	der, _ := generateTestCert(t, true)
	staged, _ := importBytes(t, manager, der)
	temp := staged.TempDir
	committed, err := manager.CommitImport(staged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.certDir, committed.ID, "certificate.der")); err != nil {
		t.Fatal(err)
	}
	db, _ := dbStore.Load()
	if len(db.Certificates) != 1 || db.Certificates[0].ID != committed.ID {
		t.Fatalf("metadata not committed: %#v", db.Certificates)
	}
}

func TestCommitImportRollsBackFinalDirectoryAfterMetadataFailure(t *testing.T) {
	manager, dbStore := testManager(t)
	der, _ := generateTestCert(t, true)
	staged, _ := importBytes(t, manager, der)
	dbStore.FaultInject = func(point string) error {
		if point == "before-atomic-replace" {
			return errors.New("metadata failure")
		}
		return nil
	}
	if _, err := manager.CommitImport(staged); err == nil {
		t.Fatal("expected metadata failure")
	}
	if _, err := os.Stat(filepath.Join(manager.certDir, staged.Model.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final directory was not rolled back: %v", err)
	}
}

func TestCleanupStagingRemovesOnlyAbandonedManagedStages(t *testing.T) {
	manager, _ := testManager(t)
	der, _ := generateTestCert(t, true)
	staged, _ := importBytes(t, manager, der)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(staged.TempDir, old, old); err != nil {
		t.Fatal(err)
	}
	manager.CleanupStaging()
	if _, err := os.Stat(staged.TempDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned stage remains: %v", err)
	}
}

func TestCleanupStagingDoesNotDeleteUnknownDirectories(t *testing.T) {
	manager, _ := testManager(t)
	if err := os.MkdirAll(manager.certDir, 0700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(manager.certDir, "user-directory")
	if err := os.Mkdir(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(unknown, old, old)
	manager.CleanupStaging()
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("unknown directory was deleted: %v", err)
	}
}

func TestAuditResourcesDetectsMissingDERAndOrphanWithoutDeleting(t *testing.T) {
	manager, dbStore := testManager(t)
	if err := os.MkdirAll(manager.certDir, 0700); err != nil {
		t.Fatal(err)
	}
	missingID := "11111111-1111-4111-8111-111111111111"
	orphanID := "22222222-2222-4222-8222-222222222222"
	if err := dbStore.Update(func(db *model.Database) error {
		db.Certificates = append(db.Certificates, model.Certificate{ID: missingID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(manager.certDir, orphanID)
	if err := os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	issues, err := manager.AuditResources()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, issue := range issues {
		kinds[issue.Kind] = true
	}
	if !kinds["missing_der"] || !kinds["orphan_resource"] {
		t.Fatalf("missing issues: %#v", issues)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("audit deleted orphan: %v", err)
	}
}

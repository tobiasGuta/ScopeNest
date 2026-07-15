//go:build windows
// +build windows

package certstore

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsTrustStore_Integration(t *testing.T) {
	if os.Getenv("SCOPENEST_RUN_CERTSTORE_INTEGRATION") != "1" {
		t.Skip("Skipping Windows certstore integration test. Set SCOPENEST_RUN_CERTSTORE_INTEGRATION=1 to run.")
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"ScopeNest Test CA"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}

	hash := sha256.Sum256(der)
	fingerprint := strings.ToUpper(hex.EncodeToString(hash[:]))

	store := WindowsTrustStore{}

	t.Cleanup(func() {
		// Ensure it is removed even if test fails
		_ = store.Remove(der, fingerprint)
	})

	// 1. Install new
	already, err := store.Install(der, fingerprint)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if already {
		t.Errorf("Expected already=false, got true")
	}

	// 2. Install again (idempotent)
	already, err = store.Install(der, fingerprint)
	if err != nil {
		t.Fatalf("Second Install failed: %v", err)
	}
	if !already {
		t.Errorf("Expected already=true on second install, got false")
	}

	// 3. Remove
	err = store.Remove(der, fingerprint)
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// 4. Remove again (should fail mismatch or not found)
	err = store.Remove(der, fingerprint)
	if err == nil {
		t.Errorf("Expected error removing absent cert")
	}
}

type fakeWindowsCertAPI struct {
	opened, closed, added, deleted, freed, consumed int
	certificates                                    [][]byte
}

func (f *fakeWindowsCertAPI) openCurrentUserRoot() (windows.Handle, error) {
	f.opened++
	return 10, nil
}
func (f *fakeWindowsCertAPI) closeStore(windows.Handle) { f.closed++ }
func (f *fakeWindowsCertAPI) nextCertificate(_ windows.Handle, previous *windows.CertContext) (*windows.CertContext, []byte, error) {
	if previous != nil {
		f.consumed++
	}
	index := f.consumed
	if previous == nil && index > 0 {
		index = f.consumed
	}
	if index >= len(f.certificates) {
		return nil, nil, nil
	}
	return &windows.CertContext{}, append([]byte(nil), f.certificates[index]...), nil
}
func (f *fakeWindowsCertAPI) freeCertificate(*windows.CertContext) { f.freed++ }
func (f *fakeWindowsCertAPI) addNew(_ windows.Handle, der []byte) error {
	f.added++
	f.certificates = append(f.certificates, append([]byte(nil), der...))
	return nil
}
func (f *fakeWindowsCertAPI) deleteCertificate(*windows.CertContext) error {
	f.deleted++
	if len(f.certificates) > 0 {
		f.certificates = f.certificates[1:]
	}
	f.consumed = 0
	return nil
}

func formattedFingerprintForTest(t *testing.T, der []byte) string {
	t.Helper()
	value, err := FingerprintDER(der)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestWindowsTrustStoreOpensOnlyCurrentUserRootAndUsesAddNew(t *testing.T) {
	der, _ := generateTestCert(t, true)
	api := &fakeWindowsCertAPI{}
	trust := WindowsTrustStore{api: api}
	already, err := trust.Install(der, formattedFingerprintForTest(t, der))
	if err != nil {
		t.Fatal(err)
	}
	if already || api.opened != 2 || api.closed != 2 || api.added != 1 || CERT_STORE_ADD_NEW != 1 || CERT_SYSTEM_STORE_CURRENT_USER != 1<<16 {
		t.Fatalf("unexpected API use: %#v", api)
	}
}

func TestWindowsTrustStoreDetectsExistingCertificateByExactFingerprintAndReleasesContext(t *testing.T) {
	der, _ := generateTestCert(t, true)
	api := &fakeWindowsCertAPI{certificates: [][]byte{der}}
	trust := WindowsTrustStore{api: api}
	already, err := trust.Install(der, formattedFingerprintForTest(t, der))
	if err != nil {
		t.Fatal(err)
	}
	if !already || api.added != 0 || api.freed != 1 || api.closed != 1 {
		t.Fatalf("existing certificate handling: %#v", api)
	}
}

func TestWindowsTrustStoreRemovalChecksEncodedBytesAndDeleteConsumesContext(t *testing.T) {
	der, _ := generateTestCert(t, true)
	api := &fakeWindowsCertAPI{certificates: [][]byte{der}}
	trust := WindowsTrustStore{api: api}
	if err := trust.Remove(der, formattedFingerprintForTest(t, der)); err != nil {
		t.Fatal(err)
	}
	if api.deleted != 1 || api.freed != 0 || api.closed != 2 {
		t.Fatalf("context ownership incorrect: %#v", api)
	}
	if encodedCertificateMatchesManagedDER(append([]byte(nil), der[:len(der)-1]...), der) {
		t.Fatal("mismatched encoded bytes were accepted")
	}
}

func TestWindowsTrustStoreRejectsManagedDERFingerprintMismatchBeforeOpeningStore(t *testing.T) {
	der, _ := generateTestCert(t, true)
	other, _ := generateTestCert(t, true)
	api := &fakeWindowsCertAPI{}
	trust := WindowsTrustStore{api: api}
	if err := trust.Remove(der, formattedFingerprintForTest(t, other)); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("got %v", err)
	}
	if api.opened != 0 || api.deleted != 0 {
		t.Fatalf("store touched for mismatched DER: %#v", api)
	}
}

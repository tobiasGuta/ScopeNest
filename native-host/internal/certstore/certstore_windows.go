//go:build windows

package certstore

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modcrypt32                           = windows.NewLazySystemDLL("crypt32.dll")
	procCertAddEncodedCertificateToStore = modcrypt32.NewProc("CertAddEncodedCertificateToStore")
)

const (
	CERT_STORE_PROV_SYSTEM         = 10
	CERT_SYSTEM_STORE_CURRENT_USER = 1 << 16
	X509_ASN_ENCODING              = 0x00000001
	CERT_STORE_ADD_NEW             = 1
)

type windowsCertAPI interface {
	openCurrentUserRoot() (windows.Handle, error)
	closeStore(windows.Handle)
	nextCertificate(windows.Handle, *windows.CertContext) (*windows.CertContext, []byte, error)
	freeCertificate(*windows.CertContext)
	addNew(windows.Handle, []byte) error
	deleteCertificate(*windows.CertContext) error
}

type systemWindowsCertAPI struct{}

func (systemWindowsCertAPI) openCurrentUserRoot() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString("Root")
	if err != nil {
		return 0, err
	}
	store, err := windows.CertOpenStore(uintptr(CERT_STORE_PROV_SYSTEM), 0, 0, CERT_SYSTEM_STORE_CURRENT_USER, uintptr(unsafe.Pointer(name)))
	if err != nil {
		return 0, fmt.Errorf("CertOpenStore CurrentUser\\Root failed: %v", err)
	}
	return store, nil
}

func (systemWindowsCertAPI) closeStore(store windows.Handle) { _ = windows.CertCloseStore(store, 0) }

func (systemWindowsCertAPI) nextCertificate(store windows.Handle, previous *windows.CertContext) (*windows.CertContext, []byte, error) {
	context, _ := windows.CertEnumCertificatesInStore(store, previous)
	if context == nil {
		return nil, nil, nil
	}
	if context.EncodedCert == nil || context.Length == 0 {
		_ = windows.CertFreeCertificateContext(context)
		return nil, nil, errors.New("Windows certificate context has no encoded bytes")
	}
	return context, bytes.Clone(unsafe.Slice(context.EncodedCert, int(context.Length))), nil
}

func (systemWindowsCertAPI) freeCertificate(context *windows.CertContext) {
	_ = windows.CertFreeCertificateContext(context)
}

func (systemWindowsCertAPI) addNew(store windows.Handle, der []byte) error {
	if len(der) == 0 {
		return ErrNoCertificate
	}
	result, _, callErr := procCertAddEncodedCertificateToStore.Call(
		uintptr(store), uintptr(X509_ASN_ENCODING), uintptr(unsafe.Pointer(&der[0])), uintptr(len(der)), uintptr(CERT_STORE_ADD_NEW), 0,
	)
	if result == 0 {
		return fmt.Errorf("CertAddEncodedCertificateToStore CERT_STORE_ADD_NEW failed: %v", callErr)
	}
	return nil
}

func (systemWindowsCertAPI) deleteCertificate(context *windows.CertContext) error {
	// CertDeleteCertificateFromStore always consumes the context, including on failure.
	return windows.CertDeleteCertificateFromStore(context)
}

type WindowsTrustStore struct{ api windowsCertAPI }

func NewTrustStore() TrustStore           { return WindowsTrustStore{api: systemWindowsCertAPI{}} }
func (WindowsTrustStore) Scope() string   { return "windows-current-user-root" }
func (WindowsTrustStore) Supported() bool { return true }

func (s WindowsTrustStore) windowsAPI() windowsCertAPI {
	if s.api == nil {
		return systemWindowsCertAPI{}
	}
	return s.api
}

func canonicalFingerprint(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
}
func encodedCertificateMatchesManagedDER(encoded, managed []byte) bool {
	return bytes.Equal(encoded, managed)
}

func verifyManagedFingerprint(der []byte, expected string) error {
	actual, err := FingerprintDER(der)
	if err != nil {
		return err
	}
	if canonicalFingerprint(actual) != canonicalFingerprint(expected) {
		return ErrFingerprintMismatch
	}
	return nil
}

func findExactFingerprint(api windowsCertAPI, store windows.Handle, fingerprint string) (*windows.CertContext, []byte, error) {
	var previous *windows.CertContext
	for {
		context, encoded, err := api.nextCertificate(store, previous)
		previous = nil // Windows consumed the previous enumeration context.
		if err != nil {
			return nil, nil, err
		}
		if context == nil {
			return nil, nil, nil
		}
		actual, err := FingerprintDER(encoded)
		if err == nil && canonicalFingerprint(actual) == canonicalFingerprint(fingerprint) {
			return context, encoded, nil
		}
		previous = context
	}
}

func (s WindowsTrustStore) Verify(der []byte, fingerprint string) (bool, error) {
	if err := verifyManagedFingerprint(der, fingerprint); err != nil {
		return false, err
	}
	api := s.windowsAPI()
	store, err := api.openCurrentUserRoot()
	if err != nil {
		return false, err
	}
	defer api.closeStore(store)
	context, encoded, err := findExactFingerprint(api, store, fingerprint)
	if err != nil {
		return false, err
	}
	if context == nil {
		return false, nil
	}
	defer api.freeCertificate(context)
	return encodedCertificateMatchesManagedDER(encoded, der), nil
}

func (s WindowsTrustStore) Install(der []byte, fingerprint string) (bool, error) {
	if err := verifyManagedFingerprint(der, fingerprint); err != nil {
		return false, err
	}
	api := s.windowsAPI()
	store, err := api.openCurrentUserRoot()
	if err != nil {
		return false, err
	}
	defer api.closeStore(store)
	existing, _, err := findExactFingerprint(api, store, fingerprint)
	if err != nil {
		return false, err
	}
	if existing != nil {
		api.freeCertificate(existing)
		return true, nil
	}
	if err := api.addNew(store, der); err != nil {
		return false, err
	}
	verified, err := s.Verify(der, fingerprint)
	if err != nil {
		return false, err
	}
	if !verified {
		return false, errors.New("certificate was not verified in CurrentUser\\Root after installation")
	}
	return false, nil
}

func (s WindowsTrustStore) Remove(der []byte, fingerprint string) error {
	if err := verifyManagedFingerprint(der, fingerprint); err != nil {
		return err
	}
	api := s.windowsAPI()
	store, err := api.openCurrentUserRoot()
	if err != nil {
		return err
	}
	defer api.closeStore(store)
	context, encoded, err := findExactFingerprint(api, store, fingerprint)
	if err != nil {
		return err
	}
	if context == nil {
		return errors.New("certificate fingerprint was not found in CurrentUser\\Root")
	}
	if !encodedCertificateMatchesManagedDER(encoded, der) {
		api.freeCertificate(context)
		return errors.New("encoded certificate bytes do not match the managed DER")
	}
	// deleteCertificate consumes context, so do not free it separately.
	if err := api.deleteCertificate(context); err != nil {
		return err
	}
	verified, err := s.Verify(der, fingerprint)
	if err != nil {
		return err
	}
	if verified {
		return errors.New("certificate remained in CurrentUser\\Root after removal")
	}
	return nil
}

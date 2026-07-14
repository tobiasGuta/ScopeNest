//go:build !windows
// +build !windows

package certstore

import "errors"

type UnixTrustStore struct{}

func NewTrustStore() TrustStore {
	return UnixTrustStore{}
}

func (s UnixTrustStore) Scope() string {
	return "linux-user" // Generic scope for unsupported platform
}

func (s UnixTrustStore) Supported() bool {
	return false
}

func (s UnixTrustStore) Install(der []byte, fingerprint string) (bool, error) {
	return false, errors.New("certificate trust installation is not supported on this platform")
}

func (s UnixTrustStore) Remove(der []byte, fingerprint string) error {
	return errors.New("certificate trust installation is not supported on this platform")
}

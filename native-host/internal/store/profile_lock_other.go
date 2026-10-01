//go:build !linux

package store

func processSingletonLockDefinitelyStale(string) (bool, error) {
	return false, nil
}

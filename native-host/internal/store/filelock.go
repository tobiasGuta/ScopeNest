package store

import (
	"errors"
	"fmt"
	"os"
	"time"
)

var ErrLockTimeout = errors.New("timed out waiting for the ScopeNest metadata lock")

type fileLock struct {
	file *os.File
}

func acquireFileLock(path string, timeout time.Duration) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open metadata lock file: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect metadata lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		locked, err := tryLockFile(file)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("acquire metadata lock: %w", err)
		}
		if locked {
			return &fileLock{file: file}, nil
		}
		if time.Now().After(deadline) {
			file.Close()
			return nil, ErrLockTimeout
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (l *fileLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return fmt.Errorf("release metadata lock: %w", err)
	}
	return closeErr
}

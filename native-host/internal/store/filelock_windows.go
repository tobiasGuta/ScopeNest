//go:build windows

package store

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	lockFileExclusiveLock   = 0x00000002
	lockFileFailImmediately = 0x00000001
	errorLockViolation      = syscall.Errno(33)
)

var (
	lockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	unlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

func tryLockFile(file *os.File) (bool, error) {
	overlapped := &syscall.Overlapped{}
	result, _, callErr := lockFileEx.Call(
		file.Fd(),
		lockFileExclusiveLock|lockFileFailImmediately,
		0,
		0xffffffff,
		0xffffffff,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result != 0 {
		return true, nil
	}
	if callErr == errorLockViolation {
		return false, nil
	}
	return false, callErr
}

func unlockFile(file *os.File) error {
	overlapped := &syscall.Overlapped{}
	result, _, callErr := unlockFileEx.Call(
		file.Fd(),
		0,
		0xffffffff,
		0xffffffff,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result == 0 {
		return callErr
	}
	return nil
}

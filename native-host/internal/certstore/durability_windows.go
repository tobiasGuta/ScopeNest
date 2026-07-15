//go:build windows

package certstore

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const moveFileWriteThrough = 0x8

var moveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func syncDirectory(string) error { return nil }

func placeDirectoryAtomic(source, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return os.ErrExist
	}
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileEx.Call(uintptr(unsafe.Pointer(sourcePtr)), uintptr(unsafe.Pointer(destinationPtr)), moveFileWriteThrough)
	if result == 0 {
		return callErr
	}
	return syncDirectory(filepath.Dir(destination))
}

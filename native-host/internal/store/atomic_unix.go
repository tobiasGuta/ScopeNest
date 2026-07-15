//go:build !windows

package store

import "os"

func atomicReplace(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	return syncParent(destination)
}

func syncParent(path string) error {
	dir, err := os.Open(safeParent(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

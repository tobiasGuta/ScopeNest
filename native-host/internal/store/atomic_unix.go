//go:build !windows

package store

import "os"

func atomicReplace(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	dir, err := os.Open(safeParent(destination))
	if err != nil {
		return nil
	}
	defer dir.Close()
	return dir.Sync()
}

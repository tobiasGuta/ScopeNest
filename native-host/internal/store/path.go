package store

import "path/filepath"

func safeParent(path string) string { return filepath.Dir(path) }

//go:build linux

package store

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processSingletonLockDefinitelyStale recognizes the Linux Chromium
// SingletonLock format only when ScopeNest can prove that the same-host owner
// PID no longer exists. Anything malformed, cross-host, or otherwise
// ambiguous remains "in use" at the caller.
func processSingletonLockDefinitelyStale(path string) (bool, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return false, nil
	}
	pos := strings.LastIndexByte(target, '-')
	if pos <= 0 || pos == len(target)-1 {
		return false, nil
	}
	hostname, err := os.Hostname()
	if err != nil {
		return false, err
	}
	if target[:pos] != hostname {
		return false, nil
	}
	pid, err := strconv.Atoi(target[pos+1:])
	if err != nil || pid <= 0 {
		return false, nil
	}
	err = syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, syscall.EPERM):
		return false, nil
	case errors.Is(err, syscall.ESRCH):
		return true, nil
	default:
		return false, nil
	}
}

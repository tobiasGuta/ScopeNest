//go:build !windows && !linux

package browser

import (
	"errors"
	"syscall"
)

func processGroupHasLiveMembers(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

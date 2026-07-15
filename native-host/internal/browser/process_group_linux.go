//go:build linux

package browser

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processGroupHasLiveMembers ignores zombie processes. A container PID 1 may
// defer reaping orphaned grandchildren indefinitely even after the entire
// owned group has received SIGKILL; zombies cannot execute or escape control.
func processGroupHasLiveMembers(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return processGroupRespondsToSignal(pgid)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		closing := strings.LastIndexByte(string(stat), ')')
		if closing < 0 {
			continue
		}
		fields := strings.Fields(string(stat[closing+1:]))
		if len(fields) < 3 {
			continue
		}
		group, err := strconv.Atoi(fields[2])
		if err == nil && group == pgid && fields[0] != "Z" {
			return true
		}
	}
	return false
}

func processGroupRespondsToSignal(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

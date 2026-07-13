//go:build windows

package host

import (
	"os/exec"
	"strconv"
	"strings"
)

func processExists(pid int) bool {
	cmd := exec.Command("tasklist.exe", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), `"`+strconv.Itoa(pid)+`"`)
}

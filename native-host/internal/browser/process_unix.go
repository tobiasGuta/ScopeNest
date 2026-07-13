//go:build !windows

package browser

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type groupProcess struct {
	process *os.Process
	pgid    int
}

func (ExecLauncher) Start(executable string, args []string) (Process, error) {
	cmd := exec.Command(executable, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &groupProcess{process: cmd.Process, pgid: cmd.Process.Pid}, nil
}

func (p *groupProcess) PID() int { return p.process.Pid }

func (p *groupProcess) Running() bool { return processGroupExists(p.pgid) }

func (p *groupProcess) Wait() error {
	_, waitErr := p.process.Wait()
	for processGroupExists(p.pgid) {
		time.Sleep(100 * time.Millisecond)
	}
	return waitErr
}

func (p *groupProcess) Terminate() error {
	err := syscall.Kill(-p.pgid, syscall.SIGTERM)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for processGroupExists(p.pgid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !processGroupExists(p.pgid) {
		return nil
	}
	err = syscall.Kill(-p.pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func processGroupExists(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

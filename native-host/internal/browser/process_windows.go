//go:build windows

package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	creationSuspended                   = 0x00000004
	jobObjectBasicAccountingInformation = 1
	jobObjectExtendedLimitInformation   = 9
	jobObjectLimitKillOnJobClose        = 0x00002000
	processTerminate                    = 0x0001
	processSetQuota                     = 0x0100
	processQueryLimitedInformation      = 0x1000
	threadSuspendResume                 = 0x0002
	th32csSnapThread                    = 0x00000004
	invalidSuspendCount                 = 0xffffffff
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	createJobObject           = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject  = kernel32.NewProc("AssignProcessToJobObject")
	queryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
	terminateJobObject        = kernel32.NewProc("TerminateJobObject")
	createToolhelp32Snapshot  = kernel32.NewProc("CreateToolhelp32Snapshot")
	thread32First             = kernel32.NewProc("Thread32First")
	thread32Next              = kernel32.NewProc("Thread32Next")
	openThread                = kernel32.NewProc("OpenThread")
	resumeThread              = kernel32.NewProc("ResumeThread")
	openProcess               = kernel32.NewProc("OpenProcess")
)

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type basicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type extendedLimitInformation struct {
	BasicLimitInformation basicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type basicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

type threadEntry32 struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePriority   int32
	DeltaPriority  int32
	Flags          uint32
}

type jobProcess struct {
	process *os.Process
	job     syscall.Handle
	mu      sync.Mutex
	closed  bool
}

func (ExecLauncher) Start(executable string, args []string) (Process, error) {
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: creationSuspended}
	if err := cmd.Start(); err != nil {
		syscall.CloseHandle(job)
		return nil, err
	}

	managed := &jobProcess{process: cmd.Process, job: job}
	if err := assignToJob(job, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		managed.closeJob()
		return nil, err
	}
	if err := resumeProcessThreads(cmd.Process.Pid); err != nil {
		_ = managed.Terminate()
		_, _ = cmd.Process.Wait()
		managed.closeJob()
		return nil, err
	}
	return managed, nil
}

func (p *jobProcess) PID() int { return p.process.Pid }

func (p *jobProcess) Running() bool {
	active, err := p.activeProcesses()
	return err == nil && active > 0
}

func (p *jobProcess) Wait() error {
	_, waitErr := p.process.Wait()
	for {
		active, err := p.activeProcesses()
		if err != nil {
			p.closeJob()
			if waitErr != nil {
				return waitErr
			}
			return err
		}
		if active == 0 {
			p.closeJob()
			return waitErr
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (p *jobProcess) Terminate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	result, _, callErr := terminateJobObject.Call(uintptr(p.job), 1)
	if result == 0 {
		return callErr
	}
	return nil
}

func (p *jobProcess) activeProcesses() (uint32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, nil
	}
	var info basicAccountingInformation
	result, _, callErr := queryInformationJobObject.Call(
		uintptr(p.job),
		jobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		0,
	)
	if result == 0 {
		return 0, callErr
	}
	return info.ActiveProcesses, nil
}

func (p *jobProcess) closeJob() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		_ = syscall.CloseHandle(p.job)
		p.closed = true
	}
}

func newKillOnCloseJob() (syscall.Handle, error) {
	value, _, callErr := createJobObject.Call(0, 0)
	if value == 0 {
		return 0, fmt.Errorf("create browser job object: %w", callErr)
	}
	job := syscall.Handle(value)
	info := extendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	result, _, callErr := setInformationJobObject.Call(
		value,
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if result == 0 {
		syscall.CloseHandle(job)
		return 0, fmt.Errorf("configure browser job object: %w", callErr)
	}
	return job, nil
}

func assignToJob(job syscall.Handle, pid int) error {
	processHandle, _, callErr := openProcess.Call(
		processTerminate|processSetQuota|processQueryLimitedInformation,
		0,
		uintptr(pid),
	)
	if processHandle == 0 {
		return fmt.Errorf("open browser process for job assignment: %w", callErr)
	}
	defer syscall.CloseHandle(syscall.Handle(processHandle))
	result, _, callErr := assignProcessToJobObject.Call(uintptr(job), processHandle)
	if result == 0 {
		return fmt.Errorf("assign browser process to job object: %w", callErr)
	}
	return nil
}

func resumeProcessThreads(pid int) error {
	snapshotValue, _, callErr := createToolhelp32Snapshot.Call(th32csSnapThread, 0)
	snapshot := syscall.Handle(snapshotValue)
	if snapshot == syscall.InvalidHandle {
		return fmt.Errorf("snapshot browser threads: %w", callErr)
	}
	defer syscall.CloseHandle(snapshot)

	entry := threadEntry32{Size: uint32(unsafe.Sizeof(threadEntry32{}))}
	result, _, callErr := thread32First.Call(snapshotValue, uintptr(unsafe.Pointer(&entry)))
	if result == 0 {
		return fmt.Errorf("enumerate browser threads: %w", callErr)
	}
	resumed := 0
	for {
		if entry.OwnerProcessID == uint32(pid) {
			threadHandle, _, openErr := openThread.Call(threadSuspendResume, 0, uintptr(entry.ThreadID))
			if threadHandle == 0 {
				return fmt.Errorf("open suspended browser thread: %w", openErr)
			}
			resumeResult, _, resumeErr := resumeThread.Call(threadHandle)
			syscall.CloseHandle(syscall.Handle(threadHandle))
			if resumeResult == invalidSuspendCount {
				return fmt.Errorf("resume browser thread: %w", resumeErr)
			}
			resumed++
		}
		entry.Size = uint32(unsafe.Sizeof(threadEntry32{}))
		next, _, _ := thread32Next.Call(snapshotValue, uintptr(unsafe.Pointer(&entry)))
		if next == 0 {
			break
		}
	}
	if resumed == 0 {
		return errors.New("no suspended browser thread was found")
	}
	return nil
}

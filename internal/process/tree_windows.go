//go:build windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	openProcess              = kernel32.NewProc("OpenProcess")
	closeHandle              = kernel32.NewProc("CloseHandle")
)

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

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type extendedLimitInformation struct {
	BasicLimitInformation basicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// Tree is a kill-on-close Windows Job Object containing one backend process
// and every descendant it creates.
type Tree struct {
	handle syscall.Handle
	once   sync.Once
	err    error
}

func Configure(_ *exec.Cmd) {}

func Attach(child *os.Process) (*Tree, error) {
	job, _, createErr := createJobObjectW.Call(0, 0)
	if job == 0 {
		return nil, fmt.Errorf("create backend job object: %w", createErr)
	}
	tree := &Tree{handle: syscall.Handle(job)}
	limits := extendedLimitInformation{}
	limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	ok, _, setErr := setInformationJobObject.Call(
		job,
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		unsafe.Sizeof(limits),
	)
	if ok == 0 {
		_ = tree.Terminate()
		return nil, fmt.Errorf("set backend job limits: %w", setErr)
	}

	processHandle, _, openErr := openProcess.Call(
		processSetQuota|processTerminate,
		0,
		uintptr(child.Pid),
	)
	if processHandle == 0 {
		_ = tree.Terminate()
		return nil, fmt.Errorf("open backend process for job assignment: %w", openErr)
	}
	defer closeHandle.Call(processHandle)
	ok, _, assignErr := assignProcessToJobObject.Call(job, processHandle)
	if ok == 0 {
		_ = tree.Terminate()
		return nil, fmt.Errorf("assign backend process to job object: %w", assignErr)
	}
	return tree, nil
}

func (t *Tree) Terminate() error {
	t.once.Do(func() {
		if t.handle == 0 {
			return
		}
		ok, _, err := closeHandle.Call(uintptr(t.handle))
		if ok == 0 {
			t.err = fmt.Errorf("close backend job object: %w", err)
		}
		t.handle = 0
	})
	return t.err
}

func (t *Tree) Kill() error { return t.Terminate() }

//go:build !windows

package process

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// Tree owns a spawned process. Unix app-server children receive an interrupt;
// Child supplies the bounded wait and kill fallback.
type Tree struct {
	process *os.Process
	once    sync.Once
	err     error
}

func Configure(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func Attach(child *os.Process) (*Tree, error) {
	return &Tree{process: child}, nil
}

func (t *Tree) Terminate() error {
	t.once.Do(func() {
		t.err = syscall.Kill(-t.process.Pid, syscall.SIGINT)
	})
	return t.err
}

func (t *Tree) Kill() error {
	err := syscall.Kill(-t.process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

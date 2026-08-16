//go:build windows

package runtime

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	createMutexW    = kernel32.NewProc("CreateMutexW")
	closeHandleProc = kernel32.NewProc("CloseHandle")
)

type lease struct {
	handle syscall.Handle
}

func acquireLease(root string) (*lease, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime root: %w", err)
	}
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(absolute))))
	name, err := syscall.UTF16PtrFromString(fmt.Sprintf("Local\\CodexMux-%x", digest[:16]))
	if err != nil {
		return nil, fmt.Errorf("encode runtime mutex name: %w", err)
	}
	handle, _, callErr := createMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, fmt.Errorf("create runtime mutex: %w", callErr)
	}
	if errors.Is(callErr, syscall.ERROR_ALREADY_EXISTS) {
		_, _, _ = closeHandleProc.Call(handle)
		return nil, ErrAlreadyRunning
	}
	return &lease{handle: syscall.Handle(handle)}, nil
}

func (l *lease) Close() error {
	if l == nil || l.handle == 0 {
		return nil
	}
	handle := l.handle
	l.handle = 0
	result, _, err := closeHandleProc.Call(uintptr(handle))
	if result == 0 {
		return fmt.Errorf("close runtime mutex: %w", err)
	}
	return nil
}

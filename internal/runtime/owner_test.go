package runtime

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestAcquireRejectsDuplicateOwner(t *testing.T) {
	root := t.TempDir()
	first, err := Acquire(root, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := Acquire(root, "test-build")
	if second != nil {
		_ = second.Close()
		t.Fatal("duplicate runtime owner unexpectedly acquired the lease")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("duplicate owner error = %v, want ErrAlreadyRunning", err)
	}
}

func TestAcquireRejectsOwnerInAnotherProcess(t *testing.T) {
	if os.Getenv("CODEX_MUX_OWNER_HELPER") == "1" {
		owner, err := Acquire(os.Getenv("CODEX_MUX_OWNER_ROOT"), "helper-build")
		if err != nil {
			os.Exit(2)
		}
		defer owner.Close()
		_, _ = os.Stdout.WriteString("ready\n")
		time.Sleep(30 * time.Second)
		return
	}

	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestAcquireRejectsOwnerInAnotherProcess$")
	command.Env = append(os.Environ(), "CODEX_MUX_OWNER_HELPER=1", "CODEX_MUX_OWNER_ROOT="+root)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	if scanner := bufio.NewScanner(stdout); !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("helper process did not acquire the runtime owner lease")
	}

	owner, err := Acquire(root, "parent-build")
	if owner != nil {
		_ = owner.Close()
		t.Fatal("parent unexpectedly acquired a lease held by another process")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("cross-process duplicate owner error = %v, want ErrAlreadyRunning", err)
	}
}

func TestOwnerPublishesDynamicExactEndpointAndCleansUp(t *testing.T) {
	root := t.TempDir()
	owner, err := Acquire(root, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	receipt := owner.Receipt()
	addresses := []string{receipt.Address, receipt.ControlAddress, receipt.BridgeAddress}
	seen := make(map[string]struct{})
	for _, address := range addresses {
		if err := validateLoopbackAddress(address); err != nil {
			t.Fatalf("address %q is not dynamic loopback: %v", address, err)
		}
		_, port, _ := net.SplitHostPort(address)
		if port == "48123" {
			t.Fatalf("address %q reused the retired fixed port", address)
		}
		if _, duplicate := seen[address]; duplicate {
			t.Fatalf("listeners unexpectedly share address %q", address)
		}
		seen[address] = struct{}{}
	}
	if err := owner.Publish(nil); err != nil {
		t.Fatal(err)
	}
	published, err := ReadReceipt(root)
	if err != nil {
		t.Fatal(err)
	}
	if published.Instance != receipt.Instance || published.PID != os.Getpid() || published.Build != "test-build" {
		t.Fatalf("published receipt identity = %+v", published)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Probe(ctx, published); err != nil {
		t.Fatalf("exact readiness probe failed: %v", err)
	}
	wrong := published
	wrong.Instance = strings.Repeat("0", len(published.Instance))
	if err := Probe(ctx, wrong); err == nil {
		t.Fatal("readiness probe unexpectedly accepted the wrong instance")
	}
	if err := owner.ControlListener().Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ReceiptPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime receipt survived clean shutdown: %v", err)
	}
}

func TestAuthenticatedBridgeRelaysProtocolLines(t *testing.T) {
	owner, err := Acquire(t.TempDir(), "test-build")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := &OutputHub{}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- ServeBridge(ctx, owner.BridgeListener(), owner.Receipt().Instance, hub, func(line []byte) {
			_, _ = hub.Write(append(append([]byte(nil), line...), '\n'))
		})
	}()

	var output bytes.Buffer
	input := strings.NewReader("{\"id\":1,\"method\":\"initialize\"}\n")
	if err := RunBridge(ctx, owner.Receipt(), input, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "{\"id\":1,\"method\":\"initialize\"}\n" {
		t.Fatalf("bridge output = %q", got)
	}
	cancel()
	_ = owner.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge server did not stop after listener shutdown")
	}
}

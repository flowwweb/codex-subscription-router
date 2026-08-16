//go:build windows

package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/protocol"
)

func TestBackendTreeHelperProcess(t *testing.T) {
	if os.Getenv("CODEX_MUX_TREE_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		message, err := protocol.Parse(scanner.Bytes())
		if err != nil || message.Method != "test/spawn" {
			continue
		}
		grandchild := exec.Command(os.Args[0], "-test.run=TestBackendGrandchildProcess", "--")
		grandchild.Env = append(os.Environ(), "CODEX_MUX_GRANDCHILD=1")
		if err := grandchild.Start(); err != nil {
			writeTreeMessage(protocol.Failure(message.ID, -1, err.Error()))
			continue
		}
		result, _ := json.Marshal(map[string]int{"pid": grandchild.Process.Pid})
		writeTreeMessage(protocol.Success(message.ID, result))
	}
	os.Exit(0)
}

func TestBackendGrandchildProcess(t *testing.T) {
	if os.Getenv("CODEX_MUX_GRANDCHILD") != "1" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestCloseTerminatesWindowsBackendDescendants(t *testing.T) {
	environment := append(os.Environ(), "CODEX_MUX_TREE_HELPER=1")
	child, err := Start(
		"tree-test", t.TempDir(), os.Args[0],
		[]string{"-test.run=TestBackendTreeHelperProcess", "--"},
		environment, make(chan Inbound, 8),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	response, err := child.Request(ctx, "test/spawn", nil)
	cancel()
	if err != nil {
		_ = child.Close()
		t.Fatal(err)
	}
	var result struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil || result.PID == 0 {
		_ = child.Close()
		t.Fatalf("spawn result = %s, %v", response.Result, err)
	}
	if !windowsProcessExists(result.PID) {
		_ = child.Close()
		t.Fatalf("grandchild %d was not running before close", result.PID)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !windowsProcessExists(result.PID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived backend close", result.PID)
}

func windowsProcessExists(pid int) bool {
	output, err := exec.Command(
		"tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH",
	).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), `"`+strconv.Itoa(pid)+`"`)
}

func writeTreeMessage(message protocol.Message) {
	encoded, err := protocol.Encode(message)
	if err == nil {
		_, _ = fmt.Fprintln(os.Stdout, string(encoded))
	}
}

package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	processgroup "github.com/b-nnett/codex-subscription-router/internal/process"
	"github.com/b-nnett/codex-subscription-router/internal/protocol"
)

const (
	gracefulStopTimeout = 5 * time.Second
	forcedStopTimeout   = 2 * time.Second
)

type Inbound struct {
	AccountID string
	Message   protocol.Message
	Raw       []byte
}

type response struct {
	message protocol.Message
	err     error
}

// Child owns one real Codex app-server process and one isolated CODEX_HOME.
type Child struct {
	accountID string
	exe       string
	args      []string
	env       []string
	inbound   chan<- Inbound

	command    *exec.Cmd
	tree       *processgroup.Tree
	stdin      io.WriteCloser
	writeMu    sync.Mutex
	pendingMu  sync.Mutex
	pending    map[string]chan response
	sequence   atomic.Uint64
	done       chan struct{}
	exitMu     sync.RWMutex
	exitErr    error
	closeOnce  sync.Once
	stopOnce   sync.Once
	stopErr    error
	shutdownMu sync.Mutex
}

func Start(accountID, codexHome, executable string, args, baseEnv []string, inbound chan<- Inbound) (*Child, error) {
	env := withEnvironment(baseEnv, "CODEX_HOME", codexHome)
	env = withEnvironment(env, "CODEX_SQLITE_HOME", codexHome)
	command := exec.Command(executable, args...)
	command.Env = env
	processgroup.Configure(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex stdout: %w", err)
	}
	command.Stderr = os.Stderr

	child := &Child{
		accountID: accountID,
		exe:       executable,
		args:      append([]string(nil), args...),
		env:       env,
		inbound:   inbound,
		command:   command,
		stdin:     stdin,
		pending:   make(map[string]chan response),
		done:      make(chan struct{}),
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server for %s: %w", accountID, err)
	}
	tree, err := processgroup.Attach(command.Process)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("contain Codex app-server for %s: %w", accountID, err)
	}
	child.tree = tree
	go child.readLoop(stdout)
	go child.waitLoop()
	return child, nil
}

func (c *Child) AccountID() string {
	return c.accountID
}

func (c *Child) Send(message protocol.Message) error {
	encoded, err := protocol.Encode(message)
	if err != nil {
		return err
	}
	return c.SendRaw(encoded)
}

func (c *Child) SendRaw(encoded []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.done:
		return errors.New("Codex app-server is closed")
	default:
	}
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write Codex app-server request: %w", err)
	}
	return nil
}

func (c *Child) Request(ctx context.Context, method string, params json.RawMessage) (protocol.Message, error) {
	id := protocol.StringID("__codex_mux_" + strconv.FormatUint(c.sequence.Add(1), 10))
	key := protocol.RequestIDKey(id)
	responses := make(chan response, 1)
	c.pendingMu.Lock()
	c.pending[key] = responses
	c.pendingMu.Unlock()

	if err := c.Send(protocol.Request(method, id, params)); err != nil {
		c.removePending(key)
		return protocol.Message{}, err
	}
	select {
	case received := <-responses:
		if received.err != nil {
			return protocol.Message{}, received.err
		}
		if received.message.Error != nil {
			return received.message, fmt.Errorf("%s: %s", method, received.message.Error.Message)
		}
		return received.message, nil
	case <-ctx.Done():
		c.removePending(key)
		return protocol.Message{}, ctx.Err()
	case <-c.done:
		c.removePending(key)
		return protocol.Message{}, errors.New("Codex app-server closed while awaiting response")
	}
}

func (c *Child) Close() error {
	c.shutdownMu.Lock()
	defer c.shutdownMu.Unlock()
	if c.command.Process == nil {
		return nil
	}
	c.stopOnce.Do(func() {
		c.stopErr = c.tree.Terminate()
	})
	if c.waitForExit(gracefulStopTimeout) {
		return c.stopErr
	}
	if err := c.tree.Kill(); c.stopErr == nil {
		c.stopErr = err
	}
	if !c.waitForExit(forcedStopTimeout) && c.stopErr == nil {
		c.stopErr = errors.New("Codex app-server process tree did not stop within timeout")
	}
	return c.stopErr
}

func (c *Child) Done() <-chan struct{} {
	return c.done
}

func (c *Child) ExitError() error {
	c.exitMu.RLock()
	defer c.exitMu.RUnlock()
	return c.exitErr
}

func (c *Child) waitForExit(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return true
	case <-timer.C:
		return false
	}
}

func (c *Child) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		raw := append([]byte(nil), scanner.Bytes()...)
		message, err := protocol.Parse(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "codex-mux: %s emitted invalid JSON: %v\n", c.accountID, err)
			continue
		}
		if message.Method == "" && len(message.ID) > 0 {
			key := protocol.RequestIDKey(message.ID)
			c.pendingMu.Lock()
			responses := c.pending[key]
			if responses != nil {
				delete(c.pending, key)
			}
			c.pendingMu.Unlock()
			if responses != nil {
				responses <- response{message: message}
				continue
			}
		}
		c.inbound <- Inbound{AccountID: c.accountID, Message: message, Raw: raw}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "codex-mux: read %s app-server: %v\n", c.accountID, err)
	}
}

func (c *Child) waitLoop() {
	err := c.command.Wait()
	_ = c.tree.Terminate()
	_ = c.tree.Kill()
	c.exitMu.Lock()
	c.exitErr = err
	c.exitMu.Unlock()
	c.closeOnce.Do(func() { close(c.done) })
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for key, responses := range c.pending {
		responses <- response{err: fmt.Errorf("Codex app-server exited: %w", err)}
		delete(c.pending, key)
	}
}

func (c *Child) removePending(key string) {
	c.pendingMu.Lock()
	delete(c.pending, key)
	c.pendingMu.Unlock()
}

func withEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

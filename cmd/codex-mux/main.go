package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/control"
	"github.com/b-nnett/codex-subscription-router/internal/mux"
	"github.com/b-nnett/codex-subscription-router/internal/protocol"
	muxruntime "github.com/b-nnett/codex-subscription-router/internal/runtime"
	"github.com/b-nnett/codex-subscription-router/internal/state"
)

var buildID = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "codex-mux: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "daemon" {
		return runDaemon(args[1:])
	}
	if isInteractiveAppServer(args) {
		return runBridge()
	}
	realExecutable, err := resolveRealExecutable()
	if err != nil {
		return err
	}
	return passthrough(realExecutable, args)
}

func runtimeRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	root := os.Getenv("CODEX_MUX_HOME")
	if root == "" {
		root = filepath.Join(home, ".codex-mux")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve runtime root: %w", err)
	}
	return absolute, nil
}

func runBridge() error {
	root, err := runtimeRoot()
	if err != nil {
		return err
	}
	receipt, err := muxruntime.ReadReceipt(root)
	if err != nil {
		return fmt.Errorf("router daemon is unavailable; start codex-mux daemon first: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
	err = muxruntime.Probe(probeCtx, receipt)
	probeCancel()
	if err != nil {
		return fmt.Errorf("router daemon receipt is stale or unhealthy: %w", err)
	}
	return muxruntime.RunBridge(ctx, receipt, os.Stdin, os.Stdout)
}

func runDaemon(realArgs []string) error {
	if len(realArgs) == 0 {
		realArgs = []string{"app-server"}
	}
	if !isInteractiveAppServer(realArgs) {
		return errors.New("daemon requires interactive app-server arguments")
	}
	realExecutable, err := resolveRealExecutable()
	if err != nil {
		return err
	}
	root, err := runtimeRoot()
	if err != nil {
		return err
	}
	owner, err := muxruntime.Acquire(root, buildID)
	if err != nil {
		return err
	}
	defer owner.Close()

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	primaryCodexHome := os.Getenv("CODEX_HOME")
	if primaryCodexHome == "" {
		primaryCodexHome = filepath.Join(home, ".codex")
	}
	store, err := state.Open(root, primaryCodexHome)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	output := &muxruntime.OutputHub{}
	multiplexer, err := mux.New(mux.Options{
		RealExecutable: realExecutable,
		RealArgs:       realArgs,
		Environment:    os.Environ(),
		Store:          store,
		Output:         output,
	})
	if err != nil {
		return err
	}
	if err := multiplexer.Start(ctx); err != nil {
		return err
	}
	defer multiplexer.Close()

	token, err := loadOrCreateToken(root)
	if err != nil {
		return err
	}
	receipt := owner.Receipt()
	controlServer := control.New(
		receipt.ControlAddress,
		token,
		multiplexer,
		os.Getenv("CODEX_MUX_UI_TESTS") == "1",
	)
	errorsChannel := make(chan error, 2)
	go func() {
		serveErr := controlServer.Serve(owner.ControlListener())
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorsChannel <- fmt.Errorf("control server: %w", serveErr)
		}
	}()
	go func() {
		errorsChannel <- muxruntime.ServeBridge(ctx, owner.BridgeListener(), receipt.Instance, output, func(line []byte) {
			message, parseErr := protocol.Parse(line)
			if parseErr != nil {
				fmt.Fprintf(os.Stderr, "codex-mux: ignore invalid client JSON: %v\n", parseErr)
				return
			}
			multiplexer.HandleClient(message)
		})
	}()
	if err := owner.Publish(cancel); err != nil {
		return err
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
	err = muxruntime.Probe(probeCtx, receipt)
	probeCancel()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "codex-mux: daemon ready pid=%d address=%s build=%s\n", receipt.PID, receipt.Address, receipt.Build)

	select {
	case <-ctx.Done():
	case serveErr := <-errorsChannel:
		if serveErr != nil {
			return serveErr
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	shutdownErr := controlServer.Shutdown(shutdownCtx)
	shutdownCancel()
	if closeErr := owner.Close(); closeErr != nil {
		return errors.Join(shutdownErr, closeErr)
	}
	return shutdownErr
}

func resolveRealExecutable() (string, error) {
	if configured := os.Getenv("CODEX_MUX_REAL_CODEX"); configured != "" {
		info, err := os.Stat(configured)
		if err != nil {
			return "", fmt.Errorf("find configured real Codex executable: %w", err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("configured real Codex executable is a directory: %s", configured)
		}
		return configured, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve wrapper executable: %w", err)
	}
	base := filepath.Join(filepath.Dir(executable), "codex.real")
	candidates := []string{base}
	if stdruntime.GOOS == "windows" {
		candidates = []string{base + ".exe", base}
	}
	for _, candidate := range candidates {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("find bundled real Codex executable beside %s", executable)
}

func isInteractiveAppServer(args []string) bool {
	for index, argument := range args {
		if argument != "app-server" {
			continue
		}
		if index+1 < len(args) {
			switch args[index+1] {
			case "daemon", "proxy", "generate-ts", "generate-json-schema", "help":
				return false
			}
		}
		return true
	}
	return false
}

func passthrough(realExecutable string, args []string) error {
	command := exec.Command(realExecutable, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		return err
	}
	return nil
}

func loadOrCreateToken(root string) (string, error) {
	if configured := os.Getenv("CODEX_MUX_CONTROL_TOKEN"); configured != "" {
		return validateControlToken(configured)
	}
	path := filepath.Join(root, "control-token")
	if _, statErr := os.Stat(path); statErr == nil {
		if aclErr := state.SecureFile(path); aclErr != nil {
			return "", fmt.Errorf("secure existing control token ACL: %w", aclErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect control token: %w", statErr)
	}
	if data, err := os.ReadFile(path); err == nil {
		token, validateErr := validateControlToken(string(data))
		if validateErr != nil {
			return "", fmt.Errorf("read control token: %w", validateErr)
		}
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
			return "", fmt.Errorf("secure control token: %w", chmodErr)
		}
		if aclErr := state.SecureFile(path); aclErr != nil {
			return "", fmt.Errorf("secure control token ACL: %w", aclErr)
		}
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read control token: %w", err)
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate control token: %w", err)
	}
	token := hex.EncodeToString(bytes)
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("write control token: %w", err)
	}
	if err := state.SecureFile(path); err != nil {
		return "", fmt.Errorf("secure control token ACL: %w", err)
	}
	return token, nil
}

func validateControlToken(value string) (string, error) {
	token := strings.TrimSpace(value)
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("control token must be exactly 32 random bytes encoded as hexadecimal")
	}
	return token, nil
}

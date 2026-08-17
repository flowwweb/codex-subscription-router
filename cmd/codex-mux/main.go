package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
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

const legacyControlPort = 48123

func main() {
	if err := run(); err != nil {
		reportCommandError(err, os.Args[1:], os.Stdout, os.Stderr)
		os.Exit(1)
	}
}

func reportCommandError(err error, args []string, stdout, stderr io.Writer) {
	var reported *reportedError
	if errors.As(err, &reported) {
		return
	}
	if wantsJSONError(args) {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"event": "error", "message": safeText(err.Error())})
		return
	}
	_, _ = fmt.Fprintf(stderr, "codex-mux: %v\n", err)
}

func wantsJSONError(args []string) bool {
	if len(args) == 0 || (args[0] != "status" && args[0] != "connect-account") {
		return false
	}
	for _, argument := range args[1:] {
		if argument == "--json" || argument == "--json=true" {
			return true
		}
	}
	return false
}

func run() error {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "status" {
		return runAccountStatus(args[1:])
	}
	if len(args) > 0 && args[0] == "connect-account" {
		return runConnectAccount(args[1:])
	}
	if len(args) > 0 && args[0] == "daemon" {
		return runDaemon(args[1:])
	}
	if len(args) == 1 && args[0] == "dashboard-url" {
		return runDashboardURL()
	}
	if isInteractiveAppServer(args) {
		if useDaemonBridge(stdruntime.GOOS, os.Getenv("CODEX_MUX_DAEMON_REQUIRED")) {
			return runBridge()
		}
		return runDirectInteractive(args)
	}
	realExecutable, err := resolveRealExecutable()
	if err != nil {
		return err
	}
	return passthrough(realExecutable, args)
}

func useDaemonBridge(platform, required string) bool {
	return platform == "windows" || required == "1"
}

// runDirectInteractive preserves the existing macOS patcher's single-process
// contract. Windows v2 uses the persistent daemon and thin bridge instead.
func runDirectInteractive(args []string) error {
	realExecutable, err := resolveRealExecutable()
	if err != nil {
		return err
	}
	root, err := runtimeRoot()
	if err != nil {
		return err
	}
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
	multiplexer, err := mux.New(mux.Options{
		RealExecutable: realExecutable,
		RealArgs:       args,
		Environment:    os.Environ(),
		Store:          store,
		Output:         os.Stdout,
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
	port := legacyControlPort
	if value := os.Getenv("CODEX_MUX_CONTROL_PORT"); value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed > 0 && parsed <= 65535 {
			port = parsed
		}
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-mux: account UI unavailable: %v\n", err)
	} else {
		controlServer := control.New(listener.Addr().String(), token, multiplexer, os.Getenv("CODEX_MUX_UI_TESTS") == "1")
		controlServer.SetTechnicalDetails(control.TechnicalDetails{Build: buildID, StateRoot: root, PrimaryCodexHome: primaryCodexHome})
		go func() {
			if serveErr := controlServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "codex-mux: control server: %v\n", serveErr)
			}
		}()
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer shutdownCancel()
			_ = controlServer.Shutdown(shutdownCtx)
		}()
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		message, parseErr := protocol.Parse(scanner.Bytes())
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "codex-mux: ignore invalid client JSON: %v\n", parseErr)
			continue
		}
		multiplexer.HandleClient(message)
	}
	cancel()
	return scanner.Err()
}

func runDashboardURL() error {
	root, err := runtimeRoot()
	if err != nil {
		return err
	}
	receipt, err := muxruntime.ReadReceipt(root)
	if err != nil {
		return fmt.Errorf("router daemon is unavailable: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := muxruntime.Probe(ctx, receipt); err != nil {
		return fmt.Errorf("router daemon receipt is stale or unhealthy: %w", err)
	}
	dashboardURL, err := muxruntime.RequestDashboardURL(ctx, receipt)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, dashboardURL)
	return err
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
	initializeParams, _ := json.Marshal(map[string]any{
		"clientInfo": map[string]string{
			"name": "codex-subscription-router", "title": "Codex Subscription Router", "version": buildID,
		},
		"capabilities": map[string]any{},
	})
	initializeCtx, initializeCancel := context.WithTimeout(ctx, 30*time.Second)
	if err := multiplexer.InitializeStandalone(initializeCtx, initializeParams); err != nil {
		initializeCancel()
		return fmt.Errorf("initialize daemon account pool: %w", err)
	}
	initializeCancel()

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
	controlServer.SetRuntimeIdentity(receipt.Instance)
	controlServer.SetTechnicalDetails(control.TechnicalDetails{Build: buildID, StateRoot: root, PrimaryCodexHome: primaryCodexHome})
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
	if err := owner.Publish(cancel, func() (string, error) {
		return controlServer.IssueBootstrap("http://" + receipt.ControlAddress + "/")
	}); err != nil {
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

func loadExistingToken(root string) (string, error) {
	path := filepath.Join(root, "control-token")
	if err := state.SecureFile(path); err != nil {
		return "", fmt.Errorf("secure control token ACL: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read installed control token: %w", err)
	}
	token, err := validateControlToken(string(data))
	if err != nil {
		return "", fmt.Errorf("read installed control token: %w", err)
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

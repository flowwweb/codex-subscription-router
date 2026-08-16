package runtime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/state"
)

var ErrAlreadyRunning = errors.New("router daemon is already running")

type Owner struct {
	root            string
	lease           *lease
	readyListener   net.Listener
	controlListener net.Listener
	bridgeListener  net.Listener
	receipt         Receipt
	server          *http.Server
	published       bool
	closeOnce       sync.Once
	closeErr        error
}

func Acquire(root, build string) (*Owner, error) {
	if build == "" {
		build = "dev"
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create runtime root: %w", err)
	}
	if err := state.SecureDirectory(root); err != nil {
		return nil, fmt.Errorf("secure runtime root: %w", err)
	}
	lease, err := acquireLease(root)
	if err != nil {
		return nil, err
	}
	owner := &Owner{root: root, lease: lease}
	closeOnError := func(err error) (*Owner, error) {
		_ = owner.Close()
		return nil, err
	}
	owner.readyListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return closeOnError(fmt.Errorf("bind readiness listener: %w", err))
	}
	owner.controlListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return closeOnError(fmt.Errorf("bind control listener: %w", err))
	}
	owner.bridgeListener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return closeOnError(fmt.Errorf("bind bridge listener: %w", err))
	}
	instanceBytes := make([]byte, 32)
	if _, err := rand.Read(instanceBytes); err != nil {
		return closeOnError(fmt.Errorf("generate runtime instance: %w", err))
	}
	owner.receipt = Receipt{
		Schema:         ReceiptSchema,
		PID:            os.Getpid(),
		Address:        owner.readyListener.Addr().String(),
		ControlAddress: owner.controlListener.Addr().String(),
		BridgeAddress:  owner.bridgeListener.Addr().String(),
		Instance:       hex.EncodeToString(instanceBytes),
		Build:          build,
		StartedAt:      time.Now().UTC(),
	}
	return owner, nil
}

func (o *Owner) Receipt() Receipt              { return o.receipt }
func (o *Owner) ControlListener() net.Listener { return o.controlListener }
func (o *Owner) BridgeListener() net.Listener  { return o.bridgeListener }

func (o *Owner) Publish(onShutdown func(), issueDashboardURL func() (string, error)) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/runtime/ready", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !o.authorized(request) {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(response).Encode(o.receipt)
	})
	mux.HandleFunc("/v1/runtime/dashboard-url", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !o.authorized(request) {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		if issueDashboardURL == nil {
			http.Error(response, "dashboard unavailable", http.StatusServiceUnavailable)
			return
		}
		dashboardURL, err := issueDashboardURL()
		if err != nil {
			http.Error(response, "dashboard unavailable", http.StatusServiceUnavailable)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(response).Encode(map[string]string{"url": dashboardURL})
	})
	mux.HandleFunc("/v1/runtime/shutdown", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !o.authorized(request) {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		response.WriteHeader(http.StatusAccepted)
		if onShutdown != nil {
			go onShutdown()
		}
	})
	o.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 * 1024,
	}
	go func() {
		_ = o.server.Serve(o.readyListener)
	}()
	if err := writeReceipt(o.root, o.receipt); err != nil {
		return err
	}
	o.published = true
	return nil
}

func (o *Owner) authorized(request *http.Request) bool {
	provided := request.Header.Get("X-Codex-Mux-Instance")
	return len(provided) == len(o.receipt.Instance) && subtle.ConstantTimeCompare([]byte(provided), []byte(o.receipt.Instance)) == 1
}

func (o *Owner) Close() error {
	o.closeOnce.Do(func() {
		var errs []error
		appendCloseError := func(err error) {
			if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
				errs = append(errs, err)
			}
		}
		if o.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			appendCloseError(o.server.Shutdown(ctx))
			cancel()
		} else if o.readyListener != nil {
			appendCloseError(o.readyListener.Close())
		}
		if o.controlListener != nil {
			appendCloseError(o.controlListener.Close())
		}
		if o.bridgeListener != nil {
			appendCloseError(o.bridgeListener.Close())
		}
		if o.published {
			appendCloseError(removeReceipt(o.root, o.receipt.Instance))
		}
		if o.lease != nil {
			appendCloseError(o.lease.Close())
		}
		o.closeErr = errors.Join(errs...)
	})
	return o.closeErr
}

func Probe(ctx context.Context, receipt Receipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+receipt.Address+"/v1/runtime/ready", nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Codex-Mux-Instance", receipt.Instance)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("probe runtime readiness: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("probe runtime readiness: status %s", response.Status)
	}
	var observed Receipt
	if err := json.NewDecoder(response.Body).Decode(&observed); err != nil {
		return fmt.Errorf("decode runtime readiness: %w", err)
	}
	if observed.Instance != receipt.Instance || observed.PID != receipt.PID || observed.Build != receipt.Build {
		return errors.New("runtime readiness identity does not match receipt")
	}
	return nil
}

// RequestDashboardURL asks the exact runtime instance for a fresh one-use URL.
// The URL is returned to the caller only and is never added to the receipt.
func RequestDashboardURL(ctx context.Context, receipt Receipt) (string, error) {
	if err := receipt.Validate(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+receipt.Address+"/v1/runtime/dashboard-url", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("X-Codex-Mux-Instance", receipt.Instance)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request dashboard URL: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request dashboard URL: status %s", response.Status)
	}
	var result struct {
		URL string `json:"url"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return "", fmt.Errorf("decode dashboard URL: %w", err)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Host != receipt.ControlAddress || parsed.User != nil || parsed.RawQuery != "" {
		return "", errors.New("runtime returned an invalid dashboard URL")
	}
	fragment, err := url.ParseQuery(parsed.Fragment)
	if err != nil || fragment.Get("bootstrap") == "" {
		return "", errors.New("runtime returned a dashboard URL without a bootstrap nonce")
	}
	return result.URL, nil
}

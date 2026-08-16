package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/state"
)

const (
	ReceiptSchema = 1
	ReceiptName   = "runtime.json"
)

type Receipt struct {
	Schema         int       `json:"schema"`
	PID            int       `json:"pid"`
	Address        string    `json:"address"`
	ControlAddress string    `json:"controlAddress"`
	BridgeAddress  string    `json:"bridgeAddress"`
	Instance       string    `json:"instance"`
	Build          string    `json:"build"`
	StartedAt      time.Time `json:"startedAt"`
}

func ReceiptPath(root string) string {
	return filepath.Join(root, ReceiptName)
}

func ReadReceipt(root string) (Receipt, error) {
	path := ReceiptPath(root)
	if _, err := os.Stat(path); err != nil {
		return Receipt{}, fmt.Errorf("read runtime receipt: %w", err)
	}
	if err := state.SecureFile(path); err != nil {
		return Receipt{}, fmt.Errorf("secure runtime receipt ACL: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, fmt.Errorf("read runtime receipt: %w", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("decode runtime receipt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func (r Receipt) Validate() error {
	if r.Schema != ReceiptSchema {
		return fmt.Errorf("unsupported runtime receipt schema %d", r.Schema)
	}
	if r.PID <= 0 || r.Instance == "" || r.Build == "" || r.StartedAt.IsZero() {
		return errors.New("runtime receipt is incomplete")
	}
	for label, address := range map[string]string{
		"readiness": r.Address,
		"control":   r.ControlAddress,
		"bridge":    r.BridgeAddress,
	} {
		if err := validateLoopbackAddress(address); err != nil {
			return fmt.Errorf("invalid %s address: %w", label, err)
		}
	}
	return nil
}

func validateLoopbackAddress(address string) error {
	host, portValue, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("address is not numeric loopback")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port is outside 1-65535")
	}
	return nil
}

func writeReceipt(root string, receipt Receipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if err := state.SecureDirectory(root); err != nil {
		return fmt.Errorf("secure runtime directory: %w", err)
	}
	temporary, err := os.CreateTemp(root, ".runtime-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary runtime receipt: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary runtime receipt: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(receipt); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode runtime receipt: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync runtime receipt: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close runtime receipt: %w", err)
	}
	if err := state.SecureFile(temporaryPath); err != nil {
		return fmt.Errorf("secure temporary runtime receipt ACL: %w", err)
	}
	path := ReceiptPath(root)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace stale runtime receipt: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish runtime receipt: %w", err)
	}
	removeTemporary = false
	if err := state.SecureFile(path); err != nil {
		return fmt.Errorf("secure runtime receipt ACL: %w", err)
	}
	return nil
}

func removeReceipt(root, instance string) error {
	receipt, err := ReadReceipt(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("verify runtime receipt before removal: %w", err)
	}
	if receipt.Instance != instance {
		return errors.New("runtime receipt belongs to another instance")
	}
	if err := os.Remove(ReceiptPath(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove runtime receipt: %w", err)
	}
	return nil
}

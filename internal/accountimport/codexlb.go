package accountimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/b-nnett/codex-subscription-router/internal/state"
)

const maxExportBytes = 1 << 20

type Options struct {
	// SourcePaused acknowledges that codex-lb will not independently refresh
	// the copied credential lineage after this migration.
	SourcePaused bool
}

type Result struct {
	AccountID  string
	BackupPath string
}

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	LastRefresh string `json:"last_refresh"`
}

// ImportCodexLBExport installs one canonical Codex auth export into an
// account-owned Codex home. It accepts the current codex-lb export/auth
// envelope, its deprecated auth_json envelope, or a directly exported
// auth.json document. Token material is never included in the result or errors.
func ImportCodexLBExport(accountHome string, raw []byte, options Options) (Result, error) {
	if !options.SourcePaused {
		return Result{}, errors.New("codex-lb must be paused for this account before migration")
	}
	if len(raw) == 0 || len(raw) > maxExportBytes {
		return Result{}, errors.New("codex-lb export is empty or too large")
	}
	if strings.TrimSpace(accountHome) == "" {
		return Result{}, errors.New("account home is required")
	}

	canonical, err := canonicalAuth(raw)
	if err != nil {
		return Result{}, err
	}
	auth, err := parseCodexAuth(canonical)
	if err != nil {
		return Result{}, err
	}

	if err := os.MkdirAll(accountHome, 0o700); err != nil {
		return Result{}, fmt.Errorf("prepare account home: %w", err)
	}
	if err := state.SecureDirectory(accountHome); err != nil {
		return Result{}, fmt.Errorf("secure account home: %w", err)
	}

	formatted := bytes.NewBuffer(nil)
	if err := json.Indent(formatted, canonical, "", "  "); err != nil {
		return Result{}, errors.New("codex-lb export does not contain valid Codex auth")
	}
	formatted.WriteByte('\n')

	temporary, err := os.CreateTemp(accountHome, ".auth-import-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("stage migrated auth: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return Result{}, fmt.Errorf("secure staged auth: %w", err)
	}
	if _, err := temporary.Write(formatted.Bytes()); err != nil {
		temporary.Close()
		return Result{}, fmt.Errorf("stage migrated auth: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return Result{}, fmt.Errorf("flush migrated auth: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Result{}, fmt.Errorf("close migrated auth: %w", err)
	}
	if err := state.SecureFile(temporaryPath); err != nil {
		return Result{}, fmt.Errorf("secure staged auth ACL: %w", err)
	}

	target := filepath.Join(accountHome, "auth.json")
	backupPath, err := replaceWithBackup(target, temporaryPath)
	if err != nil {
		return Result{}, err
	}
	if err := state.SecureFile(target); err != nil {
		return Result{}, fmt.Errorf("secure migrated auth ACL: %w", err)
	}
	return Result{AccountID: auth.Tokens.AccountID, BackupPath: backupPath}, nil
}

// CodexLBAccountID validates an export without writing it. The router uses
// this before mutation so the same credential cannot be imported twice.
func CodexLBAccountID(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > maxExportBytes {
		return "", errors.New("codex-lb export is empty or too large")
	}
	canonical, err := canonicalAuth(raw)
	if err != nil {
		return "", err
	}
	auth, err := parseCodexAuth(canonical)
	if err != nil {
		return "", err
	}
	return auth.Tokens.AccountID, nil
}

// RollbackCodexLBImport restores the pre-migration credential, or removes the
// newly installed credential when no prior auth file existed. Backups are kept
// so the recovery remains inspectable and repeatable.
func RollbackCodexLBImport(accountHome string, result Result) error {
	target := filepath.Join(accountHome, "auth.json")
	if result.BackupPath == "" {
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove failed migrated auth: %w", err)
		}
		return nil
	}
	relative, err := filepath.Rel(accountHome, result.BackupPath)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) || filepath.Base(result.BackupPath) != "auth.json" {
		return errors.New("migration backup path is outside the account home")
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove failed migrated auth: %w", err)
	}
	if err := copyFile(result.BackupPath, target); err != nil {
		return fmt.Errorf("restore auth backup: %w", err)
	}
	if err := state.SecureFile(target); err != nil {
		return fmt.Errorf("secure restored auth ACL: %w", err)
	}
	return nil
}

func canonicalAuth(raw []byte) ([]byte, error) {
	var envelope struct {
		CodexAuthJSON json.RawMessage `json:"codex_auth_json"`
		AuthJSON      string          `json:"auth_json"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, errors.New("codex-lb export is not valid JSON")
	}
	switch {
	case len(envelope.CodexAuthJSON) > 0 && string(envelope.CodexAuthJSON) != "null":
		return envelope.CodexAuthJSON, nil
	case envelope.AuthJSON != "":
		return []byte(envelope.AuthJSON), nil
	default:
		return raw, nil
	}
}

func parseCodexAuth(canonical []byte) (codexAuth, error) {
	var auth codexAuth
	if err := json.Unmarshal(canonical, &auth); err != nil {
		return codexAuth{}, errors.New("codex-lb export does not contain valid Codex auth")
	}
	if auth.AuthMode != "chatgpt" || auth.Tokens.IDToken == "" || auth.Tokens.AccessToken == "" ||
		auth.Tokens.RefreshToken == "" || auth.Tokens.AccountID == "" || auth.LastRefresh == "" {
		return codexAuth{}, errors.New("codex-lb export is missing required Codex auth fields")
	}
	return auth, nil
}

func replaceWithBackup(target, temporary string) (string, error) {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(temporary, target); err != nil {
			return "", fmt.Errorf("install migrated auth: %w", err)
		}
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("inspect existing auth: %w", err)
	}

	backupDirectory := filepath.Join(filepath.Dir(target), "backups", "codex-lb-migration", time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(backupDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create auth backup directory: %w", err)
	}
	if err := state.SecureDirectory(backupDirectory); err != nil {
		return "", fmt.Errorf("secure auth backup directory: %w", err)
	}
	backup := filepath.Join(backupDirectory, "auth.json")
	if err := copyFile(target, backup); err != nil {
		return "", fmt.Errorf("back up existing auth: %w", err)
	}
	if err := state.SecureFile(backup); err != nil {
		return "", fmt.Errorf("secure auth backup ACL: %w", err)
	}
	if err := os.Remove(target); err != nil {
		return "", fmt.Errorf("prepare auth replacement: %w", err)
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = copyFile(backup, target)
		_ = state.SecureFile(target)
		return "", fmt.Errorf("install migrated auth; original restored when possible: %w", err)
	}
	return backup, nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

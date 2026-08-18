package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const stateVersion = 1

const maxAccountCreateKeyLength = 128

type Account struct {
	ID                 string `json:"id"`
	Label              string `json:"label"`
	CodexHome          string `json:"codexHome"`
	Enabled            bool   `json:"enabled"`
	Controller         bool   `json:"controller"`
	LastKnownConnected bool   `json:"lastKnownConnected,omitempty"`
	LastKnownEmail     string `json:"lastKnownEmail,omitempty"`
	LastKnownPlanType  string `json:"lastKnownPlanType,omitempty"`
	CreatedAt          int64  `json:"createdAt"`
}

type persistedState struct {
	Version           int               `json:"version"`
	Accounts          []Account         `json:"accounts"`
	ThreadOwner       map[string]string `json:"threadOwner"`
	AccountCreateKeys map[string]string `json:"accountCreateKeys,omitempty"`
}

// Store persists routing metadata and non-secret account identity metadata.
// OAuth credentials and conversation databases remain inside each account's
// isolated Codex home.
type Store struct {
	mu               sync.RWMutex
	root             string
	path             string
	primaryCodexHome string
	accounts         []Account
	owners           map[string]string
	accountCreateKey map[string]string
}

func Open(root, primaryCodexHome string) (*Store, error) {
	if root == "" {
		return nil, errors.New("state root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create state root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure state root: %w", err)
	}
	if err := SecureDirectory(root); err != nil {
		return nil, fmt.Errorf("secure state root ACL: %w", err)
	}
	if err := os.MkdirAll(primaryCodexHome, 0o700); err != nil {
		return nil, fmt.Errorf("create primary Codex home: %w", err)
	}
	if err := os.Chmod(primaryCodexHome, 0o700); err != nil {
		return nil, fmt.Errorf("secure primary Codex home: %w", err)
	}
	if err := SecureDirectory(primaryCodexHome); err != nil {
		return nil, fmt.Errorf("secure primary Codex home ACL: %w", err)
	}

	store := &Store{
		root:             root,
		path:             filepath.Join(root, "state.json"),
		primaryCodexHome: primaryCodexHome,
		owners:           make(map[string]string),
		accountCreateKey: make(map[string]string),
	}
	if _, statErr := os.Stat(store.path); statErr == nil {
		if err := SecureFile(store.path); err != nil {
			return nil, fmt.Errorf("secure existing state ACL: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect state: %w", statErr)
	}
	data, err := os.ReadFile(store.path)
	switch {
	case err == nil:
		var persisted persistedState
		if err := json.Unmarshal(data, &persisted); err != nil {
			return nil, fmt.Errorf("read state: %w", err)
		}
		if persisted.Version != stateVersion {
			return nil, fmt.Errorf("unsupported state version %d", persisted.Version)
		}
		store.accounts = persisted.Accounts
		if persisted.ThreadOwner != nil {
			store.owners = persisted.ThreadOwner
		}
		if persisted.AccountCreateKeys != nil {
			store.accountCreateKey = persisted.AccountCreateKeys
		}
	case errors.Is(err, os.ErrNotExist):
		store.accounts = []Account{{
			ID:         "primary",
			Label:      "Primary",
			CodexHome:  primaryCodexHome,
			Enabled:    true,
			Controller: true,
			CreatedAt:  time.Now().Unix(),
		}}
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("read state: %w", err)
	}
	for _, account := range store.accounts {
		if samePath(account.CodexHome, primaryCodexHome) {
			continue
		}
		if err := syncIsolatedConfig(primaryCodexHome, account.CodexHome); err != nil {
			return nil, fmt.Errorf("sync account %q config: %w", account.ID, err)
		}
	}
	return store, nil
}

func (s *Store) Root() string {
	return s.root
}

// SyncManagedConfig propagates desktop-managed configuration (including
// plugins, marketplaces, skills, and MCP server definitions) to every
// isolated subscription. Credential stores and project trust remain local to
// each account; syncIsolatedConfig deliberately excludes both.
func (s *Store) SyncManagedConfig() error {
	s.mu.RLock()
	accounts := slices.Clone(s.accounts)
	primaryCodexHome := s.primaryCodexHome
	s.mu.RUnlock()

	for _, account := range accounts {
		if samePath(account.CodexHome, primaryCodexHome) {
			continue
		}
		if err := syncIsolatedConfig(primaryCodexHome, account.CodexHome); err != nil {
			return fmt.Errorf("sync account %q config: %w", account.ID, err)
		}
	}
	return nil
}

func (s *Store) Accounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.accounts)
}

func (s *Store) Account(id string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.accounts {
		if account.ID == id {
			return account, true
		}
	}
	return Account{}, false
}

func (s *Store) Controller() (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, account := range s.accounts {
		if account.Controller {
			return account, true
		}
	}
	if len(s.accounts) == 0 {
		return Account{}, false
	}
	return s.accounts[0], true
}

func (s *Store) AddAccount(label string) (Account, error) {
	account, _, err := s.AddAccountIdempotent(label, "")
	return account, err
}

// AddAccountIdempotent returns the account already associated with key. Empty
// keys preserve the original always-create behavior.
func (s *Store) AddAccountIdempotent(label, key string) (Account, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(key) > maxAccountCreateKeyLength {
		return Account{}, false, fmt.Errorf("account creation key exceeds %d bytes", maxAccountCreateKeyLength)
	}
	if key != "" {
		if accountID := s.accountCreateKey[key]; accountID != "" {
			for _, account := range s.accounts {
				if account.ID == accountID {
					return account, false, nil
				}
			}
			return Account{}, false, fmt.Errorf("account creation key refers to missing account %q", accountID)
		}
	}

	label = strings.TrimSpace(label)
	if label == "" {
		label = "OpenAI account"
	}
	id, err := randomID()
	if err != nil {
		return Account{}, false, err
	}
	codexHome := filepath.Join(s.root, "accounts", id, "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return Account{}, false, fmt.Errorf("create account home: %w", err)
	}
	if err := os.Chmod(codexHome, 0o700); err != nil {
		return Account{}, false, fmt.Errorf("secure account home: %w", err)
	}
	if err := syncIsolatedConfig(s.primaryCodexHome, codexHome); err != nil {
		return Account{}, false, fmt.Errorf("write account config: %w", err)
	}

	account := Account{
		ID:        id,
		Label:     label,
		CodexHome: codexHome,
		Enabled:   true,
		CreatedAt: time.Now().Unix(),
	}
	s.accounts = append(s.accounts, account)
	if key != "" {
		s.accountCreateKey[key] = account.ID
	}
	if err := s.saveLocked(); err != nil {
		s.accounts = s.accounts[:len(s.accounts)-1]
		delete(s.accountCreateKey, key)
		return Account{}, false, err
	}
	return account, true, nil
}

// RemoveAccount removes non-controller routing metadata and moves its isolated
// home into a recoverable removed-accounts directory.
func (s *Store) RemoveAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	var account Account
	for candidateIndex, candidate := range s.accounts {
		if candidate.ID == id {
			index = candidateIndex
			account = candidate
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("account %q not found", id)
	}
	if account.Controller {
		return errors.New("controller account cannot be removed")
	}

	accountsBefore := slices.Clone(s.accounts)
	ownersBefore := maps.Clone(s.owners)
	keysBefore := maps.Clone(s.accountCreateKey)
	archivePath := ""
	if !samePath(account.CodexHome, s.primaryCodexHome) {
		accountsRoot := filepath.Join(s.root, "accounts")
		relativeHome, err := filepath.Rel(accountsRoot, account.CodexHome)
		if err != nil || relativeHome == "." || relativeHome == ".." || strings.HasPrefix(relativeHome, ".."+string(filepath.Separator)) {
			return fmt.Errorf("refuse to archive account home outside managed accounts root: %q", account.CodexHome)
		}
		if _, err := os.Stat(account.CodexHome); err == nil {
			removedRoot := filepath.Join(s.root, "removed-accounts")
			if err := os.MkdirAll(removedRoot, 0o700); err != nil {
				return fmt.Errorf("create removed account archive: %w", err)
			}
			archivePath = filepath.Join(removedRoot, fmt.Sprintf("%s-%d", account.ID, time.Now().UnixNano()))
			if err := os.Rename(account.CodexHome, archivePath); err != nil {
				return fmt.Errorf("archive removed account home: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect removed account home: %w", err)
		}
	}
	s.accounts = append(s.accounts[:index], s.accounts[index+1:]...)
	for threadID, accountID := range s.owners {
		if accountID == id {
			delete(s.owners, threadID)
		}
	}
	for key, accountID := range s.accountCreateKey {
		if accountID == id {
			delete(s.accountCreateKey, key)
		}
	}
	if err := s.saveLocked(); err != nil {
		s.accounts = accountsBefore
		s.owners = ownersBefore
		s.accountCreateKey = keysBefore
		if archivePath != "" {
			_ = os.Rename(archivePath, account.CodexHome)
		}
		return err
	}
	return nil
}

func (s *Store) UpdateAccount(id string, label *string, enabled *bool) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID != id {
			continue
		}
		if label != nil {
			trimmed := strings.TrimSpace(*label)
			if trimmed == "" {
				return Account{}, errors.New("account label cannot be empty")
			}
			s.accounts[index].Label = trimmed
		}
		if enabled != nil {
			s.accounts[index].Enabled = *enabled
		}
		if err := s.saveLocked(); err != nil {
			return Account{}, err
		}
		return s.accounts[index], nil
	}
	return Account{}, fmt.Errorf("account %q not found", id)
}

func (s *Store) SetAccountConnected(id string, connected bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID != id {
			continue
		}
		if s.accounts[index].LastKnownConnected == connected {
			return nil
		}
		s.accounts[index].LastKnownConnected = connected
		return s.saveLocked()
	}
	return fmt.Errorf("account %q not found", id)
}

func (s *Store) SetAccountIdentity(id, email, planType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID != id {
			continue
		}
		changed := false
		if email != "" && s.accounts[index].LastKnownEmail != email {
			s.accounts[index].LastKnownEmail = email
			changed = true
		}
		if planType != "" && s.accounts[index].LastKnownPlanType != planType {
			s.accounts[index].LastKnownPlanType = planType
			changed = true
		}
		if !changed {
			return nil
		}
		return s.saveLocked()
	}
	return fmt.Errorf("account %q not found", id)
}

func (s *Store) ThreadOwner(threadID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	owner, ok := s.owners[threadID]
	return owner, ok
}

func (s *Store) SetThreadOwner(threadID, accountID string) error {
	if threadID == "" || accountID == "" {
		return errors.New("thread and account IDs are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owners[threadID] == accountID {
		return nil
	}
	s.owners[threadID] = accountID
	return s.saveLocked()
}

func (s *Store) ThreadCounts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[string]int)
	for _, accountID := range s.owners {
		counts[accountID]++
	}
	return counts
}

func (s *Store) saveLocked() error {
	persisted := persistedState{
		Version:           stateVersion,
		Accounts:          s.accounts,
		ThreadOwner:       s.owners,
		AccountCreateKeys: s.accountCreateKey,
	}
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return fmt.Errorf("secure state: %w", err)
	}
	if err := SecureFile(temporary); err != nil {
		return fmt.Errorf("secure state ACL: %w", err)
	}
	if err := os.Rename(temporary, s.path); err != nil {
		return fmt.Errorf("commit state: %w", err)
	}
	return nil
}

func randomID() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate account ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

package mux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/b-nnett/codex-subscription-router/internal/protocol"
)

// InitializeStandalone prepares the account pool when the daemon is running
// without an attached stdio host. A later bridge may initialize again with its
// own client contract; the most recent successful contract remains the restart
// replay source.
func (m *Multiplexer) InitializeStandalone(ctx context.Context, params json.RawMessage) error {
	if len(params) == 0 {
		return errors.New("standalone initialization parameters are required")
	}
	m.initializationMu.Lock()
	m.initializeParams = append(json.RawMessage(nil), params...)
	m.initialized = false
	m.initializationMu.Unlock()

	var errs []error
	initialized := 0
	for _, entry := range m.childEntries() {
		response, err := entry.child.Request(ctx, "initialize", params)
		if err != nil {
			errs = append(errs, fmt.Errorf("initialize account %q: %w", entry.account.ID, err))
			continue
		}
		if len(response.Result) == 0 {
			errs = append(errs, fmt.Errorf("initialize account %q: empty response", entry.account.ID))
			continue
		}
		if err := entry.child.Send(protocol.Message{Method: "initialized"}); err != nil {
			errs = append(errs, fmt.Errorf("notify account %q initialized: %w", entry.account.ID, err))
			continue
		}
		initialized++
	}
	if initialized == 0 {
		return errors.Join(errs...)
	}
	m.initializationMu.Lock()
	m.initialized = true
	m.initializationMu.Unlock()
	return errors.Join(errs...)
}

package mux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/b-nnett/codex-subscription-router/internal/accountimport"
)

type AccountImportResult struct {
	Account         AccountSnapshot `json:"account"`
	SourceAccountID string          `json:"sourceAccountId"`
	BackupAvailable bool            `json:"backupAvailable"`
}

func (m *Multiplexer) ImportCodexLBExport(ctx context.Context, accountID string, export json.RawMessage, sourcePaused bool) (AccountImportResult, error) {
	account, ok := m.store.Account(accountID)
	if !ok {
		return AccountImportResult{}, fmt.Errorf("account %q not found", accountID)
	}
	if len(export) == 0 {
		return AccountImportResult{}, errors.New("codex-lb export is required")
	}
	if !sourcePaused {
		return AccountImportResult{}, errors.New("codex-lb must be paused for this account before migration")
	}
	if err := m.stopChild(accountID); err != nil {
		return AccountImportResult{}, fmt.Errorf("pause destination account before import: %w", err)
	}
	result, err := accountimport.ImportCodexLBExport(account.CodexHome, export, accountimport.Options{SourcePaused: sourcePaused})
	if err != nil {
		_, restartErr := m.startChild(ctx, account)
		return AccountImportResult{}, errors.Join(err, restartErr)
	}
	recoverImport := func(cause error) error {
		rollbackErr := accountimport.RollbackCodexLBImport(account.CodexHome, result)
		_, restartErr := m.startChild(ctx, account)
		return errors.Join(cause, rollbackErr, restartErr)
	}
	if _, err := m.startChild(ctx, account); err != nil {
		return AccountImportResult{}, recoverImport(fmt.Errorf("start imported account: %w", err))
	}
	snapshot, err := m.accountSnapshot(ctx, accountID)
	if err != nil {
		_ = m.stopChild(accountID)
		return AccountImportResult{}, recoverImport(fmt.Errorf("verify imported account: %w", err))
	}
	if !snapshot.Connected {
		_ = m.stopChild(accountID)
		return AccountImportResult{}, recoverImport(errors.New("imported credential did not produce a connected subscription"))
	}
	return AccountImportResult{
		Account: snapshot, SourceAccountID: result.AccountID, BackupAvailable: result.BackupPath != "",
	}, nil
}

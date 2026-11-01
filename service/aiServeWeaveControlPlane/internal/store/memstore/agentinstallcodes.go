package memstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateAgentInstallCode implements store.AgentInstallCodes.
//
// CreateAgentInstallCode 实现 store.AgentInstallCodes。
func (s *Store) CreateAgentInstallCode(_ context.Context, row *model.AgentInstallCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.installCodes[row.CodeHash]; exists {
		return store.ErrConflict
	}
	s.installCodes[row.CodeHash] = *row
	return nil
}

// RedeemAgentInstallCode implements store.AgentInstallCodes.
//
// RedeemAgentInstallCode 实现 store.AgentInstallCodes。
func (s *Store) RedeemAgentInstallCode(_ context.Context, codeHash string, now time.Time) (model.AgentInstallCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.installCodes[codeHash]
	if !ok || row.RedeemedAt != nil || !row.ExpiresAt.After(now) {
		return model.AgentInstallCode{}, store.ErrNotFound
	}
	row.RedeemedAt = &now
	s.installCodes[codeHash] = row
	return row, nil
}

// UnredeemAgentInstallCode implements store.AgentInstallCodes.
//
// UnredeemAgentInstallCode 实现 store.AgentInstallCodes。
func (s *Store) UnredeemAgentInstallCode(_ context.Context, codeHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.installCodes[codeHash]; ok {
		row.RedeemedAt = nil
		s.installCodes[codeHash] = row
	}
	return nil
}

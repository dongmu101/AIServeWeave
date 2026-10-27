package memstore

import (
	"context"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateResponseTurn implements store.ResponseTurns.
func (s *Store) CreateResponseTurn(_ context.Context, turn *model.ResponseTurn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.responseTurns[turn.ID]; exists {
		return store.ErrConflict
	}
	s.responseTurns[turn.ID] = *turn
	return nil
}

// GetResponseTurn implements store.ResponseTurns.
func (s *Store) GetResponseTurn(_ context.Context, tenantID, id string) (model.ResponseTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn, ok := s.responseTurns[id]
	if !ok || turn.TenantID != tenantID {
		return model.ResponseTurn{}, store.ErrNotFound
	}
	return turn, nil
}

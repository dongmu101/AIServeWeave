package memstore

import (
	"context"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// GetArkCredential implements store.ArkCredentials.
//
// GetArkCredential 实现 store.ArkCredentials。
func (s *Store) GetArkCredential(_ context.Context, scope string) (model.ArkCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.arkCredentials[scope]
	if !ok {
		return model.ArkCredential{}, store.ErrNotFound
	}
	return row, nil
}

// PutArkCredential implements store.ArkCredentials.
//
// PutArkCredential 实现 store.ArkCredentials。
func (s *Store) PutArkCredential(_ context.Context, row *model.ArkCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.arkCredentials == nil {
		s.arkCredentials = map[string]model.ArkCredential{}
	}
	row.Version = 1
	if existing, ok := s.arkCredentials[row.Scope]; ok {
		row.CreatedAt = existing.CreatedAt
		row.Version = existing.Version + 1
	}
	s.arkCredentials[row.Scope] = *row
	return nil
}

// DeleteArkCredential implements store.ArkCredentials.
//
// DeleteArkCredential 实现 store.ArkCredentials。
func (s *Store) DeleteArkCredential(_ context.Context, scope string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.arkCredentials[scope]; !ok {
		return store.ErrNotFound
	}
	delete(s.arkCredentials, scope)
	return nil
}

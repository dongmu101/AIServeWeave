package memstore

import (
	"context"
	"sort"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// nodeProviderKey identifies one provider row.
// nodeProviderKey 标识一行供应商。
type nodeProviderKey struct{ nodeID, runtimeID string }

// ListNodeProviders implements store.NodeProviders.
//
// ListNodeProviders 实现 store.NodeProviders。
func (s *Store) ListNodeProviders(_ context.Context, nodeID string) ([]model.NodeProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []model.NodeProvider{}
	for key, row := range s.nodeProviders {
		if key.nodeID == nodeID {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RuntimeID < rows[j].RuntimeID })
	return rows, nil
}

// PutNodeProvider implements store.NodeProviders.
//
// PutNodeProvider 实现 store.NodeProviders。
func (s *Store) PutNodeProvider(_ context.Context, row *model.NodeProvider) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nodeProviderKey{row.NodeID, row.RuntimeID}
	if existing, ok := s.nodeProviders[key]; ok {
		row.CreatedAt = existing.CreatedAt
	}
	s.nodeProviders[key] = *row
	return s.bumpNodeProviderVersion(row.NodeID, row.UpdatedAt), nil
}

// DeleteNodeProvider implements store.NodeProviders.
//
// DeleteNodeProvider 实现 store.NodeProviders。
func (s *Store) DeleteNodeProvider(_ context.Context, nodeID, runtimeID string, at time.Time) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nodeProviderKey{nodeID, runtimeID}
	if _, ok := s.nodeProviders[key]; !ok {
		return 0, store.ErrNotFound
	}
	delete(s.nodeProviders, key)
	return s.bumpNodeProviderVersion(nodeID, at), nil
}

// bumpNodeProviderVersion increments nodeID's version; s.mu must be held.
// bumpNodeProviderVersion 把 nodeID 的版本加一；调用方必须持有 s.mu。
func (s *Store) bumpNodeProviderVersion(nodeID string, at time.Time) uint64 {
	row := s.nodeProviderVersions[nodeID]
	row.NodeID = nodeID
	row.Version++
	row.UpdatedAt = at
	s.nodeProviderVersions[nodeID] = row
	return row.Version
}

// NodeProviderVersion implements store.NodeProviders.
//
// NodeProviderVersion 实现 store.NodeProviders。
func (s *Store) NodeProviderVersion(_ context.Context, nodeID string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nodeProviderVersions[nodeID].Version, nil
}

// ListNodeProviderVersions implements store.NodeProviders.
//
// ListNodeProviderVersions 实现 store.NodeProviders。
func (s *Store) ListNodeProviderVersions(_ context.Context) ([]model.NodeProviderVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]model.NodeProviderVersion, 0, len(s.nodeProviderVersions))
	for _, row := range s.nodeProviderVersions {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].NodeID < rows[j].NodeID })
	return rows, nil
}

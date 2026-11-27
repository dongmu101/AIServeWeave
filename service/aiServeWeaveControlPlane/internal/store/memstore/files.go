package memstore

import (
	"context"
	"sort"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// RegisterFile implements store.Files.
//
// RegisterFile 实现 store.Files。
func (s *Store) RegisterFile(_ context.Context, f *model.File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string]model.File{}
	}
	if existing, ok := s.files[f.ID]; ok {
		f.CreatedAt = existing.CreatedAt
	}
	s.files[f.ID] = *f
	return nil
}

// CompleteFile implements store.Files.
//
// CompleteFile 实现 store.Files。
func (s *Store) CompleteFile(_ context.Context, id string, done store.FileCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.files[id]
	if !ok {
		return store.ErrNotFound
	}
	f.SizeBytes, f.SHA256, f.ContentType = done.SizeBytes, done.SHA256, done.ContentType
	f.Status, f.UpdatedAt = model.FileStatusReady, done.UpdatedAt
	s.files[id] = f
	return nil
}

// DeleteFile implements store.Files.
//
// DeleteFile 实现 store.Files。
func (s *Store) DeleteFile(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, id)
	return nil
}

// ListFiles implements store.Files.
//
// ListFiles 实现 store.Files。
func (s *Store) ListFiles(_ context.Context, filter store.FileFilter) ([]model.File, error) {
	limit := filter.Limit
	if limit <= 0 || limit > store.MaxFilePage {
		limit = store.MaxFilePage
	}
	match := func(want, got string) bool { return want == "" || want == got }
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.File{}
	for _, f := range s.files {
		if f.CreatedAt.Before(filter.Before) && match(filter.Storage, f.Storage) && match(filter.TenantID, f.TenantID) &&
			match(filter.BizType, f.BizType) && match(filter.Kind, f.Kind) && match(filter.Status, f.Status) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

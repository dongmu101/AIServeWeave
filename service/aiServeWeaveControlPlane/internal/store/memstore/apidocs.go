package memstore

import (
	"context"
	"sort"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/apidocs"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ListAPIDocs implements store.APIDocs. / ListAPIDocs 实现 store.APIDocs。
func (s *Store) ListAPIDocs(_ context.Context, publishedOnly bool) ([]model.APIDoc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.APIDoc, 0, len(s.apiDocs))
	for _, doc := range s.apiDocs {
		if publishedOnly && doc.Status != apidocs.StatusPublished {
			continue
		}
		doc.ContentJSON = ""
		out = append(out, doc)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.CategoryOrder != b.CategoryOrder {
			return a.CategoryOrder < b.CategoryOrder
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return a.Slug < b.Slug
	})
	return out, nil
}

// GetAPIDoc implements store.APIDocs. / GetAPIDoc 实现 store.APIDocs。
func (s *Store) GetAPIDoc(_ context.Context, slug string) (model.APIDoc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.apiDocs[slug]
	if !ok {
		return model.APIDoc{}, store.ErrNotFound
	}
	return doc, nil
}

// CreateAPIDoc implements store.APIDocs. / CreateAPIDoc 实现 store.APIDocs。
func (s *Store) CreateAPIDoc(_ context.Context, doc *model.APIDoc, audit *model.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apiDocs[doc.Slug]; ok {
		return store.ErrConflict
	}
	s.apiDocs[doc.Slug] = *doc
	s.audit = append(s.audit, *audit)
	return nil
}

// UpdateAPIDoc implements store.APIDocs. / UpdateAPIDoc 实现 store.APIDocs。
func (s *Store) UpdateAPIDoc(_ context.Context, doc *model.APIDoc, expected int64, audit *model.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.apiDocCASLocked(doc.Slug, expected); err != nil {
		return err
	}
	doc.Version = expected + 1
	s.apiDocs[doc.Slug] = *doc
	s.audit = append(s.audit, *audit)
	return nil
}

// DeleteAPIDoc implements store.APIDocs. / DeleteAPIDoc 实现 store.APIDocs。
func (s *Store) DeleteAPIDoc(_ context.Context, slug string, expected int64, audit *model.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.apiDocCASLocked(slug, expected); err != nil {
		return err
	}
	delete(s.apiDocs, slug)
	s.audit = append(s.audit, *audit)
	return nil
}

func (s *Store) apiDocCASLocked(slug string, expected int64) error {
	current, ok := s.apiDocs[slug]
	if !ok {
		return store.ErrNotFound
	}
	if current.Version != expected {
		return store.ErrConflict
	}
	return nil
}

// SeedAPIDoc implements store.APIDocs. / SeedAPIDoc 实现 store.APIDocs。
func (s *Store) SeedAPIDoc(_ context.Context, doc *model.APIDoc) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.apiDocs[doc.Slug]
	if !ok {
		s.apiDocs[doc.Slug] = *doc
		return true, nil
	}
	if current.Source != apidocs.SourceSeed || current.SeedRevision >= doc.SeedRevision {
		return false, nil
	}
	refreshed := *doc
	refreshed.Status = current.Status
	refreshed.Source = apidocs.SourceSeed
	refreshed.Version = current.Version + 1
	refreshed.UpdatedBy = ""
	s.apiDocs[doc.Slug] = refreshed
	return true, nil
}

package memstore

import (
	"context"

	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CurrentRouteRevision reads the published snapshot. / CurrentRouteRevision 读取已发布快照。
func (s *Store) CurrentRouteRevision(_ context.Context) (model.RouteRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.routes) == 0 {
		return model.RouteRevision{}, store.ErrNotFound
	}
	return s.routes[len(s.routes)-1], nil
}

// GetRouteRevision reads an immutable revision. / GetRouteRevision 读取不可变版本。
func (s *Store) GetRouteRevision(_ context.Context, revision int64) (model.RouteRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision < 1 || revision > int64(len(s.routes)) {
		return model.RouteRevision{}, store.ErrNotFound
	}
	return s.routes[revision-1], nil
}

// ListRouteRevisions reads bounded history, newest first. / ListRouteRevisions 有界读取历史，新版本在前。
func (s *Store) ListRouteRevisions(_ context.Context, before int64, limit int) ([]model.RouteRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.RouteRevision{}
	if limit < 1 || limit > 51 {
		limit = 51
	}
	for i := len(s.routes) - 1; i >= 0 && len(out) < limit; i-- {
		if before == 0 || s.routes[i].Revision < before {
			out = append(out, s.routes[i])
		}
	}
	return out, nil
}

// PublishRouteRevision atomically checks the pointer, appends history and audits. / PublishRouteRevision 原子检查指针、追加历史并记审计。
func (s *Store) PublishRouteRevision(_ context.Context, expected int64, row *model.RouteRevision, audit *model.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != int64(len(s.routes)) {
		return store.ErrConflict
	}
	if len(s.routes) >= modelroute.MaxRevisions {
		return store.ErrRouteCapacity
	}
	row.Revision = expected + 1
	s.routes = append(s.routes, *row)
	s.audit = append(s.audit, *audit)
	return nil
}

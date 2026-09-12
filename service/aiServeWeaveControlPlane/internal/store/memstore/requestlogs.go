package memstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateRequestLogs implements store.RequestLogs, mirroring CreateJob's
// duplicate-id handling except that a duplicate here is a silent skip
// (idempotent batch push), never store.ErrConflict — nothing downstream
// reads back a single created record the way CreateJob's caller does.
//
// CreateRequestLogs 实现 store.RequestLogs，其对重复 id 的处理方式与
// CreateJob 相仿，只是这里重复是静默跳过（幂等的批量推送），而不是
// store.ErrConflict——不像 CreateJob 那样，没有下游会读回单条创建结果。
func (s *Store) CreateRequestLogs(_ context.Context, records []model.RequestLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range records {
		if _, exists := s.requestLogs[r.ID]; exists {
			continue
		}
		s.requestLogs[r.ID] = r
	}
	return nil
}

// ListRequestLogs implements store.RequestLogs.
func (s *Store) ListRequestLogs(_ context.Context, query store.ListQuery, filter store.RequestLogFilter) (store.Page[model.RequestLog], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.RequestLog
	for _, r := range s.requestLogs {
		if filter.TenantID != "" && r.TenantID != filter.TenantID {
			continue
		}
		if filter.RequestID != "" && r.ID != filter.RequestID {
			continue
		}
		if filter.Outcome != "" && r.Outcome != filter.Outcome {
			continue
		}
		if !filter.Since.IsZero() && r.CreatedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !r.CreatedAt.Before(filter.Until) {
			continue
		}
		out = append(out, r)
	}
	sortNewestFirst(out, func(r model.RequestLog) (time.Time, string) { return r.CreatedAt, r.ID })
	return paginate(out, query, func(r model.RequestLog) (time.Time, string) { return r.CreatedAt, r.ID })
}

// DeleteRequestLogsBefore implements store.RequestLogs.
func (s *Store) DeleteRequestLogsBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	for id, r := range s.requestLogs {
		if r.CreatedAt.Before(before) {
			delete(s.requestLogs, id)
			removed++
		}
	}
	return removed, nil
}

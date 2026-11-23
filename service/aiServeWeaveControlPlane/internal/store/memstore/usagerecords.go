package memstore

import (
	"context"
	"sort"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateUsageRecords implements store.UsageRecords, mirroring
// CreateRequestLogs's duplicate-id handling exactly: a duplicate is a
// silent skip (idempotent batch push), never store.ErrConflict.
func (s *Store) CreateUsageRecords(_ context.Context, records []model.UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range records {
		if _, exists := s.usageRecords[r.ID]; exists {
			continue
		}
		s.usageRecords[r.ID] = r
	}
	return nil
}

// SummarizeUsage implements store.UsageRecords, aggregating in Go over the
// in-memory map — the equivalent of gormstore's SQL GROUP BY, since this
// store has no database to push the aggregation into.
func (s *Store) SummarizeUsage(_ context.Context, filter store.UsageRecordFilter) ([]store.UsageSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type key struct{ tenantID, userID, keyID, model string }
	totals := map[key]*store.UsageSummary{}
	for _, r := range s.usageRecords {
		if filter.TenantID != "" && r.TenantID != filter.TenantID {
			continue
		}
		if !filter.Since.IsZero() && r.CreatedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !r.CreatedAt.Before(filter.Until) {
			continue
		}
		k := key{tenantID: r.TenantID, model: r.Model}
		if filter.ByKey {
			k.userID, k.keyID = r.UserID, r.KeyID
		}
		agg, ok := totals[k]
		if !ok {
			agg = &store.UsageSummary{TenantID: k.tenantID, UserID: k.userID, KeyID: k.keyID, Model: k.model}
			totals[k] = agg
		}
		agg.PromptTokens += r.PromptTokens
		agg.CompletionTokens += r.CompletionTokens
		agg.TotalTokens += r.TotalTokens
		agg.RequestCount++
		if filter.ByKey && r.CreatedAt.After(agg.LastUsedAt) {
			agg.LastUsedAt = r.CreatedAt
		}
		agg.CacheReadTokens += r.CacheReadTokens
		agg.CacheWriteTokens += r.CacheWriteTokens
		agg.CostMicro += r.CostMicro
	}
	out := make([]store.UsageSummary, 0, len(totals))
	for _, agg := range totals {
		out = append(out, *agg)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.KeyID != b.KeyID {
			return a.KeyID < b.KeyID
		}
		return a.Model < b.Model
	})
	return out, nil
}

// ListUsageRecords implements store.UsageRecords.
func (s *Store) ListUsageRecords(_ context.Context, query store.ListQuery, filter store.UsageRecordListFilter) (store.Page[model.UsageRecord], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.UsageRecord
	for _, r := range s.usageRecords {
		if (filter.TenantID != "" && r.TenantID != filter.TenantID) || (filter.Model != "" && r.Model != filter.Model) ||
			(filter.KeyID != "" && r.KeyID != filter.KeyID) {
			continue
		}
		if (!filter.Since.IsZero() && r.CreatedAt.Before(filter.Since)) || (!filter.Until.IsZero() && !r.CreatedAt.Before(filter.Until)) {
			continue
		}
		out = append(out, r)
	}
	sortNewestFirst(out, func(r model.UsageRecord) (time.Time, string) { return r.CreatedAt, r.ID })
	return paginate(out, query, func(r model.UsageRecord) (time.Time, string) { return r.CreatedAt, r.ID })
}

// CountUsageRecordsBySource implements store.UsageRecords.
func (s *Store) CountUsageRecordsBySource(_ context.Context, source string, since time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, r := range s.usageRecords {
		if r.PriceSource == source && !r.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

// DeleteUsageRecordsBefore implements store.UsageRecords.
func (s *Store) DeleteUsageRecordsBefore(_ context.Context, before time.Time) (store.RetentionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	for id, r := range s.usageRecords {
		if r.CreatedAt.Before(before) {
			delete(s.usageRecords, id)
			removed++
		}
	}
	return store.RetentionResult{RowsDeleted: removed}, nil
}

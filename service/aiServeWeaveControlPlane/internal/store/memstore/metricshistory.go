package memstore

import (
	"context"
	"sort"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// UpsertRollup implements store.MetricsHistory.
func (s *Store) UpsertRollup(_ context.Context, points []model.MetricsHistoryPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, point := range points {
		replaced := false
		for i, existing := range s.metricsHistory {
			if existing.Metric == point.Metric && existing.Labels == point.Labels && existing.BucketAt.Equal(point.BucketAt) {
				s.metricsHistory[i].Value = point.Value
				replaced = true
				break
			}
		}
		if !replaced {
			s.metricsHistory = append(s.metricsHistory, point)
		}
	}
	return nil
}

// ListRollup implements store.MetricsHistory.
func (s *Store) ListRollup(_ context.Context, metrics []string, since, until time.Time) ([]model.MetricsHistoryPoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]bool, len(metrics))
	for _, m := range metrics {
		want[m] = true
	}
	var out []model.MetricsHistoryPoint
	for _, point := range s.metricsHistory {
		if !want[point.Metric] {
			continue
		}
		if point.BucketAt.Before(since) || !point.BucketAt.Before(until) {
			continue
		}
		out = append(out, point)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BucketAt.Before(out[j].BucketAt) })
	return out, nil
}

// DeleteRollupBefore implements store.MetricsHistory.
func (s *Store) DeleteRollupBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.metricsHistory[:0]
	var removed int64
	for _, point := range s.metricsHistory {
		if point.BucketAt.Before(before) {
			removed++
			continue
		}
		kept = append(kept, point)
	}
	s.metricsHistory = kept
	return removed, nil
}

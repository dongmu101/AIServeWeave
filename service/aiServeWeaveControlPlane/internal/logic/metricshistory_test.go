package logic_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

func TestListMetricsHistoryRejectsInvertedWindow(t *testing.T) {
	f := newFixture(t)
	since := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	until := since.Add(-time.Hour)

	if _, err := f.svc.ListMetricsHistory(context.Background(), since, until); err == nil {
		t.Fatal("ListMetricsHistory() error = nil, want error for until before since")
	}
}

func TestListMetricsHistoryReturnsStoredPoints(t *testing.T) {
	f := newFixture(t)
	since := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	if err := f.store.UpsertRollup(context.Background(), []model.MetricsHistoryPoint{
		{Metric: logic.HistoryMetricNames[0], Labels: "", BucketAt: since.Add(time.Hour), Value: 42},
	}); err != nil {
		t.Fatalf("seeding UpsertRollup() error = %v", err)
	}

	points, err := f.svc.ListMetricsHistory(context.Background(), since, until)
	if err != nil {
		t.Fatalf("ListMetricsHistory() error = %v", err)
	}
	if len(points) != 1 || points[0].Value != 42 {
		t.Fatalf("ListMetricsHistory() = %+v, want one point with value 42", points)
	}
}

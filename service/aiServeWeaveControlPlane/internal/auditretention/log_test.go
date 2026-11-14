package auditretention_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/auditretention"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func TestRunOnceLogsTableCutoffPartitionsAndRows(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	clock := fakeClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
	r := auditretention.New(&fakeStore{toReturn: store.RetentionResult{PartitionsDropped: 2, RowsDeleted: 10}}, time.Hour, clock, logger)
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"table=audit_logs", "before=2026-11-13T23:00:00", "partitions_dropped=2", "rows_deleted=10"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log %q lacks %q", buf.String(), want)
		}
	}
}

func TestRunOnceStaysQuietWhenNothingExpired(t *testing.T) {
	var buf bytes.Buffer
	r := auditretention.New(&fakeStore{}, time.Hour, fakeClock{now: time.Now()}, slog.New(slog.NewTextHandler(&buf, nil)))
	if err := r.RunOnce(context.Background()); err != nil || buf.Len() != 0 {
		t.Errorf("RunOnce = %v, log %q; want silence", err, buf.String())
	}
}

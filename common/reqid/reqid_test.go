package reqid_test

import (
	"context"
	"testing"

	"AIServeWeave/common/reqid"
)

func TestWithValueAndFromContext(t *testing.T) {
	ctx := reqid.WithValue(context.Background(), "abc123")
	if got := reqid.FromContext(ctx); got != "abc123" {
		t.Errorf("FromContext() = %q, want %q", got, "abc123")
	}
}

func TestFromContextEmptyWhenUnset(t *testing.T) {
	if got := reqid.FromContext(context.Background()); got != "" {
		t.Errorf("FromContext() = %q, want empty", got)
	}
}

func TestNewProducesDistinctHexIDs(t *testing.T) {
	a, b := reqid.New(), reqid.New()
	if a == b {
		t.Fatalf("New() produced the same id twice: %q", a)
	}
	if len(a) != 32 {
		t.Errorf("len(New()) = %d, want 32 (16 bytes hex-encoded)", len(a))
	}
}

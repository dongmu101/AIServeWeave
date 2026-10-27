package httpapi

import (
	"context"
	"testing"

	"AIServeWeave/common/reqid"
)

func TestWithRequestIDIsReadableViaSharedPackage(t *testing.T) {
	ctx := withRequestID(context.Background(), "req-1")
	if got := reqid.FromContext(ctx); got != "req-1" {
		t.Errorf("reqid.FromContext() = %q, want %q — httpapi must use the shared context key so tunnelserver can read the same id", got, "req-1")
	}
	if got := requestIDFrom(ctx); got != "req-1" {
		t.Errorf("requestIDFrom() = %q, want %q", got, "req-1")
	}
}

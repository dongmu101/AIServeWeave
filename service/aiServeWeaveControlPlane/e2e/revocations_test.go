package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

type fixedRevocations int64

// WatchGeneration returns the fixed route-test generation.
//
// WatchGeneration 返回路由测试使用的固定 generation。
func (f fixedRevocations) WatchGeneration(context.Context, int64, time.Duration) (int64, error) {
	return int64(f), nil
}

// TestRevocationWatchRouteAndGuard pins the real go-zero route and its
// InternalToken guard, not just the handler in isolation.
//
// TestRevocationWatchRouteAndGuard 固定真实 go-zero 路由及其 InternalToken 守卫，
// 而不只是在隔离状态下测试 handler。
func TestRevocationWatchRouteAndGuard(t *testing.T) {
	h := newHarness(t)
	h.svcCtx.Revocations = fixedRevocations(7)

	var got types.RevocationGenerationResponse
	status := h.call(http.MethodGet, "/internal/v1/apikeys/revocations/watch?after=6", internalToken, nil, &got)
	if status != http.StatusOK || got.Generation != 7 {
		t.Fatalf("watch = (status %d, generation %d), want (%d, 7)", status, got.Generation, http.StatusOK)
	}
	status = h.call(http.MethodGet, "/internal/v1/apikeys/revocations/watch?after=6", "wrong-token", nil, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("wrong-token status = %d, want %d", status, http.StatusUnauthorized)
	}
}

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

const revocationTestToken = "internal-token-value-at-least-32-bytes"

type fakeRevocationSource struct {
	generation int64
	err        error
	after      int64
	wait       time.Duration
	called     bool
}

// WatchGeneration records the handler arguments and returns the scripted result.
//
// WatchGeneration 记录 handler 参数并返回脚本化结果。
func (f *fakeRevocationSource) WatchGeneration(_ context.Context, after int64, wait time.Duration) (int64, error) {
	f.called = true
	f.after = after
	f.wait = wait
	return f.generation, f.err
}

type revocationSourceFunc func(context.Context, int64, time.Duration) (int64, error)

// WatchGeneration adapts a function into the revocation source contract.
//
// WatchGeneration 把一个函数适配为吊销通知源契约。
func (f revocationSourceFunc) WatchGeneration(ctx context.Context, after int64, wait time.Duration) (int64, error) {
	return f(ctx, after, wait)
}

// TestWatchAPIKeyRevocations pins the bounded query, response, failure, and
// authentication contract of the internal watch endpoint.
//
// TestWatchAPIKeyRevocations 固定内部监听端点的有界查询、响应、故障与认证契约。
func TestWatchAPIKeyRevocations(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		token      string
		source     *fakeRevocationSource
		wantStatus int
		wantBody   string
		wantCalled bool
		wantAfter  int64
	}{
		{name: "generation changed", target: "/internal/v1/apikeys/revocations/watch?after=41", token: revocationTestToken, source: &fakeRevocationSource{generation: 42}, wantStatus: http.StatusOK, wantBody: "{\"generation\":42}\n", wantCalled: true, wantAfter: 41},
		{name: "heartbeat", target: "/internal/v1/apikeys/revocations/watch?after=42", token: revocationTestToken, source: &fakeRevocationSource{generation: 42}, wantStatus: http.StatusOK, wantBody: "{\"generation\":42}\n", wantCalled: true, wantAfter: 42},
		{name: "missing after", target: "/internal/v1/apikeys/revocations/watch", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: http.StatusBadRequest, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
		{name: "empty after", target: "/internal/v1/apikeys/revocations/watch?after=", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: http.StatusBadRequest, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
		{name: "repeated after", target: "/internal/v1/apikeys/revocations/watch?after=1&after=2", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: http.StatusBadRequest, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
		{name: "negative after", target: "/internal/v1/apikeys/revocations/watch?after=-1", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: http.StatusBadRequest, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
		{name: "overflowing after", target: "/internal/v1/apikeys/revocations/watch?after=9223372036854775808", token: revocationTestToken, source: &fakeRevocationSource{}, wantStatus: http.StatusBadRequest, wantBody: "{\"error\":\"exactly one non-negative after value is required\"}\n"},
		{name: "source unavailable", target: "/internal/v1/apikeys/revocations/watch?after=1", token: revocationTestToken, source: &fakeRevocationSource{err: errors.New("redis unavailable")}, wantStatus: http.StatusServiceUnavailable, wantBody: "{\"error\":\"revocation notification unavailable\"}\n", wantCalled: true, wantAfter: 1},
		{name: "negative source generation", target: "/internal/v1/apikeys/revocations/watch?after=1", token: revocationTestToken, source: &fakeRevocationSource{generation: -1}, wantStatus: http.StatusServiceUnavailable, wantBody: "{\"error\":\"revocation notification unavailable\"}\n", wantCalled: true, wantAfter: 1},
		{name: "missing token", target: "/internal/v1/apikeys/revocations/watch?after=1", source: &fakeRevocationSource{generation: 2}, wantStatus: http.StatusUnauthorized, wantBody: "{\"error\":\"unauthorized\"}\n"},
		{name: "wrong token", target: "/internal/v1/apikeys/revocations/watch?after=1", token: "wrong-token", source: &fakeRevocationSource{generation: 2}, wantStatus: http.StatusUnauthorized, wantBody: "{\"error\":\"unauthorized\"}\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svcCtx := &svc.ServiceContext{
				Config:      config.Config{InternalToken: revocationTestToken},
				Revocations: tt.source,
			}
			request := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.token != "" {
				request.Header.Set("Authorization", "Bearer "+tt.token)
			}
			response := httptest.NewRecorder()

			requireSharedSecret(svcCtx.Config.InternalToken, watchAPIKeyRevocations(svcCtx)).ServeHTTP(response, request)

			if response.Code != tt.wantStatus || response.Body.String() != tt.wantBody {
				t.Fatalf("response = (%d, %q), want (%d, %q)", response.Code, response.Body.String(), tt.wantStatus, tt.wantBody)
			}
			if tt.source.called != tt.wantCalled {
				t.Errorf("source called = %v, want %v", tt.source.called, tt.wantCalled)
			}
			if tt.wantCalled {
				if tt.source.after != tt.wantAfter {
					t.Errorf("after = %d, want %d", tt.source.after, tt.wantAfter)
				}
				if tt.source.wait != revocationWatchWait {
					t.Errorf("wait = %s, want %s", tt.source.wait, revocationWatchWait)
				}
			}
		})
	}
}

// TestWatchAPIKeyRevocationsCancelsSource proves request cancellation reaches
// the blocking notification source and lets the handler return.
//
// TestWatchAPIKeyRevocationsCancelsSource 证明请求取消会传到阻塞的通知源，并让
// handler 返回。
func TestWatchAPIKeyRevocationsCancelsSource(t *testing.T) {
	entered := make(chan struct{})
	returned := make(chan struct{})
	source := revocationSourceFunc(func(ctx context.Context, _ int64, _ time.Duration) (int64, error) {
		close(entered)
		<-ctx.Done()
		close(returned)
		return 0, ctx.Err()
	})
	svcCtx := &svc.ServiceContext{Revocations: source}
	requestCtx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/apikeys/revocations/watch?after=0", nil).WithContext(requestCtx)
	done := make(chan struct{})
	go func() {
		watchAPIKeyRevocations(svcCtx).ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()

	<-entered
	cancel()
	<-returned
	<-done
}

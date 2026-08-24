package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// fakeVerifier is a scripted KeyVerifier. It records what it was asked, which
// is how the tests below assert that a request never reached verification at
// all.
//
// fakeVerifier 是一个脚本化的 KeyVerifier。它记录自己被问了什么，这正是下面的测试
// 用来断言「某个请求根本没走到校验」的方式。
type fakeVerifier struct {
	calls atomic.Int64
	// answer returns the verdict for one key.
	//
	// answer 返回对某个 key 的判定。
	answer func(key string) (httpapi.Identity, error)
}

func (f *fakeVerifier) Verify(_ context.Context, key string) (httpapi.Identity, error) {
	f.calls.Add(1)
	return f.answer(key)
}

const (
	goodKey = "aisw-good"
	badKey  = "aisw-bad"
)

func acceptingVerifier() *fakeVerifier {
	return &fakeVerifier{answer: func(key string) (httpapi.Identity, error) {
		if key == goodKey {
			return httpapi.Identity{TenantID: "tnt_1", KeyID: "key_1"}, nil
		}
		return httpapi.Identity{}, httpapi.ErrKeyRejected
	}}
}

// TestVerifierAuthentication covers the status code each outcome produces. The
// 401-versus-503 split is the one that matters operationally: a caller told
// 401 goes and regenerates a key, and doing that because the control plane was
// briefly down wastes their time and does not fix anything.
//
// TestVerifierAuthentication 覆盖各种结果所产生的状态码。401 与 503 的区分在运维上
// 最要紧：收到 401 的调用方会跑去重新生成 key，而如果这只是因为控制面短暂宕机，那
// 既浪费了他们的时间，也解决不了任何问题。
func TestVerifierAuthentication(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		verifier   *fakeVerifier
		wantStatus int
		wantCalls  int64
	}{
		{
			name:       "a valid key is admitted",
			header:     "Bearer " + goodKey,
			verifier:   acceptingVerifier(),
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		{
			name:       "a rejected key is a 401",
			header:     "Bearer " + badKey,
			verifier:   acceptingVerifier(),
			wantStatus: http.StatusUnauthorized,
			wantCalls:  1,
		},
		{
			name:       "no header is a 401 without asking the control plane",
			header:     "",
			verifier:   acceptingVerifier(),
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "a non-bearer header is a 401 without asking",
			header:     "Basic dXNlcjpwYXNz",
			verifier:   acceptingVerifier(),
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:   "an unavailable control plane is a 503, not a 401",
			header: "Bearer " + goodKey,
			verifier: &fakeVerifier{answer: func(string) (httpapi.Identity, error) {
				return httpapi.Identity{}, errors.New("dial tcp: connection refused")
			}},
			wantStatus: http.StatusServiceUnavailable,
			wantCalls:  1,
		},
		{
			name:   "a wrapped rejection is still a 401",
			header: "Bearer " + goodKey,
			verifier: &fakeVerifier{answer: func(string) (httpapi.Identity, error) {
				return httpapi.Identity{}, fmt.Errorf("checking: %w", httpapi.ErrKeyRejected)
			}},
			wantStatus: http.StatusUnauthorized,
			wantCalls:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newServer(t, httpapi.Config{
				Verifier: tt.verifier,
				Logger:   slog.New(slog.DiscardHandler),
			})

			req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET /v1/models: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if got := tt.verifier.calls.Load(); got != tt.wantCalls {
				t.Errorf("the verifier was called %d times, want %d", got, tt.wantCalls)
			}
		})
	}
}

// TestVerifierWinsOverStaticKeys asserts the precedence auth.go documents: a
// configured control plane is the authority, and a stale -api-keys list left
// in a deployment cannot quietly keep working.
//
// TestVerifierWinsOverStaticKeys 断言 auth.go 所声明的优先级：已配置的控制面是权威，
// 而部署里遗留的过期 -api-keys 列表不能悄悄继续生效。
func TestVerifierWinsOverStaticKeys(t *testing.T) {
	verifier := acceptingVerifier()
	srv, _ := newServer(t, httpapi.Config{
		Verifier: verifier,
		APIKeys:  []string{"legacy-static-key"},
		Logger:   slog.New(slog.DiscardHandler),
	})

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer legacy-static-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a static key was accepted while a control plane is configured: status = %d", resp.StatusCode)
	}
}

// TestStaticKeysStillWorkWithoutAControlPlane asserts the fallback keeps
// working, so a deployment without a control plane is not broken by this
// change.
//
// TestStaticKeysStillWorkWithoutAControlPlane 断言回退路径依然可用，这样没有控制面的
// 部署不会被本次改动弄坏。
func TestStaticKeysStillWorkWithoutAControlPlane(t *testing.T) {
	srv, _ := newServer(t, httpapi.Config{
		APIKeys: []string{"static-key"},
		Logger:  slog.New(slog.DiscardHandler),
	})

	tests := []struct {
		name       string
		key        string
		wantStatus int
	}{
		{name: "the configured key", key: "static-key", wantStatus: http.StatusOK},
		{name: "another key", key: "other-key", wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+tt.key)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET /v1/models: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

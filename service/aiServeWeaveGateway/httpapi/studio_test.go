package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"AIServeWeave/common/apikey"
	"AIServeWeave/common/modelaccess"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// fakeSessions is a scripted SessionVerifier that counts its calls.
//
// fakeSessions 是一个脚本化的 SessionVerifier，并记录被调用的次数。
type fakeSessions struct {
	calls  atomic.Int64
	answer func(token string) (httpapi.Identity, error)
}

func (f *fakeSessions) VerifySession(_ context.Context, token string) (httpapi.Identity, error) {
	f.calls.Add(1)
	return f.answer(token)
}

const goodSession = "eyJ.session.token"

func acceptingSessions() *fakeSessions {
	return &fakeSessions{answer: func(token string) (httpapi.Identity, error) {
		if token == goodSession {
			return httpapi.Identity{TenantID: "tnt_1", UserID: "usr_1", Role: "member"}, nil
		}
		return httpapi.Identity{}, httpapi.ErrSessionRejected
	}}
}

// wellFormedKey returns a real, freshly generated API key.
//
// wellFormedKey 返回一把真实、新生成的 API Key。
func wellFormedKey(t *testing.T) string {
	t.Helper()
	generated, err := apikey.Generate()
	if err != nil {
		t.Fatalf("apikey.Generate: %v", err)
	}
	return generated.Plaintext
}

func studioGet(t *testing.T, url, authorization string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	return resp, raw
}

// TestStudioSessionAuthentication covers each outcome of Studio's session
// check, and that an API key is neither accepted nor forwarded to the
// control plane's session endpoint.
//
// TestStudioSessionAuthentication 覆盖 Studio 会话校验的各种结果，以及 API Key 既不被
// 接受、也不被转发到控制面的会话端点。
func TestStudioSessionAuthentication(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		sessions   *fakeSessions
		wantStatus int
		wantCode   string
		wantCalls  int64
	}{
		{name: "a live session is admitted", header: "Bearer " + goodSession, sessions: acceptingSessions(), wantStatus: http.StatusOK, wantCalls: 1},
		{name: "no header is a 401 without asking", sessions: acceptingSessions(), wantStatus: http.StatusUnauthorized, wantCode: "unauthorized"},
		{name: "a rejected session is a 401", header: "Bearer eyJ.other", sessions: acceptingSessions(), wantStatus: http.StatusUnauthorized, wantCode: "unauthorized", wantCalls: 1},
		{
			name:       "an API key is refused without being sent anywhere",
			header:     "Bearer " + wellFormedKey(t),
			sessions:   acceptingSessions(),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "unauthorized",
		},
		{
			name:   "an unavailable control plane is a 503, not a 401",
			header: "Bearer " + goodSession,
			sessions: &fakeSessions{answer: func(string) (httpapi.Identity, error) {
				return httpapi.Identity{}, errors.New("dial tcp: connection refused")
			}},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "verification_unavailable",
			wantCalls:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newServer(t, httpapi.Config{StudioSessions: tt.sessions})
			resp, raw := studioGet(t, srv.URL+"/studio/v1/me", tt.header)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tt.wantStatus, raw)
			}
			if tt.wantCode != "" {
				var body struct {
					Error struct{ Code string } `json:"error"`
				}
				_ = json.Unmarshal(raw, &body)
				if body.Error.Code != tt.wantCode {
					t.Errorf("error code = %q, want %q", body.Error.Code, tt.wantCode)
				}
			}
			if got := tt.sessions.calls.Load(); got != tt.wantCalls {
				t.Errorf("the session verifier was called %d times, want %d", got, tt.wantCalls)
			}
		})
	}
}

// TestStudioRejectsWhatTheOtherFrontDoorAccepts asserts the two front doors
// do not share credentials: an API key verifier never sees a /studio/ path,
// and a session never authenticates a /v1 path.
//
// TestStudioRejectsWhatTheOtherFrontDoorAccepts 断言两扇前门不共享凭据：API Key
// 校验器从不会看到 /studio/ 路径，会话也从不能认证 /v1 路径。
func TestStudioRejectsWhatTheOtherFrontDoorAccepts(t *testing.T) {
	keys := acceptingVerifier()
	sessions := acceptingSessions()
	srv, _ := newServer(t, httpapi.Config{Verifier: keys, StudioSessions: sessions})

	if resp, _ := studioGet(t, srv.URL+"/studio/v1/me", "Bearer "+goodKey); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an API key on /studio/v1/me: status = %d, want 401", resp.StatusCode)
	}
	if resp, _ := studioGet(t, srv.URL+"/v1/models", "Bearer "+goodSession); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a session on /v1/models: status = %d, want 401", resp.StatusCode)
	}
	if keys.calls.Load() != 1 {
		t.Errorf("the key verifier was called %d times, want 1 (only for /v1/models)", keys.calls.Load())
	}
}

// TestStudioIsUnmountedWithoutSessions asserts a Gateway without a session
// verifier serves no Studio API: the path falls through to the API-key front
// door, which refuses a session token.
//
// TestStudioIsUnmountedWithoutSessions 断言没有会话校验器的 Gateway 不提供 Studio
// API：该路径落到 API Key 前门，后者拒绝会话令牌。
func TestStudioIsUnmountedWithoutSessions(t *testing.T) {
	srv, _ := newServer(t, httpapi.Config{Verifier: acceptingVerifier()})
	if resp, _ := studioGet(t, srv.URL+"/studio/v1/me", "Bearer "+goodSession); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestStudioMeAndModels asserts the two endpoints the Studio home page reads:
// who is signed in, and the models they may use under their tenant's model
// access policy.
//
// TestStudioMeAndModels 断言 Studio 首页读取的两个端点：谁已登录，以及在其租户的
// 模型授权下他们可用的模型。
func TestStudioMeAndModels(t *testing.T) {
	sessions := &fakeSessions{answer: func(string) (httpapi.Identity, error) {
		return httpapi.Identity{
			TenantID: "tnt_1", UserID: "usr_1", Role: "admin",
			Models: &modelaccess.Policy{Tenant: []string{"qwen3:8b"}},
		}, nil
	}}
	srv, h := newServer(t, httpapi.Config{StudioSessions: sessions})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)
	connectNode(t, h, "node-b", "backend-1", chatCapableSnapshot("backend-1", "gemma3:27b"), chatHandler)

	resp, raw := studioGet(t, srv.URL+"/studio/v1/me", "Bearer "+goodSession)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me: status = %d", resp.StatusCode)
	}
	var me struct{ TenantID, UserID, Role string }
	var meJSON struct {
		TenantID string `json:"tenant_id"`
		UserID   string `json:"user_id"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(raw, &meJSON); err != nil {
		t.Fatalf("decoding me: %v", err)
	}
	me.TenantID, me.UserID, me.Role = meJSON.TenantID, meJSON.UserID, meJSON.Role
	if me.TenantID != "tnt_1" || me.UserID != "usr_1" || me.Role != "admin" {
		t.Errorf("me = %+v", me)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	resp, raw = studioGet(t, srv.URL+"/studio/v1/models", "Bearer "+goodSession)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("models: status = %d", resp.StatusCode)
	}
	var models struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &models); err != nil {
		t.Fatalf("decoding models: %v", err)
	}
	if len(models.Data) != 1 || models.Data[0].ID != "qwen3:8b" || models.Data[0].Name == "" {
		t.Errorf("models = %+v, want only qwen3:8b", models.Data)
	}

	if resp, _ := studioGet(t, srv.URL+"/studio/v1/nope", "Bearer "+goodSession); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown Studio path: status = %d, want 404", resp.StatusCode)
	}
}

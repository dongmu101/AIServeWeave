package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMessagesAPIKeyHeader scopes Anthropic authentication to its Messages endpoint.
// TestMessagesAPIKeyHeader 将 Anthropic 请求头认证限制在 Messages 端点。
func TestMessagesAPIKeyHeader(t *testing.T) {
	auth := newAuthenticator(nil, []string{"test-key"}, slog.New(slog.DiscardHandler))
	handler := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		name, path, bearer, key string
		want                    int
	}{
		{"native key", "/v1/messages", "", "test-key", 204},
		{"other endpoint", "/v1/chat/completions", "", "test-key", 401},
		{"wrong key", "/v1/messages", "", "wrong", 401},
		{"invalid bearer wins", "/v1/messages", "Bearer wrong", "test-key", 401},
		{"bearer", "/v1/messages", "Bearer test-key", "", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			r.Header.Set("Authorization", tc.bearer)
			r.Header.Set("X-Api-Key", tc.key)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d,want %d", w.Code, tc.want)
			}
		})
	}
}

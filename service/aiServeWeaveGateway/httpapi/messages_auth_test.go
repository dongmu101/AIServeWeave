package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMessagesAPIKeyHeader scopes Anthropic authentication to Messages and model listing.
// TestMessagesAPIKeyHeader 将 Anthropic 请求头认证限制在 Messages 和模型列表端点。
func TestMessagesAPIKeyHeader(t *testing.T) {
	auth := newAuthenticator(nil, []string{"test-key"}, slog.New(slog.DiscardHandler))
	handler := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		name, method, path, bearer, key string
		want                            int
	}{
		{"native key", "POST", "/v1/messages", "", "test-key", 204},
		{"model listing", "GET", "/v1/models?limit=20", "", "test-key", 204},
		{"model listing wrong key", "GET", "/v1/models", "", "wrong", 401},
		{"model listing missing key", "GET", "/v1/models", "", "", 401},
		{"model listing invalid bearer wins", "GET", "/v1/models", "Bearer wrong", "test-key", 401},
		{"model listing bearer", "GET", "/v1/models", "Bearer test-key", "", 204},
		{"model listing is read only", "POST", "/v1/models", "", "test-key", 401},
		{"model detail is outside scope", "GET", "/v1/models/test-model", "", "test-key", 401},
		{"other endpoint", "POST", "/v1/chat/completions", "", "test-key", 401},
		{"responses endpoint", "POST", "/v1/responses", "", "test-key", 401},
		{"wrong key", "POST", "/v1/messages", "", "wrong", 401},
		{"invalid bearer wins", "POST", "/v1/messages", "Bearer wrong", "test-key", 401},
		{"bearer", "POST", "/v1/messages", "Bearer test-key", "", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
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

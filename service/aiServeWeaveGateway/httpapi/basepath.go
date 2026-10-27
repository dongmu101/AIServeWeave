package httpapi

import "net/http"

// withAnthropicBasePath accepts the extra /v1 appended by Anthropic SDKs to a /v1 base URL.
// It normalizes before authentication, quotas and observation without redirecting or buffering.
//
// withAnthropicBasePath 兼容 Anthropic SDK 在 /v1 基础地址后追加的 /v1。
// 在认证、配额和观测之前统一路径，不重定向、不缓冲请求或响应。
func withAnthropicBasePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/v1/messages", "/v1/v1/models":
			r = r.Clone(r.Context())
			r.URL.Path = r.URL.Path[len("/v1"):]
			r.URL.RawPath = ""
		}
		next.ServeHTTP(w, r)
	})
}

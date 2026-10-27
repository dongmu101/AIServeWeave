package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// TestUnifiedBaseURL verifies routing and authentication for clients sharing a /v1 base URL.
// TestUnifiedBaseURL 验证共享 /v1 基础地址的客户端路由和认证。
func TestUnifiedBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, bearer, key string
		want                            int
	}{
		{"OpenAI Chat", "POST", "/v1/chat/completions", "Bearer " + goodKey, "", 400},
		{"canonical Messages", "POST", "/v1/messages", "", goodKey, 400},
		{"Anthropic SDK Messages", "POST", "/v1/v1/messages", "", goodKey, 400},
		{"Anthropic SDK Messages bearer", "POST", "/v1/v1/messages", "Bearer " + goodKey, "", 400},
		{"Anthropic SDK Messages missing key", "POST", "/v1/v1/messages", "", "", 401},
		{"Anthropic SDK Messages rejected key", "POST", "/v1/v1/messages", "", badKey, 401},
		{"Anthropic SDK Messages invalid bearer wins", "POST", "/v1/v1/messages", "Bearer " + badKey, goodKey, 401},
		{"Anthropic SDK models", "GET", "/v1/v1/models?limit=1000", "", goodKey, 200},
		{"canonical models", "GET", "/v1/models?limit=1000", "", goodKey, 200},
		{"Anthropic SDK models rejected key", "GET", "/v1/v1/models", "", badKey, 401},
		{"Messages method unchanged", "GET", "/v1/v1/messages", "", goodKey, 405},
		{"model listing method unchanged", "POST", "/v1/v1/models", "Bearer " + goodKey, "", 405},
		{"other duplicate paths unchanged", "POST", "/v1/v1/chat/completions", "Bearer " + goodKey, "", 404},
		{"extra duplicate prefix unchanged", "POST", "/v1/v1/v1/messages", "Bearer " + goodKey, "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newServer(t, httpapi.Config{Verifier: acceptingVerifier()})
			request, err := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("request error = %v, want nil", err)
			}
			request.Header.Set("Authorization", tc.bearer)
			request.Header.Set("X-Api-Key", tc.key)
			request.Header.Set("Content-Type", "application/json")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatalf("HTTP error = %v, want nil", err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.want || response.Header.Get("Location") != "" {
				t.Fatalf("status = %d, Location = %q; want status %d without redirect", response.StatusCode, response.Header.Get("Location"), tc.want)
			}
		})
	}
}

package httpapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// TestOutputLimitsReachOtherBackends ensures Codex compatibility never strips another backend's limit.
// TestOutputLimitsReachOtherBackends 确保 Codex 兼容行为不移除其他后端的输出上限。
func TestOutputLimitsReachOtherBackends(t *testing.T) {
	for _, kind := range []runtime.Kind{runtime.KindOllama, runtime.KindVLLM, runtime.KindSGLang} {
		t.Run(string(kind), func(t *testing.T) {
			srv, h := newServer(t, httpapi.Config{})
			snapshot := chatCapableSnapshot("backend", "gpt-5.6-luna")
			snapshot.Descriptor.Kind = kind
			received := make(chan runtime.ChatRequest, 1)
			connectNode(t, h, "node", "backend", snapshot, func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
				in, err := tunnelwire.UnmarshalChatRequest(req.GetPayload())
				if err != nil {
					return err
				}
				received <- in
				return chatHandler(req, body, reply)
			})
			resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":32768}`))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d, want 200", resp.StatusCode)
			}
			for _, header := range []string{"X-Gateway-Ignored-Parameters", "Warning"} {
				if got := resp.Header.Get(header); got != "" {
					t.Errorf("%s=%q, want no Codex warning for %s", header, got, kind)
				}
			}
			select {
			case request := <-received:
				if request.MaxTokens == nil || *request.MaxTokens != 32768 {
					t.Errorf("received MaxTokens=%v, want 32768", request.MaxTokens)
				}
			default:
				t.Fatal("no request reached the backend, want output limit forwarded through the tunnel")
			}
		})
	}
}

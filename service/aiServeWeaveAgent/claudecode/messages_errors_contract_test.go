package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

func TestNativeCLIRejectionContract(t *testing.T) {
	program := installedContractCLI(t)
	for _, tc := range []struct{ name, message, want string }{
		{"context", "context_management: Extra inputs are not permitted PRIVATE_MARKER", "context_management: Extra inputs are not permitted"},
		{"thinking", "thinking.type: adaptive is not supported PRIVATE_MARKER", "capability_rejected: thinking_type:adaptive"},
		{"prompt", "prompt is too long PRIVATE_MARKER", "capability_rejected: prompt_too_long"},
		{"beta", "Unexpected value(s) future-feature-2099-01-01 for anthropic-beta PRIVATE_MARKER", "capability_rejected: beta_header:future-feature-2099-01-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if !strings.HasSuffix(r.URL.Path, "/messages") {
					_, _ = io.WriteString(w, `{}`)
					return
				}
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": tc.message}})
			}))
			defer api.Close()
			b := newMessagesBridge(program, "claude-opus-5-5", nil, runtime.NewSystemClock())
			b.env = append(withAccountDir(probeEnv(os.Environ()), t.TempDir()), "ANTHROPIC_API_KEY=loopback-fixture-only", "ANTHROPIC_BASE_URL="+api.URL, "DISABLE_TELEMETRY=1", "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			r := &Runtime{cfg: runtime.Config{ID: "contract"}, bridge: b, root: ctx, cancel: cancel, models: map[string]struct{}{b.model: {}}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			defer r.Close()
			stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: b.model, TenantID: "t", KeyID: "k", BetaFeatures: "future-feature-2099-01-01", JSON: json.RawMessage(`{"model":"claude-opus-5-5","max_tokens":4096,"messages":[{"role":"user","content":"contract"}],"safeguards":[{"type":"dangerous_tool_use","classifier_context":{}}]}`)})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			_, err = stream.Recv()
			var failure *runtime.RuntimeError
			if !errors.As(err, &failure) || failure.Code != runtime.ErrorCapability || failure.Message != tc.want || strings.Contains(err.Error(), "PRIVATE_MARKER") {
				t.Fatalf("actual CLI rejection=%v, want %s", err, tc.want)
			}
		})
	}
}

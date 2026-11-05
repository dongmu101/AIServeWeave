package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestNativeCLICompactionContract(t *testing.T) {
	program := installedContractCLI(t)
	for _, onDemand := range []bool{false, true} {
		t.Run(fmt.Sprintf("onDemand=%t", onDemand), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/messages") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{}`)
					return
				}
				var request map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&request) != nil || (onDemand && string(request["compaction"]) != `{"type":"summarize"}`) {
					http.Error(w, "compaction request changed", 400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(kind string, event map[string]any) {
					event["type"] = kind
					data, _ := json.Marshal(event)
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
				}
				emit("message_start", map[string]any{"message": map[string]any{"id": "msg_compact_contract", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{}, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}})
				block := map[string]any{"type": "compaction", "content": "original summary", "signature": "original-signature"}
				if !onDemand {
					block["content"] = nil
				}
				emit("content_block_start", map[string]any{"index": 0, "content_block": block})
				if !onDemand {
					emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]string{"type": "compaction_delta", "content": "original summary", "encrypted_content": "opaque-summary"}})
				}
				emit("content_block_stop", map[string]any{"index": 0})
				emit("message_delta", map[string]any{"delta": map[string]string{"stop_reason": "compaction"}, "usage": map[string]int{"output_tokens": 20}})
				emit("message_stop", map[string]any{})
			}))
			defer api.Close()
			b := newMessagesBridge(program, "claude-opus-5-5", nil, runtime.NewSystemClock())
			b.env = append(withAccountDir(probeEnv(os.Environ()), t.TempDir()), "ANTHROPIC_API_KEY=loopback-fixture-only", "ANTHROPIC_BASE_URL="+api.URL, "DISABLE_TELEMETRY=1", "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			r := &Runtime{cfg: runtime.Config{ID: "contract"}, bridge: b, root: ctx, cancel: cancel, models: map[string]struct{}{b.model: {}}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			defer r.Close()
			doc := map[string]any{"model": b.model, "max_tokens": 4096, "messages": []any{map[string]string{"role": "user", "content": "summarize"}}, "safeguards": []any{map[string]any{"type": "dangerous_tool_use", "classifier_context": map[string]any{}}}}
			if onDemand {
				doc["compaction"] = map[string]string{"type": "summarize"}
			}
			body, _ := json.Marshal(doc)
			stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: b.model, TenantID: "t", KeyID: "k", BetaFeatures: "compact-2026-09-04", JSON: body})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var events bytes.Buffer
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				events.Write(event.JSON)
			}
			if !bytes.Contains(events.Bytes(), []byte("original summary")) || !bytes.Contains(events.Bytes(), []byte("original-signature")) || !bytes.Contains(events.Bytes(), []byte(`"stop_reason":"compaction"`)) {
				t.Fatalf("actual CLI lost compaction: %s", events.Bytes())
			}
		})
	}
}

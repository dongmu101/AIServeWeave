package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// TestNativeCLICallerToolIsolationContract verifies real CLI isolation using a loopback API.
// TestNativeCLICallerToolIsolationContract 用真实 CLI 与回环 API 验证调用方工具不会在 Agent 执行。
func TestNativeCLICallerToolIsolationContract(t *testing.T) {
	program := installedContractCLI(t)
	for _, name := range []string{"Bash", "Read", "Write", "ToolSearch", "future_cli_tool", "mcp__fixture__future"} {
		t.Run(name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "must-not-be-executed")
			input := map[string]string{"command": "printf fixture > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'"}
			if name == "Write" {
				input = map[string]string{"file_path": marker, "content": "fixture"}
			} else if name == "Read" {
				input = map[string]string{"file_path": marker}
			}
			var calls atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if !strings.HasSuffix(request.URL.Path, "/messages") {
					_, _ = io.WriteString(w, `{}`)
					return
				}
				calls.Add(1)
				var document struct {
					Tools []struct{ Name string } `json:"tools"`
				}
				if json.NewDecoder(request.Body).Decode(&document) != nil || len(document.Tools) != 1 || document.Tools[0].Name != name {
					t.Error("local tool restrictions changed the caller's upstream definition")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var wire bytes.Buffer
				fixtureRound(&wire, "caller-only-id", "")
				w.Header().Set("Content-Type", "text/event-stream")
				for _, line := range bytes.Split(wire.Bytes(), []byte("\n")) {
					if len(line) == 0 {
						continue
					}
					var envelope struct{ Event map[string]any }
					_ = json.Unmarshal(line, &envelope)
					event := envelope.Event
					switch event["type"] {
					case "content_block_start":
						event["content_block"].(map[string]any)["name"] = name
					case "content_block_delta":
						args, _ := json.Marshal(input)
						event["delta"].(map[string]any)["partial_json"] = string(args)
					}
					encoded, _ := json.Marshal(event)
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], encoded)
				}
			}))
			t.Cleanup(api.Close)
			rt, err := NewFactory(program)(runtime.Config{ID: "fixture", Kind: runtime.KindClaude}, runtime.Dependencies{
				Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			r := rt.(*Runtime)
			t.Cleanup(func() { _ = r.Close() })
			r.models = map[string]struct{}{"claude-opus-5-5": {}}
			r.bridge.env = append(withAccountDir(probeEnv(os.Environ()), t.TempDir()), "ANTHROPIC_API_KEY=loopback-fixture-only", "ANTHROPIC_BASE_URL="+api.URL,
				"DISABLE_TELEMETRY=1", "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
			document, _ := json.Marshal(map[string]any{
				"model": "claude-opus-5-5", "max_tokens": 1024,
				"messages": []any{map[string]string{"role": "user", "content": "fixture"}},
				"tools":    []any{map[string]any{"name": name, "input_schema": map[string]any{"type": "object", "additionalProperties": true}}},
			})
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: "claude-opus-5-5", Native: true, TenantID: "t", KeyID: "k", JSON: document})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var gotName, gotID, arguments string
			for {
				item, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("real CLI failed to forward caller tool: %v", err)
				}
				var event struct {
					Type  string                    `json:"type"`
					Block struct{ Name, ID string } `json:"content_block"`
					Delta struct {
						PartialJSON string `json:"partial_json"`
					} `json:"delta"`
				}
				_ = json.Unmarshal(item.JSON, &event)
				if event.Type == "content_block_start" {
					gotName, gotID = event.Block.Name, event.Block.ID
				}
				arguments += event.Delta.PartialJSON
			}
			_ = stream.Close()
			_ = r.Close()
			if gotName != name || gotID != "caller-only-id" || canonical(json.RawMessage(arguments)) != canonical(input) {
				t.Fatal("CLI changed the caller tool's name, ID or input")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("caller tool executed on Agent or marker check failed: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("CLI continued the caller tool round locally: %d requests", calls.Load())
			}
		})
	}
}

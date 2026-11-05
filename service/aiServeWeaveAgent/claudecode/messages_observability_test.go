package claudecode

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"AIServeWeave/common/runtime"
)

// TestMessagesRoundDiagnostics checks delivered results and bounded, non-secret logs together.
// TestMessagesRoundDiagnostics 同时验证结果透传与有界、无敏感内容的运行日志。
func TestMessagesRoundDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, results, status string
		guarded, fail         bool
	}{
		{name: "compatibility", status: "not_requested"},
		{name: "missing", guarded: true, status: "missing"},
		{name: "available", guarded: true, results: `[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"private-marker-id":{"type":"evaluated","outcome":"not_flagged"}}}}]`, status: "available"},
		{name: "unavailable", guarded: true, results: `[{"type":"dangerous_tool_use","status":{"type":"unavailable","reason":"private-marker-reason"}}]`, status: "unavailable"},
		{name: "unsupported", guarded: true, results: `[{"type":"dangerous_tool_use","status":{"type":"unsupported"}}]`, status: "unsupported"},
		{name: "future status", guarded: true, results: `[{"type":"dangerous_tool_use","status":{"type":"private-marker-future"}}]`, status: "unknown"},
		{name: "future kind", guarded: true, results: `[{"type":"private-marker-kind","opaque":"private-marker"}]`, status: "missing"},
		{name: "null", guarded: true, results: `null`, status: "missing"},
		{name: "invalid", guarded: true, results: `{"private-marker":"invalid"}`, status: "invalid"},
		{name: "failed after verdict", guarded: true, fail: true, results: `[{"type":"dangerous_tool_use","status":{"type":"unavailable"}}]`, status: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{
				Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			r := rt.(*Runtime)
			defer r.Close()
			r.models = map[string]struct{}{"sonnet": {}}
			r.bridge.start = func(s *messagesSession) {
				_ = s.send(json.RawMessage(`{"type":"message_start","message":{"id":"private-marker-message","type":"message","role":"assistant","content":[],"usage":{}}}`), nil)
				delta := map[string]any{"stop_reason": "end_turn"}
				if tc.results != "" {
					delta["safeguard_results"] = json.RawMessage(tc.results)
				}
				event, _ := json.Marshal(map[string]any{"type": "message_delta", "delta": delta})
				_ = s.send(event, nil)
				if tc.fail {
					_ = s.send(nil, errCLIExit)
				} else {
					_ = s.send(json.RawMessage(`{"type":"message_stop"}`), nil)
				}
			}
			req := testMessagesRequest()
			req.System = json.RawMessage(`"private-marker-system"`)
			if tc.guarded {
				req.Safeguards = guardedRequest().Safeguards
			}
			body, _ := json.Marshal(req)
			stream, err := r.Messages(t.Context(), runtime.MessagesRequest{Model: req.Model, TenantID: "private-marker-tenant", KeyID: "private-marker-key", JSON: body})
			if err != nil {
				t.Fatal(err)
			}
			var delivered bytes.Buffer
			for {
				item, err := stream.Recv()
				if err != nil {
					if err != io.EOF && !tc.fail {
						t.Fatal(err)
					}
					break
				}
				delivered.Write(item.JSON)
			}
			_ = stream.Close()
			if tc.results != "" && !bytes.Contains(delivered.Bytes(), []byte(tc.results)) {
				t.Fatal("diagnostics changed the upstream result")
			}
			decoder := json.NewDecoder(&logs)
			var started, finished map[string]any
			for decoder.More() {
				var record map[string]any
				if err := decoder.Decode(&record); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(record)
				if bytes.Contains(data, []byte("private-marker")) {
					t.Fatal("request or upstream payload reached diagnostic logs")
				}
				switch record["msg"] {
				case "Claude Messages round started":
					started = record
				case "Claude Messages round finished":
					finished = record
				}
			}
			path := "mcp_bridge"
			if tc.guarded {
				path = "native_safeguards"
			}
			outcome := "completed"
			if tc.fail {
				outcome = "failed"
			}
			if started["message_path"] != path || finished["message_path"] != path || started["round_id"] == "" || started["round_id"] != finished["round_id"] || finished["safeguard_status"] != tc.status || finished["outcome"] != outcome {
				t.Fatalf("round diagnostics = %v / %v", started, finished)
			}
		})
	}
}

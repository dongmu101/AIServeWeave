package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNativeCallerToolNamesAreNotLocalRegistrations(t *testing.T) {
	for _, name := range []string{"Bash", "Read", "Write", "Agent", "ToolSearch", "EndConversation", "future_cli_tool", "mcp__future__tool"} {
		t.Run(name, func(t *testing.T) {
			req := guardedRequest()
			req.Tools[0].Name = name
			var wire bytes.Buffer
			wire.WriteString("{\"type\":\"system\",\"subtype\":\"init\",\"tools\":[]}\n")
			fixtureSafeguardRound(&wire, "caller-id", "", "not_flagged")
			input := strings.ReplaceAll(wire.String(), `"name":"read_file"`, `"name":"`+name+`"`)
			s := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 16)}
			final, err := bridgeEvents(strings.NewReader(input), s)
			if !errors.Is(err, errTerminalStop) || string(final) != `{"type":"message_stop"}` {
				t.Fatalf("declared caller tool rejected: %v / %s", err, final)
			}
			close(s.events)
			found := false
			for item := range s.events {
				var event struct {
					Type  string       `json:"type"`
					Block messageBlock `json:"content_block"`
				}
				_ = json.Unmarshal(item.data, &event)
				if event.Type == "content_block_start" && event.Block.Type == "tool_use" {
					found = event.Block.Name == name && event.Block.ID == "caller-id"
				}
			}
			if !found || len(s.pending) != 0 || s.waiting {
				t.Fatal("caller tool was rewritten or registered with the local bridge")
			}
			if name == "EndConversation" {
				return // The CLI's documented control tool is allowed at init.
			}
			// A caller declaration must never authorize local execution of that name.
			init, _ := json.Marshal(map[string]any{"type": "system", "subtype": "init", "tools": []string{name}})
			if _, err := bridgeEvents(bytes.NewReader(init), s); !errors.Is(err, errUnsafeTools) {
				t.Fatalf("caller tool authorized a local registration: %v", err)
			}
		})
	}
}

func TestNativeDeferredToolSearchSequence(t *testing.T) {
	req := guardedRequest()
	req.Tools[0].Name = "future_cli_tool"
	req.Tools = append(req.Tools, bridgeTool{Name: "tool_search_tool_regex"})
	var wire bytes.Buffer
	emit := func(v any) { emitFixture(&wire, v) }
	emit(map[string]any{"type": "message_start"})
	emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_search", "name": "tool_search_tool_regex", "input": map[string]any{}}})
	emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": `{"pattern":"future"}`}})
	emit(map[string]any{"type": "content_block_stop", "index": 0})
	result := map[string]any{"type": "tool_search_tool_result", "tool_use_id": "srvtoolu_search", "content": map[string]any{"type": "tool_search_tool_search_result", "tool_references": []any{map[string]string{"type": "tool_reference", "tool_name": "future_cli_tool"}}}}
	emit(map[string]any{"type": "content_block_start", "index": 1, "content_block": result})
	emit(map[string]any{"type": "content_block_stop", "index": 1})
	emit(map[string]any{"type": "content_block_start", "index": 2, "content_block": map[string]any{"type": "tool_use", "id": "caller-id", "name": "future_cli_tool", "input": map[string]any{}}})
	emit(map[string]any{"type": "content_block_delta", "index": 2, "delta": map[string]string{"type": "input_json_delta", "partial_json": `{"value":"fixture"}`}})
	emit(map[string]any{"type": "content_block_stop", "index": 2})
	emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}})
	emit(map[string]any{"type": "message_stop"})
	s := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 16)}
	_, err := bridgeEvents(&wire, s)
	if !errors.Is(err, errTerminalStop) {
		t.Fatalf("native server search sequence rejected: %v", err)
	}
	close(s.events)
	found := false
	for item := range s.events {
		var event struct {
			Type  string          `json:"type"`
			Block json.RawMessage `json:"content_block"`
		}
		_ = json.Unmarshal(item.data, &event)
		if canonical(event.Block) == canonical(result) {
			found = true
		}
	}
	if !found || len(s.pending) != 0 {
		t.Fatal("server tool search result was rewritten or scheduled locally")
	}
}

func TestLifecycleNotificationCannotCompleteRound(t *testing.T) {
	for _, wire := range []string{
		`{"type":"auth_status","isAuthenticating":false,"output":[]}`,
		`{"type":"tool_progress","elapsed_time_seconds":1}`,
		`{"type":"prompt_suggestion","suggestion":"fixture"}`,
	} {
		var frame struct{ Type string }
		_ = json.Unmarshal([]byte(wire), &frame)
		t.Run(frame.Type, func(t *testing.T) {
			s := &messagesSession{ctx: context.Background(), request: guardedRequest(), events: make(chan bridgeEvent, 4)}
			if _, err := bridgeEvents(strings.NewReader(wire+"\n"), s); !errors.Is(err, errMissingResult) {
				t.Fatalf("notification substituted for an inference result: %v", err)
			}
		})
	}
}

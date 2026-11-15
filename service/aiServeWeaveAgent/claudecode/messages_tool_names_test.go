package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const harnessTools = `[{"name":"read","input_schema":{"type":"object"}},{"name":"bash","input_schema":{"type":"object"}},{"name":"web_fetch","input_schema":{"type":"object"}},{"name":"mcp__fs__list","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]`

func nativeFixtureRequest(t *testing.T, raw string) messagesRequest {
	t.Helper()
	var req messagesRequest
	if err := decodeMessagesRequest(json.RawMessage(raw), true, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return req
}

// TestNativeToolAliases renames a non-Claude Code caller's custom tools only.
// TestNativeToolAliases 只改写非 Claude Code 调用方的自定义工具。
func TestNativeToolAliases(t *testing.T) {
	req := nativeFixtureRequest(t, `{"model":"m","max_tokens":10,"system":"harness","messages":[{"role":"user","content":"hi"}],"tools":`+harnessTools+`}`)
	got := req.nativeToolAliases()
	want := map[string]string{"read": "Read", "bash": "Bash", "web_fetch": "mcp__caller__web_fetch"}
	if len(got) != len(want) {
		t.Fatalf("aliases = %v", got)
	}
	for name, alias := range want {
		if got[name] != alias {
			t.Fatalf("aliases = %v", got)
		}
	}
	for name, raw := range map[string]string{
		"Claude Code caller": `{"model":"m","max_tokens":10,"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[{"role":"user","content":"hi"}],"tools":` + harnessTools + `}`,
		"guarded":            `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":` + harnessTools + `,"safeguards":[{"type":"dangerous_tool_use","classifier_context":{}}]}`,
		"collision":          `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{}},{"name":"Read","input_schema":{}}]}`,
		"prefixed collision": `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"x","input_schema":{}},{"name":"mcp__caller__x","input_schema":{}}]}`,
		"already Claude":     `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Read","input_schema":{}}]}`,
		"no tools":           `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`,
	} {
		if aliases := nativeFixtureRequest(t, raw).nativeToolAliases(); aliases != nil {
			t.Fatalf("%s: aliases = %v", name, aliases)
		}
	}
	long := strings.Repeat("x", maxUpstreamToolName)
	if upstreamToolAlias(long) != long {
		t.Fatal("over-long prefixed name was not left alone")
	}
}

// TestRenameNativeTools rewrites definitions, tool_choice and history, and
// keeps every other field.
// TestRenameNativeTools 改写工具定义、tool_choice 与历史，其他字段保持不变。
func TestRenameNativeTools(t *testing.T) {
	raw := `{"model":"m","max_tokens":10,"tool_choice":{"type":"tool","name":"read"},"messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":[{"type":"text","text":"<ok>"},{"type":"tool_use","id":"toolu_1","name":"read","input":{"p":"a"}},{"type":"tool_use","id":"toolu_2","name":"web_fetch","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"tool_reference","tool_name":"bash"}]},{"type":"tool_result","tool_use_id":"toolu_2","content":"done"}]}],"tools":` + harnessTools + `}`
	req := nativeFixtureRequest(t, raw)
	extra := make(map[string]any)
	for key, value := range req.nativeDocument {
		extra[key] = value
	}
	renameNativeTools(extra, req.nativeToolAliases())
	body, _ := encodeMessagesJSON(extra)
	text := string(body)
	for _, want := range []string{`"name":"Read"`, `"name":"Bash"`, `"name":"mcp__caller__web_fetch"`, `"name":"mcp__fs__list"`, `"name":"web_search"`, `"tool_name":"Bash"`, `"<ok>"`, `"tool_use_id":"toolu_1"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
	for _, unwanted := range []string{`"name":"read"`, `"name":"bash"`, `"name":"web_fetch"`, `"tool_name":"bash"`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("kept %s in %s", unwanted, text)
		}
	}
	var choice struct{ Name string }
	if json.Unmarshal(extra["tool_choice"].(json.RawMessage), &choice) != nil || choice.Name != "Read" {
		t.Fatalf("tool_choice = %s", extra["tool_choice"])
	}
}

// TestNativeToolUseRestoresCallerName sends the upstream's renamed call to
// the caller under the name it declared, with its other fields intact.
// TestNativeToolUseRestoresCallerName 把上游改名后的调用以调用方声明的名称发给调用方，其他字段保持不变。
func TestNativeToolUseRestoresCallerName(t *testing.T) {
	for upstream, want := range map[string]string{"Read": "read", "mcp__caller__web_fetch": "web_fetch", "mcp__fs__list": "mcp__fs__list"} {
		t.Run(upstream, func(t *testing.T) {
			req := nativeFixtureRequest(t, `{"model":"m","max_tokens":10,"system":"harness","messages":[{"role":"user","content":"hi"}],"tools":`+harnessTools+`}`)
			var input bytes.Buffer
			emitFixture(&input, map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "fixture", "content": []any{}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 0}}})
			emitFixture(&input, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_up", "name": upstream, "input": map[string]any{}, "caller": map[string]string{"type": "direct"}}})
			emitFixture(&input, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": `{"p":"a"}`}})
			emitFixture(&input, map[string]any{"type": "content_block_stop", "index": 0})
			emitFixture(&input, map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 7}})
			emitFixture(&input, map[string]string{"type": "message_stop"})
			s := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 16)}
			if _, err := bridgeEvents(&input, s); err != nil && !errors.Is(err, errTerminalStop) {
				t.Fatalf("bridgeEvents: %v", err)
			}
			close(s.events)
			for event := range s.events {
				var start struct {
					Type  string                     `json:"type"`
					Block map[string]json.RawMessage `json:"content_block"`
				}
				if json.Unmarshal(event.data, &start) != nil || start.Type != "content_block_start" {
					continue
				}
				var name string
				_ = json.Unmarshal(start.Block["name"], &name)
				if name != want || string(start.Block["id"]) != `"toolu_up"` || len(start.Block["caller"]) == 0 {
					t.Fatalf("content_block = %v", start.Block)
				}
				return
			}
			t.Fatal("no content_block_start sent")
		})
	}
}

// TestNativeTextRoundWithAliases keeps a round without tool calls unaffected.
// TestNativeTextRoundWithAliases 保证没有工具调用的回合不受改名影响。
func TestNativeTextRoundWithAliases(t *testing.T) {
	req := nativeFixtureRequest(t, `{"model":"m","max_tokens":10,"system":"harness","messages":[{"role":"user","content":"hi"}],"tools":`+harnessTools+`}`)
	var input bytes.Buffer
	fixtureRound(&input, "", "answer")
	input.WriteString("{\"type\":\"result\",\"subtype\":\"success\"}\n")
	s := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 16)}
	if _, err := bridgeEvents(&input, s); err != nil {
		t.Fatalf("text round failed with aliases: %v", err)
	}
}

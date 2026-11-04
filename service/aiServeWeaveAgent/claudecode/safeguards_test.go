package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

func guardedRequest() messagesRequest {
	req := testMessagesRequest()
	req.Safeguards = []json.RawMessage{json.RawMessage(`{"type":"dangerous_tool_use","classifier_context":{"v":1,"permission_mode":"auto","opaque":"caller-context"}}`)}
	return req
}

func fixtureSafeguardRound(w io.Writer, toolID, text, outcome string) {
	var input bytes.Buffer
	fixtureRound(&input, toolID, text)
	for _, line := range strings.Split(strings.ReplaceAll(strings.TrimSpace(input.String()), "mcp__aisw_bridge__tool_0", "read_file"), "\n") {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(line), &envelope)
		var event map[string]json.RawMessage
		_ = json.Unmarshal(envelope["event"], &event)
		if string(event["type"]) == `"message_delta"` {
			var delta map[string]any
			_ = json.Unmarshal(event["delta"], &delta)
			calls := map[string]any{}
			if toolID != "" {
				calls[toolID] = map[string]string{"type": "evaluated", "outcome": outcome}
			}
			delta["safeguard_results"] = []any{map[string]any{"type": "dangerous_tool_use", "status": map[string]any{"type": "available", "tool_uses": calls}}}
			event["delta"], _ = json.Marshal(delta)
			envelope["event"], _ = json.Marshal(event)
		}
		encoded, _ := json.Marshal(envelope)
		fmt.Fprintln(w, string(encoded))
	}
}

func safeguardsProcessFixture() {
	var extra struct {
		Safeguards  []json.RawMessage `json:"safeguards"`
		Betas       []string          `json:"betas"`
		Temperature *float64          `json:"temperature"`
		Tools       []bridgeTool      `json:"tools"`
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	var settingsPath, mcpPath string
	for i := 1; i+1 < len(os.Args); i++ {
		switch os.Args[i] {
		case "--settings":
			settingsPath = os.Args[i+1]
		case "--mcp-config":
			mcpPath = os.Args[i+1]
		case "--tools", "--allowedTools":
			if os.Args[i+1] != "" {
				os.Exit(3)
			}
		}
	}
	data, err := os.ReadFile(settingsPath)
	info, statErr := os.Stat(settingsPath)
	if err != nil || statErr != nil || info.Mode().Perm() != 0600 || json.Unmarshal(data, &settings) != nil {
		os.Exit(3)
	}
	var mcp struct {
		Servers map[string]any `json:"mcpServers"`
	}
	data, _ = os.ReadFile(mcpPath)
	if json.Unmarshal(data, &mcp) != nil || len(mcp.Servers) != 0 {
		os.Exit(3)
	}
	if json.Unmarshal([]byte(settings.Env["CLAUDE_CODE_EXTRA_BODY"]), &extra) != nil || len(extra.Safeguards) != 1 || len(extra.Tools) != 1 || extra.Tools[0].Name != "read_file" || !slices.Contains(extra.Betas, "dangerous-tool-use-2026-09-03") || !slices.Contains(extra.Betas, "future-feature-2099-01-01") || extra.Temperature == nil || *extra.Temperature != .25 {
		os.Exit(3)
	}
	if !strings.Contains(os.Getenv("ANTHROPIC_CUSTOM_HEADERS"), "anthropic-version: 2023-06-01") || !slices.Contains(os.Args, "auto") {
		os.Exit(3)
	}
	_, _ = io.ReadAll(os.Stdin)
	var native struct {
		Messages json.RawMessage `json:"messages"`
		System   json.RawMessage `json:"system"`
	}
	_ = json.Unmarshal([]byte(settings.Env["CLAUDE_CODE_EXTRA_BODY"]), &native)
	prompt := []byte(native.Messages)
	if strings.Contains(string(prompt), "caller-result") {
		if !bytes.Contains(extra.Safeguards[0], []byte("second-context")) {
			os.Exit(3)
		}
		fixtureSafeguardRound(os.Stdout, "", "second-context:caller-result", "not_flagged")
		fmt.Println(`{"type":"result","subtype":"success"}`)
		return
	}
	if !bytes.Contains(extra.Safeguards[0], []byte("caller-context")) {
		os.Exit(3)
	}
	fixtureSafeguardRound(os.Stdout, "native-guarded-id", "", "not_flagged")
	time.Sleep(30 * time.Second) // only an explicitly stopped CLI may finish this public tool round / 此工具回合必须由桥接主动停止 CLI
}

func TestSafeguardsNativeDocument(t *testing.T) {
	raw := json.RawMessage(`{"model":"fixture","max_tokens":4096,"messages":[{"role":"user","content":[{"type":"text","text":"question","cache_control":{"type":"ephemeral"}}]},{"role":"system","content":"caller environment"}],"system":[{"type":"text","text":"caller attribution","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"Bash","description":"caller Bash","input_schema":{"type":"object"},"defer_loading":false}],"thinking":{"type":"adaptive"},"context_management":{"edits":[]},"safeguards":[{"type":"dangerous_tool_use","classifier_context":{"v":1,"prior_turn_context":{"tool_use_ids":["original-id"]},"text_provenance":{"opaque":"unchanged"}}}]}`)
	var req messagesRequest
	if err := decodeMessagesRequest(raw, &req); err != nil || req.validate("fixture") != nil {
		t.Fatalf("native guarded document rejected: %v", err)
	}
	var want map[string]json.RawMessage
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(req.nativeDocument, want) {
		t.Fatal("structured native request or policy provenance was rewritten")
	}
	for _, key := range []string{"headers", "base_url", "env", "command", "api_key"} {
		doc := map[string]any{}
		_ = json.Unmarshal(raw, &doc)
		doc[key] = "private-control"
		encoded, _ := json.Marshal(doc)
		if decodeMessagesRequest(encoded, &req) == nil {
			t.Fatalf("non-inference control %s accepted", key)
		}
	}
}

func TestSafeguardsKeepsTruncatedToolStream(t *testing.T) {
	var input bytes.Buffer
	emitFixture(&input, map[string]any{"type": "message_start"})
	emitFixture(&input, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "native-partial", "name": "read_file", "input": map[string]any{}, "caller": map[string]string{"type": "direct"}}})
	emitFixture(&input, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": "{\"path\":\"unfinished"}})
	emitFixture(&input, map[string]any{"type": "content_block_stop", "index": 0})
	emitFixture(&input, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "max_tokens", "safeguard_results": []any{map[string]any{"type": "dangerous_tool_use", "status": map[string]any{"type": "unavailable", "reason": "truncated"}}}}})
	emitFixture(&input, map[string]any{"type": "message_stop"})
	session := &messagesSession{ctx: context.Background(), request: guardedRequest(), events: make(chan bridgeEvent, 16), pending: make(map[string]*pendingTool), changed: make(chan struct{})}
	_, err := bridgeEvents(&input, session)
	if !errors.Is(err, errTerminalStop) {
		t.Fatalf("truncated native stream rejected before stop_reason: %v", err)
	}
	close(session.events)
	var output strings.Builder
	for event := range session.events {
		output.Write(event.data)
	}
	if !strings.Contains(output.String(), `"stop_reason":"max_tokens"`) || !strings.Contains(output.String(), `"caller":{"type":"direct"}`) || !strings.Contains(output.String(), `"reason":"truncated"`) {
		t.Fatalf("native truncated stream lost fields: %s", &output)
	}
}

func TestSafeguardsRejectsLocalTools(t *testing.T) {
	for _, name := range []string{"Bash", "mcp__aisw_bridge__tool_0"} {
		session := &messagesSession{ctx: context.Background(), request: guardedRequest(), events: make(chan bridgeEvent, 1)}
		wire, _ := json.Marshal(map[string]any{"type": "system", "subtype": "init", "tools": []string{name}})
		if _, err := bridgeEvents(bytes.NewReader(wire), session); !errors.Is(err, errUnsafeTools) {
			t.Fatalf("local tool %s was registered in guarded mode: %v", name, err)
		}
	}
}

func TestSafeguardsFreshContext(t *testing.T) {
	rt, err := NewFactory(os.Args[0])(runtime.Config{ID: "guarded", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	defer r.Close()
	r.models = map[string]struct{}{"claude-opus-5-5": {}}
	r.bridge.env = append(r.bridge.cliEnv(), "AISW_CLAUDE_SAFEGUARDS_FIXTURE=1")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	req := guardedRequest()
	temperature := .25
	req.Temperature = &temperature
	round := func(req messagesRequest) []byte {
		t.Helper()
		data, _ := json.Marshal(req)
		stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: "claude-opus-5-5", TenantID: "tenant", KeyID: "key", JSON: data, APIVersion: "2023-06-01", BetaFeatures: "future-feature-2099-01-01"})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		var output bytes.Buffer
		for {
			item, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&output, "data: %s\n\n", item.JSON)
		}
		return output.Bytes()
	}
	first := round(req)
	var content []messageBlock
	for _, line := range strings.Split(string(first), "\n") {
		var event struct {
			Type  string
			Block messageBlock `json:"content_block"`
			Delta struct {
				Partial string `json:"partial_json"`
			}
		}
		if !strings.HasPrefix(line, "data: ") || json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			continue
		}
		if event.Type == "content_block_start" {
			content = append(content, event.Block)
			content[len(content)-1].Input = nil
		}
		if event.Type == "content_block_delta" {
			content[len(content)-1].Input = append(content[len(content)-1].Input, event.Delta.Partial...)
		}
	}
	if len(content) != 1 || !bytes.Contains(first, []byte(content[0].ID)) || !bytes.Contains(first, []byte("safeguard_results")) {
		t.Fatalf("guarded tool response incomplete: %s", first)
	}
	encoded, _ := json.Marshal(content)
	req.Messages = append(req.Messages, bridgeMessage{Role: "assistant", Content: encoded}, bridgeMessage{Role: "user", Content: json.RawMessage(fmt.Sprintf(`[{"type":"tool_result","tool_use_id":%q,"content":"caller-result"}]`, content[0].ID))})
	req.Safeguards[0] = json.RawMessage(strings.ReplaceAll(string(req.Safeguards[0]), "caller-context", "second-context"))
	if second := round(req); !bytes.Contains(second, []byte("second-context:caller-result")) {
		t.Fatalf("continuation did not use fresh caller context: %s", second)
	}
}

func TestSafeguardsVerdictStates(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		wantError       bool
	}{
		{"unsupported", `[{"type":"dangerous_tool_use","status":{"type":"unsupported","future":true}}]`, `[{"type":"dangerous_tool_use","status":{"type":"unsupported","future":true}}]`, false},
		{"unavailable", `[{"type":"dangerous_tool_use","status":{"type":"unavailable","reason":"timeout"}}]`, `[{"type":"dangerous_tool_use","status":{"type":"unavailable","reason":"timeout"}}]`, false},
		{"null result", `null`, `null`, false},
		{"future kind", `[{"type":"future_guard","opaque":{"kept":true}}]`, `[{"type":"future_guard","opaque":{"kept":true}}]`, false},
		{"escaped type", `[{"type":"dangerous_\u0074ool_use","status":{"type":"avail\u0061ble","tool_uses":{"native":{"type":"skipped"}}}}]`, `[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"public":{"type":"skipped"}}}}]`, false},
		{"unknown ID", `[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"private-unknown-id":{"type":"evaluated","outcome":"not_flagged"}}}}]`, ``, true},
		{"bad result shape", `{"private":"invalid"}`, ``, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remapSafeguardResults(json.RawMessage(tc.raw), map[string]string{"native": "public"})
			if (err != nil) != tc.wantError || (err != nil && strings.Contains(err.Error(), "private")) {
				t.Fatalf("error=%v, want error=%t without input reflection", err, tc.wantError)
			}
			if err != nil {
				return
			}
			var actual, expected any
			_ = json.Unmarshal(got, &actual)
			_ = json.Unmarshal([]byte(tc.want), &expected)
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("verdict=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestSafeguardsStopSequence(t *testing.T) {
	var input bytes.Buffer
	emitFixture(&input, map[string]any{"type": "message_start"})
	emitFixture(&input, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
	emitFixture(&input, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "before "}})
	emitFixture(&input, map[string]any{"type": "content_block_stop", "index": 0})
	emitFixture(&input, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "stop_sequence", "stop_sequence": "END", "safeguard_results": []any{map[string]any{"type": "dangerous_tool_use", "status": map[string]any{"type": "available", "tool_uses": map[string]any{}}}}}, "usage": map[string]int{"output_tokens": 7}})
	emitFixture(&input, map[string]any{"type": "message_stop"})
	req := guardedRequest()
	req.StopSequences = []string{"END"}
	session := &messagesSession{ctx: context.Background(), request: req, events: make(chan bridgeEvent, 16), pending: make(map[string]*pendingTool), changed: make(chan struct{})}
	_, err := bridgeEvents(&input, session)
	if !errors.Is(err, errTerminalStop) {
		t.Fatalf("error=%v, want native stop sequence completion", err)
	}
	close(session.events)
	var output strings.Builder
	for event := range session.events {
		output.Write(event.data)
	}
	if !strings.Contains(output.String(), `"safeguard_results"`) || !strings.Contains(output.String(), `"stop_sequence":"END"`) {
		t.Fatalf("native stop sequence/verdict lost: %s", &output)
	}
}

func TestSafeguardsParallelIdenticalCalls(t *testing.T) {
	var input bytes.Buffer
	emitFixture(&input, map[string]any{"type": "message_start"})
	for i, id := range []string{"native-first", "native-second"} {
		emitFixture(&input, map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": id, "name": "read_file", "input": map[string]string{"path": "same.txt"}}})
		emitFixture(&input, map[string]any{"type": "content_block_stop", "index": i})
	}
	emitFixture(&input, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use", "safeguard_results": []any{map[string]any{"type": "dangerous_tool_use", "status": map[string]any{"type": "available", "tool_uses": map[string]any{"native-first": map[string]string{"type": "evaluated", "outcome": "flagged"}, "native-second": map[string]string{"type": "evaluated", "outcome": "not_flagged"}}}}}}})
	emitFixture(&input, map[string]any{"type": "message_stop"})
	s := &messagesSession{ctx: context.Background(), request: guardedRequest(), events: make(chan bridgeEvent, 24), pending: make(map[string]*pendingTool), changed: make(chan struct{})}
	_, err := bridgeEvents(&input, s)
	if !errors.Is(err, errTerminalStop) {
		t.Fatalf("error=%v, want independently reviewed tool calls", err)
	}
	close(s.events)
	var ids []string
	var verdict map[string]struct {
		Outcome string `json:"outcome"`
	}
	for item := range s.events {
		var event struct {
			Block messageBlock `json:"content_block"`
			Delta struct {
				Results []struct {
					Status struct {
						Calls map[string]struct {
							Outcome string `json:"outcome"`
						} `json:"tool_uses"`
					} `json:"status"`
				} `json:"safeguard_results"`
			}
		}
		_ = json.Unmarshal(item.data, &event)
		if event.Block.Type == "tool_use" {
			ids = append(ids, event.Block.ID)
		}
		if len(event.Delta.Results) > 0 {
			verdict = event.Delta.Results[0].Status.Calls
		}
	}
	if len(ids) != 2 || ids[0] != "native-first" || ids[1] != "native-second" || verdict[ids[0]].Outcome != "flagged" || verdict[ids[1]].Outcome != "not_flagged" {
		t.Fatalf("parallel tool/verdict association lost: ids=%v verdict=%+v", ids, verdict)
	}
}

func TestMessagesProtocolSelectorsBindContinuation(t *testing.T) {
	b := newMessagesBridge("unused", "sonnet", map[string]principal{"first": {"tenant", "key"}}, runtime.NewSystemClock())
	b.start = fixtureStart
	defer b.close()
	req := testMessagesRequest()
	continued := continuation(t, req, sendMessages(b, req, "first"), "result")
	changed := continued
	changed.apiVersion = "future-version"
	if _, err := b.acquire(principal{"tenant", "key"}, changed); !errors.Is(err, errCallRejected) {
		t.Fatalf("changed API version was silently applied to retained CLI: %v", err)
	}
	if _, err := b.acquire(principal{"tenant", "key"}, continued); err != nil {
		t.Fatalf("rejected change consumed the real continuation: %v", err)
	}
}

func TestSafeguardsInput(t *testing.T) {
	for _, tc := range []struct {
		name, guard string
		wantError   bool
	}{
		{"caller context", `{"type":"dangerous_tool_use","classifier_context":{"v":1,"opaque":"preserved"}}`, false},
		{"unknown kind", `{"type":"unknown","classifier_context":{}}`, true},
		{"missing context", `{"type":"dangerous_tool_use"}`, true},
		{"non-object context", `{"type":"dangerous_tool_use","classifier_context":"private"}`, true},
		{"large context", `{"type":"dangerous_tool_use","classifier_context":{"data":"` + strings.Repeat("x", 96<<10) + `"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := guardedRequest()
			req.Safeguards = []json.RawMessage{json.RawMessage(tc.guard)}
			err := req.validate(req.Model)
			if (err != nil) != tc.wantError || (err != nil && strings.Contains(err.Error(), "private")) {
				t.Fatalf("error=%v, want error=%t without reflected payload", err, tc.wantError)
			}
		})
	}
}

func TestSafeguardsResultsFollowPublicToolIDs(t *testing.T) {
	for _, outcome := range []string{"not_flagged", "flagged"} {
		t.Run(outcome, func(t *testing.T) {
			var input bytes.Buffer
			fixtureRound(&input, "native-call", "")
			var wire strings.Builder
			for _, line := range strings.Split(strings.ReplaceAll(strings.TrimSpace(input.String()), "mcp__aisw_bridge__tool_0", "read_file"), "\n") {
				var envelope map[string]json.RawMessage
				_ = json.Unmarshal([]byte(line), &envelope)
				var event map[string]json.RawMessage
				_ = json.Unmarshal(envelope["event"], &event)
				if string(event["type"]) == `"message_delta"` {
					event["delta"] = json.RawMessage(fmt.Sprintf(`{"stop_reason":"tool_use","safeguard_results":[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"native-call":{"type":"evaluated","outcome":%q,"opaque":"upstream-proof"}}},"future":{"preserved":true}}]}`, outcome))
					envelope["event"], _ = json.Marshal(event)
				}
				encoded, _ := json.Marshal(envelope)
				fmt.Fprintln(&wire, string(encoded))
			}
			s := &messagesSession{ctx: context.Background(), request: guardedRequest(), events: make(chan bridgeEvent, 16), pending: make(map[string]*pendingTool), changed: make(chan struct{})}
			final, err := bridgeEvents(strings.NewReader(wire.String()), s)
			if !errors.Is(err, errTerminalStop) || string(final) != `{"type":"message_stop"}` {
				t.Fatalf("final/error=%s/%v, want complete tool round and stopped CLI", final, err)
			}
			close(s.events)
			var id string
			var results []struct {
				Status struct {
					ToolUses map[string]map[string]any `json:"tool_uses"`
				} `json:"status"`
				Future map[string]bool `json:"future"`
			}
			for item := range s.events {
				var event struct {
					Type  string
					Block messageBlock `json:"content_block"`
					Delta struct {
						Results json.RawMessage `json:"safeguard_results"`
					}
				}
				_ = json.Unmarshal(item.data, &event)
				if event.Block.Type == "tool_use" {
					id = event.Block.ID
				}
				if len(event.Delta.Results) > 0 {
					if err := json.Unmarshal(event.Delta.Results, &results); err != nil {
						t.Fatal(err)
					}
				}
			}
			if id != "native-call" || len(results) != 1 || len(results[0].Status.ToolUses) != 1 || results[0].Status.ToolUses[id]["outcome"] != outcome || results[0].Status.ToolUses[id]["opaque"] != "upstream-proof" || !results[0].Future["preserved"] {
				t.Fatalf("tool/verdict correlation lost: id=%q results=%+v", id, results)
			}
		})
	}
}

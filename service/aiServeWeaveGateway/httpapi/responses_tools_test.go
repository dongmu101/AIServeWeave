package httpapi_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// These tests cover the tool loop an agent client such as Codex CLI runs over
// the Responses API: tool definitions in, function_call items out, their
// function_call_output items back in. Every one of them asserts on what the
// backend below actually received, or on the wire shape the client actually
// gets, because the translation in between is the whole feature.
//
// 这些测试覆盖 Codex CLI 之类的 agent 客户端在 Responses API 上跑的工具循环：工具定义
// 进来、function_call 项出去、它们的 function_call_output 项再回来。每一个都断言后端
// 实际收到了什么，或客户端实际拿到的线上形状，因为中间那次转换就是这个功能的全部。

// describeRequestHandler answers Chat with a text description of the canonical
// request it received: one line per message (tool calls and tool-result ids
// included), then one line per tool, then the tool_choice.
//
// describeRequestHandler 用一段文字描述它所收到的 canonical 请求来应答 Chat：每条消息
// 一行（含工具调用与工具结果的 id），随后每个工具一行，最后是 tool_choice。
func describeRequestHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	in, err := tunnelwire.UnmarshalChatRequest(req.GetPayload())
	if err != nil {
		return err
	}
	var lines []string
	for _, m := range in.Messages {
		line := m.Role + ":" + m.Content
		if m.ToolCallID != "" {
			line += " [for " + m.ToolCallID + "]"
		}
		for _, c := range m.ToolCalls {
			line += " [call " + c.ID + " " + c.Function.Name + " " + c.Function.Arguments + "]"
		}
		lines = append(lines, line)
	}
	for _, tl := range in.Tools {
		lines = append(lines, "tool:"+tl.Function.Name)
	}
	if in.ToolChoice != "" {
		lines = append(lines, "tool_choice:"+in.ToolChoice)
	}
	payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
		ID:           "chat-1",
		Model:        in.Model,
		Message:      runtime.ChatMessage{Role: "assistant", Content: strings.Join(lines, "\n")},
		FinishReason: "stop",
	})
	if err != nil {
		return err
	}
	return reply(gatewaytest.DataFrame(payload))
}

// backendSaw posts body to /v1/responses against describeRequestHandler and
// returns the lines describing the request the backend received.
//
// backendSaw 把 body 发到 /v1/responses，对着 describeRequestHandler，返回描述后端所收到
// 请求的各行。
func backendSaw(t *testing.T, body string) ([]string, *http.Response) {
	t.Helper()
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), describeRequestHandler)
	resp, out := postResponses(t, srv.URL, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(out.Output) != 1 || len(out.Output[0].Content) != 1 {
		t.Fatalf("output = %+v, want one text part", out.Output)
	}
	return strings.Split(out.Output[0].Content[0].Text, "\n"), resp
}

func TestResponsesToolLoopInputBecomesChatMessages(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name: "developer is the system role, reasoning is dropped",
			input: `[
				{"type":"message","role":"developer","content":[{"type":"input_text","text":"be careful"}]},
				{"type":"reasoning","summary":[],"encrypted_content":"opaque"},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"list files"}]}
			]`,
			want: []string{"system:be careful", "user:list files"},
		},
		{
			name: "a call and its output become an assistant tool call and a tool message",
			input: `[
				{"type":"message","role":"user","content":"list files"},
				{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
				{"type":"function_call_output","call_id":"call_1","output":"a.go b.go"}
			]`,
			want: []string{
				"user:list files",
				`assistant: [call call_1 shell {"cmd":"ls"}]`,
				"tool:a.go b.go [for call_1]",
			},
		},
		{
			name: "parallel calls share one assistant message",
			input: `[
				{"type":"message","role":"user","content":"go"},
				{"type":"function_call","call_id":"call_1","name":"a","arguments":"{}"},
				{"type":"function_call","call_id":"call_2","name":"b","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":"one"},
				{"type":"function_call_output","call_id":"call_2","output":"two"}
			]`,
			want: []string{
				"user:go",
				"assistant: [call call_1 a {}] [call call_2 b {}]",
				"tool:one [for call_1]",
				"tool:two [for call_2]",
			},
		},
		{
			name: "text before a call stays in the same assistant message",
			input: `[
				{"type":"message","role":"user","content":"go"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"checking"}]},
				{"type":"function_call","call_id":"call_1","name":"a","arguments":"{}"}
			]`,
			want: []string{"user:go", "assistant:checking [call call_1 a {}]"},
		},
		{
			name: "an output given as an array of parts, and as a legacy object",
			input: `[
				{"type":"function_call","call_id":"call_1","name":"a","arguments":"{}"},
				{"type":"function_call","call_id":"call_2","name":"b","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"x"},{"type":"input_text","text":"y"}]},
				{"type":"function_call_output","call_id":"call_2","output":{"content":"legacy","success":true}}
			]`,
			want: []string{
				"assistant: [call call_1 a {}] [call call_2 b {}]",
				"tool:xy [for call_1]",
				"tool:legacy [for call_2]",
			},
		},
		{
			name: "a namespaced call is qualified with its namespace",
			input: `[
				{"type":"function_call","call_id":"call_1","namespace":"multi_agent_v1","name":"close_agent","arguments":"{}"},
				{"type":"function_call","call_id":"call_2","namespace":"mcp__docs__","name":"search","arguments":"{}"}
			]`,
			want: []string{"assistant: [call call_1 multi_agent_v1__close_agent {}] [call call_2 mcp__docs__search {}]"},
		},
		{
			name: "a custom tool call carries its text as the input argument",
			input: `[
				{"type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"*** Begin Patch"},
				{"type":"custom_tool_call_output","call_id":"call_1","output":"Done!"}
			]`,
			want: []string{
				`assistant: [call call_1 apply_patch {"input":"*** Begin Patch"}]`,
				"tool:Done! [for call_1]",
			},
		},
		{
			name: "a local shell call becomes a local_shell function call",
			input: `[
				{"type":"local_shell_call","id":"lsh_1","call_id":"call_1","status":"completed","action":{"type":"exec","command":["ls","-la"],"timeout_ms":5000,"working_directory":"/tmp"}},
				{"type":"local_shell_call_output","call_id":"call_1","output":"total 0"}
			]`,
			want: []string{
				`assistant: [call call_1 local_shell {"command":["ls","-la"],"timeout_ms":5000,"working_directory":"/tmp"}]`,
				"tool:total 0 [for call_1]",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := backendSaw(t, `{"model":"qwen3:8b","input":`+tt.input+`}`)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("the backend saw:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestResponsesToolDefinitionsBecomeChatFunctions(t *testing.T) {
	got, resp := backendSaw(t, `{"model":"qwen3:8b","input":"hi","tool_choice":{"type":"function","name":"shell"},"tools":[
		{"type":"function","name":"shell","parameters":{"type":"object"}},
		{"type":"custom","name":"apply_patch","description":"edit files","format":{"type":"grammar","syntax":"lark","definition":"start: x"}},
		{"type":"local_shell"},
		{"type":"namespace","name":"multi_agent_v1","description":"sub-agents","tools":[
			{"type":"function","name":"close_agent","parameters":{"type":"object"}},
			{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}
		]},
		{"type":"web_search","external_web_access":false}
	]}`)

	want := []string{
		"user:hi",
		"tool:shell",
		"tool:apply_patch",
		"tool:local_shell",
		"tool:multi_agent_v1__close_agent",
		"tool:multi_agent_v1__spawn_agent",
		`tool_choice:{"function":{"name":"shell"},"type":"function"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the backend saw:\n%q\nwant:\n%q", got, want)
	}
	// The one tool that was not forwarded is reported rather than dropped
	// without a word.
	//
	// 唯一没有被转发的那个工具会被如实报告，而不是一声不吭地丢掉。
	if h := resp.Header.Get("X-Gateway-Ignored-Tools"); h != "web_search" {
		t.Errorf("X-Gateway-Ignored-Tools = %q, want web_search", h)
	}
}

func TestResponsesRejectsToolShapesItCannotServe(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantIn string
	}{
		{
			name:   "an input item type with nowhere to go",
			body:   `{"model":"qwen3:8b","input":[{"type":"compaction","encrypted_content":"x"}]}`,
			wantIn: "compaction",
		},
		{
			name:   "a call output without a call_id",
			body:   `{"model":"qwen3:8b","input":[{"type":"function_call_output","output":"x"}]}`,
			wantIn: "call_id",
		},
		{
			name:   "a tool_choice type it cannot express",
			body:   `{"model":"qwen3:8b","input":"hi","tool_choice":{"type":"file_search"}}`,
			wantIn: "file_search",
		},
		{
			name:   "a custom tool with no name",
			body:   `{"model":"qwen3:8b","input":"hi","tools":[{"type":"custom"}]}`,
			wantIn: "name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, h := newServer(t, httpapi.Config{})
			connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), describeRequestHandler)

			resp, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decoding error: %v", err)
			}
			if !strings.Contains(body.Error.Message, tt.wantIn) {
				t.Errorf("error = %q, want it to name %q", body.Error.Message, tt.wantIn)
			}
		})
	}
}

// mixedCallsHandler answers with some text and one call of each tool kind.
//
// mixedCallsHandler 用一段文字加上每种工具类型各一个调用作答。
func mixedCallsHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
		ID: "chat-1",
		Message: runtime.ChatMessage{Role: "assistant", Content: "on it", ToolCalls: []runtime.ToolCall{
			{ID: "call_1", Type: "function", Function: runtime.FunctionCall{Name: "multi_agent_v1__close_agent", Arguments: `{"target":"a"}`}},
			{ID: "call_2", Type: "function", Function: runtime.FunctionCall{Name: "apply_patch", Arguments: `{"input":"*** Begin Patch"}`}},
			{ID: "call_3", Type: "function", Function: runtime.FunctionCall{Name: "local_shell", Arguments: `{"command":["ls"],"timeout_ms":1000}`}},
			{ID: "call_4", Type: "function", Function: runtime.FunctionCall{Name: "noargs", Arguments: ``}},
		}},
		FinishReason: "tool_calls",
	})
	if err != nil {
		return err
	}
	return reply(gatewaytest.DataFrame(payload))
}

const mixedTools = `[
	{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]},
	{"type":"custom","name":"apply_patch"},
	{"type":"local_shell"},
	{"type":"function","name":"noargs","parameters":{"type":"object"}}
]`

func TestResponsesRendersEachToolKindAsItsOwnItemType(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), mixedCallsHandler)

	resp, err := http.Post(srv.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"qwen3:8b","input":"go","tools":`+mixedTools+`}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	// The text and every call are all present, in that order: a reply that
	// says "on it" and then calls tools must not lose either half.
	//
	// 文本与每个调用都在，且顺序如此：一个先说「开始了」再调用工具的回复，不能丢掉任何一半。
	if len(body.Output) != 5 {
		t.Fatalf("output has %d items, want 5: %v", len(body.Output), body.Output)
	}
	types := make([]string, len(body.Output))
	for i, it := range body.Output {
		types[i], _ = it["type"].(string)
	}
	wantTypes := []string{"message", "function_call", "custom_tool_call", "local_shell_call", "function_call"}
	if !reflect.DeepEqual(types, wantTypes) {
		t.Fatalf("item types = %v, want %v", types, wantTypes)
	}

	if fc := body.Output[1]; fc["name"] != "close_agent" || fc["namespace"] != "multi_agent_v1" ||
		fc["call_id"] != "call_1" || fc["arguments"] != `{"target":"a"}` {
		t.Errorf("namespaced function_call = %v, want close_agent in multi_agent_v1 with its arguments", fc)
	}
	if ct := body.Output[2]; ct["name"] != "apply_patch" || ct["input"] != "*** Begin Patch" || ct["call_id"] != "call_2" {
		t.Errorf("custom_tool_call = %v, want apply_patch with the unwrapped input", ct)
	}
	ls := body.Output[3]
	action, _ := ls["action"].(map[string]any)
	if ls["call_id"] != "call_3" || action["type"] != "exec" || action["timeout_ms"] != float64(1000) ||
		!reflect.DeepEqual(action["command"], []any{"ls"}) {
		t.Errorf("local_shell_call = %v, want an exec action running ls with a 1000ms timeout", ls)
	}
	// A call with no arguments still carries an arguments string: Responses
	// clients treat the field as required on function_call.
	//
	// 没有参数的调用依旧带有 arguments 字符串：Responses 客户端把该字段视为
	// function_call 的必填项。
	if na := body.Output[4]; na["arguments"] != "{}" {
		t.Errorf("no-argument function_call arguments = %v, want {}", na["arguments"])
	}
}

// sseFrame is one decoded SSE data frame of a Responses stream.
//
// sseFrame 是 Responses 流中一个已解码的 SSE data 帧。
type sseFrame map[string]any

func (f sseFrame) typ() string { s, _ := f["type"].(string); return s }

// streamResponses posts a streaming request and returns every decoded frame.
//
// streamResponses 发出一个流式请求并返回每个已解码的帧。
func streamResponses(t *testing.T, url, body string) []sseFrame {
	t.Helper()
	resp, err := http.Post(url+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var frames []sseFrame
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if strings.HasPrefix(line, "data: ") {
			var f sseFrame
			if uerr := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &f); uerr != nil {
				t.Fatalf("decoding SSE data: %v", uerr)
			}
			frames = append(frames, f)
		}
		if err != nil {
			break
		}
	}
	return frames
}

func frameTypes(frames []sseFrame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = f.typ()
	}
	return out
}

// streamingEventsHandler streams the given events as one chat stream.
//
// streamingEventsHandler 把给定的事件作为一条聊天流发出。
func streamingEventsHandler(events ...runtime.ChatEvent) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		for _, ev := range events {
			payload, err := tunnelwire.MarshalChatEvent(ev)
			if err != nil {
				return err
			}
			if err := reply(gatewaytest.DataFrame(payload)); err != nil {
				return err
			}
		}
		return nil
	}
}

func toolDelta(index int, id, name, args string) runtime.ChatEvent {
	return runtime.ChatEvent{ID: "chat-1", Delta: runtime.ChatMessageDelta{ToolCalls: []runtime.ToolCallDelta{{
		Index: index, ID: id, Type: "function", Function: runtime.FunctionCallDelta{Name: name, Arguments: args},
	}}}}
}

func textDelta(s string) runtime.ChatEvent {
	return runtime.ChatEvent{ID: "chat-1", Delta: runtime.ChatMessageDelta{Role: "assistant", Content: s}}
}

// TestResponsesStreamEmitsToolCallItems pins the sequence Codex builds its turn
// from: it acts on output_item.done, so every item must arrive closed, whole
// and at its own output_index — with a message that was open when the first
// call started closed before that call is announced.
//
// TestResponsesStreamEmitsToolCallItems 钉住 Codex 据以构建回合的那个序列：它依据
// output_item.done 行动，因此每个项都必须完整、闭合地抵达，且位于各自的 output_index
// ——在第一个调用开始时仍然打开着的消息，要在该调用被宣告之前先关闭。
func TestResponsesStreamEmitsToolCallItems(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), streamingEventsHandler(
		textDelta("let me "), textDelta("look"),
		toolDelta(0, "call_1", "multi_agent_v1__close_agent", `{"tar`),
		toolDelta(1, "call_2", "apply_patch", `{"input":`),
		toolDelta(0, "", "", `get":"a"}`),
		toolDelta(1, "", "", `"*** Begin Patch"}`),
		runtime.ChatEvent{ID: "chat-1", FinishReason: "tool_calls", Usage: &runtime.Usage{PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10}},
	))

	frames := streamResponses(t, srv.URL, `{"model":"qwen3:8b","input":"go","stream":true,"tools":`+mixedTools+`}`)

	// Argument fragments of the first call arrive after the second call opened,
	// so the events interleave; pin the structure that matters rather than one
	// exact ordering of them.
	//
	// 第一个调用的参数片段在第二个调用打开之后才到达，因此事件是交错的；钉住真正要紧的
	// 结构，而不是它们的某一种精确排列。
	got := frameTypes(frames)
	if got[0] != "response.created" || got[len(got)-1] != "response.completed" {
		t.Fatalf("events = %v, want created … completed", got)
	}
	for i := 1; i < len(frames); i++ {
		if a, b := frames[i-1]["sequence_number"].(float64), frames[i]["sequence_number"].(float64); b <= a {
			t.Errorf("sequence_number went %v then %v at event %d, want strictly increasing", a, b, i)
		}
	}

	// Collect what the client would act on: the done item of each index.
	//
	// 收集客户端会据以行动的东西：每个下标的 done 项。
	done := map[int]map[string]any{}
	var order []int
	for i, f := range frames {
		if f.typ() != "response.output_item.done" {
			continue
		}
		idx := int(f["output_index"].(float64))
		done[idx] = f["item"].(map[string]any)
		order = append(order, idx)
		// A call is announced before it is closed.
		//
		// 调用先被宣告、后被关闭。
		announced := false
		for _, earlier := range frames[:i] {
			if earlier.typ() == "response.output_item.added" && int(earlier["output_index"].(float64)) == idx {
				announced = true
			}
		}
		if !announced {
			t.Errorf("output_index %d was closed without ever being announced", idx)
		}
	}
	if !reflect.DeepEqual(order, []int{0, 1, 2}) {
		t.Fatalf("items closed in order %v, want 0, 1, 2 (message first, then the calls)", order)
	}
	if done[0]["type"] != "message" {
		t.Errorf("item 0 = %v, want the message", done[0])
	}
	if it := done[1]; it["type"] != "function_call" || it["name"] != "close_agent" || it["namespace"] != "multi_agent_v1" ||
		it["call_id"] != "call_1" || it["arguments"] != `{"target":"a"}` || it["status"] != "completed" {
		t.Errorf("item 1 = %v, want the namespaced function_call with its reassembled arguments", it)
	}
	if it := done[2]; it["type"] != "custom_tool_call" || it["name"] != "apply_patch" ||
		it["call_id"] != "call_2" || it["input"] != "*** Begin Patch" {
		t.Errorf("item 2 = %v, want the custom_tool_call with its unwrapped input", it)
	}

	// response.completed repeats the whole output and carries usage, which is
	// where Codex reads the turn's token counts.
	//
	// response.completed 会重复整个 output 并携带 usage，Codex 就是从那里读取本回合的
	// token 数。
	last := frames[len(frames)-1]["response"].(map[string]any)
	if out := last["output"].([]any); len(out) != 3 {
		t.Errorf("completed output has %d items, want 3", len(out))
	}
	if u := last["usage"].(map[string]any); u["input_tokens"] != float64(4) || u["total_tokens"] != float64(10) {
		t.Errorf("completed usage = %v, want input 4 total 10", u)
	}

	// The argument deltas of the plain function reassemble to its arguments.
	//
	// 普通 function 的参数 delta 能重新拼回它的 arguments。
	var args strings.Builder
	for _, f := range frames {
		if f.typ() == "response.function_call_arguments.delta" && f["output_index"].(float64) == 1 {
			args.WriteString(f["delta"].(string))
		}
	}
	if args.String() != `{"target":"a"}` {
		t.Errorf("argument deltas for item 1 = %q, want {\"target\":\"a\"}", args.String())
	}
}

// TestResponsesStreamToleratesBackendsThatNumberOrNameCallsLoosely covers what
// real backends do: omit the call id, resend nothing on later chunks, or number
// every call 0 while giving each its own id.
//
// TestResponsesStreamToleratesBackendsThatNumberOrNameCallsLoosely 覆盖真实后端会做的
// 事：省略 call id、后续 chunk 什么都不重发，或者把每个调用都编号为 0、同时各给一个 id。
func TestResponsesStreamToleratesBackendsThatNumberOrNameCallsLoosely(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), streamingEventsHandler(
		toolDelta(0, "", "first", `{"a":1}`),
		toolDelta(0, "call_x", "second", `{"b":2}`),
		toolDelta(0, "call_y", "third", ``),
		runtime.ChatEvent{ID: "chat-1", FinishReason: "tool_calls"},
	))

	frames := streamResponses(t, srv.URL, `{"model":"qwen3:8b","input":"go","stream":true,"tools":[
		{"type":"function","name":"first"},{"type":"function","name":"second"},{"type":"function","name":"third"}]}`)

	var calls []map[string]any
	for _, f := range frames {
		if f.typ() == "response.output_item.done" {
			calls = append(calls, f["item"].(map[string]any))
		}
	}
	if len(calls) != 3 {
		t.Fatalf("got %d closed items, want 3 (each new call id starts a new call): %v", len(calls), calls)
	}
	if id, _ := calls[0]["call_id"].(string); !strings.HasPrefix(id, "call_") {
		t.Errorf("a call the backend never named got call_id %q, want a generated call_-prefixed id", id)
	}
	if calls[1]["call_id"] != "call_x" || calls[1]["arguments"] != `{"b":2}` {
		t.Errorf("second call = %v, want call_x with {\"b\":2}", calls[1])
	}
	if calls[2]["name"] != "third" || calls[2]["arguments"] != "{}" {
		t.Errorf("third call = %v, want name third with empty arguments normalised to {}", calls[2])
	}
}

func TestResponsesStreamReportsATruncatedReplyAsIncomplete(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), streamingEventsHandler(
		textDelta("cut"),
		runtime.ChatEvent{ID: "chat-1", FinishReason: "length"},
	))

	frames := streamResponses(t, srv.URL, `{"model":"qwen3:8b","input":"go","stream":true}`)
	got := frameTypes(frames)
	if last := got[len(got)-1]; last != "response.incomplete" {
		t.Fatalf("stream ends with %q, want response.incomplete (events %v)", last, got)
	}
	resp := frames[len(frames)-1]["response"].(map[string]any)
	if resp["status"] != "incomplete" {
		t.Errorf("status = %v, want incomplete", resp["status"])
	}
}

// TestResponsesAdditionalTools merges inline declarations into caller-executed tools.
// TestResponsesAdditionalTools 将内联声明合并为调用方执行的工具。
func TestResponsesAdditionalTools(t *testing.T) {
	got, _ := backendSaw(t, `{"model":"qwen3:8b","tools":[{"type":"function","name":"top","parameters":{"type":"object"}}],"input":[
  {"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}]},{"type":"custom","name":"apply_patch"}]},
  {"role":"user","content":"hi"}
 ]}`)
	want := []string{"user:hi", "tool:top", "tool:functions__shell", "tool:apply_patch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backend=%q, want %q", got, want)
	}
	for _, tc := range []struct{ name, inline string }{
		{"wrong role", `{"type":"additional_tools","role":"user","tools":[{"type":"function","name":"shell"}]}`},
		{"empty", `{"type":"additional_tools","role":"developer","tools":[]}`},
		{"hosted", `{"type":"additional_tools","role":"developer","tools":[{"type":"file_search"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newServer(t, httpapi.Config{})
			resp, _ := postResponses(t, srv.URL, `{"model":"qwen3:8b","input":[`+tc.inline+`,{"role":"user","content":"hi"}]}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400", resp.StatusCode)
			}
		})
	}
}

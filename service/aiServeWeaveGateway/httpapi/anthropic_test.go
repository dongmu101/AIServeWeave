package httpapi_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

func postMessages(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// anthropicEchoRequestHandler answers Chat by echoing the canonical request
// it received — Tools, ToolChoice, and each message's role/content/
// ToolCallID/ToolCalls — one line per message, so a test can assert on
// exactly what crossed the tunnel rather than just the wire shape near the
// front door.
func anthropicEchoRequestHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	in, err := tunnelwire.UnmarshalChatRequest(req.GetPayload())
	if err != nil {
		return err
	}
	var lines []string
	lines = append(lines, "tools="+strings.Join(toolNames(in.Tools), ","))
	lines = append(lines, "tool_choice="+in.ToolChoice)
	for _, m := range in.Messages {
		line := m.Role + ":" + m.Content
		if m.ToolCallID != "" {
			line += "[tool_call_id:" + m.ToolCallID + "]"
		}
		for _, c := range m.ToolCalls {
			line += "[tool_use:" + c.ID + "/" + c.Function.Name + "/" + c.Function.Arguments + "]"
		}
		lines = append(lines, line)
	}
	payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
		ID:           "chat-1",
		Model:        in.Model,
		Message:      runtime.ChatMessage{Role: "assistant", Content: strings.Join(lines, "\n")},
		FinishReason: "stop",
		Usage:        runtime.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
	})
	if err != nil {
		return err
	}
	return reply(gatewaytest.DataFrame(payload))
}

func toolNames(tools []runtime.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Function.Name
	}
	return out
}

// TestAnthropicMessagesToolsAndToolResultTranslate proves the whole tool
// round trip on the request side: "tools" becomes runtime.Tool, a
// named-tool "tool_choice" becomes the OpenAI-shaped ToolChoice string, an
// assistant "tool_use" block becomes a ToolCall on the assistant message,
// and a user "tool_result" block becomes its own synthetic "tool"-role
// message carrying ToolCallID — matching every other front door's shape
// rather than Anthropic's own content-block embedding.
func TestAnthropicMessagesToolsAndToolResultTranslate(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), anthropicEchoRequestHandler)

	resp := postMessages(t, srv.URL, `{
		"model":"qwen3:8b","max_tokens":256,
		"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"tool","name":"get_weather"},
		"messages":[
			{"role":"user","content":"what's the weather?"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Beijing"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"72F"}
			]}
		]
	}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200, body = %s", resp.StatusCode, body)
	}

	var body struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Content) != 1 {
		t.Fatalf("content = %+v, want one block", body.Content)
	}
	want := strings.Join([]string{
		"tools=get_weather",
		`tool_choice={"type":"function","function":{"name":"get_weather"}}`,
		"user:what's the weather?",
		`assistant:[tool_use:toolu_1/get_weather/{"city":"Beijing"}]`,
		"tool:72F[tool_call_id:toolu_1]",
	}, "\n")
	if body.Content[0].Text != want {
		t.Errorf("echoed request =\n%s\nwant\n%s", body.Content[0].Text, want)
	}
}

// TestAnthropicMessagesRendersToolUseBlock proves the response side: a
// ToolCall on the backend's answer becomes a "tool_use" content block with
// stop_reason "tool_use", not silently dropped text.
func TestAnthropicMessagesRendersToolUseBlock(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), toolCallingHandler)

	resp := postMessages(t, srv.URL, `{"model":"qwen3:8b","max_tokens":256,
		"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"weather?"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Content []struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", body.StopReason)
	}
	if len(body.Content) != 1 || body.Content[0].Type != "tool_use" {
		t.Fatalf("content = %+v, want one tool_use block", body.Content)
	}
	block := body.Content[0]
	if block.ID != "call_abc" || block.Name != "get_weather" || string(block.Input) != `{"city":"Beijing"}` {
		t.Errorf("tool_use block = %+v, want call_abc/get_weather/{\"city\":\"Beijing\"}", block)
	}
}

// toolCallStreamHandler streams one tool call in two deltas — the first
// carrying id/name, the second the arguments fragment — then finishes with
// FinishReason "tool_calls", the shape an OpenAI-compatible backend uses to
// stream a single (non-parallel) tool call.
func toolCallStreamHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	events := []runtime.ChatEvent{
		{ID: "chat-1", Delta: runtime.ChatMessageDelta{Role: "assistant"}},
		{ID: "chat-1", Delta: runtime.ChatMessageDelta{ToolCalls: []runtime.ToolCallDelta{
			{Index: 0, ID: "call_abc", Type: "function", Function: runtime.FunctionCallDelta{Name: "get_weather"}},
		}}},
		{ID: "chat-1", Delta: runtime.ChatMessageDelta{ToolCalls: []runtime.ToolCallDelta{
			{Index: 0, Function: runtime.FunctionCallDelta{Arguments: `{"city":"Beijing"}`}},
		}}},
		{ID: "chat-1", FinishReason: "tool_calls", Usage: &runtime.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8}},
	}
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

// TestAnthropicMessagesStreamingToolUse proves the streaming side emits
// Anthropic's own tool_use content-block sequence: the leading text block
// (index 0) is stopped as soon as a tool-call delta arrives, a
// content_block_start announces the tool_use block with its id/name before
// any argument fragment, and content_block_delta carries each argument
// fragment as input_json_delta.partial_json rather than parsed JSON.
func TestAnthropicMessagesStreamingToolUse(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), toolCallStreamHandler)

	resp := postMessages(t, srv.URL, `{"model":"qwen3:8b","max_tokens":256,"stream":true,
		"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"weather?"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	type event struct {
		name string
		data string
	}
	var events []event
	scanner := bufio.NewScanner(resp.Body)
	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			events = append(events, event{name: currentEvent, data: strings.TrimPrefix(line, "data: ")})
		}
	}

	wantNames := []string{"message_start", "content_block_start", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if len(events) != len(wantNames) {
		t.Fatalf("events = %+v, want %d events %v", events, len(wantNames), wantNames)
	}
	for i, want := range wantNames {
		if events[i].name != want {
			t.Errorf("events[%d].name = %q, want %q (full sequence: %+v)", i, events[i].name, want, events)
		}
	}

	var start struct {
		Index        int `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(events[3].data), &start); err != nil {
		t.Fatalf("decoding tool_use content_block_start: %v", err)
	}
	if start.Index != 1 || start.ContentBlock.Type != "tool_use" || start.ContentBlock.ID != "call_abc" || start.ContentBlock.Name != "get_weather" {
		t.Errorf("tool_use content_block_start = %+v, want index=1 id=call_abc name=get_weather", start)
	}

	var delta struct {
		Index int `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(events[4].data), &delta); err != nil {
		t.Fatalf("decoding tool_use content_block_delta: %v", err)
	}
	if delta.Index != 1 || delta.Delta.Type != "input_json_delta" || delta.Delta.PartialJSON != `{"city":"Beijing"}` {
		t.Errorf("tool_use content_block_delta = %+v, want index=1 input_json_delta {\"city\":\"Beijing\"}", delta)
	}
}

// TestAnthropicMessagesAcceptsImageBlocks proves an Anthropic "image"
// content block (base64 source) crosses the whole pipeline — httpapi's
// anthropicMessageContent, runtime.ChatMessage.ContentParts,
// tunnelwire's proto codec, and back out through a fake node's own
// tunnelwire decode (STATUS.md's P2 ChatMessage.Content structured
// rework) — rather than only asserting on a wire shape near the front
// door.
func TestAnthropicMessagesAcceptsImageBlocks(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandlerEchoingParts)

	resp := postMessages(t, srv.URL, `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":[
		{"type":"text","text":"what is this"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"Zm9v"}}
	]}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Content) != 1 {
		t.Fatalf("content = %+v, want one text block", body.Content)
	}
	want := "text= text=what is this image=data:image/png;base64,Zm9v"
	if body.Content[0].Text != want {
		t.Errorf("content text = %q, want %q", body.Content[0].Text, want)
	}
}

// TestAnthropicMessagesAcceptsDocumentBlocks proves an Anthropic "document"
// content block (base64 source, with an optional title) crosses the whole
// pipeline the same way TestAnthropicMessagesAcceptsImageBlocks already
// proves for "image" (STATUS.md's P2 multimodal input, following on the
// earlier image delivery).
func TestAnthropicMessagesAcceptsDocumentBlocks(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandlerEchoingParts)

	resp := postMessages(t, srv.URL, `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":[
		{"type":"text","text":"summarize this"},
		{"type":"document","title":"report.pdf","source":{"type":"base64","media_type":"application/pdf","data":"cGRm"}}
	]}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Content) != 1 {
		t.Fatalf("content = %+v, want one text block", body.Content)
	}
	want := "text= text=summarize this file=data:application/pdf;base64,cGRm/report.pdf"
	if body.Content[0].Text != want {
		t.Errorf("content text = %q, want %q", body.Content[0].Text, want)
	}
}

func TestAnthropicMessagesNonStreaming(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postMessages(t, srv.URL,
		`{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Type != "message" || body.Role != "assistant" {
		t.Errorf("type/role = %q/%q, want message/assistant", body.Type, body.Role)
	}
	if len(body.Content) != 1 || body.Content[0].Type != "text" || body.Content[0].Text != "answer to: hello" {
		t.Errorf("content = %+v, want one text block %q", body.Content, "answer to: hello")
	}
	if body.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", body.StopReason)
	}
	if body.Usage.InputTokens != 3 || body.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v, want input=3 output=5", body.Usage)
	}
}

// TestAnthropicMessagesSystemAndBlocksTranslate covers the translation this
// endpoint exists to do: a top-level "system" string becomes a leading
// system message, and a "content" array of text blocks is joined into one
// plain-text message — both landing on the same canonical request every
// other front door produces.
func TestAnthropicMessagesSystemAndBlocksTranslate(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp := postMessages(t, srv.URL, `{
		"model":"qwen3:8b",
		"max_tokens":256,
		"system":"be terse",
		"messages":[{"role":"user","content":[{"type":"text","text":"hello "},{"type":"text","text":"there"}]}]
	}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	want := "system:be terse\nuser:hello there"
	if len(body.Content) != 1 || body.Content[0].Text != want {
		t.Errorf("echoed messages = %+v, want one block %q", body.Content, want)
	}
}

func TestAnthropicMessagesStreamingSequence(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postMessages(t, srv.URL,
		`{"model":"qwen3:8b","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	var events []string
	var content strings.Builder
	sawMessageStop := false
	scanner := bufio.NewScanner(resp.Body)
	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
			events = append(events, currentEvent)
			if currentEvent == "message_stop" {
				sawMessageStop = true
			}
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			if currentEvent == "content_block_delta" {
				var chunk struct {
					Delta struct{ Text string } `json:"delta"`
				}
				if err := json.Unmarshal([]byte(data), &chunk); err != nil {
					t.Fatalf("decoding content_block_delta %q: %v", data, err)
				}
				content.WriteString(chunk.Delta.Text)
			}
		}
	}
	if !sawMessageStop {
		t.Error("stream ended without a message_stop event")
	}
	if content.String() != "Hello" {
		t.Errorf("streamed content = %q, want %q", content.String(), "Hello")
	}

	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i, ev := range want {
		if events[i] != ev {
			t.Errorf("events[%d] = %q, want %q (full sequence: %v)", i, events[i], ev, events)
		}
	}
}

func TestAnthropicMessagesRejectsUnsupportedBlocks(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "an image block with an unsupported source type",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":[
				{"type":"image","source":{"type":"file","file_id":"file_1"}}]}]}`,
		},
		{
			name: "a document block with an unsupported source type",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":[
				{"type":"document","source":{"type":"file","file_id":"file_1"}}]}]}`,
		},
		{
			name: "a tool_use content block missing id",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"assistant","content":[
				{"type":"tool_use","name":"get_weather","input":{}}]}]}`,
		},
		{
			name: "a tool_result content block missing tool_use_id",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":[
				{"type":"tool_result","content":"72F"}]}]}`,
		},
		{
			name: "an unsupported tool_choice type",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":"hi"}],
				"tool_choice":{"type":"bogus"}}`,
		},
		{
			name: "an invalid message role",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"tool","content":"hi"}]}`,
		},
		{
			name: "missing max_tokens",
			body: `{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "empty messages",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[]}`,
		},
	}
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := postMessages(t, srv.URL, tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			var body struct {
				Type  string `json:"type"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decoding error body: %v", err)
			}
			if body.Type != "error" || body.Error.Type != "invalid_request_error" {
				t.Errorf("error body = %+v, want type=error, error.type=invalid_request_error", body)
			}
			if body.Error.Message == "" {
				t.Error("error.message is empty, want a description of the rejected input")
			}
		})
	}
}

func TestAnthropicMessagesUnknownModelReturns404(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postMessages(t, srv.URL,
		`{"model":"does-not-exist","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	var body struct {
		Type  string                `json:"type"`
		Error struct{ Type string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Type != "error" || body.Error.Type != "invalid_request_error" {
		t.Errorf("error body = %+v, want type=error, error.type=invalid_request_error", body)
	}
}

package httpapi_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func postMessages(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"/v1/messages", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
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

func TestAnthropicMessagesRejectsToolsAndUnsupportedBlocks(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "tools is present",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":"hi"}],
				"tools":[{"name":"get_weather","input_schema":{}}]}`,
		},
		{
			name: "tool_choice is present",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":"hi"}],
				"tool_choice":{"type":"auto"}}`,
		},
		{
			name: "a non-text content block",
			body: `{"model":"qwen3:8b","max_tokens":256,"messages":[{"role":"user","content":[
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}]}]}`,
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

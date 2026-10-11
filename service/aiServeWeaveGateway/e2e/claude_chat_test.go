//go:build darwin || linux

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/claudecode"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/messagesession"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// TestClaudeChatThroughTunnel verifies Chat tools, affinity, ownership and SSE over real mTLS streams.
// TestClaudeChatThroughTunnel 通过真实 mTLS 流验证 Chat 工具、节点绑定、归属与 SSE。
func TestClaudeChatThroughTunnel(t *testing.T) {
	f := newFleetWithBackend(t, 2, runtime.KindClaude, claudecode.NewFactory(os.Args[0]))
	f.awaitReady(f.replicas...)
	store := &fixtureMessagesStore{rounds: make(map[string]messagesession.Record)}
	var urls []string
	for _, replica := range f.replicas {
		api := httpapi.New(scheduler.New(replica.server, scheduler.Config{}), httpapi.Config{Verifier: claudeLiveVerifier{}, MessagesSessions: store})
		t.Cleanup(api.Close)
		server := httptest.NewServer(api)
		t.Cleanup(server.Close)
		urls = append(urls, server.URL)
	}
	for _, tc := range []struct {
		name   string
		stream bool
	}{{"JSON", false}, {"SSE", true}} {
		t.Run(tc.name, func(t *testing.T) { runClaudeChatTools(t, urls, "claude-sonnet-4-6", tc.stream, true) })
	}
}

type claudeChatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []claudeChatCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type claudeChatCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func postClaudeChat(t *testing.T, url, key, model string, messages []claudeChatMessage, tools []any, stream bool, wantStatus int) (claudeChatMessage, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "tools": tools, "max_completion_tokens": 1024, "stream": stream, "stream_options": map[string]bool{"include_usage": true}, "store": false})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("Chat status=%d, want %d", resp.StatusCode, wantStatus)
	}
	if wantStatus != 200 {
		return claudeChatMessage{}, ""
	}
	var result struct {
		Model   string
		Choices []struct {
			Message      claudeChatMessage
			Delta        claudeChatMessage
			FinishReason *string `json:"finish_reason"`
		}
		Usage *struct {
			Total int `json:"total_tokens"`
		}
	}
	if !stream {
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if len(result.Choices) != 1 || result.Choices[0].FinishReason == nil || result.Model != model {
			t.Fatal("Chat response must contain one finished choice and unchanged model ID")
		}
		return result.Choices[0].Message, *result.Choices[0].FinishReason
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	message, finish, done, usage := claudeChatMessage{}, "", 0, false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" {
			done++
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		result.Choices = nil
		result.Usage = nil
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &result) != nil || result.Model != model {
			t.Fatal("invalid Chat SSE chunk or changed model ID")
		}
		if result.Usage != nil {
			usage = result.Usage.Total > 0 && len(result.Choices) == 0
		}
		for _, choice := range result.Choices {
			if choice.Delta.Role != "" {
				message.Role = choice.Delta.Role
			}
			message.Content += choice.Delta.Content
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
			for _, call := range choice.Delta.ToolCalls {
				if call.Index < 0 || call.Index > 7 {
					t.Fatalf("tool index=%d, want 0..7", call.Index)
				}
				for len(message.ToolCalls) <= call.Index {
					message.ToolCalls = append(message.ToolCalls, claudeChatCall{})
				}
				out := &message.ToolCalls[call.Index]
				out.ID += call.ID
				out.Type += call.Type
				out.Function.Name += call.Function.Name
				out.Function.Arguments += call.Function.Arguments
			}
		}
	}
	if scanner.Err() != nil || done != 1 || finish == "" || !usage {
		t.Fatalf("SSE error=%v DONE=%d finish=%q usage=%t, want nil, 1, nonempty, true", scanner.Err(), done, finish, usage)
	}
	return message, finish
}

func runClaudeChatTools(t *testing.T, urls []string, model string, stream, negativeChecks bool) {
	t.Helper()
	messages := []claudeChatMessage{{Role: "user", Content: "Use read_file to read caller-only.txt. After the tool result, reply with its exact content. Do not guess the content."}}
	tools := []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}, "required": []string{"path"}}}}}
	first, finish := postClaudeChat(t, urls[0], "claude-live-first", model, messages, tools, stream, 200)
	if finish != "tool_calls" || len(first.ToolCalls) != 1 || first.ToolCalls[0].Function.Name != "read_file" || !json.Valid([]byte(first.ToolCalls[0].Function.Arguments)) {
		t.Fatalf("first=%+v finish=%q, want one read_file call and tool_calls", first, finish)
	}
	messages = append(messages, first, claudeChatMessage{Role: "tool", ToolCallID: first.ToolCalls[0].ID, Content: "unique-caller-result"})
	if negativeChecks {
		postClaudeChat(t, urls[1], "claude-live-second", model, messages, tools, false, 409)
		postClaudeChat(t, urls[1], "claude-live-first", "wrong-model", messages, tools, false, 409)
		changed := append([]claudeChatMessage(nil), messages...)
		changed[0].Content = "tampered history"
		postClaudeChat(t, urls[1], "claude-live-first", model, changed, tools, false, 400)
	}
	final, finish := postClaudeChat(t, urls[1], "claude-live-first", model, messages, tools, stream, 200)
	if !strings.Contains(final.Content, "unique-caller-result") || finish != "stop" {
		t.Fatalf("final=%+v finish=%q, want caller result and stop", final, finish)
	}
	if negativeChecks {
		postClaudeChat(t, urls[0], "claude-live-first", model, messages, tools, false, 409)
	}
}

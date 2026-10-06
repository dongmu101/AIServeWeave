package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

// TestRuntimeMessages keeps a completed tool session alive after its tunnel request closes.
// TestRuntimeMessages 在隧道请求关闭后仍保留已完成回合的工具会话。
func TestRuntimeMessages(t *testing.T) {
	rt, err := NewFactory("unused")(runtime.Config{ID: "claude-local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	r.bridge.start = fixtureStart
	defer r.Close()
	req := testMessagesRequest()
	round := func(parent context.Context, req messagesRequest) []messageBlock {
		t.Helper()
		data, _ := json.Marshal(req)
		stream, err := r.Messages(parent, runtime.MessagesRequest{Model: "sonnet", TenantID: "tenant", KeyID: "key", JSON: data})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		var content []messageBlock
		stops := 0
		for {
			item, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			var event struct {
				Type  string
				Index int
				Block messageBlock `json:"content_block"`
				Delta struct {
					Type, Text string
					Partial    string `json:"partial_json"`
				}
			}
			if json.Unmarshal(item.JSON, &event) != nil {
				t.Fatal("invalid native JSON event")
			}
			switch event.Type {
			case "content_block_start":
				content = append(content, event.Block)
				if event.Block.Type == "tool_use" {
					content[event.Index].Input = nil
				}
			case "content_block_delta":
				content[event.Index].Text += event.Delta.Text
				content[event.Index].Input = append(content[event.Index].Input, event.Delta.Partial...)
			case "message_stop":
				stops++
			}
		}
		if stops != 1 {
			t.Fatalf("message_stop count=%d, want 1", stops)
		}
		return content
	}
	ctx, cancel := context.WithCancel(t.Context())
	content := round(ctx, req)
	cancel()
	if len(content) != 1 || content[0].Type != "tool_use" {
		t.Fatal("first round must return one tool")
	}
	data, _ := json.Marshal(content)
	req.Messages = append(req.Messages, bridgeMessage{Role: "assistant", Content: data})
	data, _ = json.Marshal([]messageBlock{{Type: "tool_result", ToolUseID: content[0].ID, Content: json.RawMessage(`"caller-result"`)}})
	req.Messages = append(req.Messages, bridgeMessage{Role: "user", Content: data})
	content = round(t.Context(), req)
	if len(content) != 1 || content[0].Text != "caller-result" {
		t.Fatal("final text did not match caller result")
	}
}

// TestToolSchemaDefaults permits declared defaults without accepting altered arguments.
// TestToolSchemaDefaults 允许显式声明的默认值，但不接受修改已有参数。
func TestToolSchemaDefaults(t *testing.T) {
	tools := []bridgeTool{{Name: "Edit", InputSchema: json.RawMessage(`{"properties":{"replace_all":{"default":false}}}`)}}
	for _, tc := range []struct {
		name, before, after string
		equal               bool
	}{
		{"declared default", `{"path":"a"}`, `{"path":"a","replace_all":false}`, true},
		{"changed explicit value", `{"path":"a","replace_all":true}`, `{"path":"a","replace_all":false}`, false},
		{"undeclared argument", `{"path":"a"}`, `{"path":"a","offset":0}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := canonical(normalizedToolInput("Edit", json.RawMessage(tc.before), tools))
			b := canonical(normalizedToolInput("Edit", json.RawMessage(tc.after), tools))
			if (a == b) != tc.equal {
				t.Fatalf("equivalent=%t, want %t", a == b, tc.equal)
			}
		})
	}
}

// TestNativeOwnership rejects missing attribution before starting a CLI.
// TestNativeOwnership 在启动 CLI 前拒绝缺失的归属信息。
func TestNativeOwnership(t *testing.T) {
	rt, _ := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	r := rt.(*Runtime)
	defer r.Close()
	for _, tc := range []struct{ name, tenant, key string }{
		{"missing tenant", "", "key"}, {"missing key", "tenant", ""}, {"oversized key", "tenant", strings.Repeat("k", 257)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Messages(t.Context(), runtime.MessagesRequest{Model: "sonnet", TenantID: tc.tenant, KeyID: tc.key, JSON: json.RawMessage(`{}`)})
			if err == nil {
				t.Fatal("error=nil, want invalid config")
			}
		})
	}
}

// TestRuntimeCloseUnconsumedStream releases synchronous writers during shutdown.
// TestRuntimeCloseUnconsumedStream 在关闭时释放同步等待消费的写入方。
func TestRuntimeCloseUnconsumedStream(t *testing.T) {
	rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	r.bridge.start = fixtureStart
	data, _ := json.Marshal(testMessagesRequest())
	stream, err := r.Messages(t.Context(), runtime.MessagesRequest{Model: "sonnet", TenantID: "tenant", KeyID: "key", JSON: data})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeCapacityBound keeps even an unlimited configuration within two resident processes.
// TestClaudeCapacityBound 即使配置无限并发也将驻留进程限制为两个。
func TestClaudeCapacityBound(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configured, want int
	}{{"unlimited", -1, 2}, {"default", 0, 2}, {"one", 1, 1}, {"many", 8, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude, MaxConcurrent: tc.configured}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			if got := rt.Descriptor().MaxConcurrent; got != tc.want {
				t.Fatalf("capacity=%d,want %d", got, tc.want)
			}
		})
	}
}

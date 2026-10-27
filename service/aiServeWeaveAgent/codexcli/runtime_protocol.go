package codexcli

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"AIServeWeave/common/runtime"
)

// runChat turns canonical history into isolated app-server events.
// runChat 把规范化历史转换为隔离的 app-server 事件。
func runChat(p *protocol, dir string, req runtime.ChatRequest, input chatInput, instructions []string, emit func(runtime.ChatEvent) error) error {
	if err := initializeServer(p); err != nil {
		return err
	}
	names := make([]string, 0, len(input.allowed))
	for name := range input.allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	params := map[string]any{
		"cwd": dir, "ephemeral": true, "sandbox": "read-only", "approvalPolicy": "never", "environments": []any{},
		"baseInstructions":      "You are an assistant serving a remote client. Follow the supplied conversation. Use only the supplied dynamic tools; the caller executes them on its own machine. Never access the server's files or execute server shell, MCP or plugin tools.",
		"developerInstructions": "Request client actions using these registered dynamic tool names: " + strings.Join(names, ", ") + ". Client tools may read files or execute commands on the caller's machine. Disabled server shell tools do not disable the client's tools. Return their calls to the caller; never execute client code locally.", "dynamicTools": input.tools,
	}
	if req.Model != "" {
		params["model"] = req.Model
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model              string   `json:"model"`
		InstructionSources []string `json:"instructionSources"`
	}
	if err := p.request(3, "thread/start", params, &thread); err != nil {
		return err
	}
	if thread.Thread.ID == "" || thread.Model == "" || len(thread.Model) > 256 {
		return ErrProtocol
	}
	for _, path := range thread.InstructionSources {
		allowed := false
		for _, expected := range instructions {
			if filepath.Clean(path) == expected {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrIsolation
		}
	}
	if err := p.request(4, "thread/inject_items", map[string]any{"threadId": thread.Thread.ID, "items": input.items}, new(map[string]any)); err != nil {
		return err
	}
	turnParams := map[string]any{"threadId": thread.Thread.ID, "input": []any{}}
	if len(input.schema) > 0 {
		turnParams["outputSchema"] = input.schema
	}
	if err := p.write(map[string]any{"id": 5, "method": "turn/start", "params": turnParams}); err != nil {
		return err
	}
	var turnID string
	var early []frame
	earlyBytes := 0
	for turnID == "" {
		msg, err := p.read()
		if err != nil {
			return err
		}
		if msg.Method != "" {
			if len(msg.ID) > 0 {
				return ErrUnexpectedTool
			}
			earlyBytes += len(msg.Params)
			if earlyBytes > maxFrameBytes || len(early) >= 64 {
				return ErrProtocol
			}
			early = append(early, msg)
			continue
		}
		var id int
		if json.Unmarshal(msg.ID, &id) != nil || id != 5 {
			return ErrProtocol
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return ErrRPC
		}
		var result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(msg.Result, &result) != nil || result.Turn.ID == "" {
			return ErrProtocol
		}
		turnID = result.Turn.ID
	}
	event := runtime.ChatEvent{ID: turnID, Model: thread.Model}
	var usage *runtime.Usage
	textBytes := 0
	for {
		var msg frame
		if len(early) > 0 {
			msg = early[0]
			early = early[1:]
		} else {
			var err error
			msg, err = p.read()
			if err != nil {
				return err
			}
		}
		var n struct {
			ThreadID  string          `json:"threadId"`
			TurnID    string          `json:"turnId"`
			CallID    string          `json:"callId"`
			Tool      string          `json:"tool"`
			Namespace *string         `json:"namespace"`
			Arguments json.RawMessage `json:"arguments"`
			Delta     string          `json:"delta"`
			ItemID    string          `json:"itemId"`
			Item      struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"item"`
			Turn struct {
				ID     string          `json:"id"`
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"turn"`
			TokenUsage struct {
				Last struct {
					Input  int `json:"inputTokens"`
					Output int `json:"outputTokens"`
					Total  int `json:"totalTokens"`
				} `json:"last"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(msg.Params, &n) != nil {
			return ErrProtocol
		}
		if n.ThreadID != "" && n.ThreadID != thread.Thread.ID {
			return ErrProtocol
		}
		if n.TurnID != "" && n.TurnID != turnID {
			return ErrProtocol
		}
		if len(msg.ID) > 0 {
			if msg.Method != "item/tool/call" {
				return ErrUnexpectedTool
			}
			if !validRequestID(msg.ID) || n.ThreadID != thread.Thread.ID || n.TurnID != turnID || n.Namespace != nil || !input.allowed[n.Tool] || n.CallID == "" || len(n.CallID) > 256 || !json.Valid(n.Arguments) {
				return ErrProtocol
			}
			var args map[string]any
			if json.Unmarshal(n.Arguments, &args) != nil || args == nil {
				return ErrProtocol
			}
			event.Delta = runtime.ChatMessageDelta{Role: "assistant", ToolCalls: []runtime.ToolCallDelta{{Index: 0, ID: n.CallID, Type: "function", Function: runtime.FunctionCallDelta{Name: n.Tool, Arguments: string(n.Arguments)}}}}
			if err := emit(event); err != nil {
				return err
			}
			event.Delta = runtime.ChatMessageDelta{}
			event.FinishReason = "tool_calls"
			event.Usage = usage
			// Stop at the callback boundary; never synthesize a result or execute it locally.
			// 在回调边界停止，绝不伪造工具结果或在本机执行客户端工具。
			return emit(event)
		}
		switch msg.Method {
		case "item/agentMessage/delta":
			if n.ThreadID != thread.Thread.ID || n.TurnID != turnID || n.ItemID == "" {
				return ErrProtocol
			}
			textBytes += len(n.Delta)
			if textBytes > maxFrameBytes {
				return ErrProtocol
			}
			event.Delta = runtime.ChatMessageDelta{Role: "assistant", Content: n.Delta}
			if err := emit(event); err != nil {
				return err
			}
		case "thread/tokenUsage/updated":
			if n.ThreadID != thread.Thread.ID || n.TurnID != turnID || n.TokenUsage.Last.Input < 0 || n.TokenUsage.Last.Output < 0 || n.TokenUsage.Last.Total < 0 {
				return ErrProtocol
			}
			usage = &runtime.Usage{PromptTokens: n.TokenUsage.Last.Input, CompletionTokens: n.TokenUsage.Last.Output, TotalTokens: n.TokenUsage.Last.Total}
		case "turn/completed":
			if n.ThreadID != thread.Thread.ID || n.Turn.ID != turnID {
				return ErrProtocol
			}
			if n.Turn.Status != "completed" || (len(n.Turn.Error) > 0 && string(n.Turn.Error) != "null") {
				return ErrTurn
			}
			event.Delta = runtime.ChatMessageDelta{}
			event.FinishReason = "stop"
			event.Usage = usage
			return emit(event)
		case "item/started", "item/completed":
			if n.ThreadID != thread.Thread.ID || n.TurnID != turnID {
				return ErrProtocol
			}
		case "error":
			return ErrTurn
		}
	}
}

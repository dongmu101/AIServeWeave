package codexcli

import (
	"encoding/json"
	"regexp"
	"strings"

	"AIServeWeave/common/runtime"
)

var toolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

type chatInput struct {
	items   []any
	tools   []any
	allowed map[string]bool
	schema  json.RawMessage
}

// prepareChat preserves ordered message and tool history without prompt flattening.
// prepareChat 保留有序消息及工具历史，不把工具结果拼成普通提示词。
func prepareChat(req runtime.ChatRequest) (chatInput, error) {
	out := chatInput{allowed: map[string]bool{}, tools: []any{}}
	if len(req.Messages) == 0 || len(req.Messages) > 2048 || len(req.Tools) > 128 || len(req.Model) > 256 {
		return out, ErrConfig
	}
	if req.Temperature != nil || req.TopP != nil || req.MaxTokens != nil || req.Seed != nil || len(req.Stop) > 0 || len(req.Extra) > 0 {
		return out, errUnsupported
	}
	if req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" {
		return out, errUnsupported
	}
	for _, tool := range req.Tools {
		f := tool.Function
		var schema map[string]any
		if tool.Type != "function" || !toolName.MatchString(f.Name) || out.allowed[f.Name] || json.Unmarshal(f.Parameters, &schema) != nil || schema == nil || schema["type"] != "object" {
			return out, ErrConfig
		}
		out.allowed[f.Name] = true
		if req.ToolChoice != "none" {
			out.tools = append(out.tools, map[string]any{"name": f.Name, "description": f.Description, "inputSchema": f.Parameters})
		}
	}
	if req.ToolChoice == "none" {
		out.allowed = map[string]bool{}
	}
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "text":
		case "json_schema":
			if req.ResponseFormat.JSONSchema == nil || !json.Valid(req.ResponseFormat.JSONSchema.Schema) {
				return out, ErrConfig
			}
			out.schema = req.ResponseFormat.JSONSchema.Schema
		default:
			return out, errUnsupported
		}
	}
	pending := map[string]string{}
	seen := map[string]bool{}
	for _, m := range req.Messages {
		text := m.Content
		if len(m.ContentParts) > 0 {
			if text != "" {
				return out, ErrConfig
			}
			var b strings.Builder
			for _, part := range m.ContentParts {
				if part.Type != "text" {
					return out, errUnsupported
				}
				b.WriteString(part.Text)
			}
			text = b.String()
		}
		if m.Name != "" {
			return out, errUnsupported
		}
		if m.Role == "tool" {
			if m.ToolCallID == "" || pending[m.ToolCallID] == "" || len(m.ToolCalls) > 0 {
				return out, ErrConfig
			}
			out.items = append(out.items, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": text})
			delete(pending, m.ToolCallID)
			continue
		}
		if m.ToolCallID != "" {
			return out, ErrConfig
		}
		switch m.Role {
		case "system", "developer", "user", "assistant":
		default:
			return out, ErrConfig
		}
		if len(pending) > 0 {
			return out, ErrConfig
		}
		if m.Role != "assistant" && len(m.ToolCalls) > 0 {
			return out, ErrConfig
		}
		if text != "" {
			contentType := "input_text"
			if m.Role == "assistant" {
				contentType = "output_text"
			}
			out.items = append(out.items, map[string]any{"type": "message", "role": m.Role, "content": []any{map[string]string{"type": contentType, "text": text}}})
		}
		for _, call := range m.ToolCalls {
			if call.ID == "" || len(call.ID) > 256 || seen[call.ID] || call.Type != "function" || !toolName.MatchString(call.Function.Name) || !json.Valid([]byte(call.Function.Arguments)) {
				return out, ErrConfig
			}
			seen[call.ID] = true
			pending[call.ID] = call.Function.Name
			out.items = append(out.items, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	if len(pending) > 0 || len(out.items) == 0 {
		return out, ErrConfig
	}
	data, err := json.Marshal(out.items)
	if err != nil || len(data) > maxFrameBytes-1024 {
		return out, ErrConfig
	}
	return out, nil
}

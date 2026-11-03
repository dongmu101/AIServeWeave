package codexcli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"AIServeWeave/common/runtime"
)

var toolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// maxToolNameLen is the longest tool name toolName admits, and so the longest
// alias wireToolName may produce.
//
// maxToolNameLen 是 toolName 允许的最长工具名，也是 wireToolName 生成别名的长度上限。
const maxToolNameLen = 64

// reservedToolName reports whether app-server refuses name as a dynamic tool:
// "mcp" and the "mcp__" prefix are the namespace of its own MCP tools, and a
// turn carrying one fails as a whole. Claude Code names every MCP tool
// "mcp__<server>__<tool>", so any client with an MCP server configured hits
// this. The check is case-sensitive, matching app-server.
//
// reservedToolName 报告 app-server 是否拒绝把 name 作为动态工具："mcp" 与 "mcp__"
// 前缀是它自己 MCP 工具的命名空间，带上这类名字的回合会整体失败。Claude Code 把每个
// MCP 工具命名为 "mcp__<服务>__<工具>"，因此配置了任何 MCP 服务的客户端都会碰到。
// 与 app-server 一致，检查区分大小写。
func reservedToolName(name string) bool {
	return name == "mcp" || strings.HasPrefix(name, "mcp__")
}

// wireToolName returns the name a client tool is registered under with
// app-server: the name itself, or for a reserved name a "c_"-prefixed alias,
// cut and suffixed with a hash of the full name when the prefix would exceed
// maxToolNameLen. It is a pure function of name, so tool definitions and the
// tool calls replayed from history always agree.
//
// wireToolName 返回客户端工具在 app-server 中的注册名：通常就是原名；保留名则换成
// 加 "c_" 前缀的别名，加前缀后超过 maxToolNameLen 时截断并拼上完整名字的哈希。它只
// 取决于 name，因此工具定义与历史中回放的工具调用总能对上。
func wireToolName(name string) string {
	if !reservedToolName(name) {
		return name
	}
	alias := "c_" + name
	if len(alias) <= maxToolNameLen {
		return alias
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:6])
	return alias[:maxToolNameLen-len(suffix)] + suffix
}

type chatInput struct {
	items   []any
	tools   []any
	allowed map[string]bool // registered (wire) names / 注册名（别名）
	// original maps a wire name back to the client's tool name.
	// original 把注册名映射回客户端的工具名。
	original map[string]string
	schema   json.RawMessage
	// effort and summary are turn/start's reasoning overrides, empty to keep
	// the CLI's defaults; Runtime.reasoningFor derives them.
	// effort 与 summary 是 turn/start 的思考覆盖项，为空表示沿用 CLI 默认值；由
	// Runtime.reasoningFor 推导。
	effort, summary string
}

// clientName maps a tool name app-server called back to the client's name.
// clientName 把 app-server 回调时使用的工具名映射回客户端的名字。
func (in chatInput) clientName(wire string) string {
	if name, ok := in.original[wire]; ok {
		return name
	}
	return wire
}

// prepareChat preserves ordered message and tool history without prompt flattening.
// Positive output limits are advisory: app-server cannot enforce them. Tools
// are registered under wireToolName, so a reserved client name does not fail
// the turn.
//
// prepareChat 保留有序消息及工具历史，不把工具结果拼成普通提示词。
// 正数输出上限仅作兼容接收：app-server 无法执行该上限。工具以 wireToolName 注册，
// 客户端的保留名因此不会让回合失败。
func prepareChat(req runtime.ChatRequest) (chatInput, error) {
	out := chatInput{allowed: map[string]bool{}, original: map[string]string{}, tools: []any{}}
	if len(req.Messages) == 0 || len(req.Messages) > 2048 || len(req.Tools) > 128 || len(req.Model) > 256 {
		return out, ErrConfig
	}
	if req.MaxTokens != nil && *req.MaxTokens <= 0 {
		return out, ErrConfig
	}
	if req.Temperature != nil || req.TopP != nil || req.Seed != nil || len(req.Stop) > 0 || len(req.Extra) > 0 {
		return out, errUnsupported
	}
	if req.ToolChoice != "" && req.ToolChoice != "auto" && req.ToolChoice != "none" {
		return out, errUnsupported
	}
	for _, tool := range req.Tools {
		f := tool.Function
		var schema map[string]any
		wire := wireToolName(f.Name)
		// A duplicate, or an alias equal to another tool's real name, is refused.
		// 重名、或别名恰好等于另一个工具的真实名字，都拒绝。
		if tool.Type != "function" || !toolName.MatchString(f.Name) || out.allowed[wire] || json.Unmarshal(f.Parameters, &schema) != nil || schema == nil || schema["type"] != "object" {
			return out, ErrConfig
		}
		out.allowed[wire] = true
		out.original[wire] = f.Name
		if req.ToolChoice != "none" {
			out.tools = append(out.tools, map[string]any{"name": wire, "description": f.Description, "inputSchema": f.Parameters})
		}
	}
	if req.ToolChoice == "none" {
		out.allowed = map[string]bool{}
		out.original = map[string]string{}
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
			out.items = append(out.items, map[string]any{"type": "function_call", "call_id": call.ID, "name": wireToolName(call.Function.Name), "arguments": call.Function.Arguments})
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

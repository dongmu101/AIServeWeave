package codexcli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"AIServeWeave/common/runtime"
)

// toolName is what app-server accepts as a dynamic tool name; wireToolName
// maps every clientToolName onto it.
//
// toolName 是 app-server 接受的动态工具名；wireToolName 把每个 clientToolName 映射到它。
var toolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// clientToolName bounds the tool names a caller may send: longer than
// app-server allows (MCP names carry their server's name) and with the
// separators some clients use, but still a bounded identifier.
//
// clientToolName 限定调用方可发送的工具名：比 app-server 允许的更长（MCP 名字带着服务
// 名）、并允许部分客户端使用的分隔符，但仍是有界的标识符。
var clientToolName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)

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
// app-server. A name app-server accepts is kept; a reserved one that fits
// becomes "c_" + name. Anything else — too long, or with characters
// app-server refuses — becomes "c_" + a readable stem + "_" + 8 hex digits of
// the full name's hash: for "mcp__<server>__<tool>" the stem keeps the tool's
// own name and cuts the server first, so a long-named server's tools stay
// distinguishable to the model. It is a pure function of name, so tool
// definitions and the tool calls replayed from history always agree; the
// hash keeps distinct names apart.
//
// wireToolName 返回客户端工具在 app-server 中的注册名。app-server 接受的名字保持不变；
// 放得下的保留名变成 "c_" + 原名。其他情况——过长，或含 app-server 拒绝的字符——变成
// "c_" + 可读主干 + "_" + 完整名字哈希的 8 位十六进制：对 "mcp__<服务>__<工具>"，主干
// 保留工具自己的名字、优先截短服务名，让长服务名下的各个工具对模型仍可区分。它只取决于
// name，因此工具定义与历史中回放的工具调用总能对上；哈希让不同的名字互不相撞。
func wireToolName(name string) string {
	if toolName.MatchString(name) && !reservedToolName(name) {
		return name
	}
	if alias := "c_" + name; reservedToolName(name) && toolName.MatchString(alias) {
		return alias
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:4])
	budget := maxToolNameLen - len("c_") - len(suffix)
	stem := sanitizeToolName(name)
	if rest, ok := strings.CutPrefix(stem, "mcp__"); ok {
		stem = rest
		if i := strings.LastIndex(rest, "__"); i > 0 {
			server, tool := rest[:i], rest[i+2:]
			if keep := budget - len(tool) - len("__"); keep > 0 {
				stem = server[:min(len(server), keep)] + "__" + tool // cut the server first / 先截服务名
			} else {
				stem = tool
			}
		}
	}
	return "c_" + stem[:min(len(stem), budget)] + suffix
}

// sanitizeToolName replaces every character app-server refuses in a tool
// name with "_".
//
// sanitizeToolName 把工具名中 app-server 拒绝的每个字符替换为 "_"。
func sanitizeToolName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, name)
}

// objectSchema is the schema of a tool that declares no parameters.
// objectSchema 是未声明参数的工具所用的 schema。
var objectSchema = json.RawMessage(`{"type":"object","properties":{}}`)

// normalizeToolSchema returns a tool's parameter schema in the shape the
// tool turn requires — an object whose "type" is the string "object" — and
// whether it could be. An absent schema means no parameters; a missing or
// null "type" is taken as "object"; a type list that includes "object"
// becomes "object", since tool arguments are always an object. Any other
// type, an explicit null schema, or a schema that is not a JSON object,
// cannot be. A schema already in shape is returned byte for byte; nothing
// else is rewritten.
//
// normalizeToolSchema 把工具的参数 schema 整理成工具回合要求的形状——"type" 为字符串
// "object" 的对象——并报告能否做到。缺失的 schema 表示没有参数；缺少或为 null 的
// "type" 视为 "object"；包含 "object" 的类型列表变成 "object"，因为工具参数总是对象。
// 其他类型、显式的 null schema、或不是 JSON 对象的 schema 都做不到。已经符合形状的
// schema 逐字节原样返回；其他内容一概不改写。
func normalizeToolSchema(raw json.RawMessage) (json.RawMessage, bool) {
	if strings.TrimSpace(string(raw)) == "" {
		return objectSchema, true
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(raw, &schema) != nil || schema == nil {
		return nil, false
	}
	var typ any
	if t, ok := schema["type"]; ok && json.Unmarshal(t, &typ) != nil {
		return nil, false
	}
	switch v := typ.(type) {
	case nil:
	case string:
		if v != "object" {
			return nil, false
		}
		return raw, true
	case []any:
		object := false
		for _, item := range v {
			object = object || item == "object"
		}
		if !object {
			return nil, false
		}
	default:
		return nil, false
	}
	schema["type"] = json.RawMessage(`"object"`)
	out, err := json.Marshal(schema)
	if err != nil {
		return nil, false
	}
	return out, true
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
// are registered under wireToolName with a normalizeToolSchema schema, so a
// reserved or overlong client name, or a schema that only omits "object",
// does not fail the turn.
//
// prepareChat 保留有序消息及工具历史，不把工具结果拼成普通提示词。
// 正数输出上限仅作兼容接收：app-server 无法执行该上限。工具以 wireToolName 注册、
// schema 经 normalizeToolSchema 整理，因此客户端的保留名或超长名、以及只是漏写
// "object" 的 schema 都不会让回合失败。
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
		wire := wireToolName(f.Name)
		schema, ok := normalizeToolSchema(f.Parameters)
		// A duplicate, or an alias equal to another tool's real name, is refused.
		// 重名、或别名恰好等于另一个工具的真实名字，都拒绝。
		if tool.Type != "function" || !clientToolName.MatchString(f.Name) || out.allowed[wire] || !ok {
			return out, ErrConfig
		}
		out.allowed[wire] = true
		out.original[wire] = f.Name
		if req.ToolChoice != "none" {
			out.tools = append(out.tools, map[string]any{"name": wire, "description": f.Description, "inputSchema": schema})
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
			if call.ID == "" || len(call.ID) > 256 || seen[call.ID] || call.Type != "function" || !clientToolName.MatchString(call.Function.Name) || !json.Valid([]byte(call.Function.Arguments)) {
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

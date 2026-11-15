package claudecode

import (
	"encoding/json"
	"strings"
)

// Tool names a caller that is not Claude Code (DeepSeek Harness, an SDK)
// sends under the subscription login are given Claude Code's form upstream:
// its built-in tools in Claude Code's spelling, every other custom tool under
// an MCP-style prefix. The mapping is a function of the request alone, so the
// round that sends the document and the round that reads its output agree,
// and every upstream tool_use is renamed back before the caller sees it.
//
// 不是 Claude Code 的调用方（DeepSeek Harness、SDK）经订阅登录发出的工具名，在上游使用
// Claude Code 的形式：内建工具用 Claude Code 的拼写，其余自定义工具加 MCP 风格前缀。映射只由
// 请求本身决定，因此发送文档的一侧与读取输出的一侧一致；上游每个 tool_use 在到达调用方之前都
// 改回原名。
var claudeCodeToolNames = []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob"}

// callerToolPrefix marks the caller's other tools. callerToolPrefix 标记调用方的其他工具。
const callerToolPrefix = "mcp__caller__"

// maxUpstreamToolName is the upstream's limit on a custom tool name.
// maxUpstreamToolName 是上游对自定义工具名的长度上限。
const maxUpstreamToolName = 64

// nativeToolAliases maps each renamed caller tool to its upstream name, or
// returns nil when nothing is renamed: a guarded document (its policy context
// names the original tools), a caller that is Claude Code, or a mapping that
// would make two tools share a name.
//
// nativeToolAliases 把每个需要改名的调用方工具映射到上游名；不改名时返回 nil：带 safeguards 的
// 文档（其策略上下文引用原工具名）、调用方本身是 Claude Code，或映射会让两个工具同名。
func (r messagesRequest) nativeToolAliases() map[string]string {
	if !r.native || len(r.Safeguards) > 0 || r.nativeDocument == nil {
		return nil
	}
	if blocks, ok := systemBlocks(r.nativeDocument["system"]); !ok || hasPrefixBlock(blocks, identityPrefixes...) {
		return nil
	}
	var tools []map[string]json.RawMessage
	if json.Unmarshal(r.nativeDocument["tools"], &tools) != nil || len(tools) == 0 {
		return nil
	}
	declared := make(map[string]bool, len(tools))
	var custom []string
	for _, tool := range tools {
		var name string
		_ = json.Unmarshal(tool["name"], &name)
		if name == "" {
			continue
		}
		declared[name] = true
		if customTool(tool) {
			custom = append(custom, name)
		}
	}
	aliases := make(map[string]string)
	taken := make(map[string]bool)
	for _, name := range custom {
		alias := upstreamToolAlias(name)
		if alias == name {
			continue
		}
		if declared[alias] || taken[alias] {
			return nil
		}
		aliases[name], taken[alias] = alias, true
	}
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

// upstreamToolAlias returns the upstream name of one custom tool: a Claude
// Code built-in in its spelling, an MCP name unchanged, anything else
// prefixed. A prefixed name over the upstream limit stays as it is.
//
// upstreamToolAlias 返回单个自定义工具的上游名：Claude Code 内建工具用其拼写，MCP 名不变，
// 其余加前缀。加前缀后超过上游上限的名称保持原样。
func upstreamToolAlias(name string) string {
	for _, builtin := range claudeCodeToolNames {
		if strings.EqualFold(name, builtin) {
			return builtin
		}
	}
	if strings.HasPrefix(name, "mcp__") || len(callerToolPrefix)+len(name) > maxUpstreamToolName {
		return name
	}
	return callerToolPrefix + name
}

// customTool reports whether a tool is defined by the caller rather than
// being a typed server or client tool such as web_search or bash_20250124.
//
// customTool 判断工具是否由调用方定义，而不是 web_search、bash_20250124 这类带类型的服务端或
// 客户端工具。
func customTool(tool map[string]json.RawMessage) bool {
	var kind string
	_ = json.Unmarshal(tool["type"], &kind)
	return kind == "" || kind == "custom"
}

// renameNativeTools rewrites the fields of extra that name caller tools:
// tool definitions, a forced tool_choice, and the tool_use and
// tool_reference blocks of the history. Fields it cannot read are left for
// the upstream to judge.
//
// renameNativeTools 改写 extra 中点名调用方工具的字段：工具定义、强制的 tool_choice，以及
// 历史中的 tool_use 与 tool_reference 块。无法解析的字段留给上游判断。
func renameNativeTools(extra map[string]any, aliases map[string]string) {
	if len(aliases) == 0 {
		return
	}
	rename := func(key string, rewrite func(json.RawMessage) (json.RawMessage, bool)) {
		raw, ok := extra[key].(json.RawMessage)
		if !ok {
			return
		}
		if changed, ok := rewrite(raw); ok {
			extra[key] = changed
		}
	}
	rename("tools", func(raw json.RawMessage) (json.RawMessage, bool) {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(raw, &tools) != nil {
			return nil, false
		}
		changed := false
		for _, tool := range tools {
			if customTool(tool) && renameField(tool, "name", aliases) {
				changed = true
			}
		}
		return encodeIfChanged(tools, changed)
	})
	rename("tool_choice", func(raw json.RawMessage) (json.RawMessage, bool) {
		var choice map[string]json.RawMessage
		if json.Unmarshal(raw, &choice) != nil || choice == nil {
			return nil, false
		}
		var kind string
		_ = json.Unmarshal(choice["type"], &kind)
		return encodeIfChanged(choice, kind == "tool" && renameField(choice, "name", aliases))
	})
	rename("messages", func(raw json.RawMessage) (json.RawMessage, bool) {
		var messages []map[string]json.RawMessage
		if json.Unmarshal(raw, &messages) != nil {
			return nil, false
		}
		changed := false
		for _, message := range messages {
			if content, ok := renameBlocks(message["content"], aliases); ok {
				message["content"], changed = content, true
			}
		}
		return encodeIfChanged(messages, changed)
	})
}

// renameBlocks renames tool_use blocks, and tool_reference blocks inside
// tool_result content. A string content has no blocks.
//
// renameBlocks 改写 tool_use 块，以及 tool_result 内容里的 tool_reference 块。字符串内容没有块。
func renameBlocks(raw json.RawMessage, aliases map[string]string) (json.RawMessage, bool) {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, false
	}
	changed := false
	for _, block := range blocks {
		var kind string
		_ = json.Unmarshal(block["type"], &kind)
		switch kind {
		case "tool_use":
			changed = renameField(block, "name", aliases) || changed
		case "tool_reference":
			changed = renameField(block, "tool_name", aliases) || changed
		case "tool_result":
			if content, ok := renameBlocks(block["content"], aliases); ok {
				block["content"], changed = content, true
			}
		}
	}
	return encodeIfChanged(blocks, changed)
}

func renameField(object map[string]json.RawMessage, key string, aliases map[string]string) bool {
	var name string
	if json.Unmarshal(object[key], &name) != nil {
		return false
	}
	alias, ok := aliases[name]
	if !ok {
		return false
	}
	object[key], _ = json.Marshal(alias)
	return true
}

func encodeIfChanged(value any, changed bool) (json.RawMessage, bool) {
	if !changed {
		return nil, false
	}
	encoded, err := encodeMessagesJSON(value)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// restoreToolUseName renames the content_block of a content_block_start event
// to name, keeping every other field of the event and the block.
//
// restoreToolUseName 把 content_block_start 事件里 content_block 的名称改为 name，事件与块的
// 其他字段保持不变。
func restoreToolUseName(event json.RawMessage, name string) (json.RawMessage, bool) {
	var fields, block map[string]json.RawMessage
	if json.Unmarshal(event, &fields) != nil || json.Unmarshal(fields["content_block"], &block) != nil || block == nil {
		return nil, false
	}
	block["name"], _ = json.Marshal(name)
	encodedBlock, err := encodeMessagesJSON(block)
	if err != nil {
		return nil, false
	}
	fields["content_block"] = encodedBlock
	encoded, err := encodeMessagesJSON(fields)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

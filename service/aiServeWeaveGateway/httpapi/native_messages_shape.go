package httpapi

import (
	"encoding/json"
	"errors"
)

// native_messages_shape.go reshapes a public Messages body into the shape the
// Agent's Claude CLI bridge decodes strictly. Claude Code itself sends fields
// that are optimisation hints for the Messages API or that a CLI owning its
// own context has no use for; they are removed or folded here, while every
// field that changes what the model is asked stays and is still judged by
// the bridge.
//
// native_messages_shape.go 把公开 Messages 请求体整形为 Agent 的 Claude CLI 桥接严格解码
// 的形式。Claude Code 自己会发送一些字段：它们要么是 Messages API 的优化提示，要么对
// 自行管理上下文的 CLI 没有用处；这些字段在这里去掉或折叠，而凡是改变模型被要求内容的
// 字段都保留，仍由桥接判断。

// nativeBridgeShape rewrites document, the body req was decoded from, in
// place:
//
//   - context_management is removed: the retained CLI manages its own
//     context and never replays earlier thinking, so client-side context
//     edits have nothing to act on.
//   - cache_control is removed from system, message, nested tool_result and
//     tool blocks: prompt caching is an optimisation the bridge disables.
//   - tools become anthropicActiveTools(req) with only name, description
//     and input_schema, so a defer_loading tool stays hidden until it is
//     referenced, as on every other backend.
//   - messages go through nativeBridgeMessages.
//
// A system turn that cannot be understood is an error carrying
// anthropicMidConvSystemRejected, which tells Claude Code to resend without
// system turns.
//
// nativeBridgeShape 原地改写 document（即 req 解码自的请求体）：
//
//   - 去掉 context_management：保留的 CLI 自行管理上下文，也从不回放此前的思考，客户端的
//     上下文编辑没有作用对象。
//   - 去掉 system、消息、嵌套 tool_result 与工具上的 cache_control：提示缓存是桥接已关闭
//     的优化。
//   - tools 改为 anthropicActiveTools(req)，只保留 name、description 与 input_schema，
//     因此 defer_loading 工具在被引用之前保持隐藏，与其他后端一致。
//   - messages 经 nativeBridgeMessages 处理。
//
// 无法理解的 system 轮次返回带 anthropicMidConvSystemRejected 的错误，让 Claude Code
// 去掉 system 轮次后重发。
func nativeBridgeShape(document map[string]json.RawMessage, req anthropicMessagesRequest) error {
	delete(document, "context_management")
	if system, ok := document["system"]; ok {
		document["system"] = withoutCacheControl(system)
	}
	if _, ok := document["tools"]; ok {
		active, err := anthropicActiveTools(req)
		if err != nil {
			return err
		}
		type bridgeTool struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		tools := make([]bridgeTool, len(active))
		for i, t := range active {
			tools[i] = bridgeTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
		}
		if len(tools) == 0 {
			delete(document, "tools")
		} else {
			document["tools"], _ = json.Marshal(tools)
		}
	}
	if _, ok := document["messages"]; ok {
		messages, err := nativeBridgeMessages(req.Messages)
		if err != nil {
			return err
		}
		document["messages"], _ = json.Marshal(messages)
	}
	return nil
}

// nativeBridgeMessages returns messages without cache_control and without
// system turns. A system turn's text is appended, after a blank line, to the
// user message right before it when that message holds no tool_result —
// where Claude Code puts its environment turn — so a single question stays
// one prompt instead of a transcript whose last message is the environment.
// Otherwise the text becomes a user message of its own, held until after
// the tool results when it falls between a tool call and its results; a
// system turn with no text is dropped. Inside a tool_result, a
// tool_reference block becomes the same "Tool loaded" text other backends
// see. The rewrite depends only on the messages, so every round of a
// conversation folds its history the same way and continuations still match.
//
// nativeBridgeMessages 返回去掉 cache_control 与 system 轮次之后的消息。system 轮次的
// 文本在空一行后追加到紧邻其前、且不含 tool_result 的 user 消息上——Claude Code 正是
// 把环境轮次放在这里——这样单个提问仍是一条提问，而不会变成最后一条是环境信息的文本记录。
// 其他情况下文本自成一条 user 消息；若它落在工具调用与其结果之间，则暂缓到结果之后；
// 没有文本的 system 轮次直接省略。tool_result 内的 tool_reference 块变成与其他后端相同的
// "Tool loaded" 文本。改写只取决于消息本身，因此同一对话的每一轮都以相同方式折叠历史，
// 续接仍能匹配。
func nativeBridgeMessages(messages []anthropicMessageJSON) ([]anthropicMessageJSON, error) {
	out := make([]anthropicMessageJSON, 0, len(messages))
	var held []anthropicMessageJSON // system text waiting for tool results / 等待工具结果的 system 文本
	for i, m := range messages {
		if m.Role != "system" {
			content, err := nativeBridgeContent(m.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, anthropicMessageJSON{Role: m.Role, Content: content})
			if len(held) > 0 && m.Role == "user" {
				out, held = append(out, held...), nil
			}
			continue
		}
		turn, err := parseAnthropicSystemTurn(i, m.Content)
		if err != nil {
			return nil, err
		}
		if turn.text == "" {
			continue
		}
		block := map[string]string{"type": "text", "text": turn.text}
		if n := len(out); n > 0 && len(held) == 0 && out[n-1].Role == "user" && !hasToolResult(out[n-1].Content) {
			out[n-1].Content = appendTextBlock(out[n-1].Content, "\n\n"+turn.text)
			continue
		}
		content, _ := json.Marshal([]any{block})
		message := anthropicMessageJSON{Role: "user", Content: content}
		if n := len(out); len(held) > 0 || n > 0 && out[n-1].Role == "assistant" && hasToolUse(out[n-1].Content) {
			held = append(held, message)
		} else {
			out = append(out, message)
		}
	}
	return append(out, held...), nil
}

// nativeBridgeContent removes cache_control from a message's content
// blocks and reduces each tool_result's nested content the same way, with
// tool_reference blocks turned into text. String content and content that
// is not a block array pass through for the bridge to judge.
//
// nativeBridgeContent 去掉消息内容块上的 cache_control，并以同样方式处理每个 tool_result
// 的嵌套内容，其中 tool_reference 块变成文本。字符串内容以及不是块数组的内容原样交给
// 桥接判断。
func nativeBridgeContent(raw json.RawMessage) (json.RawMessage, error) {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return raw, nil
	}
	for _, b := range blocks {
		delete(b, "cache_control")
		if blockType(b) != "tool_result" || len(b["content"]) == 0 {
			continue
		}
		content, err := nativeBridgeToolResult(b["content"])
		if err != nil {
			return nil, err
		}
		b["content"] = content
	}
	data, _ := json.Marshal(blocks)
	return data, nil
}

// nativeBridgeToolResult removes cache_control from a tool_result's content
// blocks and replaces each tool_reference block with a text block; the
// referenced tool itself joins the tool list through anthropicActiveTools.
// A tool_reference without tool_name is an error, as on other backends.
//
// nativeBridgeToolResult 去掉 tool_result 内容块上的 cache_control，并把每个
// tool_reference 块换成文本块；被引用的工具本身经 anthropicActiveTools 加入工具列表。
// 缺少 tool_name 的 tool_reference 与其他后端一样报错。
func nativeBridgeToolResult(raw json.RawMessage) (json.RawMessage, error) {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return raw, nil
	}
	for i, b := range blocks {
		delete(b, "cache_control")
		if blockType(b) != "tool_reference" {
			continue
		}
		var name string
		if json.Unmarshal(b["tool_name"], &name) != nil || name == "" {
			return nil, errors.New(`a "tool_reference" block requires "tool_name"`)
		}
		text, _ := json.Marshal("Tool loaded and now callable: " + name)
		blocks[i] = map[string]json.RawMessage{"type": json.RawMessage(`"text"`), "text": text}
	}
	data, _ := json.Marshal(blocks)
	return data, nil
}

// withoutCacheControl removes cache_control from every block of a block
// array; anything else passes through unchanged.
//
// withoutCacheControl 去掉块数组中每个块的 cache_control；其他内容原样返回。
func withoutCacheControl(raw json.RawMessage) json.RawMessage {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return raw
	}
	for _, b := range blocks {
		delete(b, "cache_control")
	}
	data, _ := json.Marshal(blocks)
	return data
}

// appendTextBlock returns content with a text block holding text appended;
// string content first becomes one text block.
//
// appendTextBlock 返回追加了一个文本块后的 content；字符串内容先变成一个文本块。
func appendTextBlock(content json.RawMessage, text string) json.RawMessage {
	var blocks []json.RawMessage
	var s string
	if json.Unmarshal(content, &s) == nil {
		first, _ := json.Marshal(map[string]string{"type": "text", "text": s})
		blocks = []json.RawMessage{first}
	} else if json.Unmarshal(content, &blocks) != nil {
		return content
	}
	block, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	data, _ := json.Marshal(append(blocks, block))
	return data
}

// hasToolResult reports whether content holds a tool_result block.
// hasToolResult 报告 content 是否含有 tool_result 块。
func hasToolResult(content json.RawMessage) bool { return hasBlock(content, "tool_result") }

// hasToolUse reports whether content holds a tool_use block.
// hasToolUse 报告 content 是否含有 tool_use 块。
func hasToolUse(content json.RawMessage) bool { return hasBlock(content, "tool_use") }

func hasBlock(content json.RawMessage, want string) bool {
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if blockType(b) == want {
			return true
		}
	}
	return false
}

func blockType(b map[string]json.RawMessage) string {
	var t string
	_ = json.Unmarshal(b["type"], &t)
	return t
}

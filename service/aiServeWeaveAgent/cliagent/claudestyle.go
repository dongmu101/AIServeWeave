package cliagent

import (
	"bytes"
	"encoding/json"
	"strings"

	"AIServeWeave/common/runtime"
)

// claudeParser reads the Claude-Code-style `--output-format stream-json`
// dialect that CodeBuddy and Qoder share. Full assistant messages carry text
// and "thinking" blocks; Qoder may additionally stream text_delta and
// thinking_delta events first, so a full message that repeats already-streamed
// deltas is forwarded only past that prefix, separately for reply and
// thinking. Thinking goes to sink.reasoning, never into the reply; a
// redacted_thinking block carries no readable text and is skipped. Any
// tool_use or tool_result aborts the turn, and only a successful result event
// ends it.
//
// claudeParser 读取 CodeBuddy 与 Qoder 共用的 Claude Code 风格
// `--output-format stream-json` 方言。完整的 assistant 消息携带文本与 "thinking"
// 块；Qoder 还可能先流式发送 text_delta 与 thinking_delta 事件，因此重复已流式发送
// 增量的完整消息只转发该前缀之后的部分，回复与思考分别比对。思考交给
// sink.reasoning，从不混入回复；redacted_thinking 块没有可读文本，直接跳过。任何
// tool_use 或 tool_result 都会中止回合，只有成功的 result 事件才会结束回合。
type claudeParser struct {
	streamed strings.Builder // reply deltas since the last full message / 上一条完整消息之后的回复增量
	thought  strings.Builder // thinking deltas since the last full message / 上一条完整消息之后的思考增量
	emitted  bool            // reply text only; thinking alone is no reply / 仅指回复文本；只有思考不算回复
	usage    *runtime.Usage
	done     bool
}

func (p *claudeParser) line(event []byte, out sink) (bool, error) {
	obj := object(event)
	switch str(obj, "type") {
	case "stream_event":
		inner := object(obj["event"])
		delta := object(inner["delta"])
		switch {
		case str(inner, "type") == "content_block_start" && str(object(inner["content_block"]), "type") == "tool_use":
			return false, ErrUnexpectedTool
		case str(delta, "type") == "text_delta":
			s := str(delta, "text")
			p.streamed.WriteString(s)
			p.emitted = p.emitted || s != ""
			return false, out.text(s)
		case str(delta, "type") == "thinking_delta":
			s := str(delta, "thinking")
			p.thought.WriteString(s)
			return false, out.reasoning(s)
		}
		return false, nil
	case "assistant", "user":
		var message struct {
			Content json.RawMessage `json:"content"`
		}
		if raw := obj["message"]; raw != nil && json.Unmarshal(raw, &message) != nil {
			return false, ErrProtocol
		}
		// A plain-string content (an echoed user prompt) carries no blocks.
		// 字符串形式的 content（回显的用户提示词）不含任何块。
		var blocks []map[string]json.RawMessage
		if trimmed := bytes.TrimSpace(message.Content); len(trimmed) > 0 && trimmed[0] == '[' && json.Unmarshal(trimmed, &blocks) != nil {
			return false, ErrProtocol
		}
		var full, thinking strings.Builder
		for _, block := range blocks {
			switch str(block, "type") {
			case "tool_use", "tool_result", "server_tool_use", "mcp_tool_use":
				return false, ErrUnexpectedTool
			case "text":
				full.WriteString(str(block, "text"))
			case "thinking":
				thinking.WriteString(str(block, "thinking"))
			}
		}
		if str(obj, "type") == "user" {
			return false, nil
		}
		// Thinking precedes the reply within a message, so it is forwarded first.
		// 同一条消息里思考在回复之前，因此先转发思考。
		if thinking.Len() > 0 {
			if rest, ok := cutStreamed(&p.thought, thinking.String()); ok {
				if err := out.reasoning(rest); err != nil {
					return false, err
				}
			}
		}
		if full.Len() == 0 {
			return false, nil // keep streamed deltas for the next text message / 保留已流出的增量给下一条文本消息比对
		}
		rest, ok := cutStreamed(&p.streamed, full.String())
		if !ok {
			return false, nil // deltas already carried a different rendering / 增量已给出另一种呈现
		}
		p.emitted = p.emitted || rest != ""
		return false, out.text(rest)
	case "result":
		var isError bool
		_ = json.Unmarshal(obj["is_error"], &isError)
		if str(obj, "subtype") != "success" || isError {
			return false, ErrTurn
		}
		if !p.emitted {
			// Some versions only print the reply on the result event.
			// 某些版本只在 result 事件中给出回复。
			if err := out.text(str(obj, "result")); err != nil {
				return false, err
			}
		}
		var err error
		p.usage, err = usageFrom(object(obj["usage"]),
			[][]string{{"input_tokens", "inputTokens"}, {"cache_read_input_tokens", "cacheReadInputTokens"}, {"cache_creation_input_tokens", "cacheCreationInputTokens"}},
			[][]string{{"output_tokens", "outputTokens"}})
		p.done = err == nil
		return p.done, err
	case "error":
		return false, ErrTurn
	}
	// system, rate_limit_event and future types carry neither reply nor thinking text.
	// system、rate_limit_event 及未来新增类型既不含回复文本，也不含思考文本。
	return false, nil
}

// cutStreamed returns the part of full not yet forwarded as deltas and resets
// streamed for the next message. ok is false when the deltas carried a
// different rendering, in which case nothing more is forwarded.
//
// cutStreamed 返回 full 中尚未以增量转发的部分，并为下一条消息清空 streamed。增量
// 给出的是另一种呈现时 ok 为 false，此时不再转发任何内容。
func cutStreamed(streamed *strings.Builder, full string) (string, bool) {
	prefix := streamed.String()
	streamed.Reset()
	return strings.CutPrefix(full, prefix)
}

func (p *claudeParser) finish() (*runtime.Usage, error) {
	if !p.done {
		return nil, ErrProtocol
	}
	return p.usage, nil
}

// claudeModels reads model IDs from an array of objects or strings, trying
// the given keys on each object.
// claudeModels 从对象或字符串数组中读取模型 ID，对每个对象依次尝试给定的键。
func claudeModels(raw json.RawMessage, keys ...string) ([]string, error) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, ErrProtocol
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		var id string
		if json.Unmarshal(item, &id) != nil {
			id = str(object(item), keys...)
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

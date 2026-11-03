package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"AIServeWeave/common/runtime"
)

// anthropic_system.go converts the mid-conversation {role:"system"} turns and
// deferred-tool references Claude Code sends, so a backend that only knows a
// flat chat history and one tool list can serve them. A system turn's text
// becomes a user message; its tool_addition / tool_removal blocks, and the
// tool_reference blocks a client-side tool search returns inside a
// tool_result, decide which declared tools reach the backend. A tool declared
// with defer_loading stays hidden until something references it.
//
// anthropic_system.go 转换 Claude Code 发送的对话中途 {role:"system"} 轮次与延迟工具
// 引用，让只认识扁平聊天历史与单一工具列表的后端也能服务它们。system 轮次的文本变成
// 一条 user 消息；其中的 tool_addition / tool_removal 块，以及客户端工具搜索在
// tool_result 里返回的 tool_reference 块，决定哪些已声明的工具交给后端。以
// defer_loading 声明的工具在被引用之前保持隐藏。

// anthropicSystemToolJSON is the "tool" object of a tool_addition or
// tool_removal block: a reference to a declared tool by name, or a full
// definition.
//
// anthropicSystemToolJSON 是 tool_addition 或 tool_removal 块的 "tool" 对象：
// 按名字引用一个已声明的工具，或给出完整定义。
type anthropicSystemToolJSON struct {
	Type       string             `json:"type"`
	Name       string             `json:"name"`
	Definition *anthropicToolJSON `json:"definition"`
}

// anthropicSystemBlockJSON is one content block of a system turn.
// anthropicSystemBlockJSON 是 system 轮次的一个内容块。
type anthropicSystemBlockJSON struct {
	Type string                  `json:"type"`
	Text string                  `json:"text"`
	Tool anthropicSystemToolJSON `json:"tool"`
}

// anthropicSystemTurn is a parsed system turn: its text, and its tool
// changes in order.
//
// anthropicSystemTurn 是解析后的 system 轮次：其文本，以及按顺序的工具变更。
type anthropicSystemTurn struct {
	text  string
	tools []anthropicSystemBlockJSON // tool_addition / tool_removal only / 仅 tool_addition 与 tool_removal
}

// errSystemTurn wraps every way a system turn cannot be understood with
// anthropicMidConvSystemRejected, so Claude Code drops system turns for the
// conversation rather than stalling on a 400 it does not recognise.
//
// errSystemTurn 给 system 轮次无法理解的每种情况都带上
// anthropicMidConvSystemRejected，让 Claude Code 在本次对话中放弃 system 轮次，而不是
// 卡在一个它不认识的 400 上。
func errSystemTurn(index int, format string, args ...any) error {
	return fmt.Errorf("messages[%d]: %s; %s", index, fmt.Sprintf(format, args...), anthropicMidConvSystemRejected)
}

// parseAnthropicSystemTurn parses the content of the system turn at index:
// a bare string, or text, tool_addition and tool_removal blocks. Any other
// block type, or a tool block that names no tool, is an errSystemTurn.
//
// parseAnthropicSystemTurn 解析位于 index 的 system 轮次内容：裸字符串，或 text、
// tool_addition、tool_removal 块。其他块类型、或没有点名工具的工具块都是
// errSystemTurn。
func parseAnthropicSystemTurn(index int, raw json.RawMessage) (anthropicSystemTurn, error) {
	var turn anthropicSystemTurn
	if len(raw) == 0 {
		return turn, nil
	}
	if err := json.Unmarshal(raw, &turn.text); err == nil {
		return turn, nil
	}
	var blocks []anthropicSystemBlockJSON
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return turn, errSystemTurn(index, "content must be a string or an array of content blocks")
	}
	var text strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if text.Len() > 0 && b.Text != "" {
				text.WriteString("\n\n")
			}
			text.WriteString(b.Text)
		case "tool_addition", "tool_removal":
			if anthropicSystemToolName(b.Tool) == "" {
				return turn, errSystemTurn(index, "a %q block must name a tool", b.Type)
			}
			if b.Type == "tool_removal" && b.Tool.Type != "tool_reference" {
				return turn, errSystemTurn(index, `a "tool_removal" block must hold a "tool_reference"`)
			}
			turn.tools = append(turn.tools, b)
		default:
			return turn, errSystemTurn(index, "system content block type %q is not supported", b.Type)
		}
	}
	turn.text = text.String()
	return turn, nil
}

// anthropicSystemToolName returns the tool name a tool_addition or
// tool_removal refers to, or "" when it names none.
//
// anthropicSystemToolName 返回 tool_addition 或 tool_removal 所指的工具名，没有点名时
// 返回 ""。
func anthropicSystemToolName(t anthropicSystemToolJSON) string {
	switch t.Type {
	case "tool_reference":
		return t.Name
	case "tool_definition":
		if t.Definition != nil {
			return t.Definition.Name
		}
	}
	return ""
}

// anthropicToolResultReferences returns the tool names the tool_reference
// blocks inside a tool_result's content name, in order. Malformed content is
// left for anthropicToolResultContent to report.
//
// anthropicToolResultReferences 按顺序返回 tool_result 内容中 tool_reference 块点名的
// 工具名。格式错误的内容留给 anthropicToolResultContent 报告。
func anthropicToolResultReferences(raw json.RawMessage) []string {
	var blocks []anthropicContentBlockJSON
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var names []string
	for _, b := range blocks {
		if b.Type == "tool_reference" && b.ToolName != "" {
			names = append(names, b.ToolName)
		}
	}
	return names
}

// anthropicActiveTools returns the tools the backend should see once every
// turn of req has been applied, in a stable order: the declared tools that are
// not deferred, in declaration order, then each tool as it is first
// activated. A tool_addition holding a definition declares (or redeclares)
// and activates that tool; one holding a reference, or a tool_reference block
// in a tool_result, activates a declared tool; a tool_removal deactivates
// one. A reference to an undeclared tool is ignored: the client only refers
// to tools it declared, and a stale reference is not worth failing the turn.
//
// anthropicActiveTools 返回应用完 req 每一轮之后后端应看到的工具，顺序稳定：先是未延迟
// 的已声明工具（按声明顺序），再按首次激活的顺序排列其他工具。带定义的 tool_addition
// 声明（或重新声明）并激活该工具；带引用的 tool_addition、或 tool_result 中的
// tool_reference 块，激活一个已声明的工具；tool_removal 停用一个工具。引用未声明的
// 工具会被忽略：客户端只引用自己声明过的工具，为一个过时的引用让回合失败不值得。
func anthropicActiveTools(req anthropicMessagesRequest) ([]anthropicToolJSON, error) {
	declared := make(map[string]anthropicToolJSON, len(req.Tools))
	active := map[string]bool{}
	var order []string
	activate := func(name string) {
		if _, ok := declared[name]; !ok || active[name] {
			return
		}
		if !containsString(order, name) {
			order = append(order, name)
		}
		active[name] = true
	}
	for _, t := range req.Tools {
		declared[t.Name] = t
		if !t.DeferLoading {
			activate(t.Name)
		}
	}
	for i, m := range req.Messages {
		switch m.Role {
		case "system":
			turn, err := parseAnthropicSystemTurn(i, m.Content)
			if err != nil {
				return nil, err
			}
			for _, b := range turn.tools {
				name := anthropicSystemToolName(b.Tool)
				switch {
				case b.Type == "tool_removal":
					active[name] = false
				case b.Tool.Type == "tool_definition":
					declared[name] = *b.Tool.Definition
					active[name] = false // re-activate below with the new definition / 下面用新定义重新激活
					activate(name)
				default:
					activate(name)
				}
			}
		case "user":
			var blocks []anthropicContentBlockJSON
			if json.Unmarshal(m.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "tool_result" {
					for _, name := range anthropicToolResultReferences(b.Content) {
						activate(name)
					}
				}
			}
		}
	}
	out := make([]anthropicToolJSON, 0, len(order))
	for _, name := range order {
		if active[name] {
			out = append(out, declared[name])
		}
	}
	return out, nil
}

// containsString reports whether list holds s.
// containsString 报告 list 是否包含 s。
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// awaitingToolResults reports whether the last message is an assistant turn
// whose tool calls still wait for their results. A system turn arriving then
// is held back until the results are in, because a backend pairs each tool
// call with the tool messages that directly follow it.
//
// awaitingToolResults 报告最后一条消息是否为工具调用仍在等待结果的 assistant 轮次。
// 此时到达的 system 轮次会被暂缓到结果之后，因为后端要求每个工具调用紧跟着它的
// tool 消息。
func awaitingToolResults(messages []runtime.ChatMessage) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	return last.Role == "assistant" && len(last.ToolCalls) > 0
}

// insertAfterToolResults returns msgs with held inserted after its leading
// "tool" messages, i.e. right after the results that answer the previous
// assistant's tool calls and before the user's own text.
//
// insertAfterToolResults 返回把 held 插入到 msgs 开头那些 "tool" 消息之后的结果，
// 也就是紧跟在回应上一条 assistant 工具调用的结果之后、用户自己的文本之前。
func insertAfterToolResults(msgs, held []runtime.ChatMessage) []runtime.ChatMessage {
	n := 0
	for n < len(msgs) && msgs[n].Role == "tool" {
		n++
	}
	out := make([]runtime.ChatMessage, 0, len(msgs)+len(held))
	out = append(out, msgs[:n]...)
	out = append(out, held...)
	return append(out, msgs[n:]...)
}

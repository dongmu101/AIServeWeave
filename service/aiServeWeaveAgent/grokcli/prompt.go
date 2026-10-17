package grokcli

import (
	"encoding/json"
	"regexp"
	"strings"

	"AIServeWeave/common/runtime"
)

const (
	maxMessages    = 2048
	maxPromptBytes = 1 << 20
)

// modelID admits catalogue IDs that are safe as a single argv value: no
// leading dash (it would read as a flag), no whitespace or control bytes.
// modelID 只接受可安全作为单个 argv 值的目录 ID：不以短横线开头（否则会被
// 当作参数），不含空白或控制字符。
var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,255}$`)

// promptPreamble frames the transcript; it carries no request content.
// promptPreamble 为对话记录加上框架说明，不含任何请求内容。
const promptPreamble = "You are answering through the AIServeWeave Grok CLI adapter. " +
	"Do not use tools, read or write files, browse, or run commands. " +
	"The conversation so far is the JSON array below, in order, each element holding a role and its text. " +
	"Follow the system and developer messages, then reply only to the final user message as the assistant.\n\n"

type transcriptEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// buildPrompt validates a text-only chat request and renders its history as a
// role-tagged JSON transcript for one fresh headless turn. Positive output
// limits are accepted but advisory: the CLI cannot enforce them.
//
// buildPrompt 校验纯文本聊天请求，并把历史渲染为带角色标记的 JSON 对话记录，
// 供一次全新的 headless 回合使用。正数输出上限仅作兼容接收：CLI 无法执行。
func buildPrompt(req runtime.ChatRequest) (string, error) {
	if !modelID.MatchString(req.Model) || len(req.Messages) == 0 || len(req.Messages) > maxMessages {
		return "", ErrRequest
	}
	if req.MaxTokens != nil && *req.MaxTokens <= 0 {
		return "", ErrRequest
	}
	if req.Temperature != nil || req.TopP != nil || req.Seed != nil || len(req.Stop) > 0 || len(req.Extra) > 0 || len(req.Tools) > 0 {
		return "", errUnsupported
	}
	if req.ToolChoice != "" && req.ToolChoice != "none" && req.ToolChoice != "auto" {
		return "", errUnsupported
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type != "text" {
		return "", errUnsupported
	}
	entries := make([]transcriptEntry, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Name != "" || m.ToolCallID != "" || len(m.ToolCalls) > 0 {
			return "", errUnsupported
		}
		switch m.Role {
		case "system", "developer", "user", "assistant":
		case "tool":
			return "", errUnsupported
		default:
			return "", ErrRequest
		}
		text := m.Content
		if len(m.ContentParts) > 0 {
			if text != "" {
				return "", ErrRequest
			}
			var b strings.Builder
			for _, part := range m.ContentParts {
				if part.Type != "text" {
					return "", errUnsupported
				}
				b.WriteString(part.Text)
			}
			text = b.String()
		}
		entries = append(entries, transcriptEntry{Role: m.Role, Content: text})
	}
	if last := entries[len(entries)-1]; last.Role != "user" || strings.TrimSpace(last.Content) == "" {
		return "", ErrRequest
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", ErrRequest
	}
	prompt := promptPreamble + string(data) + "\n"
	if len(prompt) > maxPromptBytes {
		return "", ErrRequest
	}
	return prompt, nil
}

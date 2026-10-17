package cliagent

import (
	"encoding/json"
	"strings"

	"AIServeWeave/common/runtime"
)

const (
	maxMessages    = 2048
	maxPromptBytes = 1 << 20
)

type transcriptEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// buildPrompt validates a text-only chat request and renders its history as a
// role-tagged JSON transcript for one fresh headless turn. The preamble
// carries no request content. Positive output limits are accepted but
// advisory: no CLI here can enforce them.
//
// buildPrompt 校验纯文本聊天请求，并把历史渲染为带角色标记的 JSON 对话记录，
// 供一次全新的 headless 回合使用。前言不含任何请求内容。正数输出上限仅作兼容
// 接收：这里的 CLI 都无法执行。
func buildPrompt(label string, req runtime.ChatRequest) (string, error) {
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
	prompt := "You are answering through the AIServeWeave " + label + " CLI adapter. " +
		"Do not use tools, read or write files, browse, or run commands. " +
		"The conversation so far is the JSON array below, in order, each element holding a role and its text. " +
		"Follow the system and developer messages, then reply only to the final user message as the assistant.\n\n" +
		string(data) + "\n"
	if len(prompt) > maxPromptBytes {
		return "", ErrRequest
	}
	return prompt, nil
}

package claudecode

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"AIServeWeave/common/runtime"
)

var rejectedThinkingType = regexp.MustCompile(`(?i)thinking\.type[^a-z]{1,8}(enabled|adaptive).*?not supported|\b(adaptive) thinking is not supported`)

// messagesAPIRejection reads only CLI-marked synthetic API errors and returns fixed recovery classes.
// messagesAPIRejection 只读取 CLI 标记的合成 API 错误，并返回固定的恢复分类。
func messagesAPIRejection(raw json.RawMessage, req messagesRequest) error {
	var envelope struct {
		Type              string `json:"type"`
		Error             string `json:"error"`
		IsAPIErrorMessage bool   `json:"is_api_error_message"`
		APIErrorStatus    int    `json:"api_error_status"`
		Message           struct {
			Model   string `json:"model"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Type != "assistant" || !envelope.IsAPIErrorMessage || envelope.Error == "" || envelope.Message.Model != "<synthetic>" || len(envelope.Message.Content) != 1 || envelope.Message.Content[0].Type != "text" {
		return nil
	}
	text, status := envelope.Message.Content[0].Text, envelope.APIErrorStatus
	if prefixed, ok := strings.CutPrefix(text, "API Error: "); ok {
		code, body, ok := strings.Cut(prefixed, " ")
		parsed, err := strconv.Atoi(code)
		if !ok || err != nil || (status != 0 && status != parsed) {
			return nil
		}
		text, status = body, parsed
	}
	if status != 400 && status != 413 && status != 422 {
		return nil
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "{") {
		var envelope struct {
			Error struct {
				Type, Message string
			} `json:"error"`
		}
		if json.Unmarshal([]byte(text), &envelope) != nil || (envelope.Error.Type != "invalid_request_error" && envelope.Error.Type != "request_too_large") {
			return nil
		}
		text = envelope.Error.Message
	}
	failure := &runtime.RuntimeError{Code: runtime.ErrorCapability, Kind: runtime.KindClaude, Operation: "messages", StatusCode: status}
	failure.Message = text
	if runtime.MessagesRejectionMessage(failure) != "" && !strings.HasPrefix(text, "capability_rejected: beta_header:") {
		return failure
	}
	failure.Message = ""
	// Keep the original text local to this function; only a closed recovery vocabulary leaves it.
	// 原始文本仅保留在此函数中；只允许封闭的恢复词汇离开这里。
	lower := strings.ToLower(strings.ReplaceAll(text, "`", ""))
	switch {
	case strings.Contains(lower, "unexpected role") && strings.Contains(lower, "system"):
		failure.Message = "capability_rejected: mid_conv_system"
	case strings.Contains(lower, "thinking") && (strings.Contains(lower, "invalid signature") || strings.Contains(lower, "invalid data in redacted_thinking") || strings.Contains(lower, "cannot be modified") || strings.Contains(lower, "bound to a different conversation")):
		failure.Message = "capability_rejected: thinking_signature"
	case rejectedThinkingType.MatchString(lower):
		match := rejectedThinkingType.FindStringSubmatch(lower)
		kind := match[1]
		if kind == "" {
			kind = match[2]
		}
		failure.Message = "capability_rejected: thinking_type:" + kind
	case strings.Contains(lower, "output_config.format") && strings.Contains(lower, "extra inputs are not permitted"):
		failure.Message = "capability_rejected: structured_outputs_unsupported"
	case strings.Contains(lower, "effort parameter") && strings.Contains(lower, "not support"), strings.Contains(lower, "output_config") && (strings.Contains(lower, "extra inputs are not permitted") || strings.Contains(lower, "requires a model that supports")):
		failure.Message = "capability_rejected: effort_unsupported"
	case strings.Contains(lower, "context_management") && (strings.Contains(lower, "extra inputs are not permitted") || strings.Contains(lower, "not supported")):
		failure.Message = "context_management: Extra inputs are not permitted"
	case strings.Contains(lower, "prompt is too long"), strings.Contains(lower, "input is too long for requested model"), status == 413 && strings.Contains(lower, "context window"):
		failure.Message = "capability_rejected: prompt_too_long"
	case strings.Contains(lower, "input length and max_tokens exceed context limit"):
		failure.Message = "capability_rejected: max_tokens_context_overflow"
	default:
		betaRejected := strings.Contains(lower, "unexpected value(s)") || strings.Contains(lower, "invalid beta") || strings.Contains(lower, "not supported") || strings.Contains(lower, "unsupported")
		if betaRejected && (strings.Contains(lower, "anthropic-beta") || strings.Contains(lower, "anthropic_beta")) || strings.HasPrefix(text, "capability_rejected: beta_header:") {
			words := strings.FieldsFunc(text, func(c rune) bool {
				return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.')
			})
			for _, beta := range strings.Split(req.betaFeatures, ",") {
				beta = strings.TrimSpace(beta)
				for _, word := range words {
					if beta != "" && word == beta {
						failure.Message = "capability_rejected: beta_header:" + beta
						break
					}
				}
				if failure.Message != "" {
					break
				}
			}
		}
	}
	if runtime.MessagesRejectionMessage(failure) == "" {
		return nil
	}
	return failure
}

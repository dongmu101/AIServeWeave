package runtime

import (
	"errors"
	"strings"
)

// MessagesRejectionMessage exposes only canonical Claude capability rejections, never upstream prose.
// MessagesRejectionMessage 仅公开规范化的 Claude 能力拒绝，不公开上游原文。
func MessagesRejectionMessage(err error) string {
	var failure *RuntimeError
	if !errors.As(err, &failure) || failure.Code != ErrorCapability || failure.Kind != KindClaude || failure.Operation != "messages" {
		return ""
	}
	switch failure.Message {
	case "capability_rejected: mid_conv_system", "capability_rejected: thinking_signature",
		"capability_rejected: thinking_type:enabled", "capability_rejected: thinking_type:adaptive",
		"capability_rejected: structured_outputs_unsupported", "capability_rejected: effort_unsupported",
		"capability_rejected: prompt_too_long", "capability_rejected: max_tokens_context_overflow",
		"context_management: Extra inputs are not permitted":
		return failure.Message
	}
	const prefix = "capability_rejected: beta_header:"
	if beta, ok := strings.CutPrefix(failure.Message, prefix); ok && len(beta) > 0 && len(beta) <= 128 {
		for _, c := range []byte(beta) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return ""
			}
		}
		return failure.Message
	}
	return ""
}

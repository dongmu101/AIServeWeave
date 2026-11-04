package runtime

import (
	"bytes"
	"context"
	"encoding/json"
)

// MaxMessagesSafeguardsBytes shares the native Messages document bound.
// MaxMessagesSafeguardsBytes 与原生 Messages 文档共用大小上限。
const MaxMessagesSafeguardsBytes = 32 << 20

// ValidateMessagesSafeguards validates the supported safety request without inspecting or rewriting caller policy.
// ValidateMessagesSafeguards 校验受支持的安全请求，不解释或改写调用方策略。
func ValidateMessagesSafeguards(raw json.RawMessage) error {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	failure := &RuntimeError{Code: ErrorCapability, Message: "safeguards must contain at most one dangerous_tool_use classifier_context object within the Messages size limit"}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil || compact.Len() > MaxMessagesSafeguardsBytes {
		return failure
	}
	var guards []struct {
		Type    string                     `json:"type"`
		Context map[string]json.RawMessage `json:"classifier_context"`
	}
	if json.Unmarshal(raw, &guards) != nil || len(guards) > 1 {
		return failure
	}
	for _, guard := range guards {
		if guard.Type != "dangerous_tool_use" || guard.Context == nil {
			return failure
		}
	}
	return nil
}

// MessagesRequest carries a bounded Anthropic Messages document and authenticated ownership.
// MessagesRequest 携带有界的 Anthropic Messages 文档与已认证的归属标识。
// JSON contains inference semantics only; URL, headers, credentials and CLI settings are forbidden.
// JSON 仅含推理语义，不接受 URL、请求头、凭据或 CLI 设置。
type MessagesRequest struct {
	Model    string
	TenantID string
	KeyID    string
	JSON     json.RawMessage
	// APIVersion and BetaFeatures are native inference protocol selectors, not arbitrary headers.
	// APIVersion 与 BetaFeatures 是原生推理协议选择，不是任意 HTTP 请求头。
	APIVersion   string
	BetaFeatures string
}

// ValidateProtocol bounds the protocol selectors and prevents header injection at the CLI boundary.
// ValidateProtocol 限制协议选择字段，并防止在 CLI 边界注入请求头。
func (r MessagesRequest) ValidateProtocol() error {
	valid := func(s string, limit int) bool {
		if len(s) > limit {
			return false
		}
		for _, c := range []byte(s) {
			if c < 32 || c > 126 {
				return false
			}
		}
		return true
	}
	if !valid(r.APIVersion, 128) || !valid(r.BetaFeatures, 16<<10) {
		return &RuntimeError{Code: ErrorProtocol, Message: "invalid Messages protocol selectors"}
	}
	return nil
}

// MessagesEvent is one validated Anthropic SSE data document, without HTTP framing.
// MessagesEvent 是一条经校验的 Anthropic SSE 数据文档，不含 HTTP 帧。
type MessagesEvent struct{ JSON json.RawMessage }

// MessagesRuntime preserves caller-executed tools and native message block ordering.
// MessagesRuntime 保留调用方执行工具的语义与原生消息块顺序。
type MessagesRuntime interface {
	Runtime
	Messages(ctx context.Context, req MessagesRequest) (Stream[MessagesEvent], error)
}

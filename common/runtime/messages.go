package runtime

import (
	"context"
	"encoding/json"
)

// MessagesRequest carries a bounded Anthropic Messages document and authenticated ownership.
// MessagesRequest 携带有界的 Anthropic Messages 文档与已认证的归属标识。
// JSON contains inference semantics only; URL, headers, credentials and CLI settings are forbidden.
// JSON 仅含推理语义，不接受 URL、请求头、凭据或 CLI 设置。
type MessagesRequest struct {
	Model    string
	TenantID string
	KeyID    string
	JSON     json.RawMessage
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

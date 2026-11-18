package types

import "time"

// Studio internal API wire types. The owner (tenant_id, user_id) travels in
// the query string on every call: the Gateway asserts it after verifying the
// member's session, the same trust boundary as getJob's tenant_id.
//
// Studio 内部 API 的线上类型。每次调用都在查询串里携带所有者（tenant_id、user_id）：
// 由 Gateway 在校验成员会话后断言，与 getJob 的 tenant_id 是同一条信任边界。

// StudioConversationResponse is one conversation.
// StudioConversationResponse 是一个对话。
type StudioConversationResponse struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// StudioConversationListResponse is one page of conversations.
// StudioConversationListResponse 是一页对话。
type StudioConversationListResponse struct {
	Data []StudioConversationResponse `json:"data"`
	Next string                       `json:"next,omitempty"`
}

// CreateStudioConversationRequest starts a conversation.
// CreateStudioConversationRequest 新建一个对话。
type CreateStudioConversationRequest struct {
	Title        string `json:"title"`
	Model        string `json:"model"`
	SystemPrompt string `json:"system_prompt"`
}

// UpdateStudioConversationRequest changes the fields present.
// UpdateStudioConversationRequest 修改出现的字段。
type UpdateStudioConversationRequest struct {
	Title        *string `json:"title"`
	Model        *string `json:"model"`
	SystemPrompt *string `json:"system_prompt"`
}

// StudioMessageResponse is one message.
// StudioMessageResponse 是一条消息。
type StudioMessageResponse struct {
	ID               string    `json:"id"`
	Seq              int64     `json:"seq"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	Reasoning        string    `json:"reasoning,omitempty"`
	Status           string    `json:"status"`
	Model            string    `json:"model,omitempty"`
	PromptTokens     int64     `json:"prompt_tokens,omitempty"`
	CompletionTokens int64     `json:"completion_tokens,omitempty"`
	ErrorCode        string    `json:"error_code,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// StudioMessageListResponse is a conversation's messages in order.
// StudioMessageListResponse 是对话中按顺序排列的消息。
type StudioMessageListResponse struct {
	Data []StudioMessageResponse `json:"data"`
}

// BeginStudioTurnRequest is the user side of a turn: exactly one of
// Regenerate and Content.
//
// BeginStudioTurnRequest 是一轮中用户的一侧：Regenerate 与 Content 二选一。
type BeginStudioTurnRequest struct {
	Regenerate           bool   `json:"regenerate"`
	ReplaceFromMessageID string `json:"replace_from_message_id"`
	Content              string `json:"content"`
	Model                string `json:"model"`
}

// BeginStudioTurnResponse is the conversation and the history the reply is
// generated from; the last message is the one it answers.
//
// BeginStudioTurnResponse 是对话与生成回复所依据的历史，最后一条消息就是回复要回答的那条。
type BeginStudioTurnResponse struct {
	Conversation StudioConversationResponse `json:"conversation"`
	Messages     []StudioMessageResponse    `json:"messages"`
}

// AppendStudioReplyRequest is an assistant reply. ID is minted by the
// Gateway so a retried write is recognised.
//
// AppendStudioReplyRequest 是一条助手回复。ID 由 Gateway 铸造，使重试写入可以被识别。
type AppendStudioReplyRequest struct {
	ReplyTo          int64  `json:"reply_to"`
	ID               string `json:"id"`
	Content          string `json:"content"`
	Reasoning        string `json:"reasoning"`
	Status           string `json:"status"`
	Model            string `json:"model"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	ErrorCode        string `json:"error_code"`
}

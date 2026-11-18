package httpapi

import (
	"context"
	"errors"
	"time"
)

// Studio storage refusals. Any other error from a StudioStore means the
// control plane could not answer, which the Studio API reports as 503.
//
// Studio 存储的拒绝。StudioStore 返回的其他错误都表示控制面无法应答，Studio API 以 503 报告。
var (
	// ErrStudioNotFound: no such conversation or message for this owner.
	// ErrStudioNotFound：该所有者下没有这个对话或消息。
	ErrStudioNotFound = errors.New("studio: not found")
	// ErrStudioLimit: the user's conversation count or the conversation's
	// message count is at its limit.
	// ErrStudioLimit：用户的对话数或对话的消息数已达上限。
	ErrStudioLimit = errors.New("studio: limit reached")
	// ErrStudioConflict: the turn cannot apply, e.g. nothing to regenerate.
	// ErrStudioConflict：这一轮无法执行，例如没有可重新生成的内容。
	ErrStudioConflict = errors.New("studio: conflict")
	// ErrStudioStaleTurn: the reply no longer answers the last message.
	// ErrStudioStaleTurn：回复已不再对应最后一条消息。
	ErrStudioStaleTurn = errors.New("studio: stale turn")
	// ErrStudioInvalid: the control plane refused the request's content.
	// ErrStudioInvalid：控制面拒绝了请求的内容。
	ErrStudioInvalid = errors.New("studio: invalid request")
)

// StudioOwner is the tenant user every Studio read and write is scoped to.
//
// StudioOwner 是每次 Studio 读写所限定的租户用户。
type StudioOwner struct {
	TenantID string
	UserID   string
}

// StudioConversation is one stored conversation.
// StudioConversation 是一个已存储的对话。
type StudioConversation struct {
	ID           string
	Title        string
	Model        string
	SystemPrompt string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// StudioConversationPage is one page of conversations; Next is empty on the
// last page.
// StudioConversationPage 是一页对话；最后一页的 Next 为空。
type StudioConversationPage struct {
	Conversations []StudioConversation
	Next          string
}

// StudioConversationPatch changes the fields that are non-nil.
// StudioConversationPatch 修改非 nil 的字段。
type StudioConversationPatch struct {
	Title        *string
	Model        *string
	SystemPrompt *string
}

// StudioMessage is one stored message.
// StudioMessage 是一条已存储的消息。
type StudioMessage struct {
	ID               string
	Seq              int64
	Role             string
	Content          string
	Reasoning        string
	Status           string
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	ErrorCode        string
	CreatedAt        time.Time
}

// StudioTurn is the user side of one turn: Regenerate, or Content
// (optionally replacing the user message ReplaceFromMessageID and all after).
//
// StudioTurn 是一轮中用户的一侧：Regenerate，或 Content（可选地替换
// ReplaceFromMessageID 那条用户消息及其后的所有消息）。
type StudioTurn struct {
	Regenerate           bool
	ReplaceFromMessageID string
	Content              string
	Model                string
}

// StudioReply is the assistant reply written after a stream ends.
// StudioReply 是流结束后写入的助手回复。
type StudioReply struct {
	ReplyTo          int64
	ID               string
	Content          string
	Reasoning        string
	Status           string
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	ErrorCode        string
}

// Studio message roles and reply statuses, as stored.
// 存储中的 Studio 消息角色与回复状态。
const (
	studioRoleUser          = "user"
	studioRoleAssistant     = "assistant"
	studioReplyComplete     = "complete"
	studioReplyIncomplete   = "incomplete"
	studioReplyError        = "error"
	studioMessagePrefix     = "msg_"
	studioConversationIDMax = 40
)

// StudioStore persists Studio conversations; the control plane client
// implements it. Every method is scoped by owner.
//
// StudioStore 持久化 Studio 对话，由控制面客户端实现。每个方法都按 owner 限定。
type StudioStore interface {
	CreateConversation(ctx context.Context, owner StudioOwner, title, model, systemPrompt string) (StudioConversation, error)
	ListConversations(ctx context.Context, owner StudioOwner, query, cursor string, limit int) (StudioConversationPage, error)
	GetConversation(ctx context.Context, owner StudioOwner, id string) (StudioConversation, error)
	UpdateConversation(ctx context.Context, owner StudioOwner, id string, patch StudioConversationPatch) (StudioConversation, error)
	DeleteConversation(ctx context.Context, owner StudioOwner, id string) error
	ListMessages(ctx context.Context, owner StudioOwner, id string, afterSeq int64, limit int) ([]StudioMessage, error)
	// BeginTurn writes the user side of a turn and returns the history the
	// reply is generated from; its last message is the one the reply answers.
	// BeginTurn 写入一轮中用户的一侧，返回生成回复所依据的历史；最后一条就是回复要回答的消息。
	BeginTurn(ctx context.Context, owner StudioOwner, id string, turn StudioTurn) (StudioConversation, []StudioMessage, error)
	// AppendReply writes a reply; a retry with the same reply ID succeeds.
	// AppendReply 写入回复；以同一回复 ID 重试会成功。
	AppendReply(ctx context.Context, owner StudioOwner, id string, reply StudioReply) error
}

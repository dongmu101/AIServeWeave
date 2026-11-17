package model

import "time"

// StudioConversation is one Studio chat owned by one tenant user. Every read
// and write is scoped by (TenantID, UserID): a member never sees another
// member's conversations, even within the same tenant.
//
// StudioConversation 是某个租户用户的一个 Studio 对话。所有读写都以
// (TenantID, UserID) 限定：成员看不到他人的对话，同租户也一样。
type StudioConversation struct {
	ID           string    `gorm:"primaryKey;size:40"`
	TenantID     string    `gorm:"size:40;not null;index:idx_studio_conversations_owner,priority:1"`
	UserID       string    `gorm:"size:40;not null;index:idx_studio_conversations_owner,priority:2"`
	Title        string    `gorm:"size:200;not null"`
	Model        string    `gorm:"size:128;not null"`
	SystemPrompt string    `gorm:"type:text;not null"`
	CreatedAt    time.Time `gorm:"not null"`
	UpdatedAt    time.Time `gorm:"not null;index:idx_studio_conversations_owner,priority:3"`
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (StudioConversation) TableName() string { return "studio_conversations" }

// Studio message roles and statuses.
//
// Studio 消息的角色与状态。
const (
	StudioRoleUser      = "user"
	StudioRoleAssistant = "assistant"

	// StudioMessageComplete is a reply the model finished.
	// StudioMessageComplete 是模型正常完成的回复。
	StudioMessageComplete = "complete"
	// StudioMessageIncomplete is a reply cut short by a stop or a disconnect;
	// its content is what had arrived.
	// StudioMessageIncomplete 是因停止或断开而中断的回复，内容为已收到的部分。
	StudioMessageIncomplete = "incomplete"
	// StudioMessageError is a reply that failed; ErrorCode says why.
	// StudioMessageError 是失败的回复，ErrorCode 说明原因。
	StudioMessageError = "error"
)

// StudioMessage is one message of a StudioConversation. Seq orders the
// messages and is unique within the conversation, which makes a retried
// write of the same turn detectable.
//
// StudioMessage 是 StudioConversation 中的一条消息。Seq 决定顺序并在对话内唯一，
// 使同一轮的重试写入可以被识别。
type StudioMessage struct {
	ID               string    `gorm:"primaryKey;size:40"`
	ConversationID   string    `gorm:"size:40;not null;uniqueIndex:uk_studio_messages_seq,priority:1"`
	TenantID         string    `gorm:"size:40;not null"`
	UserID           string    `gorm:"size:40;not null"`
	Seq              int64     `gorm:"not null;uniqueIndex:uk_studio_messages_seq,priority:2"`
	Role             string    `gorm:"size:16;not null"`
	Content          string    `gorm:"type:text;not null"`
	Reasoning        string    `gorm:"type:text;not null"`
	Status           string    `gorm:"size:16;not null"`
	Model            string    `gorm:"size:128;not null"`
	PromptTokens     int64     `gorm:"not null"`
	CompletionTokens int64     `gorm:"not null"`
	ErrorCode        string    `gorm:"size:64;not null"`
	CreatedAt        time.Time `gorm:"not null"`
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (StudioMessage) TableName() string { return "studio_messages" }

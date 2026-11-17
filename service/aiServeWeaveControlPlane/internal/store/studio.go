package store

import (
	"context"
	"errors"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// ErrStudioLimit is returned when a write would take a user past the number
// of conversations, or a conversation past the number of messages, the
// caller allows.
//
// ErrStudioLimit 表示这次写入会让用户的对话数，或对话的消息数，超出调用方允许的上限。
var ErrStudioLimit = errors.New("store: studio limit reached")

// ErrStudioStaleTurn is returned when a reply no longer answers the
// conversation's last message: the turn was regenerated or edited meanwhile.
//
// ErrStudioStaleTurn 表示回复已不再对应对话的最后一条消息：这一轮在此期间被重新生成或编辑了。
var ErrStudioStaleTurn = errors.New("store: studio turn is stale")

// StudioOwner scopes every Studio read and write to one tenant user.
//
// StudioOwner 把每次 Studio 读写限定到一个租户用户。
type StudioOwner struct {
	TenantID string
	UserID   string
}

// StudioConversationCursor is the keyset position of a conversation list
// page: the (UpdatedAt, ID) of the last row returned.
//
// StudioConversationCursor 是对话列表分页的键集位置：上一页最后一行的 (UpdatedAt, ID)。
type StudioConversationCursor struct {
	UpdatedAt time.Time
	ID        string
}

// StudioConversationFilter narrows a conversation list. Query matches the
// title case-insensitively; empty matches all.
//
// StudioConversationFilter 收窄对话列表。Query 不区分大小写地匹配标题，为空表示全部。
type StudioConversationFilter struct {
	Owner  StudioOwner
	Query  string
	Before *StudioConversationCursor
	Limit  int
}

// StudioConversationPatch changes the fields that are non-nil.
//
// StudioConversationPatch 修改非 nil 的字段。
type StudioConversationPatch struct {
	Title        *string
	Model        *string
	SystemPrompt *string
}

// StudioTurn is the user side of one turn. With Regenerate the last reply is
// dropped and User is nil; with ReplaceFromID that user message and
// everything after it are dropped before User is appended. MaxMessages bounds
// the conversation after the write. Title is set only while the
// conversation's title is still empty.
//
// StudioTurn 是一轮中用户的一侧。Regenerate 时丢弃最后的回复且 User 为 nil；
// ReplaceFromID 时先丢弃那条用户消息及其后的所有消息，再追加 User。MaxMessages 限定
// 写入后对话的消息数。Title 只在对话标题仍为空时设置。
type StudioTurn struct {
	Regenerate    bool
	ReplaceFromID string
	User          *model.StudioMessage
	Model         string
	Title         string
	MaxMessages   int
	At            time.Time
}

// StudioConversations persists Studio conversations and their messages.
// Every method is scoped by owner; a row of another owner is ErrNotFound.
//
// StudioConversations 持久化 Studio 对话及其消息。每个方法都按 owner 限定，属于其他
// owner 的行一律返回 ErrNotFound。
type StudioConversations interface {
	// CreateStudioConversation inserts c unless its owner already has
	// maxPerUser conversations (ErrStudioLimit).
	//
	// CreateStudioConversation 插入 c；其所有者已有 maxPerUser 个对话时返回 ErrStudioLimit。
	CreateStudioConversation(ctx context.Context, c *model.StudioConversation, maxPerUser int) error
	// GetStudioConversation reads one conversation.
	// GetStudioConversation 读取一个对话。
	GetStudioConversation(ctx context.Context, owner StudioOwner, id string) (model.StudioConversation, error)
	// ListStudioConversations reads one page, most recently updated first.
	// ListStudioConversations 读取一页，按最近更新倒序。
	ListStudioConversations(ctx context.Context, filter StudioConversationFilter) ([]model.StudioConversation, error)
	// UpdateStudioConversation applies patch and bumps UpdatedAt to at.
	// UpdateStudioConversation 应用 patch，并把 UpdatedAt 更新为 at。
	UpdateStudioConversation(ctx context.Context, owner StudioOwner, id string, patch StudioConversationPatch, at time.Time) (model.StudioConversation, error)
	// DeleteStudioConversation removes a conversation and all its messages.
	// DeleteStudioConversation 删除一个对话及其所有消息。
	DeleteStudioConversation(ctx context.Context, owner StudioOwner, id string) error
	// ListStudioMessages reads up to limit messages with Seq > afterSeq, in order.
	// ListStudioMessages 按顺序读取至多 limit 条 Seq > afterSeq 的消息。
	ListStudioMessages(ctx context.Context, owner StudioOwner, conversationID string, afterSeq int64, limit int) ([]model.StudioMessage, error)
	// BeginStudioTurn applies turn in one transaction and returns the
	// conversation and its whole history afterwards, the last message being
	// the one the reply must answer. ErrInvalidInput-style refusals (nothing
	// to regenerate, ReplaceFromID not a user message) are ErrConflict.
	//
	// BeginStudioTurn 在一个事务中应用 turn，返回之后的对话及其完整历史，最后一条消息
	// 就是回复要回答的那条。无法执行的请求（没有可重新生成的内容、ReplaceFromID 不是
	// 用户消息）返回 ErrConflict。
	BeginStudioTurn(ctx context.Context, owner StudioOwner, conversationID string, turn StudioTurn) (model.StudioConversation, []model.StudioMessage, error)
	// AppendStudioReply appends reply right after the message at replyTo.
	// A retry of the same reply id succeeds without writing; a reply to a
	// message that is no longer last is ErrStudioStaleTurn.
	//
	// AppendStudioReply 把 reply 追加在 replyTo 那条消息之后。同一回复 id 的重试直接成功、
	// 不再写入；所回答的消息已不是最后一条时返回 ErrStudioStaleTurn。
	AppendStudioReply(ctx context.Context, owner StudioOwner, conversationID string, replyTo int64, reply *model.StudioMessage) error
}

// PlanStudioTurn decides, for a conversation's current history, which
// messages turn drops and where its user message goes. It returns the seq
// from which messages are deleted (0 for none) and the history that remains,
// and sets turn.User.Seq. Both store implementations share it, so the turn
// rules live in one place.
//
// PlanStudioTurn 根据对话当前的历史，决定 turn 要丢弃哪些消息、用户消息放在哪里。它返回
// 开始删除的 seq（0 表示不删除）与保留下来的历史，并设置 turn.User.Seq。两种存储实现
// 共用它，使轮次规则只有一处。
func PlanStudioTurn(history []model.StudioMessage, turn StudioTurn) (int64, []model.StudioMessage, error) {
	var from int64
	switch {
	case turn.Regenerate:
		if turn.User != nil || turn.ReplaceFromID != "" {
			return 0, nil, ErrConflict
		}
		for i := len(history) - 1; i >= 0 && from == 0; i-- {
			if history[i].Role == model.StudioRoleUser {
				from = history[i].Seq + 1
			}
		}
		if from == 0 {
			return 0, nil, ErrConflict
		}
	case turn.ReplaceFromID != "":
		for _, message := range history {
			if message.ID == turn.ReplaceFromID {
				if message.Role != model.StudioRoleUser {
					return 0, nil, ErrConflict
				}
				from = message.Seq
			}
		}
		if from == 0 {
			return 0, nil, ErrConflict
		}
	}
	if !turn.Regenerate && turn.User == nil {
		return 0, nil, ErrConflict
	}
	kept := history
	if from > 0 {
		kept = kept[:0:0]
		for _, message := range history {
			if message.Seq < from {
				kept = append(kept, message)
			}
		}
	}
	// Room for the user message (when there is one) and its reply.
	// 为用户消息（如有）及其回复预留位置。
	need := len(kept) + 1
	if turn.User != nil {
		need++
	}
	if turn.MaxMessages > 0 && need > turn.MaxMessages {
		return 0, nil, ErrStudioLimit
	}
	if turn.User != nil {
		turn.User.Seq = 1
		if len(kept) > 0 {
			turn.User.Seq = kept[len(kept)-1].Seq + 1
		}
		kept = append(kept, *turn.User)
	}
	return from, kept, nil
}

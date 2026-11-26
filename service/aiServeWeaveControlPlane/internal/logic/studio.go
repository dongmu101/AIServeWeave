package logic

import (
	"context"
	"encoding/base64"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ErrStudioLimit and ErrStudioStaleTurn are the store's Studio refusals,
// passed through unchanged so the handler can give each its own code.
//
// ErrStudioLimit 与 ErrStudioStaleTurn 是存储层的 Studio 拒绝，原样透传，使 handler
// 能为它们各给一个错误码。
var (
	ErrStudioLimit     = store.ErrStudioLimit
	ErrStudioStaleTurn = store.ErrStudioStaleTurn
)

// Studio storage limits. They bound what one user can store, not how much
// they can use; usage is bounded by quotas and billing at the Gateway.
//
// Studio 存储上限。它们限制一个用户能存多少，而不是能用多少；用量由 Gateway 的配额与
// 计费限制。
const (
	MaxStudioConversationsPerUser    = 1000
	MaxStudioMessagesPerConversation = 200
	MaxStudioTitleRunes              = 200
	MaxStudioSystemPromptBytes       = 16 << 10
	MaxStudioUserContentBytes        = 128 << 10
	MaxStudioReplyContentBytes       = 1 << 20
	maxStudioModelBytes              = 128
	maxStudioErrorCodeBytes          = 64
	studioAutoTitleRunes             = 40
	defaultStudioListLimit           = 30
	maxStudioListLimit               = 100
	maxStudioMessagePage             = MaxStudioMessagesPerConversation
)

// StudioConversationPage is one page of a conversation list. Next is empty on
// the last page.
//
// StudioConversationPage 是对话列表的一页。最后一页的 Next 为空。
type StudioConversationPage struct {
	Conversations []model.StudioConversation
	Next          string
}

// StudioTurnParams is the user side of one turn as the Gateway sends it.
//
// StudioTurnParams 是 Gateway 发来的一轮中用户的一侧。
type StudioTurnParams struct {
	Regenerate    bool
	ReplaceFromID string
	Content       string
	Model         string
}

// StudioReplyParams is the assistant reply the Gateway writes after a
// stream ends. ID is minted by the Gateway so a retry is recognisable.
//
// StudioReplyParams 是流结束后 Gateway 写入的助手回复。ID 由 Gateway 铸造，使重试可以
// 被识别。
type StudioReplyParams struct {
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

func validStudioOwner(owner store.StudioOwner) bool {
	return owner.TenantID != "" && owner.UserID != "" && len(owner.TenantID) <= model.IDSize && len(owner.UserID) <= model.IDSize
}

func validStudioID(id, prefix string) bool {
	return strings.HasPrefix(id, prefix) && len(id) <= model.IDSize
}

// CreateStudioConversation starts an empty conversation for owner.
//
// CreateStudioConversation 为 owner 新建一个空对话。
func (s *Service) CreateStudioConversation(ctx context.Context, owner store.StudioOwner, title, modelID, systemPrompt string) (model.StudioConversation, error) {
	title = strings.TrimSpace(title)
	if !validStudioOwner(owner) || utf8.RuneCountInString(title) > MaxStudioTitleRunes || len(modelID) > maxStudioModelBytes || len(systemPrompt) > MaxStudioSystemPromptBytes {
		return model.StudioConversation{}, ErrInvalidInput
	}
	now := s.clock.Now()
	c := model.StudioConversation{
		ID: model.NewID(model.PrefixConversation), TenantID: owner.TenantID, UserID: owner.UserID,
		Title: title, Model: modelID, SystemPrompt: systemPrompt, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateStudioConversation(ctx, &c, MaxStudioConversationsPerUser); err != nil {
		return model.StudioConversation{}, translate(err)
	}
	return c, nil
}

// GetStudioConversation reads one of owner's conversations.
//
// GetStudioConversation 读取 owner 的一个对话。
func (s *Service) GetStudioConversation(ctx context.Context, owner store.StudioOwner, id string) (model.StudioConversation, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixConversation) {
		return model.StudioConversation{}, ErrNotFound
	}
	c, err := s.store.GetStudioConversation(ctx, owner, id)
	return c, translate(err)
}

// ListStudioConversations reads one page of owner's conversations, most
// recently updated first. cursor is the Next of the previous page.
//
// ListStudioConversations 读取 owner 的一页对话，按最近更新倒序。cursor 是上一页的 Next。
func (s *Service) ListStudioConversations(ctx context.Context, owner store.StudioOwner, query, cursor string, limit int) (StudioConversationPage, error) {
	if !validStudioOwner(owner) || utf8.RuneCountInString(query) > MaxStudioTitleRunes {
		return StudioConversationPage{}, ErrInvalidInput
	}
	if limit <= 0 {
		limit = defaultStudioListLimit
	}
	limit = min(limit, maxStudioListLimit)
	filter := store.StudioConversationFilter{Owner: owner, Query: strings.TrimSpace(query), Limit: limit + 1}
	if cursor != "" {
		before, ok := decodeStudioCursor(cursor)
		if !ok {
			return StudioConversationPage{}, ErrInvalidInput
		}
		filter.Before = &before
	}
	rows, err := s.store.ListStudioConversations(ctx, filter)
	if err != nil {
		return StudioConversationPage{}, translate(err)
	}
	page := StudioConversationPage{Conversations: rows}
	if len(rows) > limit {
		page.Conversations = rows[:limit]
		last := rows[limit-1]
		page.Next = encodeStudioCursor(store.StudioConversationCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
	}
	return page, nil
}

func encodeStudioCursor(c store.StudioConversationCursor) string {
	return encodeStudioKeyset(store.StudioCursor{At: c.UpdatedAt, ID: c.ID})
}

func decodeStudioCursor(cursor string) (store.StudioConversationCursor, bool) {
	c, ok := decodeStudioKeyset(cursor, model.PrefixConversation)
	return store.StudioConversationCursor{UpdatedAt: c.At, ID: c.ID}, ok
}

// encodeStudioKeyset and decodeStudioKeyset carry a Studio list position as
// an opaque token. decodeStudioKeyset accepts only ids with prefix, so a
// cursor from one list cannot page another.
//
// encodeStudioKeyset 与 decodeStudioKeyset 把 Studio 列表位置编码为不透明的令牌。
// decodeStudioKeyset 只接受带 prefix 的 id，因此一个列表的游标不能用来翻另一个列表。
func encodeStudioKeyset(c store.StudioCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

func decodeStudioKeyset(cursor, prefix string) (store.StudioCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return store.StudioCursor{}, false
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || !validStudioID(id, prefix) {
		return store.StudioCursor{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return store.StudioCursor{}, false
	}
	return store.StudioCursor{At: parsed, ID: id}, true
}

// UpdateStudioConversation renames a conversation or changes its model or
// system prompt.
//
// UpdateStudioConversation 重命名对话，或修改其模型或系统提示词。
func (s *Service) UpdateStudioConversation(ctx context.Context, owner store.StudioOwner, id string, patch store.StudioConversationPatch) (model.StudioConversation, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixConversation) {
		return model.StudioConversation{}, ErrNotFound
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" || utf8.RuneCountInString(title) > MaxStudioTitleRunes {
			return model.StudioConversation{}, ErrInvalidInput
		}
		patch.Title = &title
	}
	if (patch.Model != nil && (*patch.Model == "" || len(*patch.Model) > maxStudioModelBytes)) ||
		(patch.SystemPrompt != nil && len(*patch.SystemPrompt) > MaxStudioSystemPromptBytes) {
		return model.StudioConversation{}, ErrInvalidInput
	}
	if patch.Title == nil && patch.Model == nil && patch.SystemPrompt == nil {
		return model.StudioConversation{}, ErrInvalidInput
	}
	c, err := s.store.UpdateStudioConversation(ctx, owner, id, patch, s.clock.Now())
	return c, translate(err)
}

// DeleteStudioConversation removes a conversation and its messages.
//
// DeleteStudioConversation 删除一个对话及其消息。
func (s *Service) DeleteStudioConversation(ctx context.Context, owner store.StudioOwner, id string) error {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixConversation) {
		return ErrNotFound
	}
	return translate(s.store.DeleteStudioConversation(ctx, owner, id))
}

// ListStudioMessages reads a conversation's messages after afterSeq.
//
// ListStudioMessages 读取对话中 afterSeq 之后的消息。
func (s *Service) ListStudioMessages(ctx context.Context, owner store.StudioOwner, id string, afterSeq int64, limit int) ([]model.StudioMessage, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixConversation) {
		return nil, ErrNotFound
	}
	if afterSeq < 0 {
		return nil, ErrInvalidInput
	}
	if limit <= 0 || limit > maxStudioMessagePage {
		limit = maxStudioMessagePage
	}
	messages, err := s.store.ListStudioMessages(ctx, owner, id, afterSeq, limit)
	return messages, translate(err)
}

// BeginStudioTurn writes the user side of a turn and returns the history the
// reply is generated from. A new conversation without a title takes the
// first line of its first message.
//
// BeginStudioTurn 写入一轮中用户的一侧，返回生成回复所依据的历史。没有标题的新对话取第一
// 条消息的首行作为标题。
func (s *Service) BeginStudioTurn(ctx context.Context, owner store.StudioOwner, id string, p StudioTurnParams) (model.StudioConversation, []model.StudioMessage, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixConversation) {
		return model.StudioConversation{}, nil, ErrNotFound
	}
	if len(p.Model) > maxStudioModelBytes || len(p.Content) > MaxStudioUserContentBytes || (p.ReplaceFromID != "" && !validStudioID(p.ReplaceFromID, model.PrefixMessage)) {
		return model.StudioConversation{}, nil, ErrInvalidInput
	}
	if p.Regenerate == (strings.TrimSpace(p.Content) != "") || (p.Regenerate && p.ReplaceFromID != "") {
		// Exactly one of: regenerate, or a message to send.
		// 只能二选一：重新生成，或发送一条消息。
		return model.StudioConversation{}, nil, ErrInvalidInput
	}
	now := s.clock.Now()
	turn := store.StudioTurn{Regenerate: p.Regenerate, ReplaceFromID: p.ReplaceFromID, Model: p.Model, MaxMessages: MaxStudioMessagesPerConversation, At: now}
	if !p.Regenerate {
		turn.User = &model.StudioMessage{
			ID: model.NewID(model.PrefixMessage), ConversationID: id, TenantID: owner.TenantID, UserID: owner.UserID,
			Role: model.StudioRoleUser, Content: p.Content, Status: model.StudioMessageComplete, CreatedAt: now,
		}
		turn.Title = autoTitle(p.Content)
	}
	c, history, err := s.store.BeginStudioTurn(ctx, owner, id, turn)
	return c, history, translate(err)
}

// autoTitle is the first non-empty line of content, cut to
// studioAutoTitleRunes.
//
// autoTitle 取 content 中第一行非空文本，截断到 studioAutoTitleRunes 个字符。
func autoTitle(content string) string {
	line := ""
	for candidate := range strings.SplitSeq(content, "\n") {
		if line = strings.TrimSpace(candidate); line != "" {
			break
		}
	}
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, line)
	if runes := []rune(line); len(runes) > studioAutoTitleRunes {
		line = strings.TrimSpace(string(runes[:studioAutoTitleRunes])) + "…"
	}
	return line
}

// AppendStudioReply writes the assistant reply of a turn.
//
// AppendStudioReply 写入一轮的助手回复。
func (s *Service) AppendStudioReply(ctx context.Context, owner store.StudioOwner, id string, p StudioReplyParams) (model.StudioMessage, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixConversation) {
		return model.StudioMessage{}, ErrNotFound
	}
	switch p.Status {
	case model.StudioMessageComplete, model.StudioMessageIncomplete, model.StudioMessageError:
	default:
		return model.StudioMessage{}, ErrInvalidInput
	}
	if p.ReplyTo <= 0 || !validStudioID(p.ID, model.PrefixMessage) || len(p.Content) > MaxStudioReplyContentBytes || len(p.Reasoning) > MaxStudioReplyContentBytes ||
		len(p.Model) > maxStudioModelBytes || len(p.ErrorCode) > maxStudioErrorCodeBytes || p.PromptTokens < 0 || p.CompletionTokens < 0 {
		return model.StudioMessage{}, ErrInvalidInput
	}
	reply := model.StudioMessage{
		ID: p.ID, ConversationID: id, TenantID: owner.TenantID, UserID: owner.UserID, Role: model.StudioRoleAssistant,
		Content: p.Content, Reasoning: p.Reasoning, Status: p.Status, Model: p.Model,
		PromptTokens: p.PromptTokens, CompletionTokens: p.CompletionTokens, ErrorCode: p.ErrorCode, CreatedAt: s.clock.Now(),
	}
	if err := s.store.AppendStudioReply(ctx, owner, id, p.ReplyTo, &reply); err != nil {
		return model.StudioMessage{}, translate(err)
	}
	return reply, nil
}

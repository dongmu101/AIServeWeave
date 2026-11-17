package controlplaneclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// DefaultStudioTimeout bounds one Studio storage call. A turn reads a whole
// conversation back, so it is longer than the Responses one.
//
// DefaultStudioTimeout 限制单次 Studio 存储调用。一轮会读回整个对话，因此比 Responses 的长。
const DefaultStudioTimeout = 10 * time.Second

// StudioClientConfig configures a StudioClient.
// StudioClientConfig 配置一个 StudioClient。
type StudioClientConfig struct {
	// Endpoint is the control plane's base URL. / Endpoint 是控制面的基础 URL。
	Endpoint string
	// Token must match the control plane's InternalToken. / Token 必须与控制面的 InternalToken 一致。
	Token string
	// Timeout bounds one call; zero uses DefaultStudioTimeout. / Timeout 限制单次调用；为零时取 DefaultStudioTimeout。
	Timeout time.Duration
	// HTTPClient is used for the calls; nil builds one. / HTTPClient 用于调用；为 nil 时自动构造。
	HTTPClient *http.Client
}

// StudioClient implements httpapi.StudioStore over the control plane's
// /internal/v1/studio API.
//
// StudioClient 基于控制面的 /internal/v1/studio API 实现 httpapi.StudioStore。
type StudioClient struct {
	endpoint string
	token    string
	client   *http.Client
}

var _ httpapi.StudioStore = (*StudioClient)(nil)

// NewStudioClient validates cfg and returns a StudioClient.
// NewStudioClient 校验 cfg 并返回一个 StudioClient。
func NewStudioClient(cfg StudioClientConfig) (*StudioClient, error) {
	endpoint := strings.TrimSuffix(strings.TrimSpace(cfg.Endpoint), "/")
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, errors.New("controlplaneclient: the endpoint must include a scheme, e.g. http://127.0.0.1:8090")
	}
	if cfg.Token == "" {
		return nil, errors.New("controlplaneclient: a token is required; it must match the control plane's InternalToken")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultStudioTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &StudioClient{endpoint: endpoint, token: cfg.Token, client: client}, nil
}

type studioConversationWire struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (w studioConversationWire) toConversation() httpapi.StudioConversation {
	return httpapi.StudioConversation{ID: w.ID, Title: w.Title, Model: w.Model, SystemPrompt: w.SystemPrompt, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
}

type studioMessageWire struct {
	ID               string    `json:"id"`
	Seq              int64     `json:"seq"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	Reasoning        string    `json:"reasoning"`
	Status           string    `json:"status"`
	Model            string    `json:"model"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	ErrorCode        string    `json:"error_code"`
	CreatedAt        time.Time `json:"created_at"`
}

func toMessages(wire []studioMessageWire) []httpapi.StudioMessage {
	out := make([]httpapi.StudioMessage, len(wire))
	for i, m := range wire {
		out[i] = httpapi.StudioMessage{
			ID: m.ID, Seq: m.Seq, Role: m.Role, Content: m.Content, Reasoning: m.Reasoning, Status: m.Status, Model: m.Model,
			PromptTokens: m.PromptTokens, CompletionTokens: m.CompletionTokens, ErrorCode: m.ErrorCode, CreatedAt: m.CreatedAt,
		}
	}
	return out
}

// studioPath builds an internal Studio path carrying the owner in its query.
// studioPath 构造一个在查询串中携带所有者的内部 Studio 路径。
func studioPath(owner httpapi.StudioOwner, conversationID, suffix string, extra url.Values) string {
	p := "/internal/v1/studio/conversations"
	if conversationID != "" {
		p += "/" + url.PathEscape(conversationID)
	}
	p += suffix
	q := url.Values{"tenant_id": {owner.TenantID}, "user_id": {owner.UserID}}
	for k, v := range extra {
		q[k] = v
	}
	return p + "?" + q.Encode()
}

// CreateConversation implements httpapi.StudioStore.
func (c *StudioClient) CreateConversation(ctx context.Context, owner httpapi.StudioOwner, title, model, systemPrompt string) (httpapi.StudioConversation, error) {
	body := map[string]string{"title": title, "model": model, "system_prompt": systemPrompt}
	var out studioConversationWire
	err := c.call(ctx, http.MethodPost, studioPath(owner, "", "", nil), body, &out)
	return out.toConversation(), err
}

// ListConversations implements httpapi.StudioStore.
func (c *StudioClient) ListConversations(ctx context.Context, owner httpapi.StudioOwner, query, cursor string, limit int) (httpapi.StudioConversationPage, error) {
	extra := url.Values{}
	if query != "" {
		extra.Set("q", query)
	}
	if cursor != "" {
		extra.Set("cursor", cursor)
	}
	if limit > 0 {
		extra.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Data []studioConversationWire `json:"data"`
		Next string                   `json:"next"`
	}
	if err := c.call(ctx, http.MethodGet, studioPath(owner, "", "", extra), nil, &out); err != nil {
		return httpapi.StudioConversationPage{}, err
	}
	page := httpapi.StudioConversationPage{Conversations: make([]httpapi.StudioConversation, len(out.Data)), Next: out.Next}
	for i, w := range out.Data {
		page.Conversations[i] = w.toConversation()
	}
	return page, nil
}

// GetConversation implements httpapi.StudioStore.
func (c *StudioClient) GetConversation(ctx context.Context, owner httpapi.StudioOwner, id string) (httpapi.StudioConversation, error) {
	var out studioConversationWire
	err := c.call(ctx, http.MethodGet, studioPath(owner, id, "", nil), nil, &out)
	return out.toConversation(), err
}

// UpdateConversation implements httpapi.StudioStore.
func (c *StudioClient) UpdateConversation(ctx context.Context, owner httpapi.StudioOwner, id string, patch httpapi.StudioConversationPatch) (httpapi.StudioConversation, error) {
	body := struct {
		Title        *string `json:"title,omitempty"`
		Model        *string `json:"model,omitempty"`
		SystemPrompt *string `json:"system_prompt,omitempty"`
	}{patch.Title, patch.Model, patch.SystemPrompt}
	var out studioConversationWire
	err := c.call(ctx, http.MethodPatch, studioPath(owner, id, "", nil), body, &out)
	return out.toConversation(), err
}

// DeleteConversation implements httpapi.StudioStore.
func (c *StudioClient) DeleteConversation(ctx context.Context, owner httpapi.StudioOwner, id string) error {
	return c.call(ctx, http.MethodDelete, studioPath(owner, id, "", nil), nil, nil)
}

// ListMessages implements httpapi.StudioStore.
func (c *StudioClient) ListMessages(ctx context.Context, owner httpapi.StudioOwner, id string, afterSeq int64, limit int) ([]httpapi.StudioMessage, error) {
	extra := url.Values{}
	if afterSeq > 0 {
		extra.Set("after_seq", strconv.FormatInt(afterSeq, 10))
	}
	if limit > 0 {
		extra.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Data []studioMessageWire `json:"data"`
	}
	if err := c.call(ctx, http.MethodGet, studioPath(owner, id, "/messages", extra), nil, &out); err != nil {
		return nil, err
	}
	return toMessages(out.Data), nil
}

// BeginTurn implements httpapi.StudioStore.
func (c *StudioClient) BeginTurn(ctx context.Context, owner httpapi.StudioOwner, id string, turn httpapi.StudioTurn) (httpapi.StudioConversation, []httpapi.StudioMessage, error) {
	body := map[string]any{"regenerate": turn.Regenerate, "replace_from_message_id": turn.ReplaceFromMessageID, "content": turn.Content, "model": turn.Model}
	var out struct {
		Conversation studioConversationWire `json:"conversation"`
		Messages     []studioMessageWire    `json:"messages"`
	}
	if err := c.call(ctx, http.MethodPost, studioPath(owner, id, "/turns", nil), body, &out); err != nil {
		return httpapi.StudioConversation{}, nil, err
	}
	return out.Conversation.toConversation(), toMessages(out.Messages), nil
}

// AppendReply implements httpapi.StudioStore.
func (c *StudioClient) AppendReply(ctx context.Context, owner httpapi.StudioOwner, id string, reply httpapi.StudioReply) error {
	body := map[string]any{
		"reply_to": reply.ReplyTo, "id": reply.ID, "content": reply.Content, "reasoning": reply.Reasoning, "status": reply.Status,
		"model": reply.Model, "prompt_tokens": reply.PromptTokens, "completion_tokens": reply.CompletionTokens, "error_code": reply.ErrorCode,
	}
	return c.call(ctx, http.MethodPost, studioPath(owner, id, "/replies", nil), body, nil)
}

// call performs one request and maps the control plane's answer to the
// httpapi Studio errors. A 409 is told apart by its error message.
//
// call 执行一次请求，并把控制面的应答映射为 httpapi 的 Studio 错误。409 按错误消息区分。
func (c *StudioClient) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: reaching the control plane: %v", ErrOutcomeUnknown, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("%w: decoding the response: %v", ErrOutcomeUnknown, err)
		}
		return nil
	case http.StatusNotFound:
		return httpapi.ErrStudioNotFound
	case http.StatusConflict:
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&refusal)
		switch refusal.Error {
		case "studio_limit":
			return httpapi.ErrStudioLimit
		case "studio_stale_turn":
			return httpapi.ErrStudioStaleTurn
		}
		return httpapi.ErrStudioConflict
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return httpapi.ErrStudioInvalid
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: this Gateway's internal token was refused by the control plane", ErrOutcomeUnknown)
	default:
		return fmt.Errorf("%w: the control plane answered %d", ErrOutcomeUnknown, resp.StatusCode)
	}
}

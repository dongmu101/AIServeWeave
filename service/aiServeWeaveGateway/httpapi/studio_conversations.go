package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"AIServeWeave/common/idgen"
)

// Studio conversation limits on the Gateway side. Storage limits (titles,
// message counts) are the control plane's; these bound what one request
// makes this replica hold.
//
// Gateway 一侧的 Studio 对话限制。存储上限（标题、消息数）属于控制面；这里限定的是单个
// 请求让本副本持有的量。
const (
	maxStudioBodyBytes      = 256 << 10
	maxStudioReplyBytes     = 1 << 20
	maxStudioCursorBytes    = 512
	studioReplyWriteTimeout = 5 * time.Second
	studioReplyWriteTries   = 3
)

// mountStudioConversations registers the conversation endpoints.
//
// mountStudioConversations 注册对话端点。
func (h *handlers) mountStudioConversations(mux *http.ServeMux, st StudioStore, logger *slog.Logger) {
	s := &studioConversations{h: h, store: st, logger: logger}
	mux.HandleFunc("GET /studio/v1/conversations", s.list)
	mux.HandleFunc("POST /studio/v1/conversations", s.create)
	mux.HandleFunc("GET /studio/v1/conversations/{id}", s.get)
	mux.HandleFunc("PATCH /studio/v1/conversations/{id}", s.update)
	mux.HandleFunc("DELETE /studio/v1/conversations/{id}", s.remove)
	mux.HandleFunc("GET /studio/v1/conversations/{id}/messages", s.messages)
	mux.HandleFunc("POST /studio/v1/conversations/{id}/messages", s.send)
}

type studioConversations struct {
	h      *handlers
	store  StudioStore
	logger *slog.Logger
}

type studioConversationJSON struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func renderConversation(c StudioConversation) studioConversationJSON {
	return studioConversationJSON{ID: c.ID, Title: c.Title, Model: c.Model, SystemPrompt: c.SystemPrompt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

type studioUsageJSON struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

type studioMessageJSON struct {
	ID        string           `json:"id"`
	Seq       int64            `json:"seq"`
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Reasoning string           `json:"reasoning,omitempty"`
	Status    string           `json:"status"`
	Model     string           `json:"model,omitempty"`
	Usage     *studioUsageJSON `json:"usage,omitempty"`
	ErrorCode string           `json:"error_code,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
}

func renderMessage(m StudioMessage) studioMessageJSON {
	out := studioMessageJSON{ID: m.ID, Seq: m.Seq, Role: m.Role, Content: m.Content, Reasoning: m.Reasoning, Status: m.Status, Model: m.Model, ErrorCode: m.ErrorCode, CreatedAt: m.CreatedAt}
	if m.PromptTokens > 0 || m.CompletionTokens > 0 {
		out.Usage = &studioUsageJSON{PromptTokens: m.PromptTokens, CompletionTokens: m.CompletionTokens}
	}
	return out
}

// studioOwnerOf is the tenant user studioAuth put on the context.
// studioOwnerOf 是 studioAuth 放到上下文中的租户用户。
func studioOwnerOf(r *http.Request) StudioOwner {
	identity, _ := IdentityFrom(r.Context())
	return StudioOwner{TenantID: identity.TenantID, UserID: identity.UserID}
}

// conversationID reads and shape-checks the {id} path value, so a malformed
// id is a 404 without a round trip to the control plane.
//
// conversationID 读取并校验 {id} 路径参数的形状，使畸形 id 直接得到 404，而不必往返控制面。
func conversationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !validStudioID(id, "cnv_") {
		writeStudioError(w, http.StatusNotFound, "not_found", "no such conversation")
		return "", false
	}
	return id, true
}

func validStudioID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) > studioConversationIDMax {
		return false
	}
	for _, c := range id[len(prefix):] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return len(id) > len(prefix)
}

// storeError answers a StudioStore failure. Anything that is not a refusal
// means the control plane could not answer.
//
// storeError 回答 StudioStore 的失败。不属于拒绝的错误都表示控制面无法应答。
func (s *studioConversations) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrStudioNotFound):
		writeStudioError(w, http.StatusNotFound, "not_found", "no such conversation")
	case errors.Is(err, ErrStudioLimit):
		writeStudioError(w, http.StatusConflict, "limit_reached", "the conversation or message limit is reached")
	case errors.Is(err, ErrStudioConflict), errors.Is(err, ErrStudioStaleTurn):
		writeStudioError(w, http.StatusConflict, "conflict", "the conversation changed; reload it")
	case errors.Is(err, ErrStudioInvalid):
		writeStudioError(w, http.StatusBadRequest, "invalid_request", "the request is not valid")
	default:
		s.logger.Error("studio storage is unavailable", slog.Any("error", err))
		writeStudioError(w, http.StatusServiceUnavailable, "storage_unavailable", "conversations cannot be reached right now")
	}
}

// decodeStudioBody decodes a JSON body, treating an empty one as {}.
// decodeStudioBody 解码 JSON 请求体，空请求体视为 {}。
func decodeStudioBody(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxStudioBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeStudioError(w, http.StatusRequestEntityTooLarge, "too_large", "the request body is too large")
		} else {
			writeStudioError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON for this endpoint")
		}
		return false
	}
	return true
}

// list implements GET /studio/v1/conversations?q=&cursor=&limit=.
func (s *studioConversations) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cursor := q.Get("cursor")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if len(cursor) > maxStudioCursorBytes || limit < 0 {
		writeStudioError(w, http.StatusBadRequest, "invalid_request", "the cursor or limit is not valid")
		return
	}
	page, err := s.store.ListConversations(r.Context(), studioOwnerOf(r), q.Get("q"), cursor, limit)
	if err != nil {
		s.storeError(w, err)
		return
	}
	out := struct {
		Data []studioConversationJSON `json:"data"`
		Next string                   `json:"next,omitempty"`
	}{Data: make([]studioConversationJSON, len(page.Conversations)), Next: page.Next}
	for i, c := range page.Conversations {
		out.Data[i] = renderConversation(c)
	}
	writeStudioJSON(w, http.StatusOK, out)
}

// create implements POST /studio/v1/conversations.
func (s *studioConversations) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title        string `json:"title"`
		Model        string `json:"model"`
		SystemPrompt string `json:"system_prompt"`
	}
	if !decodeStudioBody(w, r, &req) {
		return
	}
	c, err := s.store.CreateConversation(r.Context(), studioOwnerOf(r), req.Title, req.Model, req.SystemPrompt)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusCreated, renderConversation(c))
}

// get implements GET /studio/v1/conversations/{id}.
func (s *studioConversations) get(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	c, err := s.store.GetConversation(r.Context(), studioOwnerOf(r), id)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusOK, renderConversation(c))
}

// update implements PATCH /studio/v1/conversations/{id}.
func (s *studioConversations) update(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	var req struct {
		Title        *string `json:"title"`
		Model        *string `json:"model"`
		SystemPrompt *string `json:"system_prompt"`
	}
	if !decodeStudioBody(w, r, &req) {
		return
	}
	c, err := s.store.UpdateConversation(r.Context(), studioOwnerOf(r), id, StudioConversationPatch{Title: req.Title, Model: req.Model, SystemPrompt: req.SystemPrompt})
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeStudioJSON(w, http.StatusOK, renderConversation(c))
}

// remove implements DELETE /studio/v1/conversations/{id}.
func (s *studioConversations) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteConversation(r.Context(), studioOwnerOf(r), id); err != nil {
		s.storeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// messages implements GET /studio/v1/conversations/{id}/messages?after_seq=&limit=.
func (s *studioConversations) messages(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var after int64
	if raw := q.Get("after_seq"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeStudioError(w, http.StatusBadRequest, "invalid_request", "after_seq must be a non-negative integer")
			return
		}
		after = parsed
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	messages, err := s.store.ListMessages(r.Context(), studioOwnerOf(r), id, after, max(limit, 0))
	if err != nil {
		s.storeError(w, err)
		return
	}
	out := struct {
		Data []studioMessageJSON `json:"data"`
	}{Data: make([]studioMessageJSON, len(messages))}
	for i, m := range messages {
		out.Data[i] = renderMessage(m)
	}
	writeStudioJSON(w, http.StatusOK, out)
}

// studioSendRequest is the body of POST .../messages: Content to send (or,
// with ReplaceFromMessageID, to send in place of that message and all after
// it), or Regenerate to answer the last question again.
//
// studioSendRequest 是 POST .../messages 的请求体：要发送的 Content（带
// ReplaceFromMessageID 时替换那条消息及其后的所有消息），或 Regenerate 重新回答最后的问题。
type studioSendRequest struct {
	Content              string `json:"content"`
	Model                string `json:"model"`
	Regenerate           bool   `json:"regenerate"`
	ReplaceFromMessageID string `json:"replace_from_message_id"`
}

// send implements POST /studio/v1/conversations/{id}/messages. The user side
// of the turn is written before dispatch and a failure there dispatches
// nothing. The reply streams as Studio SSE frames and is written once the
// stream ends: complete, incomplete when the client left or the stream was
// cut, or error.
//
// The dispatch is the Gateway's own Chat Completions handler, called in
// process over a tap that translates its chunks, so a Studio turn takes
// exactly the path an API-key request to the same model takes: model wait,
// native Claude CLI chat, output limits, scheduling and usage recording.
//
// send 实现 POST /studio/v1/conversations/{id}/messages。一轮中用户的一侧在派发前写入，
// 写入失败则不派发。回复以 Studio SSE 帧流式返回，并在流结束后写入：complete；客户端
// 离开或流被截断时为 incomplete；或 error。
//
// 派发使用 Gateway 自己的 Chat Completions handler，在进程内经一个转换其 chunk 的 tap
// 调用，因此 Studio 的一轮与 API Key 对同一模型的请求走完全相同的路径：等待模型、原生
// Claude CLI 对话、输出上限、调度与用量记录。
func (s *studioConversations) send(w http.ResponseWriter, r *http.Request) {
	id, ok := conversationID(w, r)
	if !ok {
		return
	}
	var req studioSendRequest
	if !decodeStudioBody(w, r, &req) {
		return
	}
	hasContent := strings.TrimSpace(req.Content) != ""
	if req.Regenerate == hasContent || (req.Regenerate && req.ReplaceFromMessageID != "") ||
		(req.ReplaceFromMessageID != "" && !validStudioID(req.ReplaceFromMessageID, studioMessagePrefix)) {
		writeStudioError(w, http.StatusBadRequest, "invalid_request", "send either content or regenerate")
		return
	}
	if !s.offered(r.Context(), req.Model) {
		writeStudioError(w, http.StatusBadRequest, "model_unavailable", "the selected model is not available")
		return
	}
	owner := studioOwnerOf(r)
	conversation, history, err := s.store.BeginTurn(r.Context(), owner, id, StudioTurn{
		Regenerate: req.Regenerate, ReplaceFromMessageID: req.ReplaceFromMessageID, Content: req.Content, Model: req.Model,
	})
	if err != nil {
		s.storeError(w, err)
		return
	}
	if len(history) == 0 || history[len(history)-1].Role != studioRoleUser {
		writeStudioError(w, http.StatusConflict, "conflict", "the conversation changed; reload it")
		return
	}
	question := history[len(history)-1]
	replyID := studioMessagePrefix + idgen.New()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := &studioFrames{w: w}
	start := struct {
		Conversation studioConversationJSON `json:"conversation"`
		UserMessage  studioMessageJSON      `json:"user_message"`
		MessageID    string                 `json:"message_id"`
		Model        string                 `json:"model"`
	}{renderConversation(conversation), renderMessage(question), replyID, req.Model}
	out.emit("message_start", start)

	tap := newChatTap(out)
	s.h.chatCompletions(tap, studioChatRequest(r, conversation, history, req.Model))
	status, code := tap.outcome(r.Context().Err() != nil)

	reply := StudioReply{
		ReplyTo: question.Seq, ID: replyID, Content: tap.content.String(), Reasoning: tap.reasoning.String(),
		Status: status, Model: req.Model, PromptTokens: tap.usage.PromptTokens, CompletionTokens: tap.usage.CompletionTokens, ErrorCode: code,
	}
	s.saveReply(r.Context(), owner, id, reply)

	if tap.usage != (studioUsageJSON{}) {
		out.emit("usage", tap.usage)
	}
	if status == studioReplyError {
		out.emit("error", studioErrorBody{Code: code, Message: studioDispatchMessage(code)})
	}
	out.emit("message_stop", struct {
		MessageID string `json:"message_id"`
		Status    string `json:"status"`
		ErrorCode string `json:"error_code,omitempty"`
	}{replyID, status, code})
}

// offered reports whether model is one this user may chat with now.
// offered 报告 model 当前是否可供该用户对话。
func (s *studioConversations) offered(ctx context.Context, model string) bool {
	if model == "" {
		return false
	}
	for _, m := range s.h.sched.Models(ctx) {
		if m.ID == model {
			return true
		}
	}
	return false
}

// saveReply writes the reply with bounded retries, detached from the
// request so a stop or disconnect still records what had arrived. A final
// failure leaves the reply missing from history rather than invented.
//
// saveReply 以有界重试写入回复，并与请求解绑，使停止或断开时仍能记录已收到的部分。最终
// 失败时历史中如实缺失这条回复，而不是编造内容。
func (s *studioConversations) saveReply(ctx context.Context, owner StudioOwner, id string, reply StudioReply) {
	detached := context.WithoutCancel(ctx)
	var err error
	for attempt := range studioReplyWriteTries {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		callCtx, cancel := context.WithTimeout(detached, studioReplyWriteTimeout)
		err = s.store.AppendReply(callCtx, owner, id, reply)
		cancel()
		if err == nil || errors.Is(err, ErrStudioNotFound) || errors.Is(err, ErrStudioStaleTurn) || errors.Is(err, ErrStudioInvalid) {
			break
		}
	}
	if err != nil {
		s.logger.Warn("studio reply was not saved", slog.String("conversation_id", id), slog.String("status", reply.Status), slog.Any("error", err))
	}
}

// studioChatRequest builds the in-process Chat Completions request for a
// turn: the system prompt, then the history. A failed reply is left out,
// since it carries no answer; an incomplete one is kept, since the user saw
// it. The conversation id is the session key, so the turns of one
// conversation prefer the node that has its prompt cached.
//
// studioChatRequest 为一轮构造进程内的 Chat Completions 请求：先是系统提示词，然后是历史。
// 失败的回复不带入，因为它没有答案；中断的回复保留，因为用户看到过它。对话 id 作为会话
// key，使同一对话的各轮优先落到缓存了其提示的节点。
func studioChatRequest(r *http.Request, c StudioConversation, history []StudioMessage, model string) *http.Request {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	messages := make([]message, 0, len(history)+1)
	if strings.TrimSpace(c.SystemPrompt) != "" {
		messages = append(messages, message{"system", c.SystemPrompt})
	}
	for _, m := range history {
		if m.Role == studioRoleAssistant && (m.Status == studioReplyError || m.Content == "") {
			continue
		}
		messages = append(messages, message{m.Role, m.Content})
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "messages": messages, "stream": true,
		"stream_options": map[string]bool{"include_usage": true},
	})
	inner := r.Clone(r.Context())
	inner.Method = http.MethodPost
	inner.URL = &url.URL{Path: "/v1/chat/completions"}
	inner.RequestURI = ""
	inner.Header = http.Header{"Content-Type": {"application/json"}, "X-Session-Id": {c.ID}}
	inner.Body = io.NopCloser(bytes.NewReader(body))
	inner.ContentLength = int64(len(body))
	return inner
}

// studioFrames writes Studio SSE frames. A write error means the client
// left; later frames are dropped, and the turn still finishes and is saved.
//
// studioFrames 写出 Studio SSE 帧。写入出错表示客户端已离开；之后的帧被丢弃，而这一轮
// 仍会完成并保存。
type studioFrames struct {
	w    http.ResponseWriter
	gone bool
}

func (f *studioFrames) emit(event string, payload any) {
	if f.gone {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var frame bytes.Buffer
	frame.WriteString("event: ")
	frame.WriteString(event)
	frame.WriteString("\ndata: ")
	frame.Write(data)
	frame.WriteString("\n\n")
	if _, err := f.w.Write(frame.Bytes()); err != nil {
		f.gone = true
		return
	}
	if flusher, ok := f.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// chatTap is the http.ResponseWriter the in-process Chat Completions call
// writes to. It parses the OpenAI chunk stream as it arrives, forwards the
// deltas as Studio frames, and keeps what the reply needs to be saved. An
// error response (any status but 200) is kept, bounded, for its code.
//
// chatTap 是进程内 Chat Completions 调用写入的 http.ResponseWriter。它边到边解析
// OpenAI chunk 流，把增量转发为 Studio 帧，并保存写入回复所需的内容。错误响应（200 之外的
// 任何状态）被有界地保留，用于读取其错误码。
type chatTap struct {
	out       *studioFrames
	header    http.Header
	status    int
	pending   []byte
	errBody   bytes.Buffer
	done      bool
	failed    bool
	content   strings.Builder
	reasoning strings.Builder
	usage     studioUsageJSON
}

func newChatTap(out *studioFrames) *chatTap {
	return &chatTap{out: out, header: http.Header{}}
}

func (t *chatTap) Header() http.Header { return t.header }

func (t *chatTap) WriteHeader(status int) {
	if t.status == 0 {
		t.status = status
	}
}

func (t *chatTap) Flush() {}

func (t *chatTap) Write(p []byte) (int, error) {
	if t.status == 0 {
		t.status = http.StatusOK
	}
	if t.status != http.StatusOK {
		if room := 4096 - t.errBody.Len(); room > 0 {
			t.errBody.Write(p[:min(len(p), room)])
		}
		return len(p), nil
	}
	t.pending = append(t.pending, p...)
	for {
		end := bytes.Index(t.pending, []byte("\n\n"))
		if end < 0 {
			break
		}
		t.frame(t.pending[:end])
		t.pending = t.pending[end+2:]
	}
	return len(p), nil
}

// frame handles one SSE event of the chunk stream.
// frame 处理 chunk 流中的一个 SSE 事件。
func (t *chatTap) frame(event []byte) {
	for line := range bytes.SplitSeq(event, []byte("\n")) {
		data, ok := bytes.CutPrefix(line, []byte("data: "))
		if !ok {
			continue
		}
		if string(bytes.TrimSpace(data)) == "[DONE]" {
			t.done = true
			continue
		}
		var chunk chatCompletionChunk
		if json.Unmarshal(data, &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			t.usage = studioUsageJSON{PromptTokens: int64(chunk.Usage.PromptTokens), CompletionTokens: int64(chunk.Usage.CompletionTokens)}
		}
		for _, choice := range chunk.Choices {
			if d := choice.Delta.ReasoningContent; d != "" {
				appendBounded(&t.reasoning, d)
				t.out.emit("thinking_delta", struct {
					Delta string `json:"delta"`
				}{d})
			}
			if d := choice.Delta.Content; d != "" {
				appendBounded(&t.content, d)
				t.out.emit("text_delta", struct {
					Delta string `json:"delta"`
				}{d})
			}
			if choice.FinishReason != nil && *choice.FinishReason == "error" {
				t.failed = true
			}
		}
	}
}

// appendBounded appends s to b up to maxStudioReplyBytes, cutting on a rune
// boundary. What streamed past the bound reached the user but is not saved.
//
// appendBounded 把 s 追加到 b，至多 maxStudioReplyBytes，并在字符边界处截断。超出部分已
// 流给用户，但不会保存。
func appendBounded(b *strings.Builder, s string) {
	room := maxStudioReplyBytes - b.Len()
	if room <= 0 {
		return
	}
	if len(s) > room {
		s = s[:room]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	b.WriteString(s)
}

// outcome classifies the finished dispatch into a reply status and a stable
// error code. A client that left makes any unfinished reply incomplete.
//
// outcome 把结束的派发归类为回复状态与稳定错误码。客户端离开时，任何未完成的回复都是 incomplete。
func (t *chatTap) outcome(clientGone bool) (status, code string) {
	switch {
	case t.status != 0 && t.status != http.StatusOK:
		return studioReplyError, studioDispatchCode(t.status, t.errBody.Bytes())
	case t.done && !t.failed:
		return studioReplyComplete, ""
	case clientGone:
		return studioReplyIncomplete, ""
	case t.failed:
		return studioReplyError, "upstream_error"
	default:
		return studioReplyIncomplete, ""
	}
}

// studioDispatchCode maps a Chat Completions error response to a Studio
// error code. The upstream message is never passed on.
//
// studioDispatchCode 把 Chat Completions 的错误响应映射为 Studio 错误码。上游消息从不透传。
func studioDispatchCode(status int, body []byte) string {
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	switch code := parsed.Error.Code; {
	case code == "model_not_found" || code == "model_unavailable":
		return "model_unavailable"
	case code == "insufficient_balance" || code == "account_subscription_expired":
		return code
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status == http.StatusGatewayTimeout:
		return "timeout"
	case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge:
		return "invalid_request"
	default:
		return "upstream_error"
	}
}

// studioDispatchMessage is the fixed message for a dispatch error code.
// studioDispatchMessage 是派发错误码对应的固定消息。
func studioDispatchMessage(code string) string {
	switch code {
	case "model_unavailable":
		return "the selected model is not available"
	case "rate_limited":
		return "too many requests; retry shortly"
	case "timeout":
		return "the model did not answer in time"
	case "invalid_request":
		return "the model could not process this conversation"
	case "insufficient_balance":
		return "the account balance is exhausted"
	case "account_subscription_expired":
		return "renew an account subscription to continue"
	default:
		return "the model returned an error"
	}
}

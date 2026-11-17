package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// fakeStudioStore is an in-memory StudioStore scoped by owner, with
// switches to fail the turn write and a channel that reports saved replies.
//
// fakeStudioStore 是按所有者限定的内存 StudioStore，可让轮次写入失败，并通过 channel
// 报告已保存的回复。
type fakeStudioStore struct {
	mu            sync.Mutex
	conversations map[string]httpapi.StudioConversation
	owners        map[string]httpapi.StudioOwner
	messages      map[string][]httpapi.StudioMessage
	failTurn      error
	turns         int
	replies       chan httpapi.StudioReply
	next          int
}

func newFakeStudioStore() *fakeStudioStore {
	return &fakeStudioStore{
		conversations: map[string]httpapi.StudioConversation{}, owners: map[string]httpapi.StudioOwner{},
		messages: map[string][]httpapi.StudioMessage{}, replies: make(chan httpapi.StudioReply, 8),
	}
}

func (f *fakeStudioStore) owned(owner httpapi.StudioOwner, id string) bool {
	return f.owners[id] == owner && f.conversations[id].ID != ""
}

func (f *fakeStudioStore) CreateConversation(_ context.Context, owner httpapi.StudioOwner, title, model, systemPrompt string) (httpapi.StudioConversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	c := httpapi.StudioConversation{ID: fmt.Sprintf("cnv_%d", f.next), Title: title, Model: model, SystemPrompt: systemPrompt, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.conversations[c.ID], f.owners[c.ID] = c, owner
	return c, nil
}

func (f *fakeStudioStore) ListConversations(_ context.Context, owner httpapi.StudioOwner, _, _ string, _ int) (httpapi.StudioConversationPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	page := httpapi.StudioConversationPage{}
	for id, c := range f.conversations {
		if f.owners[id] == owner {
			page.Conversations = append(page.Conversations, c)
		}
	}
	return page, nil
}

func (f *fakeStudioStore) GetConversation(_ context.Context, owner httpapi.StudioOwner, id string) (httpapi.StudioConversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.owned(owner, id) {
		return httpapi.StudioConversation{}, httpapi.ErrStudioNotFound
	}
	return f.conversations[id], nil
}

func (f *fakeStudioStore) UpdateConversation(_ context.Context, owner httpapi.StudioOwner, id string, patch httpapi.StudioConversationPatch) (httpapi.StudioConversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.owned(owner, id) {
		return httpapi.StudioConversation{}, httpapi.ErrStudioNotFound
	}
	c := f.conversations[id]
	if patch.Title != nil {
		c.Title = *patch.Title
	}
	if patch.SystemPrompt != nil {
		c.SystemPrompt = *patch.SystemPrompt
	}
	f.conversations[id] = c
	return c, nil
}

func (f *fakeStudioStore) DeleteConversation(_ context.Context, owner httpapi.StudioOwner, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.owned(owner, id) {
		return httpapi.ErrStudioNotFound
	}
	delete(f.conversations, id)
	delete(f.messages, id)
	return nil
}

func (f *fakeStudioStore) ListMessages(_ context.Context, owner httpapi.StudioOwner, id string, _ int64, _ int) ([]httpapi.StudioMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.owned(owner, id) {
		return nil, httpapi.ErrStudioNotFound
	}
	return append([]httpapi.StudioMessage(nil), f.messages[id]...), nil
}

func (f *fakeStudioStore) BeginTurn(_ context.Context, owner httpapi.StudioOwner, id string, turn httpapi.StudioTurn) (httpapi.StudioConversation, []httpapi.StudioMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.turns++
	if f.failTurn != nil {
		return httpapi.StudioConversation{}, nil, f.failTurn
	}
	if !f.owned(owner, id) {
		return httpapi.StudioConversation{}, nil, httpapi.ErrStudioNotFound
	}
	history := f.messages[id]
	if turn.Regenerate {
		for len(history) > 0 && history[len(history)-1].Role != "user" {
			history = history[:len(history)-1]
		}
	} else {
		history = append(history, httpapi.StudioMessage{ID: fmt.Sprintf("msg_u%d", len(history)+1), Seq: int64(len(history) + 1), Role: "user", Content: turn.Content, Status: "complete"})
	}
	f.messages[id] = history
	c := f.conversations[id]
	c.Model = turn.Model
	f.conversations[id] = c
	return c, append([]httpapi.StudioMessage(nil), history...), nil
}

func (f *fakeStudioStore) AppendReply(_ context.Context, owner httpapi.StudioOwner, id string, reply httpapi.StudioReply) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.owned(owner, id) {
		return httpapi.ErrStudioNotFound
	}
	f.messages[id] = append(f.messages[id], httpapi.StudioMessage{
		ID: reply.ID, Seq: reply.ReplyTo + 1, Role: "assistant", Content: reply.Content, Reasoning: reply.Reasoning, Status: reply.Status,
		Model: reply.Model, PromptTokens: reply.PromptTokens, CompletionTokens: reply.CompletionTokens, ErrorCode: reply.ErrorCode,
	})
	f.replies <- reply
	return nil
}

// studioFrame is one Studio SSE frame as a client reads it.
// studioFrame 是客户端读到的一个 Studio SSE 帧。
type studioFrame struct {
	Event string
	Data  map[string]any
}

func studioDo(t *testing.T, method, url, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+goodSession)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	return resp, raw
}

// readFrames reads SSE frames until the stream ends, or until stop returns
// true for a frame.
//
// readFrames 读取 SSE 帧直到流结束，或直到 stop 对某一帧返回 true。
func readFrames(t *testing.T, resp *http.Response, stop func(studioFrame) bool) []studioFrame {
	t.Helper()
	var frames []studioFrame
	scanner := bufio.NewScanner(resp.Body)
	var current studioFrame
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			current.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &current.Data); err != nil {
				t.Fatalf("frame %q: %v", line, err)
			}
		case line == "":
			frames = append(frames, current)
			if stop != nil && stop(current) {
				return frames
			}
			current = studioFrame{}
		}
	}
	return frames
}

func events(frames []studioFrame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = f.Event
	}
	return out
}

func sendMessage(t *testing.T, ctx context.Context, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+goodSession)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// recordingChat streams events and records the chat request it served.
// recordingChat 流式发出 events，并记录它所服务的聊天请求。
func recordingChat(seen chan<- runtime.ChatRequest, events ...runtime.ChatEvent) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if in, err := tunnelwire.UnmarshalChatRequest(req.GetPayload()); err == nil {
			select {
			case seen <- in:
			default:
			}
		}
		return streamingEventsHandler(events...)(req, body, reply)
	}
}

func studioServer(t *testing.T, store *fakeStudioStore, sessions httpapi.SessionVerifier, handle gatewaytest.SlotHandler) string {
	t.Helper()
	srv, h := newServer(t, httpapi.Config{StudioSessions: sessions, StudioStore: store})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), handle)
	return srv.URL
}

// TestStudioSendStreamsAndSavesTheReply is the main path: the user message
// is written, the reply streams as Studio frames, the dispatched request
// carries the system prompt and the history, and the reply is saved
// complete with its reasoning and usage.
//
// TestStudioSendStreamsAndSavesTheReply 是主路径：写入用户消息，回复以 Studio 帧流式返回，
// 派发的请求带上系统提示词与历史，回复连同思考与用量以 complete 状态保存。
func TestStudioSendStreamsAndSavesTheReply(t *testing.T) {
	store := newFakeStudioStore()
	seen := make(chan runtime.ChatRequest, 1)
	url := studioServer(t, store, acceptingSessions(), recordingChat(seen,
		runtime.ChatEvent{ID: "c", Delta: runtime.ChatMessageDelta{Role: "assistant", ReasoningContent: "想一想"}},
		textDelta("你"), textDelta("好"),
		runtime.ChatEvent{ID: "c", FinishReason: "stop", Usage: &runtime.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}},
	))

	resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/conversations", `{"model":"qwen3:8b","system_prompt":"简洁回答"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, raw)
	}
	var created struct{ ID string }
	_ = json.Unmarshal(raw, &created)

	stream := sendMessage(t, context.Background(), url+"/studio/v1/conversations/"+created.ID+"/messages", `{"content":"打个招呼","model":"qwen3:8b"}`)
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK || stream.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("send: %d %q", stream.StatusCode, stream.Header.Get("Content-Type"))
	}
	frames := readFrames(t, stream, nil)
	want := []string{"message_start", "thinking_delta", "text_delta", "text_delta", "usage", "message_stop"}
	if got := events(frames); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("frames = %v, want %v", got, want)
	}
	if user := frames[0].Data["user_message"].(map[string]any); user["content"] != "打个招呼" || user["role"] != "user" {
		t.Errorf("message_start user_message = %v", user)
	}
	if stop := frames[len(frames)-1].Data; stop["status"] != "complete" || stop["message_id"] != frames[0].Data["message_id"] {
		t.Errorf("message_stop = %v", stop)
	}

	reply := <-store.replies
	if reply.Content != "你好" || reply.Reasoning != "想一想" || reply.Status != "complete" || reply.PromptTokens != 4 || reply.CompletionTokens != 2 || reply.ReplyTo != 1 {
		t.Errorf("saved reply = %+v", reply)
	}
	dispatched := <-seen
	if len(dispatched.Messages) != 2 || dispatched.Messages[0].Role != "system" || dispatched.Messages[0].Content != "简洁回答" || dispatched.Messages[1].Content != "打个招呼" {
		t.Errorf("dispatched messages = %+v, want the system prompt then the question", dispatched.Messages)
	}

	resp, raw = studioDo(t, http.MethodGet, url+"/studio/v1/conversations/"+created.ID+"/messages", "")
	var listed struct {
		Data []struct {
			Role, Content, Status string
			Usage                 *struct {
				PromptTokens int64 `json:"prompt_tokens"`
			}
		}
	}
	if err := json.Unmarshal(raw, &listed); err != nil || resp.StatusCode != http.StatusOK || len(listed.Data) != 2 || listed.Data[1].Usage == nil || listed.Data[1].Usage.PromptTokens != 4 {
		t.Errorf("messages = %d %s", resp.StatusCode, raw)
	}
}

// TestStudioSendRegenerateDropsTheLastReply sends the history without the
// reply being regenerated.
//
// TestStudioSendRegenerateDropsTheLastReply 派发的历史中不包含被重新生成的那条回复。
func TestStudioSendRegenerateDropsTheLastReply(t *testing.T) {
	store := newFakeStudioStore()
	seen := make(chan runtime.ChatRequest, 2)
	url := studioServer(t, store, acceptingSessions(), recordingChat(seen, textDelta("答")))
	c, _ := store.CreateConversation(context.Background(), httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_1"}, "", "qwen3:8b", "")

	for _, body := range []string{`{"content":"问","model":"qwen3:8b"}`, `{"regenerate":true,"model":"qwen3:8b"}`} {
		stream := sendMessage(t, context.Background(), url+"/studio/v1/conversations/"+c.ID+"/messages", body)
		readFrames(t, stream, nil)
		stream.Body.Close()
		<-store.replies
	}
	<-seen
	regenerated := <-seen
	if len(regenerated.Messages) != 1 || regenerated.Messages[0].Content != "问" {
		t.Errorf("regenerate dispatched %+v, want only the question", regenerated.Messages)
	}
}

// TestStudioSendRefusesBeforeDispatch covers the refusals that must not
// dispatch anything: a bad body, a model not offered, a turn that cannot be
// written, and another member's conversation.
//
// TestStudioSendRefusesBeforeDispatch 覆盖不得派发任何东西的拒绝：请求体不合法、模型不在
// 可用列表、轮次无法写入，以及他人的对话。
func TestStudioSendRefusesBeforeDispatch(t *testing.T) {
	dispatched := make(chan runtime.ChatRequest, 8)
	store := newFakeStudioStore()
	url := studioServer(t, store, acceptingSessions(), recordingChat(dispatched, textDelta("x")))
	mine, _ := store.CreateConversation(context.Background(), httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_1"}, "", "", "")
	theirs, _ := store.CreateConversation(context.Background(), httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_2"}, "", "", "")

	tests := []struct {
		name, id, body string
		failTurn       error
		status         int
		code           string
	}{
		{"empty content", mine.ID, `{"content":"  ","model":"qwen3:8b"}`, nil, 400, "invalid_request"},
		{"content and regenerate", mine.ID, `{"content":"a","regenerate":true,"model":"qwen3:8b"}`, nil, 400, "invalid_request"},
		{"unknown field", mine.ID, `{"content":"a","model":"qwen3:8b","temperature":1}`, nil, 400, "invalid_request"},
		{"model not offered", mine.ID, `{"content":"a","model":"gpt-9"}`, nil, 400, "model_unavailable"},
		{"malformed id", "not-a-conversation", `{"content":"a","model":"qwen3:8b"}`, nil, 404, "not_found"},
		{"another member's conversation", theirs.ID, `{"content":"a","model":"qwen3:8b"}`, nil, 404, "not_found"},
		{"storage unavailable", mine.ID, `{"content":"a","model":"qwen3:8b"}`, errors.New("control plane down"), 503, "storage_unavailable"},
		{"conversation full", mine.ID, `{"content":"a","model":"qwen3:8b"}`, httpapi.ErrStudioLimit, 409, "limit_reached"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store.mu.Lock()
			store.failTurn = tt.failTurn
			store.mu.Unlock()
			resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/conversations/"+tt.id+"/messages", tt.body)
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			_ = json.Unmarshal(raw, &body)
			if resp.StatusCode != tt.status || body.Error.Code != tt.code {
				t.Errorf("= %d %q, want %d %q", resp.StatusCode, body.Error.Code, tt.status, tt.code)
			}
		})
	}
	select {
	case req := <-dispatched:
		t.Errorf("a refused send dispatched %+v", req)
	default:
	}
}

// TestStudioSendSavesAnIncompleteReplyWhenTheClientLeaves: stopping is the
// client disconnecting; what had arrived is saved as incomplete.
//
// TestStudioSendSavesAnIncompleteReplyWhenTheClientLeaves：停止即客户端断开；已收到的部分
// 以 incomplete 保存。
func TestStudioSendSavesAnIncompleteReplyWhenTheClientLeaves(t *testing.T) {
	store := newFakeStudioStore()
	url := studioServer(t, store, acceptingSessions(), blockingStreamHandler(nil))
	c, _ := store.CreateConversation(context.Background(), httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_1"}, "", "", "")

	ctx, cancel := context.WithCancel(context.Background())
	stream := sendMessage(t, ctx, url+"/studio/v1/conversations/"+c.ID+"/messages", `{"content":"写长文","model":"qwen3:8b"}`)
	readFrames(t, stream, func(f studioFrame) bool { return f.Event == "text_delta" })
	cancel()
	stream.Body.Close()

	select {
	case reply := <-store.replies:
		if reply.Status != "incomplete" || !strings.HasPrefix(reply.Content, "chunk-0") {
			t.Errorf("saved reply = status %q content %.20q, want incomplete with what had arrived", reply.Status, reply.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply was saved after the client left")
	}
}

// TestStudioSendReportsAFailedStream: a stream that fails midway ends with
// an error frame and is saved as an error, without the upstream's message.
//
// TestStudioSendReportsAFailedStream：中途失败的流以 error 帧结束，并以 error 保存，不带
// 上游消息。
func TestStudioSendReportsAFailedStream(t *testing.T) {
	store := newFakeStudioStore()
	url := studioServer(t, store, acceptingSessions(), func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		payload, _ := tunnelwire.MarshalChatEvent(textDelta("半"))
		if err := reply(gatewaytest.DataFrame(payload)); err != nil {
			return err
		}
		return errors.New("backend exploded with a secret prompt")
	})
	c, _ := store.CreateConversation(context.Background(), httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_1"}, "", "", "")

	stream := sendMessage(t, context.Background(), url+"/studio/v1/conversations/"+c.ID+"/messages", `{"content":"问","model":"qwen3:8b"}`)
	defer stream.Body.Close()
	frames := readFrames(t, stream, nil)
	got := events(frames)
	if len(got) < 3 || got[len(got)-2] != "error" || got[len(got)-1] != "message_stop" {
		t.Fatalf("frames = %v, want an error frame before message_stop", got)
	}
	if msg, _ := frames[len(frames)-2].Data["message"].(string); strings.Contains(msg, "secret") {
		t.Errorf("error frame leaks the upstream message: %q", msg)
	}
	if reply := <-store.replies; reply.Status != "error" || reply.ErrorCode == "" || reply.Content != "半" {
		t.Errorf("saved reply = %+v", reply)
	}
}

// TestStudioConversationEndpoints covers create, get, rename, list and
// delete, scoped to the signed-in member, and that a member whose balance is
// exhausted can still manage conversations but not send.
//
// TestStudioConversationEndpoints 覆盖创建、读取、重命名、列表与删除，均限定在登录成员内；
// 以及余额耗尽的成员仍能管理对话、但不能发送。
func TestStudioConversationEndpoints(t *testing.T) {
	store := newFakeStudioStore()
	blocked := &fakeSessions{answer: func(string) (httpapi.Identity, error) {
		return httpapi.Identity{TenantID: "tnt_1", UserID: "usr_1", BillingBlocked: true}, nil
	}}
	url := studioServer(t, store, blocked, chatHandler)
	theirs, _ := store.CreateConversation(context.Background(), httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_2"}, "别人的", "", "")

	resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/conversations", "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create with an empty body: %d %s", resp.StatusCode, raw)
	}
	var c struct{ ID string }
	_ = json.Unmarshal(raw, &c)
	if resp, raw := studioDo(t, http.MethodPatch, url+"/studio/v1/conversations/"+c.ID, `{"title":"新名字"}`); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "新名字") {
		t.Errorf("rename: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := studioDo(t, http.MethodGet, url+"/studio/v1/conversations", ""); resp.StatusCode != http.StatusOK || strings.Contains(string(raw), theirs.ID) || !strings.Contains(string(raw), c.ID) {
		t.Errorf("list: %d %s, want only my conversation", resp.StatusCode, raw)
	}
	if resp, _ := studioDo(t, http.MethodGet, url+"/studio/v1/conversations/"+theirs.ID, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another member's conversation: %d, want 404", resp.StatusCode)
	}
	if resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/conversations/"+c.ID+"/messages", `{"content":"a","model":"qwen3:8b"}`); resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("send with no balance: %d %s, want 402", resp.StatusCode, raw)
	}
	if resp, _ := studioDo(t, http.MethodDelete, url+"/studio/v1/conversations/"+c.ID, ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if resp, _ := studioDo(t, http.MethodGet, url+"/studio/v1/conversations/"+c.ID, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("deleted conversation: %d, want 404", resp.StatusCode)
	}
}

// TestStudioConversationsAreUnmountedWithoutAStore keeps the conversation
// endpoints off a Gateway that has sessions but no storage.
//
// TestStudioConversationsAreUnmountedWithoutAStore：有会话校验但没有存储的 Gateway 不提供对话端点。
func TestStudioConversationsAreUnmountedWithoutAStore(t *testing.T) {
	srv, _ := newServer(t, httpapi.Config{StudioSessions: acceptingSessions()})
	if resp, _ := studioDo(t, http.MethodGet, srv.URL+"/studio/v1/conversations", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// fakeResponsesClient is a scripted httpapi.ResponsesPersistClient, an
// in-memory stand-in for the control plane's response_turns table — the
// same role fakePushClient plays for request-log pushes.
//
// fakeResponsesClient 是一个脚本化的 httpapi.ResponsesPersistClient，是控制面
// response_turns 表的内存替身——与 fakePushClient 对请求日志推送所扮演的角色
// 相同。
type fakeResponsesClient struct {
	mu    sync.Mutex
	turns map[string]storedResponseTurn
}

type storedResponseTurn struct {
	previousResponseID string
	messages           json.RawMessage
	model              string
}

func newFakeResponsesClient() *fakeResponsesClient {
	return &fakeResponsesClient{turns: map[string]storedResponseTurn{}}
}

func (c *fakeResponsesClient) key(tenantID, responseID string) string {
	return tenantID + "/" + responseID
}

func (c *fakeResponsesClient) CreateResponseTurn(_ context.Context, responseID, tenantID, previousResponseID, model string, messages json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns[c.key(tenantID, responseID)] = storedResponseTurn{
		previousResponseID: previousResponseID,
		messages:           append(json.RawMessage(nil), messages...),
		model:              model,
	}
	return nil
}

func (c *fakeResponsesClient) GetResponseTurn(_ context.Context, tenantID, responseID string) (string, json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	turn, ok := c.turns[c.key(tenantID, responseID)]
	if !ok {
		return "", nil, httpapi.ErrResponseTurnNotFound
	}
	return turn.previousResponseID, turn.messages, nil
}

// seed inserts a turn directly, bypassing CreateResponseTurn — the test
// equivalent of a turn a previous request already persisted.
//
// seed 直接插入一轮，绕过 CreateResponseTurn——测试场景下等价于一轮已被此前
// 某次请求持久化过的内容。
func (c *fakeResponsesClient) seed(tenantID, responseID, previousResponseID, messages string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns[c.key(tenantID, responseID)] = storedResponseTurn{previousResponseID: previousResponseID, messages: json.RawMessage(messages)}
}

func (c *fakeResponsesClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.turns)
}

func (c *fakeResponsesClient) get(tenantID, responseID string) (storedResponseTurn, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	turn, ok := c.turns[c.key(tenantID, responseID)]
	return turn, ok
}

// postResponsesAuthed posts to /v1/responses with a Bearer token, unlike
// postResponses which posts unauthenticated — store and previous_response_id
// need a real tenant identity to scope by, so every test in this file uses
// this instead.
//
// postResponsesAuthed 携带 Bearer token 向 /v1/responses 发起请求，与未鉴权的
// postResponses 不同——store 与 previous_response_id 需要一个真实的租户身份
// 才能限定范围，因此本文件中的每个测试都用这个而不是那个。
func postResponsesAuthed(t *testing.T, url, key, body string) (*http.Response, responseBody) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var out responseBody
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// TestResponsesStoreTruePersistsATurn covers the write half: a caller that
// opts into store:true gets its turn asynchronously written to the control
// plane, keyed by the response id the caller was handed back.
//
// TestResponsesStoreTruePersistsATurn 覆盖写入那一半：一个选择了 store:true
// 的调用方，其这一轮会被异步写入控制面，以调用方拿到的那个 response id 为键。
func TestResponsesStoreTruePersistsATurn(t *testing.T) {
	client := newFakeResponsesClient()
	srv, h := newServer(t, httpapi.Config{Verifier: acceptingVerifier(), ResponsesClient: client})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp, body := postResponsesAuthed(t, srv.URL, goodKey, `{"model":"qwen3:8b","input":"hi","store":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	gatewaytest.WaitFor(t, "CreateResponseTurn to be called for the new response", func() bool {
		return client.callCount() > 0
	})
	turn, ok := client.get("tnt_1", body.ID)
	if !ok {
		t.Fatalf("no turn persisted under tenant tnt_1, response id %s", body.ID)
	}
	var messages []struct {
		Role    string
		Content string
	}
	if err := json.Unmarshal(turn.messages, &messages); err != nil {
		t.Fatalf("unmarshal persisted messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[0].Content != "hi" ||
		messages[1].Role != "assistant" {
		t.Fatalf("persisted messages = %+v, want [user:hi, assistant:...]", messages)
	}
}

// TestResponsesStoreFalsePersistsNothing is the negative case: the client is
// configured, but a caller that never opts in gets nothing written — the
// default this feature must not silently change.
//
// TestResponsesStoreFalsePersistsNothing 是反面情形：客户端已配置，但一个从未
// 选择 store 的调用方不会有任何东西被写入——本功能不得默默改变这个默认行为。
func TestResponsesStoreFalsePersistsNothing(t *testing.T) {
	client := newFakeResponsesClient()
	srv, h := newServer(t, httpapi.Config{Verifier: acceptingVerifier(), ResponsesClient: client})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp, _ := postResponsesAuthed(t, srv.URL, goodKey, `{"model":"qwen3:8b","input":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if n := client.callCount(); n != 0 {
		t.Fatalf("callCount() = %d, want 0 (no store:true was asked for)", n)
	}
}

// TestResponsesPreviousResponseIDContinuesTheConversation covers the read
// half: a caller naming a stored previous_response_id gets its turn's
// messages prepended onto the new request before it reaches the backend —
// echoMessagesHandler's echo is how this test observes what actually
// crossed the wire.
//
// TestResponsesPreviousResponseIDContinuesTheConversation 覆盖读取那一半：
// 一个点名了已存储 previous_response_id 的调用方，其那一轮的消息会在请求
// 抵达后端之前被拼接到新请求的前面——echoMessagesHandler 的回显正是本测试
// 用来观察线上实际发生了什么的方式。
func TestResponsesPreviousResponseIDContinuesTheConversation(t *testing.T) {
	client := newFakeResponsesClient()
	client.seed("tnt_1", "resp_1", "", `[{"Role":"user","Content":"hi"},{"Role":"assistant","Content":"hello there"}]`)
	srv, h := newServer(t, httpapi.Config{Verifier: acceptingVerifier(), ResponsesClient: client})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp, body := postResponsesAuthed(t, srv.URL, goodKey, `{"model":"qwen3:8b","input":"how are you","previous_response_id":"resp_1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%+v", resp.StatusCode, body)
	}
	if len(body.Output) != 1 || len(body.Output[0].Content) != 1 {
		t.Fatalf("output = %+v, want one message with one content part", body.Output)
	}
	echoed := body.Output[0].Content[0].Text
	want := "user:hi\nassistant:hello there\nuser:how are you"
	if echoed != want {
		t.Errorf("backend saw %q, want %q (the stored prefix followed by this turn's own input)", echoed, want)
	}
}

// TestResponsesPreviousResponseIDUnknownIsRejected covers the client-error
// path: a previous_response_id the control plane has no record of must come
// back as a 400 the caller can act on, not a fabricated shorter history and
// not a 500 that looks like this Gateway's own fault.
//
// TestResponsesPreviousResponseIDUnknownIsRejected 覆盖客户端错误路径：一个
// 控制面没有记录的 previous_response_id，必须以一个调用方能够据以行动的 400
// 收场，而不是一段编造出的更短历史，也不是一个看起来像本 Gateway 自身故障
// 的 500。
func TestResponsesPreviousResponseIDUnknownIsRejected(t *testing.T) {
	client := newFakeResponsesClient()
	srv, h := newServer(t, httpapi.Config{Verifier: acceptingVerifier(), ResponsesClient: client})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp, _ := postResponsesAuthed(t, srv.URL, goodKey, `{"model":"qwen3:8b","input":"hi","previous_response_id":"resp_missing"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown previous_response_id", resp.StatusCode)
	}
}

// TestResponsesStoreRejectedWithoutATenantIdentity covers the case a
// configured control plane alone does not fix: an unauthenticated caller
// has no tenant to scope a stored conversation by, so store must still be
// refused by name rather than silently accepted into a tenant-less row.
//
// TestResponsesStoreRejectedWithoutATenantIdentity 覆盖仅靠配置了控制面无法
// 解决的情形：一个未鉴权的调用方没有可供限定存储会话范围的租户，因此 store
// 依然必须被指名拒绝，而不是被悄悄接受进一行没有租户的记录。
func TestResponsesStoreRejectedWithoutATenantIdentity(t *testing.T) {
	client := newFakeResponsesClient()
	srv, h := newServer(t, httpapi.Config{ResponsesClient: client}) // no Verifier configured
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp, _ := postResponses(t, srv.URL, `{"model":"qwen3:8b","input":"hi","store":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no tenant identity to store the conversation under)", resp.StatusCode)
	}
	if n := client.callCount(); n != 0 {
		t.Fatalf("callCount() = %d, want 0", n)
	}
}

// TestResponsesPreviousResponseIDChainDeeperThanTheBoundIsRejected covers
// the bounded-chain-walk discipline: a conversation with more stored turns
// than this Gateway will walk is refused with a clear error, not silently
// truncated into a shorter history the caller never asked for.
//
// TestResponsesPreviousResponseIDChainDeeperThanTheBoundIsRejected 覆盖
// 「链式遍历必须有界」的纪律：一段存储轮次多于本 Gateway 愿意遍历上限的
// 会话会被明确拒绝，而不是被默默截断成一段调用方从未要求过的更短历史。
func TestResponsesPreviousResponseIDChainDeeperThanTheBoundIsRejected(t *testing.T) {
	client := newFakeResponsesClient()
	// Seed a chain one turn deeper than this Gateway will walk (51), each
	// pointing to the previous one, ending at a root turn with no previous
	// id of its own.
	//
	// 种下一条比本 Gateway 愿意遍历的上限多一轮（51 轮）的链，每一轮指向
	// 上一轮，终止于一个自己没有 previous id 的根轮次。
	const depth = 51
	for i := 0; i < depth; i++ {
		id := "resp_" + strconv.Itoa(i)
		previous := ""
		if i > 0 {
			previous = "resp_" + strconv.Itoa(i-1)
		}
		client.seed("tnt_1", id, previous, `[{"Role":"user","Content":"hi"}]`)
	}
	srv, h := newServer(t, httpapi.Config{Verifier: acceptingVerifier(), ResponsesClient: client})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp, _ := postResponsesAuthed(t, srv.URL, goodKey,
		`{"model":"qwen3:8b","input":"one more","previous_response_id":"resp_`+strconv.Itoa(depth-1)+`"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (a chain past the bound is a server-side limit, not a bad request)", resp.StatusCode)
	}
}

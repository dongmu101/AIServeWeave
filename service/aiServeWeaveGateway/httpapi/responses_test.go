package httpapi_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// echoMessagesHandler answers Chat by echoing the conversation it was given,
// one "role:content" pair per line. That is how these tests assert what the
// Responses translation actually put on the wire — the shape of the request
// the backend saw is the whole point of this endpoint.
//
// echoMessagesHandler 用回显收到的对话来应答 Chat，每行一个「role:content」。这正是
// 这些测试断言 Responses 转换究竟往线上放了什么的方式——后端看到的请求长什么样，正是
// 这个端点的全部意义所在。
func echoMessagesHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	in, err := tunnelwire.UnmarshalChatRequest(req.GetPayload())
	if err != nil {
		return err
	}
	var lines []string
	for _, m := range in.Messages {
		lines = append(lines, m.Role+":"+m.Content)
	}
	payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
		ID:           "chat-1",
		Model:        in.Model,
		Message:      runtime.ChatMessage{Role: "assistant", Content: strings.Join(lines, "\n")},
		FinishReason: "stop",
		Usage:        runtime.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
	})
	if err != nil {
		return err
	}
	return reply(gatewaytest.DataFrame(payload))
}

type responseBody struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	CreatedAt int64  `json:"created_at"`
	Status    string `json:"status"`
	Model     string `json:"model"`
	Output    []struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Role    string `json:"role"`
		Status  string `json:"status"`
		Name    string `json:"name"`
		CallID  string `json:"call_id"`
		Args    string `json:"arguments"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func postResponses(t *testing.T, url, body string) (*http.Response, responseBody) {
	t.Helper()
	resp, err := http.Post(url+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var out responseBody
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestResponsesReturnsTheResponseShape(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp, body := postResponses(t, srv.URL, `{"model":"qwen3:8b","input":"hello"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body.Object != "response" {
		t.Errorf("object = %q, want response", body.Object)
	}
	if body.Status != "completed" {
		t.Errorf("status = %q, want completed", body.Status)
	}
	if !strings.HasPrefix(body.ID, "resp_") {
		t.Errorf("id = %q, want a resp_-prefixed id", body.ID)
	}
	if body.CreatedAt == 0 {
		t.Error("created_at is zero, want a unix timestamp")
	}
	if len(body.Output) != 1 || body.Output[0].Type != "message" || body.Output[0].Role != "assistant" {
		t.Fatalf("output = %+v, want one assistant message item", body.Output)
	}
	if len(body.Output[0].Content) != 1 || body.Output[0].Content[0].Type != "output_text" {
		t.Fatalf("content = %+v, want one output_text part", body.Output[0].Content)
	}
	// Responses names its token counts differently from Chat Completions, and
	// a client reading input_tokens must not find prompt_tokens instead.
	//
	// Responses 对 token 计数的命名与 Chat Completions 不同，读取 input_tokens 的
	// 客户端不能在那里找到 prompt_tokens。
	if body.Usage.InputTokens != 3 || body.Usage.OutputTokens != 5 || body.Usage.TotalTokens != 8 {
		t.Errorf("usage = %+v, want input=3 output=5 total=8", body.Usage)
	}
}

// TestResponsesTranslatesInput covers the translation this endpoint exists to
// do: every accepted input shape becomes the same canonical message list, so a
// backend that only speaks Chat Completions — Ollama, for one — serves a
// Responses request without knowing the API exists.
//
// TestResponsesTranslatesInput 覆盖本端点存在的意义所在的那次转换：每一种被接受的
// input 形式都变成同一份 canonical 消息列表，因此一个只会 Chat Completions 的后端
// ——比如 Ollama——在不知道这个 API 存在的情况下也能服务 Responses 请求。
func TestResponsesTranslatesInput(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a bare string becomes one user message",
			body: `{"model":"qwen3:8b","input":"hello"}`,
			want: "user:hello",
		},
		{
			name: "instructions become a leading system message",
			body: `{"model":"qwen3:8b","instructions":"be terse","input":"hello"}`,
			want: "system:be terse\nuser:hello",
		},
		{
			name: "an item array keeps its roles and order",
			body: `{"model":"qwen3:8b","input":[{"role":"user","content":"first"},{"role":"assistant","content":"second"},{"role":"user","content":"third"}]}`,
			want: "user:first\nassistant:second\nuser:third",
		},
		{
			name: "content parts are concatenated",
			body: `{"model":"qwen3:8b","input":[{"role":"user","content":[{"type":"input_text","text":"a "},{"type":"input_text","text":"b"}]}]}`,
			want: "user:a b",
		},
		{
			name: "an assistant item's output_text is accepted too",
			body: `{"model":"qwen3:8b","input":[{"role":"assistant","content":[{"type":"output_text","text":"prior"}]},{"role":"user","content":"next"}]}`,
			want: "assistant:prior\nuser:next",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, h := newServer(t, httpapi.Config{})
			connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

			resp, body := postResponses(t, srv.URL, tt.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if len(body.Output) != 1 || len(body.Output[0].Content) != 1 {
				t.Fatalf("output = %+v, want one text part", body.Output)
			}
			if got := body.Output[0].Content[0].Text; got != tt.want {
				t.Errorf("the backend saw:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

// TestResponsesRejectsUnsupportedFields is README's 不能静默丢弃参数 for this
// endpoint. A caller who asked for server-side conversation state and got a
// stateless answer would only find out by noticing the model had forgotten
// everything.
//
// TestResponsesRejectsUnsupportedFields 是 README「不能静默丢弃参数」在本端点上的
// 落实。一个要求服务端会话状态却拿到无状态答复的调用方，只能靠「发现模型什么都不记得」
// 来察觉这件事。
func TestResponsesRejectsUnsupportedFields(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantIn string
	}{
		{
			name:   "previous_response_id",
			body:   `{"model":"qwen3:8b","input":"hi","previous_response_id":"resp_1"}`,
			wantIn: "previous_response_id",
		},
		{
			name:   "store",
			body:   `{"model":"qwen3:8b","input":"hi","store":true}`,
			wantIn: "store",
		},
		{
			name:   "background",
			body:   `{"model":"qwen3:8b","input":"hi","background":true}`,
			wantIn: "background",
		},
		{
			name:   "a built-in tool this gateway does not run",
			body:   `{"model":"qwen3:8b","input":"hi","tools":[{"type":"web_search"}]}`,
			wantIn: "web_search",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, h := newServer(t, httpapi.Config{})
			connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

			resp, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decoding error: %v", err)
			}
			if !strings.Contains(body.Error.Message, tt.wantIn) {
				t.Errorf("error = %q, want it to name %q", body.Error.Message, tt.wantIn)
			}
		})
	}
}

func TestResponsesRejectsAMissingInput(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	resp, _ := postResponses(t, srv.URL, `{"model":"qwen3:8b"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// truncatingHandler answers with a length-limited completion, the case that
// distinguishes "completed" from "incomplete".
//
// truncatingHandler 用一次因长度受限而截断的补全作答，这正是区分 completed 与
// incomplete 的那种情况。
func truncatingHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
		ID:           "chat-1",
		Message:      runtime.ChatMessage{Role: "assistant", Content: "cut off"},
		FinishReason: "length",
	})
	if err != nil {
		return err
	}
	return reply(gatewaytest.DataFrame(payload))
}

func TestResponsesReportsATruncatedAnswerAsIncomplete(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), truncatingHandler)

	_, body := postResponses(t, srv.URL, `{"model":"qwen3:8b","input":"hi","max_output_tokens":2}`)
	if body.Status != "incomplete" {
		t.Errorf("status = %q, want incomplete", body.Status)
	}
	if body.IncompleteDetails == nil || body.IncompleteDetails.Reason != "max_output_tokens" {
		t.Errorf("incomplete_details = %+v, want reason max_output_tokens", body.IncompleteDetails)
	}
}

// toolCallingHandler answers with a tool call rather than text.
//
// toolCallingHandler 用一次工具调用而不是文本作答。
func toolCallingHandler(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	payload, err := tunnelwire.MarshalChatResponse(runtime.ChatResponse{
		ID: "chat-1",
		Message: runtime.ChatMessage{Role: "assistant", ToolCalls: []runtime.ToolCall{{
			ID: "call_abc", Type: "function",
			Function: runtime.FunctionCall{Name: "get_weather", Arguments: `{"city":"Beijing"}`},
		}}},
		FinishReason: "tool_calls",
	})
	if err != nil {
		return err
	}
	return reply(gatewaytest.DataFrame(payload))
}

func TestResponsesRendersAToolCallAsAFunctionCallItem(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), toolCallingHandler)

	_, body := postResponses(t, srv.URL,
		`{"model":"qwen3:8b","input":"weather?","tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}`)

	if len(body.Output) != 1 {
		t.Fatalf("output = %+v, want one item", body.Output)
	}
	item := body.Output[0]
	if item.Type != "function_call" {
		t.Fatalf("output[0].type = %q, want function_call", item.Type)
	}
	if item.Name != "get_weather" || item.CallID != "call_abc" || item.Args != `{"city":"Beijing"}` {
		t.Errorf("function call = %+v, want get_weather/call_abc with the reported arguments", item)
	}
}

// TestResponsesStreamEmitsTheEventSequence pins the order a Responses client
// depends on. An SDK that builds its state machine on these events breaks if
// output_text.delta arrives before the item it belongs to was announced.
//
// TestResponsesStreamEmitsTheEventSequence 钉住 Responses 客户端所依赖的事件顺序。
// 一个基于这些事件构建状态机的 SDK，若在所属项目尚未被宣告之前就收到
// output_text.delta，就会出错。
func TestResponsesStreamEmitsTheEventSequence(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp, err := http.Post(srv.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"qwen3:8b","input":"hi","stream":true}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	type frame struct {
		Type           string `json:"type"`
		SequenceNumber int    `json:"sequence_number"`
		Delta          string `json:"delta"`
		Text           string `json:"text"`
	}
	var frames []frame
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if strings.HasPrefix(line, "data: ") {
			var f frame
			if uerr := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &f); uerr != nil {
				t.Fatalf("decoding SSE data: %v", uerr)
			}
			frames = append(frames, f)
		}
		if err != nil {
			break
		}
	}

	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(frames) != len(want) {
		var got []string
		for _, f := range frames {
			got = append(got, f.Type)
		}
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i, w := range want {
		if frames[i].Type != w {
			t.Errorf("event %d = %q, want %q", i, frames[i].Type, w)
		}
	}
	// sequence_number is what lets a client detect a dropped frame; it must
	// be strictly increasing across the whole stream.
	//
	// sequence_number 正是客户端用来发现丢帧的东西；它必须在整条流上严格递增。
	for i := 1; i < len(frames); i++ {
		if frames[i].SequenceNumber <= frames[i-1].SequenceNumber {
			t.Errorf("sequence_number went %d then %d at event %d, want strictly increasing",
				frames[i-1].SequenceNumber, frames[i].SequenceNumber, i)
		}
	}
	if frames[4].Delta != "Hel" || frames[5].Delta != "lo" {
		t.Errorf("deltas = %q then %q, want Hel then lo", frames[4].Delta, frames[5].Delta)
	}
	if frames[6].Text != "Hello" {
		t.Errorf("output_text.done text = %q, want the accumulated Hello", frames[6].Text)
	}
}

func TestResponsesEndpointLabelIsItsOwn(t *testing.T) {
	mx := metricstest.New()
	srv, h := newServer(t, httpapi.Config{Metrics: mx})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), echoMessagesHandler)

	postResponses(t, srv.URL, `{"model":"qwen3:8b","input":"hi"}`)

	seen := false
	for _, s := range mx.All() {
		if s.Labels[httpapi.LabelEndpoint] == httpapi.EndpointResponses {
			seen = true
		}
	}
	if !seen {
		t.Errorf("no metric was recorded under endpoint=%q", httpapi.EndpointResponses)
	}
}

// TestResponsesStreamReportsAMidStreamFailure covers the stream that dies
// after its header is out: the SSE header is already written, so the failure
// cannot be a status code and has to arrive as an event the client's state
// machine understands.
//
// TestResponsesStreamReportsAMidStreamFailure 覆盖「响应头已发出后才断掉」的流：
// SSE 响应头已经写出，因此失败无法再表现为状态码，只能以一个客户端状态机认得的事件
// 抵达。
func TestResponsesStreamReportsAMidStreamFailure(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"),
		func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			payload, err := tunnelwire.MarshalChatEvent(runtime.ChatEvent{
				ID: "chat-1", Delta: runtime.ChatMessageDelta{Role: "assistant", Content: "partial"},
			})
			if err != nil {
				return err
			}
			if err := reply(gatewaytest.DataFrame(payload)); err != nil {
				return err
			}
			return &gatewaytest.WireError{Code: "upstream_error", Message: "backend died", Retryable: true}
		})

	resp, err := http.Post(srv.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"qwen3:8b","input":"hi","stream":true}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the header is out before the failure", resp.StatusCode)
	}

	var types []string
	reader := bufio.NewReader(resp.Body)
	for {
		line, rerr := reader.ReadString('\n')
		if strings.HasPrefix(line, "data: ") {
			var f struct {
				Type     string `json:"type"`
				Response struct {
					Status string `json:"status"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &f) == nil {
				types = append(types, f.Type)
				if f.Type == "response.failed" && f.Response.Status != "failed" {
					t.Errorf("response.failed carried status %q, want failed", f.Response.Status)
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	if len(types) == 0 || types[len(types)-1] != "response.failed" {
		t.Errorf("events = %v, want the stream to end with response.failed", types)
	}
}

// TestResponsesOmitsUsageTheBackendDidNotReport keeps a zero from being read
// as a measurement. "This cost nothing" and "nobody said what it cost" are
// different claims, and the first one is the kind of number that ends up on a
// cost dashboard.
//
// TestResponsesOmitsUsageTheBackendDidNotReport 防止零被当成一次度量。「这次不花钱」
// 与「没人说过它花了多少」是两个不同的断言，而前者正是那种会出现在成本看板上的数字。
func TestResponsesOmitsUsageTheBackendDidNotReport(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), truncatingHandler)

	resp, err := http.Post(srv.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"qwen3:8b","input":"hi"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if _, present := raw["usage"]; present {
		t.Errorf("usage is present as %s, want it omitted when the backend reported none", raw["usage"])
	}
}

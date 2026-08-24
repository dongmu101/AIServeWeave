package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// sseEvent is one parsed SSE frame: its event name and its decoded data.
//
// sseEvent 是解析出的一个 SSE 帧：事件名与解码后的 data。
type sseEvent struct {
	Name string
	Data struct {
		JobID      string          `json:"job_id"`
		Type       string          `json:"type"`
		Node       string          `json:"node"`
		Status     string          `json:"status"`
		Data       json.RawMessage `json:"data"`
		ReceivedAt string          `json:"received_at"`
	}
}

// readSSE reads frames until the stream ends, so a test asserts the whole
// sequence rather than a prefix of it.
//
// readSSE 一直读到流结束，好让测试断言整个序列而不是它的前缀。
func readSSE(t *testing.T, body *bufio.Reader) []sseEvent {
	t.Helper()
	var out []sseEvent
	var current sseEvent
	for {
		line, err := body.ReadString('\n')
		if line == "" && err != nil {
			return out
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			current.Name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			if err := json.Unmarshal([]byte(payload), &current.Data); err != nil {
				t.Fatalf("decoding SSE data %q: %v", payload, err)
			}
		case line == "":
			if current.Name != "" {
				out = append(out, current)
				current = sseEvent{}
			}
		}
		if err != nil {
			return out
		}
	}
}

// eventfulWorkflowHandler answers Submit, Status and Subscribe: the
// subscription delivers a progress event and then a terminal one.
//
// eventfulWorkflowHandler 应答 Submit、Status 与 Subscribe：订阅先送一个进度事件，
// 再送一个终态事件。
func eventfulWorkflowHandler(final runtime.WorkflowEventType) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		switch req.GetOperation() {
		case tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT:
			payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: "prompt-1"})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_WORKFLOW_STATUS:
			payload, err := tunnelwire.MarshalWorkflowStatus(runtime.WorkflowStatus{State: runtime.WorkflowRunning})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		case tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE:
			events := []runtime.WorkflowEvent{
				{Type: runtime.WorkflowEventProgress, RunID: "prompt-1", NodeID: "3", Raw: json.RawMessage(`{"value":5,"max":20}`)},
				{Type: final, RunID: "prompt-1"},
			}
			for _, ev := range events {
				payload, err := tunnelwire.MarshalWorkflowEvent(ev)
				if err != nil {
					return err
				}
				if err := reply(gatewaytest.DataFrame(payload)); err != nil {
					return err
				}
			}
			return nil
		}
		return errors.New("unsupported operation")
	}
}

// openEvents submits a job and opens its event stream.
//
// openEvents 提交一个 job 并打开它的事件流。
func openEvents(t *testing.T, url, jobID string) (*http.Response, []sseEvent) {
	t.Helper()
	resp, err := http.Get(url + "/v1/jobs/" + jobID + "/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	return resp, readSSE(t, bufio.NewReader(resp.Body))
}

func TestJobEventsForwardsEventsAndEndsOnATerminalOne(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), eventfulWorkflowHandler(runtime.WorkflowEventSucceeded))

	_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	resp, events := openEvents(t, srv.URL, job.JobID)

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if len(events) != 2 {
		t.Fatalf("received %d events, want 2: %+v", len(events), events)
	}
	if events[0].Name != "progress" || events[0].Data.Type != "progress" {
		t.Errorf("first event = %q/%q, want progress", events[0].Name, events[0].Data.Type)
	}
	if events[0].Data.Node != "3" {
		t.Errorf("first event node = %q, want 3", events[0].Data.Node)
	}
	// The backend's own payload is what carries the progress numbers; an
	// event stream that dropped it would report that something is happening
	// without ever saying how far along it is.
	//
	// 后端自己的载荷才带着进度数字；把它丢掉的事件流只会报告「有事在发生」，却始终
	// 说不出进行到了哪一步。
	if !strings.Contains(string(events[0].Data.Data), `"value":5`) {
		t.Errorf("first event data = %s, want it to carry the backend payload", events[0].Data.Data)
	}
	if events[1].Name != "succeeded" {
		t.Errorf("last event = %q, want succeeded", events[1].Name)
	}
	// Every frame names the job by this Gateway's own id, never by the
	// backend's prompt_id.
	//
	// 每一帧都用本 Gateway 自己的 id 指称该 job，绝不用后端的 prompt_id。
	for i, ev := range events {
		if ev.Data.JobID != job.JobID {
			t.Errorf("event %d job_id = %q, want %q", i, ev.Data.JobID, job.JobID)
		}
		if strings.Contains(ev.Data.JobID, "prompt-1") {
			t.Errorf("event %d job_id = %q: the backend's own id reached the caller", i, ev.Data.JobID)
		}
	}
}

// TestJobEventsRecordsTheTerminalState asserts a run that finished on the
// event stream is not asked about again: the status endpoint answers from the
// store afterwards, which is what makes the stream authoritative rather than
// merely decorative.
//
// TestJobEventsRecordsTheTerminalState 断言在事件流上结束的运行不会再被追问：此后
// 状态端点直接由存储作答，这正是这条流成为权威而非装饰的原因。
func TestJobEventsRecordsTheTerminalState(t *testing.T) {
	tests := []struct {
		name       string
		final      runtime.WorkflowEventType
		wantStatus string
	}{
		{name: "succeeded", final: runtime.WorkflowEventSucceeded, wantStatus: "succeeded"},
		{name: "failed", final: runtime.WorkflowEventFailed, wantStatus: "failed"},
		{name: "cancelled", final: runtime.WorkflowEventCancelled, wantStatus: "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
			connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), eventfulWorkflowHandler(tt.final))

			_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
			openEvents(t, srv.URL, job.JobID)

			// The node's Status handler always answers "running"; if the
			// stored terminal state were not honoured, this would say so.
			//
			// 节点的 Status 处理器永远答 running；若已存的终态没有被采信，这里就会
			// 暴露出来。
			if got := readJob(t, srv.URL, job.JobID); got.Status != tt.wantStatus {
				t.Errorf("status after the stream ended = %q, want %q", got.Status, tt.wantStatus)
			}
		})
	}
}

// TestJobEventsOnAFinishedJobDoesNotSubscribe covers the case where the run
// ended before anyone asked to watch it: there is nothing left to subscribe
// to, so the final state is delivered as one frame instead of opening a
// backend subscription that would have nothing to say.
//
// TestJobEventsOnAFinishedJobDoesNotSubscribe 覆盖「还没人来看，运行就已结束」这种
// 情况：没有可订阅的东西了，因此把终态作为一帧直接送出，而不是去开一条无话可说的
// 后端订阅。
func TestJobEventsOnAFinishedJobDoesNotSubscribe(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t)})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), eventfulWorkflowHandler(runtime.WorkflowEventSucceeded))

	_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	openEvents(t, srv.URL, job.JobID) // drives the job to succeeded

	_, events := openEvents(t, srv.URL, job.JobID)
	if len(events) != 1 {
		t.Fatalf("received %d events, want exactly the final state: %+v", len(events), events)
	}
	if events[0].Name != "succeeded" || events[0].Data.Status != "succeeded" {
		t.Errorf("event = %q/%q, want succeeded", events[0].Name, events[0].Data.Status)
	}
}

func TestJobEventsRejects(t *testing.T) {
	tests := []struct {
		name  string
		jobID string
	}{
		{name: "unknown job", jobID: "job_nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newServer(t, httpapi.Config{Workflows: templates(t)})
			resp, err := http.Get(srv.URL + "/v1/jobs/" + tt.jobID + "/events")
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", resp.StatusCode)
			}
		})
	}
}

// TestJobEventsIsScopedToItsTenant repeats the status endpoint's isolation
// for the stream: watching someone else's generation in real time is the same
// disclosure as reading its status, only continuous.
//
// TestJobEventsIsScopedToItsTenant 把状态端点的隔离在流上重复一遍：实时旁观别人的
// 生成，与读取它的状态是同一种泄露，只不过是持续的。
func TestJobEventsIsScopedToItsTenant(t *testing.T) {
	verifier := &fakeVerifier{answer: func(key string) (httpapi.Identity, error) {
		switch key {
		case "key-a":
			return httpapi.Identity{TenantID: "tnt_a", KeyID: "k_a"}, nil
		case "key-b":
			return httpapi.Identity{TenantID: "tnt_b", KeyID: "k_b"}, nil
		}
		return httpapi.Identity{}, httpapi.ErrKeyRejected
	}}
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), Verifier: verifier})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), eventfulWorkflowHandler(runtime.WorkflowEventSucceeded))

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/workflows/text-to-image/runs",
		strings.NewReader(`{"inputs":{"prompt":"a red fox"}}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer key-a")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	var job jobBody
	_ = json.NewDecoder(resp.Body).Decode(&job)
	resp.Body.Close()

	req, err = http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+job.JobID+"/events", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer key-b")
	other, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("status for another tenant = %d, want 404", other.StatusCode)
	}
}

// TestJobEventsEndpointLabelIsItsOwn keeps an SSE stream's duration out of the
// status endpoint's histogram. A watch lasting the length of a generation and
// a status call lasting milliseconds are not the same measurement, and
// averaging them together describes neither.
//
// TestJobEventsEndpointLabelIsItsOwn 让 SSE 流的时长不进状态端点的直方图。一次持续
// 整个生成过程的旁观，与一次毫秒级的状态查询不是同一种测量，把它们平均到一起，哪一个
// 都描述不了。
func TestJobEventsEndpointLabelIsItsOwn(t *testing.T) {
	mx := metricstest.New()
	srv, h := newServer(t, httpapi.Config{Workflows: templates(t), Metrics: mx})
	connectNode(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), eventfulWorkflowHandler(runtime.WorkflowEventSucceeded))

	_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)
	openEvents(t, srv.URL, job.JobID)

	seen := make(map[string]bool)
	for _, s := range mx.All() {
		if value, ok := s.Labels[httpapi.LabelEndpoint]; ok {
			seen[value] = true
			if strings.Contains(value, job.JobID) {
				t.Errorf("metric %s carries endpoint=%q: a job id reached a label", s.Name, value)
			}
		}
	}
	if !seen[httpapi.EndpointJobEvents] {
		t.Errorf("no metric was recorded under endpoint=%q; seen: %v", httpapi.EndpointJobEvents, seen)
	}
}

// blockingSubscribeHandler answers Submit, then delivers one event on
// Subscribe, signals started, and goes on replying — which blocks, since
// gatewaytest's Agent-to-server frame channel is unbuffered and the front
// door only reads one event per flush. That is exactly the state a watcher
// walking away has to be able to unwind.
//
// blockingSubscribeHandler 先应答 Submit，然后在 Subscribe 上送出一个事件、发出
// started 信号，并继续回复——于是阻塞住，因为 gatewaytest 的 Agent 到服务端帧通道
// 无缓冲，而前门每次 flush 只读一个事件。旁观者一走了之时，要能解开的正是这个状态。
func blockingSubscribeHandler(started chan<- struct{}) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if req.GetOperation() == tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT {
			payload, err := tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{ID: "prompt-1"})
			if err != nil {
				return err
			}
			return reply(gatewaytest.DataFrame(payload))
		}
		for i := 0; i < 1000; i++ {
			payload, err := tunnelwire.MarshalWorkflowEvent(runtime.WorkflowEvent{
				Type:  runtime.WorkflowEventProgress,
				RunID: "prompt-1",
			})
			if err != nil {
				return err
			}
			if err := reply(gatewaytest.DataFrame(payload)); err != nil {
				return err
			}
			if i == 0 && started != nil {
				close(started)
				started = nil
			}
		}
		return nil
	}
}

// TestJobEventsStopsOnClientDisconnect is README's "任何一跳都不得无界缓冲" applied
// to a watch that outlives its watcher. A workflow stream can idle for the
// length of a generation, so a handler goroutine that only unwinds when the
// backend finishes would be a leak measured in minutes, not milliseconds.
//
// The same structural caveat as the chat version applies: the scripted Agent
// unwinds only at harness teardown, and what is verified here is that the
// front door's own handler goroutine exits once the client is gone —
// httptest.Server.Close blocking on exactly that goroutine is the proof.
//
// TestJobEventsStopsOnClientDisconnect 是 README「任何一跳都不得无界缓冲」在「旁观者
// 走了而旁观还在」这件事上的落实。工作流的流可以空闲整个生成过程之久，因此一个只在
// 后端结束时才解开的处理器协程，泄漏的量级是分钟而不是毫秒。
//
// 与聊天版同样的结构性说明依然成立：脚本化的 Agent 只在测试框架拆除时才解开，这里验证
// 的是前门自己的处理器协程在客户端离开后确实退出——httptest.Server.Close 恰好阻塞在
// 那个协程上，就是证明。
func TestJobEventsStopsOnClientDisconnect(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	srv := httptest.NewServer(httpapi.New(
		scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock}),
		httpapi.Config{Workflows: templates(t), Logger: slog.New(slog.DiscardHandler)},
	))

	started := make(chan struct{})
	connectNodeWithHandler(t, h, "node-comfy", "comfy-1", workflowSnapshot("comfy-1"), blockingSubscribeHandler(started))

	_, job := postRun(t, srv.URL, "text-to-image", `{"inputs":{"prompt":"a red fox"}}`)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/jobs/"+job.JobID+"/events", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("reading the first SSE line: %v", err)
	}

	<-started
	cancel()
	resp.Body.Close()

	closed := make(chan struct{})
	go func() { srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(gatewaytest.Timeout):
		t.Fatal("the httptest server did not shut down; the event-stream handler likely did not exit after the client disconnected")
	}
}

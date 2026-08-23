package tunnel_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel/internal/tunneltest"
)

// The dispatcher tests exercise tunnel.Dispatcher directly rather than
// through a slot: the slot's frame loop already has its own tests, and going
// straight at the Handler keeps each assertion about one decision — the
// allowlist, the deadline, the quota, the payload contract.

const (
	testRuntimeID = "ollama-local"
	testRunID     = "run-7"
)

// recordingSink collects what a dispatched request wrote.
type recordingSink struct {
	mu          sync.Mutex
	contentType string
	size        int64
	headers     int
	chunks      [][]byte
	failAfter   int // when > 0, Data fails once this many chunks have been written
}

func (s *recordingSink) Headers(contentType string, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headers++
	s.contentType, s.size = contentType, size
	return nil
}

func (s *recordingSink) Data(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAfter > 0 && len(s.chunks) >= s.failAfter {
		return errors.New("sink: the slot's stream is gone")
	}
	s.chunks = append(s.chunks, append([]byte(nil), payload...))
	return nil
}

func (s *recordingSink) payloads() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.chunks...)
}

// dispatchFixture is one Dispatcher over a fake Manager and a fake clock.
type dispatchFixture struct {
	t     *testing.T
	mgr   *tunneltest.Manager
	clock *tunneltest.Clock
	disp  *tunnel.Dispatcher
}

func newDispatchFixture(t *testing.T, mutate func(*tunnel.DispatchConfig)) *dispatchFixture {
	t.Helper()

	f := &dispatchFixture{
		t:     t,
		mgr:   tunneltest.NewManager(),
		clock: tunneltest.NewClock(tunnelNow),
	}
	cfg := tunnel.DispatchConfig{
		Manager:         f.mgr,
		AllowedRuntimes: []string{testRuntimeID},
		Clock:           f.clock,
		Logger:          slog.New(slog.DiscardHandler),
	}
	if mutate != nil {
		mutate(&cfg)
	}

	disp, err := tunnel.NewDispatcher(cfg)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	f.disp = disp
	return f
}

// inference installs a scriptable InferenceRuntime under testRuntimeID.
func (f *dispatchFixture) inference(maxConcurrent int) *tunneltest.InferenceRuntime {
	rt := &tunneltest.InferenceRuntime{BaseRuntime: tunneltest.BaseRuntime{Desc: runtime.Descriptor{
		ID:            testRuntimeID,
		Kind:          runtime.KindOllama,
		BaseURL:       "http://127.0.0.1:11434",
		MaxConcurrent: maxConcurrent,
	}}}
	f.mgr.SetRuntime(testRuntimeID, rt)
	return rt
}

// workflow installs a scriptable WorkflowRuntime under testRuntimeID.
func (f *dispatchFixture) workflow(maxConcurrent int) *tunneltest.WorkflowRuntime {
	rt := &tunneltest.WorkflowRuntime{BaseRuntime: tunneltest.BaseRuntime{Desc: runtime.Descriptor{
		ID:            testRuntimeID,
		Kind:          runtime.KindComfyUI,
		BaseURL:       "http://127.0.0.1:8188",
		MaxConcurrent: maxConcurrent,
	}}}
	f.mgr.SetRuntime(testRuntimeID, rt)
	return rt
}

// dispatch runs one request to completion and returns what it wrote.
func (f *dispatchFixture) dispatch(op tunnelv1.Operation, payload []byte, mutate func(*tunnel.Request)) (*recordingSink, error) {
	f.t.Helper()
	return f.dispatchCtx(context.Background(), op, payload, mutate)
}

func (f *dispatchFixture) dispatchCtx(ctx context.Context, op tunnelv1.Operation, payload []byte, mutate func(*tunnel.Request)) (*recordingSink, error) {
	f.t.Helper()

	body := make(chan []byte)
	close(body)
	req := &tunnel.Request{
		ID:     "req-1",
		SlotID: "slot-1",
		Class:  tunnelv1.SlotClass_SLOT_CLASS_INFERENCE,
		Headers: &tunnelv1.RequestHeaders{
			RuntimeId: testRuntimeID,
			Operation: op,
			Payload:   payload,
		},
		Body: body,
	}
	if mutate != nil {
		mutate(req)
	}

	sink := &recordingSink{}
	return sink, f.disp.Handle(ctx, req, sink)
}

// wantCode fails unless err is a RuntimeError with the given code.
func wantCode(t *testing.T, err error, code runtime.ErrorCode) *runtime.RuntimeError {
	t.Helper()
	if err == nil {
		t.Fatalf("dispatch succeeded, want error %s", code)
	}
	var re *runtime.RuntimeError
	if !errors.As(err, &re) {
		t.Fatalf("dispatch returned %T (%v), want a *runtime.RuntimeError", err, err)
	}
	if re.Code != code {
		t.Fatalf("error code = %s, want %s (message %q)", re.Code, code, re.Message)
	}
	return re
}

// mustMarshal encodes v with one of convert.go's marshallers. It takes the
// marshaller rather than its result because Go forbids spreading a
// multi-valued call across a longer argument list.
func mustMarshal[T any](t *testing.T, marshal func(T) ([]byte, error), v T) []byte {
	t.Helper()
	b, err := marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// -----------------------------------------------------------------------
// Gating
// -----------------------------------------------------------------------

func TestDispatchRefusesARuntimeOutsideTheAllowlist(t *testing.T) {
	f := newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) {
		cfg.AllowedRuntimes = []string{"some-other-runtime"}
	})

	// The Manager holds the instance: only the allowlist stands between the
	// replica and it, which is exactly the case the last line of defence is
	// for.
	called := false
	rt := f.inference(0)
	rt.ListModelsFunc = func(context.Context) ([]runtime.Model, error) {
		called = true
		return nil, nil
	}

	_, err := f.dispatch(tunnelv1.Operation_OPERATION_LIST_MODELS, nil, nil)
	wantCode(t, err, runtime.ErrorInvalidConfig)
	if called {
		t.Error("a runtime outside the allowlist was called anyway")
	}
}

func TestDispatchRefusesAnUnknownRuntime(t *testing.T) {
	f := newDispatchFixture(t, nil)

	_, err := f.dispatch(tunnelv1.Operation_OPERATION_LIST_MODELS, nil, nil)
	re := wantCode(t, err, runtime.ErrorInvalidConfig)
	if !errors.Is(re, runtime.ErrRuntimeNotFound) {
		t.Errorf("error does not wrap ErrRuntimeNotFound: %v", re)
	}
}

func TestDispatchRefusesAnEmptyRuntimeID(t *testing.T) {
	f := newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) {
		// An empty allowlist means "no local narrowing", which must still not
		// mean "any id at all, including none".
		cfg.AllowedRuntimes = nil
	})
	f.inference(0)

	_, err := f.dispatch(tunnelv1.Operation_OPERATION_LIST_MODELS, nil, func(req *tunnel.Request) {
		req.Headers.RuntimeId = ""
	})
	wantCode(t, err, runtime.ErrorInvalidConfig)
}

func TestDispatchRejectsAnUnknownOperation(t *testing.T) {
	f := newDispatchFixture(t, nil)
	f.inference(0)

	_, err := f.dispatch(tunnelv1.Operation_OPERATION_UNSPECIFIED, nil, nil)
	wantCode(t, err, runtime.ErrorProtocol)
}

func TestDispatchReportsACapabilityGap(t *testing.T) {
	tests := []struct {
		name      string
		workflow  bool // install a WorkflowRuntime instead of an InferenceRuntime
		operation tunnelv1.Operation
	}{
		{name: "chat on a workflow-only backend", workflow: true, operation: tunnelv1.Operation_OPERATION_CHAT},
		{name: "list models on a workflow-only backend", workflow: true, operation: tunnelv1.Operation_OPERATION_LIST_MODELS},
		{name: "workflow submit on an inference-only backend", operation: tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT},
		{name: "artifact open on an inference-only backend", operation: tunnelv1.Operation_OPERATION_ARTIFACT_OPEN},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDispatchFixture(t, nil)
			if tt.workflow {
				f.workflow(0)
			} else {
				f.inference(0)
			}

			_, err := f.dispatch(tt.operation, nil, nil)
			re := wantCode(t, err, runtime.ErrorCapability)
			if !errors.Is(re, runtime.ErrCapabilityUnsupported) {
				t.Errorf("error does not wrap ErrCapabilityUnsupported: %v", re)
			}
			// The gap must survive the wire: the replica branches on it to
			// pick a different node.
			if pb := tunnelwire.ErrorToProto(err); pb.GetCode() != string(runtime.ErrorCapability) {
				t.Errorf("TunnelError code = %q, want %q", pb.GetCode(), runtime.ErrorCapability)
			}
		})
	}
}

// -----------------------------------------------------------------------
// The nine operations
// -----------------------------------------------------------------------

func TestDispatchServesEveryOperation(t *testing.T) {
	chatReq := runtime.ChatRequest{Model: "llama3", Messages: []runtime.ChatMessage{{Role: "user", Content: "hi"}}}

	tests := []struct {
		name      string
		operation tunnelv1.Operation
		payload   func(t *testing.T) []byte
		install   func(t *testing.T, f *dispatchFixture)
		body      []string
		wantHead  bool
		verify    func(t *testing.T, sink *recordingSink)
	}{
		{
			name:      "list models",
			operation: tunnelv1.Operation_OPERATION_LIST_MODELS,
			install: func(t *testing.T, f *dispatchFixture) {
				f.inference(0).ListModelsFunc = func(context.Context) ([]runtime.Model, error) {
					return []runtime.Model{{ID: "llama3"}, {ID: "qwen3"}}, nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				chunks := sink.payloads()
				if len(chunks) != 1 {
					t.Fatalf("chunks = %d, want 1", len(chunks))
				}
				models, err := tunnelwire.UnmarshalModels(chunks[0])
				if err != nil {
					t.Fatalf("tunnelwire.UnmarshalModels: %v", err)
				}
				if len(models) != 2 || models[0].ID != "llama3" || models[1].ID != "qwen3" {
					t.Errorf("models = %+v, want llama3 and qwen3", models)
				}
			},
		},
		{
			name:      "chat",
			operation: tunnelv1.Operation_OPERATION_CHAT,
			payload: func(t *testing.T) []byte {
				return mustMarshal(t, tunnelwire.MarshalChatRequest, chatReq)
			},
			install: func(t *testing.T, f *dispatchFixture) {
				f.inference(0).ChatFunc = func(_ context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
					if req.Model != "llama3" {
						t.Errorf("model = %q, want %q", req.Model, "llama3")
					}
					return runtime.ChatResponse{ID: "cmpl-1", Model: req.Model,
						Message: runtime.ChatMessage{Role: "assistant", Content: "hello"}}, nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				chunks := sink.payloads()
				if len(chunks) != 1 {
					t.Fatalf("chunks = %d, want 1", len(chunks))
				}
				resp, err := tunnelwire.UnmarshalChatResponse(chunks[0])
				if err != nil {
					t.Fatalf("tunnelwire.UnmarshalChatResponse: %v", err)
				}
				if resp.Message.Content != "hello" {
					t.Errorf("content = %q, want %q", resp.Message.Content, "hello")
				}
			},
		},
		{
			name:      "chat stream",
			operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
			payload: func(t *testing.T) []byte {
				return mustMarshal(t, tunnelwire.MarshalChatRequest, chatReq)
			},
			install: func(t *testing.T, f *dispatchFixture) {
				f.inference(0).ChatStreamFunc = func(context.Context, runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
					return tunneltest.EventStream(nil,
						runtime.ChatEvent{ID: "e1", Delta: runtime.ChatMessageDelta{Content: "he"}},
						runtime.ChatEvent{ID: "e2", Delta: runtime.ChatMessageDelta{Content: "llo"}},
						runtime.ChatEvent{ID: "e3", FinishReason: "stop"}), nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				chunks := sink.payloads()
				if len(chunks) != 3 {
					t.Fatalf("chunks = %d, want 3: every event is its own frame", len(chunks))
				}
				for i, want := range []string{"he", "llo", ""} {
					ev, err := tunnelwire.UnmarshalChatEvent(chunks[i])
					if err != nil {
						t.Fatalf("tunnelwire.UnmarshalChatEvent %d: %v", i, err)
					}
					if ev.Delta.Content != want {
						t.Errorf("event %d content = %q, want %q", i, ev.Delta.Content, want)
					}
				}
			},
		},
		{
			name:      "embed",
			operation: tunnelv1.Operation_OPERATION_EMBED,
			payload: func(t *testing.T) []byte {
				return mustMarshal(t, tunnelwire.MarshalEmbeddingRequest, runtime.EmbeddingRequest{
					Model: "bge-m3", Input: []string{"one"}})
			},
			install: func(t *testing.T, f *dispatchFixture) {
				f.inference(0).EmbedFunc = func(_ context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
					return runtime.EmbeddingResponse{Model: req.Model,
						Data: []runtime.Embedding{{Index: 0, Vector: []float32{0.5, 0.25}}}}, nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				chunks := sink.payloads()
				if len(chunks) != 1 {
					t.Fatalf("chunks = %d, want 1", len(chunks))
				}
				resp, err := tunnelwire.UnmarshalEmbeddingResponse(chunks[0])
				if err != nil {
					t.Fatalf("tunnelwire.UnmarshalEmbeddingResponse: %v", err)
				}
				if len(resp.Data) != 1 || len(resp.Data[0].Vector) != 2 {
					t.Errorf("embedding = %+v, want one 2-dimensional vector", resp.Data)
				}
			},
		},
		{
			name:      "workflow submit",
			operation: tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT,
			payload: func(t *testing.T) []byte {
				return mustMarshal(t, tunnelwire.MarshalWorkflowRequest, runtime.WorkflowRequest{ClientID: "c-1"})
			},
			body: []string{`{"1":{"class_type":`, `"KSampler"}}`},
			install: func(t *testing.T, f *dispatchFixture) {
				f.workflow(0).SubmitFunc = func(_ context.Context, req runtime.WorkflowRequest) (runtime.WorkflowRun, error) {
					// The template arrives as DataChunks, reassembled in order.
					if got, want := string(req.Template), `{"1":{"class_type":"KSampler"}}`; got != want {
						t.Errorf("template = %s, want %s", got, want)
					}
					if req.ClientID != "c-1" {
						t.Errorf("client id = %q, want %q", req.ClientID, "c-1")
					}
					return runtime.WorkflowRun{ID: testRunID, RuntimeID: testRuntimeID}, nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				chunks := sink.payloads()
				if len(chunks) != 1 {
					t.Fatalf("chunks = %d, want 1", len(chunks))
				}
				run, err := tunnelwire.UnmarshalWorkflowRun(chunks[0])
				if err != nil {
					t.Fatalf("tunnelwire.UnmarshalWorkflowRun: %v", err)
				}
				if run.ID != testRunID {
					t.Errorf("run id = %q, want %q", run.ID, testRunID)
				}
			},
		},
		{
			name:      "workflow subscribe",
			operation: tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE,
			payload: func(t *testing.T) []byte {
				return mustMarshal(t, tunnelwire.MarshalRunRef, testRunID)
			},
			install: func(t *testing.T, f *dispatchFixture) {
				f.workflow(0).SubscribeFunc = func(_ context.Context, runID string) (runtime.Stream[runtime.WorkflowEvent], error) {
					if runID != testRunID {
						t.Errorf("run id = %q, want %q", runID, testRunID)
					}
					return tunneltest.EventStream(nil,
						runtime.WorkflowEvent{Type: runtime.WorkflowEventStarted, RunID: runID},
						runtime.WorkflowEvent{Type: runtime.WorkflowEventSucceeded, RunID: runID}), nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				chunks := sink.payloads()
				if len(chunks) != 2 {
					t.Fatalf("chunks = %d, want 2: every event is its own frame", len(chunks))
				}
				ev, err := tunnelwire.UnmarshalWorkflowEvent(chunks[1])
				if err != nil {
					t.Fatalf("tunnelwire.UnmarshalWorkflowEvent: %v", err)
				}
				if ev.Type != runtime.WorkflowEventSucceeded {
					t.Errorf("last event = %q, want %q", ev.Type, runtime.WorkflowEventSucceeded)
				}
			},
		},
		{
			name:      "workflow status",
			operation: tunnelv1.Operation_OPERATION_WORKFLOW_STATUS,
			payload: func(t *testing.T) []byte {
				return mustMarshal(t, tunnelwire.MarshalRunRef, testRunID)
			},
			install: func(t *testing.T, f *dispatchFixture) {
				f.workflow(0).StatusFunc = func(context.Context, string) (runtime.WorkflowStatus, error) {
					return runtime.WorkflowStatus{State: runtime.WorkflowRunning, QueuePosition: 2}, nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				chunks := sink.payloads()
				if len(chunks) != 1 {
					t.Fatalf("chunks = %d, want 1", len(chunks))
				}
				status, err := tunnelwire.UnmarshalWorkflowStatus(chunks[0])
				if err != nil {
					t.Fatalf("tunnelwire.UnmarshalWorkflowStatus: %v", err)
				}
				if status.State != runtime.WorkflowRunning || status.QueuePosition != 2 {
					t.Errorf("status = %+v, want running at position 2", status)
				}
			},
		},
		{
			name:      "workflow cancel",
			operation: tunnelv1.Operation_OPERATION_WORKFLOW_CANCEL,
			payload: func(t *testing.T) []byte {
				return mustMarshal(t, tunnelwire.MarshalRunRef, testRunID)
			},
			install: func(t *testing.T, f *dispatchFixture) {
				f.workflow(0).CancelFunc = func(_ context.Context, runID string) error {
					if runID != testRunID {
						t.Errorf("run id = %q, want %q", runID, testRunID)
					}
					return nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				// The one operation whose success is a bare ResponseEnd.
				if chunks := sink.payloads(); len(chunks) != 0 {
					t.Errorf("chunks = %d, want 0", len(chunks))
				}
			},
		},
		{
			name:      "artifact open",
			operation: tunnelv1.Operation_OPERATION_ARTIFACT_OPEN,
			payload: func(t *testing.T) []byte {
				return mustMarshal(t, tunnelwire.MarshalArtifactRef, runtime.ArtifactRef{
					RunID: testRunID, Filename: "out.png", Type: "output"})
			},
			wantHead: true,
			install: func(t *testing.T, f *dispatchFixture) {
				f.workflow(0).OpenArtifactFunc = func(_ context.Context, ref runtime.ArtifactRef) (runtime.Artifact, error) {
					if ref.Filename != "out.png" {
						t.Errorf("filename = %q, want %q", ref.Filename, "out.png")
					}
					return runtime.Artifact{
						Ref:         ref,
						ContentType: "image/png",
						Size:        9,
						Body:        io.NopCloser(strings.NewReader("PNGPNGPNG")),
					}, nil
				}
			},
			verify: func(t *testing.T, sink *recordingSink) {
				if sink.contentType != "image/png" || sink.size != 9 {
					t.Errorf("headers = (%q, %d), want (image/png, 9)", sink.contentType, sink.size)
				}
				var body []byte
				for _, chunk := range sink.payloads() {
					body = append(body, chunk...)
				}
				if string(body) != "PNGPNGPNG" {
					t.Errorf("artifact body = %q, want %q", body, "PNGPNGPNG")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDispatchFixture(t, nil)
			tt.install(t, f)

			var payload []byte
			if tt.payload != nil {
				payload = tt.payload(t)
			}

			sink, err := f.dispatch(tt.operation, payload, func(req *tunnel.Request) {
				if len(tt.body) == 0 {
					return
				}
				body := make(chan []byte, len(tt.body))
				for _, part := range tt.body {
					body <- []byte(part)
				}
				close(body)
				req.Body = body
			})
			if err != nil {
				t.Fatalf("dispatch failed: %v", err)
			}
			if got, want := sink.headers, boolToInt(tt.wantHead); got != want {
				t.Errorf("ResponseHeaders frames = %d, want %d", got, want)
			}
			tt.verify(t, sink)
		})
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestDispatchForwardsAnUpstreamError(t *testing.T) {
	f := newDispatchFixture(t, nil)
	f.inference(0).ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
		return runtime.ChatResponse{}, &runtime.RuntimeError{
			Code:       runtime.ErrorUpstream,
			RuntimeID:  testRuntimeID,
			Kind:       runtime.KindOllama,
			Operation:  "chat",
			StatusCode: 502,
			Message:    "backend returned 502",
			Retryable:  true,
		}
	}

	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil)
	re := wantCode(t, err, runtime.ErrorUpstream)
	if !re.Retryable {
		t.Error("the retryable flag was lost; the replica cannot fail over without it")
	}

	// The whole point of the error mirror: code, retryable and status survive
	// the crossing intact.
	pb := tunnelwire.ErrorToProto(err)
	if pb.GetCode() != string(runtime.ErrorUpstream) || !pb.GetRetryable() || pb.GetStatusCode() != 502 {
		t.Errorf("TunnelError = %+v, want an upstream 502 marked retryable", pb)
	}
}

// -----------------------------------------------------------------------
// Streaming
// -----------------------------------------------------------------------

func TestDispatchStreamCarriesATerminalError(t *testing.T) {
	f := newDispatchFixture(t, nil)
	f.inference(0).ChatStreamFunc = func(context.Context, runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
		return tunneltest.EventStream[runtime.ChatEvent](&runtime.RuntimeError{
			Code:      runtime.ErrorUpstream,
			Operation: "chat_stream",
			Message:   "the backend hung up mid-stream",
			Retryable: true,
		}), nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	sink, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT_STREAM, payload, nil)
	re := wantCode(t, err, runtime.ErrorUpstream)

	// Nothing was delivered, so the request is still safe to retry elsewhere.
	if len(sink.payloads()) != 0 {
		t.Errorf("chunks = %d, want 0", len(sink.payloads()))
	}
	if !re.Retryable {
		t.Error("a stream that failed before its first event must stay retryable")
	}
}

func TestDispatchStreamAfterCommitIsNotRetryable(t *testing.T) {
	f := newDispatchFixture(t, nil)
	f.inference(0).ChatStreamFunc = func(context.Context, runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
		return tunneltest.EventStream(
			&runtime.RuntimeError{
				Code:      runtime.ErrorConnection,
				Operation: "chat_stream",
				Message:   "the backend hung up mid-stream",
				Retryable: true,
			},
			runtime.ChatEvent{ID: "e1", Delta: runtime.ChatMessageDelta{Content: "he"}}), nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	sink, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT_STREAM, payload, nil)
	re := wantCode(t, err, runtime.ErrorConnection)

	if len(sink.payloads()) != 1 {
		t.Fatalf("chunks = %d, want 1: the committed event must have been sent", len(sink.payloads()))
	}
	// The user has already seen the first token. Retrying on another node
	// would splice the start of one answer onto the whole of another.
	if re.Retryable {
		t.Error("a stream that already emitted an event was still marked retryable")
	}
	if pb := tunnelwire.ErrorToProto(err); pb.GetRetryable() {
		t.Error("TunnelError still marked retryable after the stream committed")
	}
}

func TestDispatchStreamStopsWhenTheSlotIsGone(t *testing.T) {
	f := newDispatchFixture(t, nil)

	closed := make(chan struct{})
	f.inference(0).ChatStreamFunc = func(context.Context, runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
		stream := runtime.NewChanStream[runtime.ChatEvent](4)
		go func() {
			defer close(closed)
			for i := range 10 {
				if !stream.Send(runtime.ChatEvent{ID: "e" + strconv.Itoa(i)}) {
					// The consumer closed the stream: the producer must stop,
					// which is how a dead slot stops costing backend work.
					return
				}
			}
			stream.CloseWithError(nil)
		}()
		return stream, nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	sink := &recordingSink{failAfter: 2}
	body := make(chan []byte)
	close(body)

	err := f.disp.Handle(context.Background(), &tunnel.Request{
		ID: "req-1",
		Headers: &tunnelv1.RequestHeaders{
			RuntimeId: testRuntimeID,
			Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
			Payload:   payload,
		},
		Body: body,
	}, sink)
	if err == nil {
		t.Fatal("a failing sink did not end the request")
	}
	select {
	case <-closed:
	case <-time.After(testTimeout):
		t.Fatal("the stream producer was never released")
	}
}

// -----------------------------------------------------------------------
// Quota
// -----------------------------------------------------------------------

func TestDispatchReturnsBackpressureAtTheConcurrencyLimit(t *testing.T) {
	f := newDispatchFixture(t, nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	f.inference(1).ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
		entered <- struct{}{}
		<-release
		return runtime.ChatResponse{ID: "cmpl-1"}, nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	first := make(chan error, 1)
	go func() {
		_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil)
		first <- err
	}()
	<-entered

	// The node's real concurrency is the limiter's, not the slot pool's: with
	// the only permit taken, the second request is refused immediately rather
	// than queued, so the replica can try another node.
	_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil)
	re := wantCode(t, err, runtime.ErrorBackpressure)
	if !errors.Is(re, runtime.ErrConcurrencyLimit) {
		t.Errorf("error does not wrap ErrConcurrencyLimit: %v", re)
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatalf("the first request failed: %v", err)
	}

	// The permit came back, so the next request goes through.
	f.inference(1).ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
		return runtime.ChatResponse{ID: "cmpl-2"}, nil
	}
	if _, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil); err != nil {
		t.Errorf("dispatch after the permit was released: %v", err)
	}
}

func TestDispatchReleasesQuotaOnEveryPath(t *testing.T) {
	payload := func(t *testing.T) []byte {
		return mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	}

	tests := []struct {
		name string
		run  func(t *testing.T, f *dispatchFixture, rt *tunneltest.InferenceRuntime)
	}{
		{
			name: "a handler that panics",
			run: func(t *testing.T, f *dispatchFixture, rt *tunneltest.InferenceRuntime) {
				rt.ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
					panic("adapter exploded")
				}
				defer func() {
					if recover() == nil {
						t.Error("the panic did not reach the caller")
					}
				}()
				_, _ = f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload(t), nil)
			},
		},
		{
			name: "a cancelled request",
			run: func(t *testing.T, f *dispatchFixture, rt *tunneltest.InferenceRuntime) {
				ctx, cancel := context.WithCancel(context.Background())
				rt.ChatFunc = func(ctx context.Context, _ runtime.ChatRequest) (runtime.ChatResponse, error) {
					cancel()
					<-ctx.Done()
					return runtime.ChatResponse{}, ctx.Err()
				}
				defer cancel()
				if _, err := f.dispatchCtx(ctx, tunnelv1.Operation_OPERATION_CHAT, payload(t), nil); err == nil {
					t.Error("a cancelled request reported success")
				}
			},
		},
		{
			name: "an early error return",
			run: func(t *testing.T, f *dispatchFixture, rt *tunneltest.InferenceRuntime) {
				rt.ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
					return runtime.ChatResponse{}, &runtime.RuntimeError{
						Code: runtime.ErrorUpstream, Message: "nope"}
				}
				if _, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload(t), nil); err == nil {
					t.Error("a failing request reported success")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDispatchFixture(t, nil)
			// One permit only: if the first request leaks it, the second one
			// gets backpressure instead of being served.
			rt := f.inference(1)
			func() {
				defer func() { _ = recover() }()
				tt.run(t, f, rt)
			}()

			rt.ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
				return runtime.ChatResponse{ID: "cmpl-after"}, nil
			}
			if _, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload(t), nil); err != nil {
				t.Errorf("the quota was not released: %v", err)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Deadlines and size limits
// -----------------------------------------------------------------------

func TestDispatchTakesTheSmallerDeadline(t *testing.T) {
	tests := []struct {
		name        string
		maxDeadline time.Duration
		offset      time.Duration // gateway deadline relative to now; 0 means unset
		want        time.Duration // expected remaining time
	}{
		{name: "the gateway's deadline is shorter", maxDeadline: 30 * time.Minute, offset: time.Minute, want: time.Minute},
		{name: "the local maximum is shorter", maxDeadline: 5 * time.Minute, offset: time.Hour, want: 5 * time.Minute},
		{name: "no gateway deadline falls back to the local maximum", maxDeadline: 5 * time.Minute, want: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) {
				cfg.MaxDeadline = tt.maxDeadline
			})

			var deadline time.Time
			f.inference(0).ChatFunc = func(ctx context.Context, _ runtime.ChatRequest) (runtime.ChatResponse, error) {
				deadline, _ = ctx.Deadline()
				return runtime.ChatResponse{}, nil
			}

			payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
			_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, func(req *tunnel.Request) {
				if tt.offset > 0 {
					req.Headers.DeadlineUnixMs = tunnelNow.Add(tt.offset).UnixMilli()
				}
			})
			if err != nil {
				t.Fatalf("dispatch failed: %v", err)
			}
			// The budget comes from the injected clock but the timer is real,
			// so compare the remaining time with a tolerance far smaller than
			// the minutes that separate the cases.
			if got := time.Until(deadline); got > tt.want || tt.want-got > time.Second {
				t.Errorf("remaining time = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDispatchRefusesAnExpiredDeadline(t *testing.T) {
	f := newDispatchFixture(t, nil)

	called := false
	f.inference(0).ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
		called = true
		return runtime.ChatResponse{}, nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, func(req *tunnel.Request) {
		req.Headers.DeadlineUnixMs = tunnelNow.Add(-time.Second).UnixMilli()
	})
	re := wantCode(t, err, runtime.ErrorTimeout)
	if !errors.Is(re, context.DeadlineExceeded) {
		t.Errorf("error does not wrap context.DeadlineExceeded: %v", re)
	}
	if called {
		t.Error("a request whose deadline had already passed still reached the backend")
	}
}

func TestDispatchRefusesAnOversizedRequestBody(t *testing.T) {
	f := newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) {
		cfg.MaxRequestBytes = 16
	})

	called := false
	f.workflow(0).SubmitFunc = func(context.Context, runtime.WorkflowRequest) (runtime.WorkflowRun, error) {
		called = true
		return runtime.WorkflowRun{}, nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalWorkflowRequest, runtime.WorkflowRequest{ClientID: "c-1"})
	_, err := f.dispatch(tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT, payload, func(req *tunnel.Request) {
		body := make(chan []byte, 3)
		for range 3 {
			body <- []byte("0123456789")
		}
		close(body)
		req.Body = body
	})
	wantCode(t, err, runtime.ErrorResponseTooLarge)
	if called {
		t.Error("an oversized template still reached the backend")
	}
}

func TestDispatchRefusesAWorkflowSubmitWithNoTemplate(t *testing.T) {
	f := newDispatchFixture(t, nil)
	f.workflow(0)

	payload := mustMarshal(t, tunnelwire.MarshalWorkflowRequest, runtime.WorkflowRequest{ClientID: "c-1"})
	_, err := f.dispatch(tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT, payload, nil)
	wantCode(t, err, runtime.ErrorProtocol)
}

func TestDispatchSplitsAnArtifactIntoBoundedFrames(t *testing.T) {
	f := newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) {
		cfg.MaxFrameBytes = 4
	})
	f.workflow(0).OpenArtifactFunc = func(_ context.Context, ref runtime.ArtifactRef) (runtime.Artifact, error) {
		return runtime.Artifact{
			Ref:         ref,
			ContentType: "application/octet-stream",
			Size:        -1,
			Body:        io.NopCloser(strings.NewReader("0123456789")),
		}, nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalArtifactRef, runtime.ArtifactRef{RunID: testRunID, Filename: "out.bin"})
	sink, err := f.dispatch(tunnelv1.Operation_OPERATION_ARTIFACT_OPEN, payload, nil)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	chunks := sink.payloads()
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3: a 10-byte body at 4 bytes a frame", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > 4 {
			t.Errorf("chunk %d is %d bytes, over the %d-byte frame limit", i, len(chunk), 4)
		}
	}
	if sink.size != -1 {
		t.Errorf("size = %d, want -1 for an artifact of unknown length", sink.size)
	}
}

func TestDispatchRefusesAnOversizedResponseFrame(t *testing.T) {
	f := newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) {
		cfg.MaxFrameBytes = 8
	})
	f.inference(0).ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
		return runtime.ChatResponse{
			ID:      "cmpl-1",
			Message: runtime.ChatMessage{Role: "assistant", Content: strings.Repeat("x", 1024)},
		}, nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	sink, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil)
	wantCode(t, err, runtime.ErrorResponseTooLarge)
	if len(sink.payloads()) != 0 {
		t.Error("the oversized frame was sent anyway")
	}
}

// -----------------------------------------------------------------------
// End to end through a slot
// -----------------------------------------------------------------------

func TestDispatchServesOverASlotStream(t *testing.T) {
	// The dispatcher's own tests call Handle directly. This one puts the real
	// slot pool in front of it and talks to it in frames, which is the
	// acceptance criterion: a fake Gateway completes an operation end to end.
	mgr := tunneltest.NewManager()
	backend := &tunneltest.InferenceRuntime{BaseRuntime: tunneltest.BaseRuntime{Desc: runtime.Descriptor{
		ID: testRuntimeID, Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434",
	}}}
	backend.ChatFunc = func(_ context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
		return runtime.ChatResponse{ID: "cmpl-1", Model: req.Model,
			Message: runtime.ChatMessage{Role: "assistant", Content: "pong"}}, nil
	}
	backend.ChatStreamFunc = func(context.Context, runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
		return tunneltest.EventStream(nil,
			runtime.ChatEvent{ID: "e1", Delta: runtime.ChatMessageDelta{Content: "po"}},
			runtime.ChatEvent{ID: "e2", Delta: runtime.ChatMessageDelta{Content: "ng"}}), nil
	}
	mgr.SetRuntime(testRuntimeID, backend)

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         mgr,
		AllowedRuntimes: []string{testRuntimeID},
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		soloSlot(cfg)
		cfg.Handler = dispatcher
	})
	f.start()
	c := f.acceptSlots(1)[0]

	chatPayload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: "req-1",
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId: testRuntimeID,
			Operation: tunnelv1.Operation_OPERATION_CHAT,
			Payload:   chatPayload,
		}},
	})

	end, chunks := f.expectEnd(c, "req-1")
	if end.GetError() != nil {
		t.Fatalf("chat over a slot failed: %v", end.GetError())
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	resp, err := tunnelwire.UnmarshalChatResponse(chunks[0])
	if err != nil {
		t.Fatalf("tunnelwire.UnmarshalChatResponse: %v", err)
	}
	if resp.Message.Content != "pong" {
		t.Errorf("content = %q, want %q", resp.Message.Content, "pong")
	}
	f.expectReady(c)

	// The same slot then serves a streaming request, one frame per event.
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: "req-2",
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId: testRuntimeID,
			Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
			Payload:   chatPayload,
		}},
	})

	end, chunks = f.expectEnd(c, "req-2")
	if end.GetError() != nil {
		t.Fatalf("chat stream over a slot failed: %v", end.GetError())
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2: every event is its own frame", len(chunks))
	}
	f.expectReady(c)
}

func TestDispatchRefusesAForbiddenRuntimeOverASlotStream(t *testing.T) {
	mgr := tunneltest.NewManager()
	mgr.SetRuntime("internal-only", &tunneltest.InferenceRuntime{
		BaseRuntime: tunneltest.BaseRuntime{Desc: runtime.Descriptor{ID: "internal-only"}}})

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         mgr,
		AllowedRuntimes: []string{testRuntimeID},
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		soloSlot(cfg)
		cfg.Handler = dispatcher
	})
	f.start()
	c := f.acceptSlots(1)[0]

	// The control plane may hand the Manager whatever it likes; the allowlist
	// still decides what a dispatched frame can reach.
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: "req-1",
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId: "internal-only",
			Operation: tunnelv1.Operation_OPERATION_LIST_MODELS,
		}},
	})

	end, _ := f.expectEnd(c, "req-1")
	if got := end.GetError().GetCode(); got != string(runtime.ErrorInvalidConfig) {
		t.Errorf("ResponseEnd code = %q, want %q", got, runtime.ErrorInvalidConfig)
	}
	// A refused request costs no slot.
	f.expectReady(c)
}

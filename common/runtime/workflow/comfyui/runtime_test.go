package comfyui_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/runtimetest"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/workflow/comfyui"
)

const (
	testRunID   = "prompt-1"
	otherRunID  = "prompt-2"
	testVersion = "0.3.40"
)

// --- fake ComfyUI HTTP server -----------------------------------------

type fakeComfy struct {
	server *httptest.Server

	mu sync.Mutex
	// calls records every request path in order, so tests can assert both
	// which routes were touched and the order they were touched in.
	calls []string

	statsStatus  int
	statsBody    any
	featuresGone bool
	objectInfo   string
	modelsGone   bool
	modelFolders []string
	modelFiles   map[string][]string

	promptStatus  int
	promptError   string
	promptID      string
	promptCalls   int
	queueRunning  []string
	queuePending  []string
	history       map[string]any
	interrupted   bool
	deletedFromQ  []string
	artifactBody  string
	artifactSize  int64 // when > 0, overrides the advertised Content-Length
	artifactGone  bool
	viewCallCount int
}

func newFakeComfy(t *testing.T, opts ...func(*fakeComfy)) *fakeComfy {
	t.Helper()
	f := &fakeComfy{
		objectInfo:   `{"KSampler":{"input":{}},"SaveImage":{"input":{}}}`,
		modelFolders: []string{"checkpoints"},
		modelFiles:   map[string][]string{"checkpoints": {"sd15.safetensors"}},
		promptID:     testRunID,
		history:      map[string]any{},
		artifactBody: "PNGDATA",
	}
	for _, opt := range opts {
		opt(f)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		writeError(w, http.StatusNotFound, "Not Found")
	})
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		f.mu.Lock()
		status, body := f.statsStatus, f.statsBody
		f.mu.Unlock()
		if status != 0 {
			writeError(w, status, "unavailable")
			return
		}
		if body == nil {
			body = map[string]any{
				"system":  map[string]any{"comfyui_version": testVersion, "python_version": "3.12"},
				"devices": []map[string]any{{"name": "cuda:0", "type": "cuda"}},
			}
		}
		writeJSON(w, body)
	})
	mux.HandleFunc("/features", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		f.mu.Lock()
		gone := f.featuresGone
		f.mu.Unlock()
		if gone {
			writeError(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, map[string]any{"supports_preview_metadata": true})
	})
	mux.HandleFunc("/object_info", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		f.mu.Lock()
		body := f.objectInfo
		f.mu.Unlock()
		if body == "" {
			writeError(w, http.StatusNotFound, "Not Found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		f.mu.Lock()
		gone, folders := f.modelsGone, f.modelFolders
		f.mu.Unlock()
		if gone {
			writeError(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, folders)
	})
	mux.HandleFunc("/models/", func(w http.ResponseWriter, r *http.Request) {
		f.record("/models/{folder}")
		folder := strings.TrimPrefix(r.URL.Path, "/models/")
		f.mu.Lock()
		files, ok := f.modelFiles[folder]
		f.mu.Unlock()
		if !ok {
			writeError(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, files)
	})
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		f.mu.Lock()
		f.promptCalls++
		status, message, id := f.promptStatus, f.promptError, f.promptID
		f.mu.Unlock()
		if status != 0 {
			writeError(w, status, message)
			return
		}
		writeJSON(w, map[string]any{"prompt_id": id, "number": 1})
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		if r.Method == http.MethodPost {
			var body struct {
				Delete []string `json:"delete"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.deletedFromQ = append(f.deletedFromQ, body.Delete...)
			f.mu.Unlock()
			writeJSON(w, map[string]any{})
			return
		}
		f.mu.Lock()
		running, pending := f.queueRunning, f.queuePending
		f.mu.Unlock()
		writeJSON(w, map[string]any{
			"queue_running": queueEntries(running),
			"queue_pending": queueEntries(pending),
		})
	})
	mux.HandleFunc("/history/", func(w http.ResponseWriter, r *http.Request) {
		f.record("/history/{prompt_id}")
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		f.mu.Lock()
		entry, ok := f.history[id]
		f.mu.Unlock()
		if !ok {
			writeJSON(w, map[string]any{})
			return
		}
		writeJSON(w, map[string]any{id: entry})
	})
	mux.HandleFunc("/interrupt", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		f.mu.Lock()
		f.interrupted = true
		f.mu.Unlock()
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path + "?" + r.URL.RawQuery)
		f.mu.Lock()
		gone, body, size := f.artifactGone, f.artifactBody, f.artifactSize
		f.viewCallCount++
		f.mu.Unlock()
		if gone {
			writeError(w, http.StatusNotFound, "Not Found")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		if size > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, body)
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func queueEntries(ids []string) [][]any {
	entries := make([][]any, 0, len(ids))
	for i, id := range ids {
		entries = append(entries, []any{i, id, map[string]any{}, map[string]any{}, []any{}})
	}
	return entries
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message}})
}

func (f *fakeComfy) record(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, path)
}

func (f *fakeComfy) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeComfy) called(path string) bool {
	for _, p := range f.recorded() {
		if p == path {
			return true
		}
	}
	return false
}

func (f *fakeComfy) setQueue(running, pending []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueRunning, f.queuePending = running, pending
}

func (f *fakeComfy) setHistory(id string, entry any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history[id] = entry
}

func (f *fakeComfy) wasInterrupted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.interrupted
}

func (f *fakeComfy) deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deletedFromQ...)
}

func (f *fakeComfy) promptSubmissions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.promptCalls
}

// --- scripted WebSocket -----------------------------------------------

type wsMessage struct {
	typ  int
	data []byte
	err  error
}

// scriptedWS hands out one pre-made connection per dial, so a test can make
// the first connection fail and assert what the reconnect does.
type scriptedWS struct {
	mu       sync.Mutex
	conns    []*scriptedConn
	dialErrs []error
	dials    int
	dialLog  func()
}

func newScriptedWS(conns int) *scriptedWS {
	s := &scriptedWS{}
	for i := 0; i < conns; i++ {
		s.conns = append(s.conns, newScriptedConn())
	}
	return s
}

func (s *scriptedWS) Dial(ctx context.Context, url string, header http.Header) (runtime.WSConn, error) {
	s.mu.Lock()
	index := s.dials
	s.dials++
	log := s.dialLog
	var err error
	if index < len(s.dialErrs) {
		err = s.dialErrs[index]
	}
	var conn *scriptedConn
	if index < len(s.conns) {
		conn = s.conns[index]
	}
	s.mu.Unlock()

	if log != nil {
		log()
	}
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, errors.New("scriptedWS: no connection scripted for this dial")
	}
	conn.dialed()
	return conn, nil
}

func (s *scriptedWS) dialCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dials
}

func (s *scriptedWS) conn(i int) *scriptedConn { return s.conns[i] }

type scriptedConn struct {
	frames    chan wsMessage
	closeOnce sync.Once
	closed    chan struct{}
	dialedCh  chan struct{}
	dialOnce  sync.Once
	closes    int
	mu        sync.Mutex
}

func newScriptedConn() *scriptedConn {
	return &scriptedConn{
		frames:   make(chan wsMessage, 64),
		closed:   make(chan struct{}),
		dialedCh: make(chan struct{}),
	}
}

func (c *scriptedConn) dialed() { c.dialOnce.Do(func() { close(c.dialedCh) }) }

// waitDialed blocks until this connection has been handed out, so a test
// can push frames knowing the read loop is running.
func (c *scriptedConn) waitDialed(t *testing.T) {
	t.Helper()
	select {
	case <-c.dialedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("the adapter never dialed the event stream")
	}
}

func (c *scriptedConn) send(t *testing.T, eventType string, data any) {
	t.Helper()
	frame := map[string]any{"type": eventType}
	if data != nil {
		frame["data"] = data
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	c.push(wsMessage{typ: 1, data: encoded})
}

func (c *scriptedConn) sendRaw(data string) { c.push(wsMessage{typ: 1, data: []byte(data)}) }

func (c *scriptedConn) sendBinary(data []byte) { c.push(wsMessage{typ: 2, data: data}) }

func (c *scriptedConn) fail(err error) { c.push(wsMessage{err: err}) }

func (c *scriptedConn) push(m wsMessage) {
	select {
	case c.frames <- m:
	case <-c.closed:
	}
}

func (c *scriptedConn) Read(ctx context.Context) (int, []byte, error) {
	select {
	case m := <-c.frames:
		if m.err != nil {
			return 0, nil, m.err
		}
		return m.typ, m.data, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (c *scriptedConn) Close() error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *scriptedConn) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

// --- helpers ----------------------------------------------------------

func newRuntime(t *testing.T, f *fakeComfy, ws runtime.WSDialer, mutate ...func(*runtime.Config)) *comfyui.Runtime {
	t.Helper()
	cfg := runtime.Config{
		ID:      "test-comfyui",
		Kind:    runtime.KindComfyUI,
		BaseURL: f.server.URL,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	rt, err := comfyui.New(cfg.Normalize(), runtime.Dependencies{
		HTTPClient: f.server.Client(),
		WSDialer:   ws,
		Clock:      runtimetest.NewClock(time.Unix(1700000000, 0)),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	adapter, ok := rt.(*comfyui.Runtime)
	if !ok {
		t.Fatalf("New returned %T, want *comfyui.Runtime", rt)
	}
	return adapter
}

func requireErrorCode(t *testing.T, err error, want runtime.ErrorCode) *runtime.RuntimeError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %q, got nil", want)
	}
	var rerr *runtime.RuntimeError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *runtime.RuntimeError, got %T: %v", err, err)
	}
	if rerr.Code != want {
		t.Fatalf("error code = %q, want %q (err: %v)", rerr.Code, want, err)
	}
	return rerr
}

func mustDiscover(t *testing.T, rt *comfyui.Runtime) runtime.Discovery {
	t.Helper()
	discovery, err := rt.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return discovery
}

func mustSubmit(t *testing.T, rt *comfyui.Runtime) runtime.WorkflowRun {
	t.Helper()
	run, err := rt.Submit(context.Background(), runtime.WorkflowRequest{Template: workflowTemplate()})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return run
}

func workflowTemplate() json.RawMessage {
	return json.RawMessage(`{"3":{"class_type":"KSampler","inputs":{"seed":42}},"9":{"class_type":"SaveImage","inputs":{}}}`)
}

// recvWithin reads one event, failing the test rather than hanging if the
// adapter never delivers it.
func recvWithin(t *testing.T, stream runtime.Stream[runtime.WorkflowEvent]) (runtime.WorkflowEvent, error) {
	t.Helper()
	type result struct {
		event runtime.WorkflowEvent
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		event, err := stream.Recv()
		ch <- result{event, err}
	}()
	select {
	case r := <-ch:
		return r.event, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a workflow event")
		return runtime.WorkflowEvent{}, nil
	}
}

func historySuccess(outputs map[string]any) map[string]any {
	entry := map[string]any{
		"status": map[string]any{"status_str": "success", "completed": true, "messages": []any{}},
	}
	if outputs != nil {
		entry["outputs"] = outputs
	}
	return entry
}

// --- probe, health, discover ------------------------------------------

func TestProbeIdentifiesComfyUI(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))

	result, err := rt.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if result.Kind != runtime.KindComfyUI || !result.IdentityVerified {
		t.Errorf("Probe = %+v, want a verified ComfyUI identity", result)
	}
	if result.Version != testVersion {
		t.Errorf("Version = %q, want %q", result.Version, testVersion)
	}
	if !strings.Contains(result.Evidence, "1 device(s)") {
		t.Errorf("Evidence = %q, want the device count", result.Evidence)
	}
}

func TestProbeRejectsNonComfyBackends(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*fakeComfy)
	}{
		{
			name:  "route is absent",
			apply: func(f *fakeComfy) { f.statsStatus = http.StatusNotFound },
		},
		{
			// Something answered on the path, but with none of the fields a
			// ComfyUI server reports.
			name:  "answer has no comfyui fields",
			apply: func(f *fakeComfy) { f.statsBody = map[string]any{"service": "something-else"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeComfy(t, tt.apply)
			rt := newRuntime(t, f, newScriptedWS(1))

			_, err := rt.Probe(context.Background())
			requireErrorCode(t, err, runtime.ErrorProbeMismatch)
		})
	}
}

func TestHealthReportsBothStates(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))

	report, err := rt.Health(context.Background())
	if err != nil || report.State != runtime.StateHealthy {
		t.Fatalf("Health = %+v, err = %v; want healthy", report, err)
	}

	f.mu.Lock()
	f.statsStatus = http.StatusInternalServerError
	f.mu.Unlock()

	report, err = rt.Health(context.Background())
	if err == nil {
		t.Fatal("Health: expected an error once /system_stats fails")
	}
	if report.State != runtime.StateUnhealthy || report.ErrorSummary == "" {
		t.Errorf("Health = %+v, want unhealthy with a summary", report)
	}
}

func TestDiscoverReportsNodeTypesModelsAndCapabilities(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))

	discovery := mustDiscover(t, rt)
	if discovery.Version != testVersion {
		t.Errorf("Version = %q, want %q", discovery.Version, testVersion)
	}
	if strings.Join(discovery.NodeTypes, ",") != "KSampler,SaveImage" {
		t.Errorf("NodeTypes = %v, want the node catalogue sorted", discovery.NodeTypes)
	}
	if len(discovery.Models) != 1 || discovery.Models[0].ID != "checkpoints/sd15.safetensors" {
		t.Errorf("Models = %+v, want the model files qualified by folder", discovery.Models)
	}
	if len(discovery.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none when every endpoint answered", discovery.Warnings)
	}

	for _, capability := range []runtime.Capability{
		runtime.CapabilityWorkflowExecution,
		runtime.CapabilityWorkflowEvents,
		runtime.CapabilityWorkflowCancel,
		runtime.CapabilityArtifactRead,
	} {
		ev := discovery.Capabilities.Resolve(capability)
		if ev.Level != runtime.SupportSupported {
			t.Errorf("%s = %q, want supported", capability, ev.Level)
		}
		if ev.Source != runtime.SourceEndpoint {
			t.Errorf("%s source = %q, want %q", capability, ev.Source, runtime.SourceEndpoint)
		}
	}
	// The inference capabilities belong to the other adapters; a workflow
	// runtime must not claim them.
	if got := discovery.Capabilities.Resolve(runtime.CapabilityChat).Level; got != runtime.SupportUnknown {
		t.Errorf("chat = %q, want unknown on a workflow runtime", got)
	}
}

func TestDiscoverToleratesMissingOptionalEndpoints(t *testing.T) {
	f := newFakeComfy(t, func(f *fakeComfy) {
		f.featuresGone = true
		f.objectInfo = ""
		f.modelsGone = true
	})
	rt := newRuntime(t, f, newScriptedWS(1))

	// Older builds do not serve these routes. They inform scheduling; they
	// do not decide whether the instance can run a workflow.
	discovery := mustDiscover(t, rt)
	if len(discovery.Warnings) != 3 {
		t.Fatalf("Warnings = %v, want one per missing optional endpoint", discovery.Warnings)
	}
	if got := discovery.Capabilities.Resolve(runtime.CapabilityWorkflowExecution).Level; got != runtime.SupportSupported {
		t.Errorf("workflow_execution = %q, want supported anyway", got)
	}
}

func TestDiscoverWarnsWhenAModelFolderCannotBeListed(t *testing.T) {
	f := newFakeComfy(t, func(f *fakeComfy) {
		f.modelFolders = []string{"checkpoints", "loras"}
		f.modelFiles = map[string][]string{"checkpoints": {"sd15.safetensors"}}
	})
	rt := newRuntime(t, f, newScriptedWS(1))

	discovery := mustDiscover(t, rt)
	if len(discovery.Models) != 1 {
		t.Errorf("Models = %+v, want the folders that did answer", discovery.Models)
	}
	if len(discovery.Warnings) != 1 || !strings.Contains(discovery.Warnings[0], "loras") {
		t.Errorf("Warnings = %v, want one naming the folder that failed", discovery.Warnings)
	}
}

// --- submit -----------------------------------------------------------

func TestSubmitConnectsTheEventStreamBeforeQueueing(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(1)

	var (
		mu    sync.Mutex
		order []string
	)
	ws.dialLog = func() {
		mu.Lock()
		order = append(order, "dial")
		mu.Unlock()
	}
	rt := newRuntime(t, f, ws)
	mustDiscover(t, rt)

	run := mustSubmit(t, rt)
	if run.ID != testRunID {
		t.Errorf("run ID = %q, want %q", run.ID, testRunID)
	}

	mu.Lock()
	dialed := len(order)
	mu.Unlock()
	if dialed != 1 {
		t.Fatalf("dial count = %d, want the event stream connected exactly once", dialed)
	}
	// ComfyUI starts executing as soon as the queue accepts the job, so a
	// connection opened after the submission would miss the start of the run.
	for _, call := range f.recorded() {
		if call == "/prompt" {
			break
		}
	}
	if !f.called("/prompt") {
		t.Fatal("the workflow was never submitted")
	}
}

func TestSubmitIsIdempotentPerKey(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))
	mustDiscover(t, rt)

	req := runtime.WorkflowRequest{Template: workflowTemplate(), IdempotencyKey: "job-42"}
	first, err := rt.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	second, err := rt.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("second Submit: %v", err)
	}

	// A duplicate submission costs real GPU time and produces a second set
	// of outputs, so a retried request must return the original run.
	if second.ID != first.ID {
		t.Errorf("second run ID = %q, want the first run's %q", second.ID, first.ID)
	}
	if got := f.promptSubmissions(); got != 1 {
		t.Errorf("POST /prompt was called %d times, want 1", got)
	}
}

func TestSubmitWithoutKeyIsNotDeduplicated(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))
	mustDiscover(t, rt)

	mustSubmit(t, rt)
	mustSubmit(t, rt)
	if got := f.promptSubmissions(); got != 2 {
		t.Errorf("POST /prompt was called %d times, want 2 without an idempotency key", got)
	}
}

func TestSubmitRejectsATemplateThatIsNotAnObject(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))
	mustDiscover(t, rt)

	for _, template := range []string{`[]`, `"a string"`, `null`, ``} {
		_, err := rt.Submit(context.Background(), runtime.WorkflowRequest{Template: json.RawMessage(template)})
		requireErrorCode(t, err, runtime.ErrorInvalidConfig)
	}
	if f.called("/prompt") {
		t.Error("an invalid template reached the server")
	}
}

func TestSubmitBeforeDiscoverIsRejected(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))

	_, err := rt.Submit(context.Background(), runtime.WorkflowRequest{Template: workflowTemplate()})
	requireErrorCode(t, err, runtime.ErrorCapability)
	if f.called("/prompt") {
		t.Error("the workflow was submitted before any capability was confirmed")
	}
}

func TestSubmitSurfacesValidationErrors(t *testing.T) {
	f := newFakeComfy(t, func(f *fakeComfy) {
		f.promptStatus = http.StatusBadRequest
		f.promptError = "Prompt outputs failed validation"
	})
	rt := newRuntime(t, f, newScriptedWS(1))
	mustDiscover(t, rt)

	_, err := rt.Submit(context.Background(), runtime.WorkflowRequest{Template: workflowTemplate()})
	rerr := requireErrorCode(t, err, runtime.ErrorProtocol)
	if !strings.Contains(rerr.Message, "failed validation") {
		t.Errorf("Message = %q, want the server's validation message", rerr.Message)
	}
}

// --- events -----------------------------------------------------------

func TestSubscribeRoutesEventsByRunID(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(1)
	rt := newRuntime(t, f, ws)
	mustDiscover(t, rt)

	mine, err := rt.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer mine.Close()
	theirs, err := rt.Subscribe(context.Background(), otherRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer theirs.Close()

	conn := ws.conn(0)
	conn.waitDialed(t)
	conn.send(t, "executing", map[string]any{"prompt_id": otherRunID, "node": "1"})
	conn.send(t, "executing", map[string]any{"prompt_id": testRunID, "node": "3"})

	// A ComfyUI instance streams every client's events down one connection,
	// so mis-routing would show one caller another caller's progress.
	event, err := recvWithin(t, mine)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if event.RunID != testRunID || event.NodeID != "3" {
		t.Errorf("event = %+v, want only this run's node", event)
	}
}

func TestSubscribeNormalizesTheEventVocabulary(t *testing.T) {
	tests := []struct {
		name     string
		event    string
		data     map[string]any
		wantType runtime.WorkflowEventType
		wantNode string
	}{
		{name: "execution_start", event: "execution_start", data: map[string]any{"prompt_id": testRunID}, wantType: runtime.WorkflowEventStarted},
		{name: "execution_cached", event: "execution_cached", data: map[string]any{"prompt_id": testRunID, "nodes": []string{"1"}}, wantType: runtime.WorkflowEventCached},
		{name: "executing a node", event: "executing", data: map[string]any{"prompt_id": testRunID, "node": "3"}, wantType: runtime.WorkflowEventNodeStarted, wantNode: "3"},
		{name: "executing null", event: "executing", data: map[string]any{"prompt_id": testRunID, "node": nil}, wantType: runtime.WorkflowEventCompleted},
		{name: "progress", event: "progress", data: map[string]any{"prompt_id": testRunID, "value": 3, "max": 20}, wantType: runtime.WorkflowEventProgress},
		{name: "executed", event: "executed", data: map[string]any{"prompt_id": testRunID, "node": "9"}, wantType: runtime.WorkflowEventNodeOutput, wantNode: "9"},
		{name: "unknown stays unknown", event: "some_future_event", data: map[string]any{"prompt_id": testRunID}, wantType: runtime.WorkflowEventUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeComfy(t)
			ws := newScriptedWS(1)
			rt := newRuntime(t, f, ws)
			mustDiscover(t, rt)

			stream, err := rt.Subscribe(context.Background(), testRunID)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			defer stream.Close()

			ws.conn(0).waitDialed(t)
			ws.conn(0).send(t, tt.event, tt.data)

			event, err := recvWithin(t, stream)
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if event.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", event.Type, tt.wantType)
			}
			if event.NodeID != tt.wantNode {
				t.Errorf("NodeID = %q, want %q", event.NodeID, tt.wantNode)
			}
			if len(event.Raw) == 0 {
				t.Error("Raw is empty, want the original frame preserved")
			}
		})
	}
}

func TestMalformedAndBinaryFramesDoNotBreakTheStream(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(1)
	rt := newRuntime(t, f, ws)
	mustDiscover(t, rt)

	stream, err := rt.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()

	conn := ws.conn(0)
	conn.waitDialed(t)
	conn.sendRaw("not json at all")
	conn.sendBinary([]byte{0x00, 0x01, 0x02}) // a live preview frame
	conn.send(t, "execution_start", map[string]any{"prompt_id": testRunID})

	// One unreadable frame must not tear down the connection every other
	// run on this instance depends on.
	event, err := recvWithin(t, stream)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if event.Type != runtime.WorkflowEventStarted {
		t.Errorf("Type = %q, want the good frame that followed", event.Type)
	}

	_, binary, _ := rt.EventStreamStats()
	if binary != 1 {
		t.Errorf("binary frames counted = %d, want 1", binary)
	}
}

func TestQueueStatusReachesEverySubscriber(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(1)
	rt := newRuntime(t, f, ws)
	mustDiscover(t, rt)

	first, err := rt.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer first.Close()
	second, err := rt.Subscribe(context.Background(), otherRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer second.Close()

	ws.conn(0).waitDialed(t)
	// Queue status names no run, but it describes the queue both runs are
	// waiting in, so both subscribers need it.
	ws.conn(0).send(t, "status", map[string]any{"status": map[string]any{"exec_info": map[string]any{"queue_remaining": 2}}})

	for i, stream := range []runtime.Stream[runtime.WorkflowEvent]{first, second} {
		event, err := recvWithin(t, stream)
		if err != nil {
			t.Fatalf("subscriber %d Recv: %v", i, err)
		}
		if event.Type != runtime.WorkflowEventQueueChanged {
			t.Errorf("subscriber %d got %q, want %q", i, event.Type, runtime.WorkflowEventQueueChanged)
		}
	}
}

func TestStreamEndsAfterATerminalEvent(t *testing.T) {
	for _, tt := range []struct {
		name  string
		event string
	}{
		{name: "success", event: "execution_success"},
		{name: "error", event: "execution_error"},
		{name: "interrupted", event: "execution_interrupted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeComfy(t)
			ws := newScriptedWS(1)
			rt := newRuntime(t, f, ws)
			mustDiscover(t, rt)

			stream, err := rt.Subscribe(context.Background(), testRunID)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			defer stream.Close()

			ws.conn(0).waitDialed(t)
			ws.conn(0).send(t, tt.event, map[string]any{"prompt_id": testRunID})

			if _, err := recvWithin(t, stream); err != nil {
				t.Fatalf("Recv: %v", err)
			}
			// A run ends once; callers should not have to guess when to
			// stop reading.
			if _, err := recvWithin(t, stream); !errors.Is(err, io.EOF) {
				t.Errorf("second Recv = %v, want io.EOF after the terminal event", err)
			}
		})
	}
}

func TestEventStreamReconnectsAndHistoryRemainsTheAuthority(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(2)
	rt := newRuntime(t, f, ws)
	mustDiscover(t, rt)

	stream, err := rt.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()

	ws.conn(0).waitDialed(t)
	ws.conn(0).fail(errors.New("connection reset by peer"))

	ws.conn(1).waitDialed(t)
	ws.conn(1).send(t, "execution_start", map[string]any{"prompt_id": testRunID})

	event, err := recvWithin(t, stream)
	if err != nil {
		t.Fatalf("Recv after reconnect: %v", err)
	}
	if event.Type != runtime.WorkflowEventStarted {
		t.Errorf("Type = %q, want events flowing again after the reconnect", event.Type)
	}
	if reconnects, _, _ := rt.EventStreamStats(); reconnects != 1 {
		t.Errorf("reconnects = %d, want 1", reconnects)
	}
	if got := ws.dialCount(); got != 2 {
		t.Errorf("dials = %d, want 2", got)
	}

	// Events that happened while the connection was down are simply gone,
	// which is why the final answer is read from History rather than
	// assembled from events.
	f.setHistory(testRunID, historySuccess(nil))
	status, err := rt.Status(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != runtime.WorkflowSucceeded {
		t.Errorf("State = %q, want %q from History", status.State, runtime.WorkflowSucceeded)
	}
}

// --- status -----------------------------------------------------------

func TestStatusFromQueue(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))

	f.setQueue([]string{otherRunID}, []string{"prompt-0", testRunID})
	status, err := rt.Status(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != runtime.WorkflowPending || status.QueuePosition != 2 {
		t.Errorf("Status = %+v, want pending at position 2", status)
	}

	f.setQueue([]string{testRunID}, nil)
	status, err = rt.Status(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != runtime.WorkflowRunning {
		t.Errorf("State = %q, want %q", status.State, runtime.WorkflowRunning)
	}
}

func TestStatusFromHistory(t *testing.T) {
	tests := []struct {
		name        string
		entry       map[string]any
		wantState   runtime.WorkflowState
		wantSummary string
	}{
		{
			name:      "success",
			entry:     historySuccess(nil),
			wantState: runtime.WorkflowSucceeded,
		},
		{
			name: "error carries the failing node",
			entry: map[string]any{"status": map[string]any{
				"status_str": "error",
				"completed":  false,
				"messages": []any{
					[]any{"execution_error", map[string]any{
						"node_id": "3", "node_type": "KSampler",
						"exception_type": "RuntimeError", "exception_message": "CUDA out of memory",
					}},
				},
			}},
			wantState:   runtime.WorkflowFailed,
			wantSummary: "CUDA out of memory",
		},
		{
			// ComfyUI records an interruption as a non-success run; only the
			// message log tells it apart from a real failure.
			name: "interruption is not a failure",
			entry: map[string]any{"status": map[string]any{
				"status_str": "error",
				"completed":  false,
				"messages":   []any{[]any{"execution_interrupted", map[string]any{"prompt_id": testRunID}}},
			}},
			wantState: runtime.WorkflowCancelled,
		},
		{
			name: "error without a message log still fails",
			entry: map[string]any{"status": map[string]any{
				"status_str": "error", "completed": true, "messages": []any{},
			}},
			wantState:   runtime.WorkflowFailed,
			wantSummary: "see the run's history",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeComfy(t)
			rt := newRuntime(t, f, newScriptedWS(1))
			f.setHistory(testRunID, tt.entry)

			status, err := rt.Status(context.Background(), testRunID)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.State != tt.wantState {
				t.Errorf("State = %q, want %q", status.State, tt.wantState)
			}
			if tt.wantSummary != "" && !strings.Contains(status.ErrorSummary, tt.wantSummary) {
				t.Errorf("ErrorSummary = %q, want it to contain %q", status.ErrorSummary, tt.wantSummary)
			}
		})
	}
}

func TestStatusForAnUnknownRunDoesNotReportPendingForever(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))

	// ComfyUI keeps history in memory, so a restarted server forgets runs
	// entirely. Reporting pending would leave the caller waiting on a run
	// that no longer exists.
	status, err := rt.Status(context.Background(), "long-gone")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != runtime.WorkflowFailed {
		t.Errorf("State = %q, want %q", status.State, runtime.WorkflowFailed)
	}
	if !strings.Contains(status.ErrorSummary, "restarted") {
		t.Errorf("ErrorSummary = %q, want it to explain what happened", status.ErrorSummary)
	}
}

// --- cancel -----------------------------------------------------------

func TestCancelRemovesAPendingRunFromTheQueue(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))
	mustDiscover(t, rt)
	f.setQueue([]string{otherRunID}, []string{testRunID})

	if err := rt.Cancel(context.Background(), testRunID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := f.deleted(); len(got) != 1 || got[0] != testRunID {
		t.Errorf("deleted = %v, want exactly the target run", got)
	}
	// Deleting by id is unambiguous; interrupting is not, and must not
	// happen here.
	if f.wasInterrupted() {
		t.Error("a pending run was cancelled by interrupting the server")
	}
}

func TestCancelRefusesToInterruptOnASharedInstance(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1)) // exclusive defaults to false
	mustDiscover(t, rt)
	f.setQueue([]string{testRunID}, nil)

	err := rt.Cancel(context.Background(), testRunID)
	requireErrorCode(t, err, runtime.ErrorCancelUnsupported)
	if !errors.Is(err, runtime.ErrCancelUnsupported) {
		t.Errorf("expected ErrCancelUnsupported, got %v", err)
	}
	// /interrupt takes no run id: on a shared instance it would stop
	// whatever the server is doing for someone else.
	if f.wasInterrupted() {
		t.Fatal("the adapter interrupted a shared ComfyUI instance")
	}
}

func TestCancelInterruptsOnlyOnAnExclusiveInstance(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1), func(c *runtime.Config) { c.Exclusive = true })
	mustDiscover(t, rt)
	f.setQueue([]string{testRunID}, nil)

	if err := rt.Cancel(context.Background(), testRunID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !f.wasInterrupted() {
		t.Error("the running job was never interrupted")
	}
}

func TestCancelRefusesWhenTheRunningJobCannotBeIdentified(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1), func(c *runtime.Config) { c.Exclusive = true })
	mustDiscover(t, rt)
	// Two running jobs: an interrupt could hit either one.
	f.setQueue([]string{testRunID, otherRunID}, nil)

	err := rt.Cancel(context.Background(), testRunID)
	requireErrorCode(t, err, runtime.ErrorCancelUnsupported)
	if f.wasInterrupted() {
		t.Fatal("the adapter interrupted without knowing which job was running")
	}
}

func TestCancelRefusesForRunsThatAreNoLongerCancellable(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*fakeComfy)
		wantPhras string
	}{
		{
			name:      "already finished",
			setup:     func(f *fakeComfy) { f.setHistory(testRunID, historySuccess(nil)) },
			wantPhras: "already finished",
		},
		{
			name:      "never heard of it",
			setup:     func(f *fakeComfy) {},
			wantPhras: "neither the queue nor the history",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeComfy(t)
			rt := newRuntime(t, f, newScriptedWS(1))
			mustDiscover(t, rt)
			tt.setup(f)

			err := rt.Cancel(context.Background(), testRunID)
			rerr := requireErrorCode(t, err, runtime.ErrorCancelUnsupported)
			if !strings.Contains(rerr.Message, tt.wantPhras) {
				t.Errorf("Message = %q, want it to contain %q", rerr.Message, tt.wantPhras)
			}
			if f.wasInterrupted() || len(f.deleted()) != 0 {
				t.Error("the adapter took a cancellation action it should have refused")
			}
		})
	}
}

// --- artifacts --------------------------------------------------------

func TestOpenArtifactStreamsTheBody(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))
	mustDiscover(t, rt)

	artifact, err := rt.OpenArtifact(context.Background(), runtime.ArtifactRef{
		RunID: testRunID, Filename: "out_00001_.png", Subfolder: "batch", Type: "output",
	})
	if err != nil {
		t.Fatalf("OpenArtifact: %v", err)
	}
	defer artifact.Body.Close()

	body, err := io.ReadAll(artifact.Body)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(body) != "PNGDATA" {
		t.Errorf("body = %q, want the artifact bytes", body)
	}
	if artifact.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want it passed through", artifact.ContentType)
	}

	var viewCall string
	for _, call := range f.recorded() {
		if strings.HasPrefix(call, "/view?") {
			viewCall = call
		}
	}
	for _, want := range []string{"filename=out_00001_.png", "subfolder=batch", "type=output"} {
		if !strings.Contains(viewCall, want) {
			t.Errorf("/view call %q is missing %q", viewCall, want)
		}
	}
}

func TestOpenArtifactRejectsAnOversizedFile(t *testing.T) {
	f := newFakeComfy(t, func(f *fakeComfy) { f.artifactSize = 1 << 40 })
	rt := newRuntime(t, f, newScriptedWS(1))
	mustDiscover(t, rt)

	_, err := rt.OpenArtifact(context.Background(), runtime.ArtifactRef{Filename: "huge.mp4"})
	requireErrorCode(t, err, runtime.ErrorResponseTooLarge)
}

func TestOpenArtifactRequiresAFilename(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))
	mustDiscover(t, rt)

	_, err := rt.OpenArtifact(context.Background(), runtime.ArtifactRef{RunID: testRunID})
	requireErrorCode(t, err, runtime.ErrorInvalidConfig)
}

func TestArtifactsAreReadFromHistory(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))
	f.setHistory(testRunID, historySuccess(map[string]any{
		"9": map[string]any{
			"images": []map[string]any{
				{"filename": "out_00001_.png", "subfolder": "", "type": "output"},
				{"filename": "out_00002_.png", "subfolder": "", "type": "output"},
			},
		},
		// A custom node can name its output list anything; the shape of the
		// entries is what identifies a file.
		"12": map[string]any{
			"gifs":     []map[string]any{{"filename": "anim.webp", "subfolder": "vids", "type": "output"}},
			"text":     []string{"not a file"},
			"metadata": map[string]any{"seed": 42},
		},
	}))

	refs, err := rt.Artifacts(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("refs = %+v, want three files", refs)
	}
	// Ordering is by node id as a string, so node "12" precedes node "9".
	// The point is that it is stable, not that it matches execution order,
	// which History does not report.
	got := []string{refs[0].Filename, refs[1].Filename, refs[2].Filename}
	want := []string{"anim.webp", "out_00001_.png", "out_00002_.png"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("filenames = %v, want %v", got, want)
	}
	if refs[0].Subfolder != "vids" {
		t.Errorf("refs[0] = %+v, want the custom node's subfolder preserved", refs[0])
	}
	for _, ref := range refs {
		if ref.RunID != testRunID {
			t.Errorf("ref %+v is missing its run id", ref)
		}
	}
}

func TestArtifactsForAnUnknownRunIsEmpty(t *testing.T) {
	f := newFakeComfy(t)
	rt := newRuntime(t, f, newScriptedWS(1))

	refs, err := rt.Artifacts(context.Background(), "long-gone")
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("refs = %+v, want none", refs)
	}
}

// --- lifecycle --------------------------------------------------------

func TestCloseStopsTheEventStreamAndRejectsFurtherCalls(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(1)
	rt := newRuntime(t, f, ws)
	mustDiscover(t, rt)

	stream, err := rt.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ws.conn(0).waitDialed(t)

	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if ws.conn(0).closeCount() == 0 {
		t.Error("the WebSocket connection was left open")
	}
	// A subscriber must not be left blocked on a stream that can never
	// produce another event.
	if _, err := recvWithin(t, stream); err == nil {
		t.Error("Recv returned an event after the runtime was closed")
	}
	_ = stream.Close()

	_, err = rt.Submit(context.Background(), runtime.WorkflowRequest{Template: workflowTemplate()})
	requireErrorCode(t, err, runtime.ErrorClosed)
	if _, err := rt.Status(context.Background(), testRunID); err == nil {
		t.Error("Status after Close: expected an error")
	}
	if err := rt.Cancel(context.Background(), testRunID); err == nil {
		t.Error("Cancel after Close: expected an error")
	}
}

func TestClosingASubscriberStopsDeliveryWithoutAffectingOthers(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(1)
	rt := newRuntime(t, f, ws)
	mustDiscover(t, rt)

	leaving, err := rt.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	staying, err := rt.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer staying.Close()

	if err := leaving.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ws.conn(0).waitDialed(t)
	ws.conn(0).send(t, "execution_start", map[string]any{"prompt_id": testRunID})

	event, err := recvWithin(t, staying)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if event.Type != runtime.WorkflowEventStarted {
		t.Errorf("Type = %q, want the remaining subscriber still served", event.Type)
	}
}

func TestNewRequiresAWebSocketDialer(t *testing.T) {
	f := newFakeComfy(t)
	_, err := comfyui.New(runtime.Config{
		ID:      "no-dialer",
		Kind:    runtime.KindComfyUI,
		BaseURL: f.server.URL,
	}.Normalize(), runtime.Dependencies{HTTPClient: f.server.Client()})
	rerr := requireErrorCode(t, err, runtime.ErrorInvalidConfig)
	if !strings.Contains(rerr.Message, "WSDialer") {
		t.Errorf("Message = %q, want it to name the missing dependency", rerr.Message)
	}
}

func TestNewRejectsAnotherKind(t *testing.T) {
	f := newFakeComfy(t)
	_, err := comfyui.New(runtime.Config{
		ID:      "wrong-kind",
		Kind:    runtime.KindVLLM,
		BaseURL: f.server.URL,
	}.Normalize(), runtime.Dependencies{HTTPClient: f.server.Client(), WSDialer: newScriptedWS(0)})
	requireErrorCode(t, err, runtime.ErrorInvalidConfig)
}

func TestNewIsUsableAsARegistryFactory(t *testing.T) {
	f := newFakeComfy(t)
	registry := runtime.NewRegistry()
	if err := registry.Register(runtime.KindComfyUI, comfyui.New); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rt, err := registry.Create(runtime.Config{
		ID:      "registered-comfyui",
		Kind:    runtime.KindComfyUI,
		BaseURL: f.server.URL,
	}, runtime.Dependencies{
		HTTPClient: f.server.Client(),
		WSDialer:   newScriptedWS(1),
		Clock:      runtimetest.NewClock(time.Unix(1700000000, 0)),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:    stubMetrics{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer rt.Close()
	if _, ok := rt.(runtime.WorkflowRuntime); !ok {
		t.Fatalf("registry produced %T, which is not a WorkflowRuntime", rt)
	}
	if _, ok := rt.(runtime.InferenceRuntime); ok {
		t.Error("the workflow adapter also satisfies InferenceRuntime, which it must not")
	}
}

// stubMetrics satisfies runtime.Dependencies' Metrics requirement for the
// Registry path without recording anything.
type stubMetrics struct{}

func (stubMetrics) Counter(string, map[string]string) runtime.Counter     { return stubInstrument{} }
func (stubMetrics) Gauge(string, map[string]string) runtime.Gauge         { return stubInstrument{} }
func (stubMetrics) Histogram(string, map[string]string) runtime.Histogram { return stubInstrument{} }

type stubInstrument struct{}

func (stubInstrument) Add(float64)     {}
func (stubInstrument) Set(float64)     {}
func (stubInstrument) Observe(float64) {}

// waitForPendingTimer blocks until the adapter has registered a timer on
// the injected clock. The polling here synchronizes with a goroutine; it
// never advances time, which only Clock.Advance does.
func waitForPendingTimer(t *testing.T, clock *runtimetest.Clock) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if clock.PendingTimers() > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the adapter never scheduled a reconnect backoff")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRepeatedReconnectFailuresBackOffInsteadOfSpinning(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(3)
	// The second dial fails, so the loop has to try a third time.
	ws.dialErrs = []error{nil, errors.New("connection refused"), nil}

	clock := runtimetest.NewClock(time.Unix(1700000000, 0))
	rt, err := comfyui.New(runtime.Config{
		ID:      "backoff-comfyui",
		Kind:    runtime.KindComfyUI,
		BaseURL: f.server.URL,
	}.Normalize(), runtime.Dependencies{
		HTTPClient: f.server.Client(),
		WSDialer:   ws,
		Clock:      clock,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()
	adapter := rt.(*comfyui.Runtime)
	mustDiscover(t, adapter)

	stream, err := adapter.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()

	ws.conn(0).waitDialed(t)
	ws.conn(0).fail(errors.New("connection reset by peer"))

	// A server that is down would otherwise be hammered as fast as the loop
	// can dial; the second failure must put the loop on the clock.
	waitForPendingTimer(t, clock)
	if got := ws.dialCount(); got != 2 {
		t.Fatalf("dials = %d before the backoff elapsed, want 2", got)
	}

	clock.Advance(time.Second)
	ws.conn(2).waitDialed(t)
	ws.conn(2).send(t, "execution_start", map[string]any{"prompt_id": testRunID})

	event, err := recvWithin(t, stream)
	if err != nil {
		t.Fatalf("Recv after the backoff: %v", err)
	}
	if event.Type != runtime.WorkflowEventStarted {
		t.Errorf("Type = %q, want the stream recovered", event.Type)
	}
}

func TestASlowSubscriberDropsEventsInsteadOfStallingTheInstance(t *testing.T) {
	f := newFakeComfy(t)
	ws := newScriptedWS(1)
	rt := newRuntime(t, f, ws)
	mustDiscover(t, rt)

	// This subscriber never reads.
	stalled, err := rt.Subscribe(context.Background(), testRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stalled.Close()

	attentive, err := rt.Subscribe(context.Background(), otherRunID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer attentive.Close()

	conn := ws.conn(0)
	conn.waitDialed(t)
	for i := 0; i < 400; i++ {
		conn.send(t, "progress", map[string]any{"prompt_id": testRunID, "value": i, "max": 400})
	}
	conn.send(t, "execution_start", map[string]any{"prompt_id": otherRunID})

	// One caller that stops reading must not stall the connection every
	// other run on this instance shares.
	event, err := recvWithin(t, attentive)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if event.RunID != otherRunID {
		t.Errorf("RunID = %q, want the attentive subscriber still served", event.RunID)
	}
	if _, _, dropped := rt.EventStreamStats(); dropped == 0 {
		t.Error("dropped events = 0, want the stalled subscriber to have lost events")
	}
}

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/flowdef"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// fakeFlowStore is an in-memory StudioFlowStore scoped by owner. It
// validates definitions as the control plane does, and can be told to refuse
// starts and step writes.
//
// fakeFlowStore 是按所有者限定的内存 StudioFlowStore。它像控制面一样校验定义，并可被
// 设置为拒绝启动运行与写入步骤。
type fakeFlowStore struct {
	mu        sync.Mutex
	flows     map[string]httpapi.StudioFlow
	owners    map[string]httpapi.StudioOwner
	runs      map[string]httpapi.StudioRun
	runOwners map[string]httpapi.StudioOwner
	next      int
	startErr  error
	stepErr   error
	stepGate  chan struct{} // when set, step writes wait for it
	finished  chan httpapi.StudioRunFinish
}

func newFakeFlowStore() *fakeFlowStore {
	return &fakeFlowStore{
		flows: map[string]httpapi.StudioFlow{}, owners: map[string]httpapi.StudioOwner{},
		runs: map[string]httpapi.StudioRun{}, runOwners: map[string]httpapi.StudioOwner{},
		finished: make(chan httpapi.StudioRunFinish, 8),
	}
}

func validFlow(raw json.RawMessage) error {
	def, err := flowdef.Decode(raw)
	if err != nil {
		return err
	}
	return flowdef.Validate(def)
}

func (f *fakeFlowStore) CreateFlow(_ context.Context, owner httpapi.StudioOwner, name string, definition json.RawMessage) (httpapi.StudioFlow, error) {
	if err := validFlow(definition); err != nil {
		return httpapi.StudioFlow{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	flow := httpapi.StudioFlow{ID: fmt.Sprintf("flw_%d", f.next), Name: name, Version: 1, Definition: definition, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.flows[flow.ID], f.owners[flow.ID] = flow, owner
	return flow, nil
}

func (f *fakeFlowStore) ListFlows(_ context.Context, owner httpapi.StudioOwner, _ string, _ int) (httpapi.StudioFlowPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var page httpapi.StudioFlowPage
	for id, flow := range f.flows {
		if f.owners[id] == owner {
			flow.Definition = nil
			page.Flows = append(page.Flows, flow)
		}
	}
	return page, nil
}

func (f *fakeFlowStore) GetFlow(_ context.Context, owner httpapi.StudioOwner, id string) (httpapi.StudioFlow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if flow, ok := f.flows[id]; ok && f.owners[id] == owner {
		return flow, nil
	}
	return httpapi.StudioFlow{}, httpapi.ErrStudioNotFound
}

func (f *fakeFlowStore) UpdateFlow(_ context.Context, owner httpapi.StudioOwner, id string, patch httpapi.StudioFlowPatch) (httpapi.StudioFlow, error) {
	if patch.Definition != nil {
		if err := validFlow(patch.Definition); err != nil {
			return httpapi.StudioFlow{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	flow, ok := f.flows[id]
	if !ok || f.owners[id] != owner {
		return httpapi.StudioFlow{}, httpapi.ErrStudioNotFound
	}
	if patch.BaseVersion > 0 && patch.BaseVersion != flow.Version {
		return httpapi.StudioFlow{}, httpapi.ErrStudioFlowChanged
	}
	if patch.Name != nil {
		flow.Name = *patch.Name
	}
	if patch.Definition != nil {
		flow.Definition, flow.Version = patch.Definition, flow.Version+1
	}
	f.flows[id] = flow
	return flow, nil
}

func (f *fakeFlowStore) DeleteFlow(_ context.Context, owner httpapi.StudioOwner, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.flows[id]; !ok || f.owners[id] != owner {
		return httpapi.ErrStudioNotFound
	}
	delete(f.flows, id)
	return nil
}

func (f *fakeFlowStore) GetFlowVersion(ctx context.Context, owner httpapi.StudioOwner, id string, version int64) (httpapi.StudioFlowVersion, error) {
	flow, err := f.GetFlow(ctx, owner, id)
	if err != nil || version != flow.Version {
		return httpapi.StudioFlowVersion{}, httpapi.ErrStudioNotFound
	}
	return httpapi.StudioFlowVersion{Version: version, Definition: flow.Definition, CreatedAt: flow.UpdatedAt}, nil
}

func (f *fakeFlowStore) ListRuns(_ context.Context, owner httpapi.StudioOwner, flowID, _ string, _ int) (httpapi.StudioRunPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var page httpapi.StudioRunPage
	for id, run := range f.runs {
		if run.FlowID == flowID && f.runOwners[id] == owner {
			run.Steps, run.Inputs, run.Outputs = nil, nil, nil
			page.Runs = append(page.Runs, run)
		}
	}
	return page, nil
}

func (f *fakeFlowStore) StartRun(_ context.Context, owner httpapi.StudioOwner, start httpapi.StudioRunStart) (httpapi.StudioRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return httpapi.StudioRun{}, f.startErr
	}
	run := httpapi.StudioRun{
		ID: start.ID, FlowID: start.FlowID, FlowVersion: start.FlowVersion, Status: "running", Inputs: start.Inputs,
		ReplicaID: start.ReplicaID, CreatedAt: time.Now(),
	}
	f.runs[run.ID], f.runOwners[run.ID] = run, owner
	return run, nil
}

func (f *fakeFlowStore) GetRun(_ context.Context, owner httpapi.StudioOwner, id string) (httpapi.StudioRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if run, ok := f.runs[id]; ok && f.runOwners[id] == owner {
		run.Steps = append([]httpapi.StudioRunStep(nil), run.Steps...)
		return run, nil
	}
	return httpapi.StudioRun{}, httpapi.ErrStudioNotFound
}

func (f *fakeFlowStore) RenewRun(context.Context, httpapi.StudioOwner, string, string, time.Duration) error {
	return nil
}

func (f *fakeFlowStore) RecordStep(_ context.Context, owner httpapi.StudioOwner, runID, replica string, step httpapi.StudioRunStep) error {
	if f.stepGate != nil {
		<-f.stepGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok || f.runOwners[runID] != owner {
		return httpapi.ErrStudioNotFound
	}
	if f.stepErr != nil {
		return f.stepErr
	}
	if replica != run.ReplicaID {
		return httpapi.ErrStudioRunFinished
	}
	run.Steps = append(run.Steps, step)
	f.runs[runID] = run
	return nil
}

func (f *fakeFlowStore) FinishRun(_ context.Context, owner httpapi.StudioOwner, id, replica string, finish httpapi.StudioRunFinish) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok || f.runOwners[id] != owner || replica != run.ReplicaID {
		return httpapi.ErrStudioNotFound
	}
	run.Status, run.ErrorCode, run.FailedNode, run.Outputs = finish.Status, finish.ErrorCode, finish.FailedNode, finish.Outputs
	run.PromptTokens, run.CompletionTokens = finish.PromptTokens, finish.CompletionTokens
	now := time.Now()
	run.FinishedAt = &now
	f.runs[id] = run
	f.finished <- finish
	return nil
}

// chainFlow drafts with one model call and polishes the draft with another.
//
//	start → draft → polish → end
const chainFlow = `{
  "inputs": [{"name": "topic", "type": "text", "required": true}],
  "nodes": [
    {"id": "start", "type": "input", "position": {"x": 0, "y": 0}},
    {"id": "draft", "type": "llm", "config": {"model": "qwen3:8b", "prompt": "Write about {{inputs.topic}}"}, "position": {"x": 1, "y": 0}},
    {"id": "polish", "type": "llm", "config": {"model": "qwen3:8b", "system": "Be brief.", "prompt": "Polish: {{draft.text}}"}, "position": {"x": 2, "y": 0}},
    {"id": "end", "type": "output", "position": {"x": 3, "y": 0}}
  ],
  "edges": [{"from": "start", "to": "draft"}, {"from": "draft", "to": "polish"}, {"from": "polish", "to": "end"}],
  "outputs": [{"name": "answer", "ref": "{{polish.text}}"}]
}`

// fakeUsageLedger collects pushed usage records.
type fakeUsageLedger struct {
	mu      sync.Mutex
	records []httpapi.UsageRecord
}

func (l *fakeUsageLedger) PushUsageRecords(_ context.Context, records []httpapi.UsageRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, records...)
	return nil
}

func (l *fakeUsageLedger) snapshot() []httpapi.UsageRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]httpapi.UsageRecord(nil), l.records...)
}

func flowServer(t *testing.T, store *fakeFlowStore, cfg httpapi.Config, handle gatewaytest.SlotHandler) string {
	t.Helper()
	cfg.StudioFlows = store
	if cfg.StudioSessions == nil {
		cfg.StudioSessions = acceptingSessions()
	}
	srv, h := newServer(t, cfg)
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), handle)
	return srv.URL
}

func createFlow(t *testing.T, url, definition string) string {
	t.Helper()
	resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/flows", `{"name":"润色","definition":`+definition+`}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create flow: %d %s", resp.StatusCode, raw)
	}
	var created struct{ ID string }
	_ = json.Unmarshal(raw, &created)
	return created.ID
}

func startRun(t *testing.T, url, flowID, body string) string {
	t.Helper()
	resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/flows/"+flowID+"/runs", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start run: %d %s", resp.StatusCode, raw)
	}
	var run struct{ ID, Status string }
	_ = json.Unmarshal(raw, &run)
	if !strings.HasPrefix(run.ID, "frn_") || run.Status != "running" {
		t.Fatalf("started run = %s", raw)
	}
	return run.ID
}

func watchRun(t *testing.T, url, runID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/studio/v1/runs/"+runID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+goodSession)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return resp
}

func frameOf(frames []studioFrame, event, node string) (studioFrame, bool) {
	for _, f := range frames {
		if f.Event == event && (node == "" || f.Data["node_id"] == node) {
			return f, true
		}
	}
	return studioFrame{}, false
}

// TestStudioFlowRunExecutesStreamsAndPersists is the main path: a saved flow
// runs on this replica through the in-process chat path, a watcher sees a
// snapshot then the live progress, every step and the end are stored, and
// each model call is accounted as a request of its own.
//
// TestStudioFlowRunExecutesStreamsAndPersists 是主路径：已保存的编排在本副本上经进程内
// 聊天路径运行，观察者先看到快照、再看到实时进度，每个步骤与结束都被存储，每次模型调用
// 都作为独立的请求计量。
func TestStudioFlowRunExecutesStreamsAndPersists(t *testing.T) {
	store := newFakeFlowStore()
	ledger := &fakeUsageLedger{}
	gate := make(chan struct{})
	seen := make(chan runtime.ChatRequest, 4)
	answer := recordingChat(seen, textDelta("Hello "), textDelta("world"),
		runtime.ChatEvent{ID: "c", FinishReason: "stop", Usage: &runtime.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}})
	url := flowServer(t, store, httpapi.Config{UsageLedgerClient: ledger, UsageLedgerFlushInterval: 10 * time.Millisecond},
		func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
			<-gate
			return answer(req, body, reply)
		})

	flowID := createFlow(t, url, chainFlow)
	runID := startRun(t, url, flowID, `{"inputs":{"topic":"cats"}}`)
	stream := watchRun(t, url, runID)
	first := readFrames(t, stream, func(studioFrame) bool { return true })
	if len(first) != 1 || first[0].Event != "run_snapshot" {
		t.Fatalf("first frame = %v, want run_snapshot", events(first))
	}
	close(gate)
	frames := readFrames(t, stream, func(f studioFrame) bool { return f.Event == "run_finished" })

	if _, ok := frameOf(frames, "node_delta", "draft"); !ok {
		t.Errorf("frames = %v, want draft's text streamed", events(frames))
	}
	finished := frames[len(frames)-1]
	if finished.Event != "run_finished" || finished.Data["status"] != "succeeded" {
		t.Fatalf("frames = %v, last = %v", events(frames), finished.Data)
	}
	if outputs := finished.Data["outputs"].(map[string]any); outputs["answer"] != "Hello world" {
		t.Errorf("outputs = %v", outputs)
	}

	select {
	case end := <-store.finished:
		if end.Status != "succeeded" || end.Outputs["answer"] != "Hello world" || end.PromptTokens != 10 || end.CompletionTokens != 4 {
			t.Errorf("stored end = %+v", end)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run's end was not stored")
	}
	run, _ := store.GetRun(context.Background(), httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_1"}, runID)
	if len(run.Steps) != 4 || run.Steps[1].NodeID != "draft" || run.Steps[1].Outputs["text"] != "Hello world" || run.Steps[1].Inputs["prompt"] != "Write about cats" {
		t.Fatalf("stored steps = %+v", run.Steps)
	}

	draft, polish := <-seen, <-seen
	if draft.Messages[0].Content != "Write about cats" || polish.Messages[0].Role != "system" || polish.Messages[1].Content != "Polish: Hello world" {
		t.Errorf("dispatched = %+v then %+v", draft.Messages, polish.Messages)
	}

	gatewaytest.WaitFor(t, "both calls to reach the usage ledger", func() bool { return len(ledger.snapshot()) == 2 })
	records := ledger.snapshot()
	if records[0].RequestID == records[1].RequestID || records[0].UserID != "usr_1" || records[0].TenantID != "tnt_1" {
		t.Errorf("usage records = %+v, want one per call, each with its own request id, owned by the member", records)
	}

	resp, raw := studioDo(t, http.MethodGet, url+"/studio/v1/runs/"+runID, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"status":"succeeded"`) {
		t.Errorf("GET run = %d %s", resp.StatusCode, raw)
	}
}

// TestStudioFlowRunRefusesBeforeStarting: bad input values, a model that is
// not available, an exhausted balance and the run limit are all refused
// before a run is stored, with what the form or editor needs to show.
//
// TestStudioFlowRunRefusesBeforeStarting：输入值有误、模型不可用、余额耗尽与运行数上限，
// 都在存储运行之前被拒绝，并附上表单或编辑器展示所需的信息。
func TestStudioFlowRunRefusesBeforeStarting(t *testing.T) {
	store := newFakeFlowStore()
	blocked := &fakeSessions{answer: func(token string) (httpapi.Identity, error) {
		if token == "eyJ.blocked" {
			return httpapi.Identity{TenantID: "tnt_1", UserID: "usr_1", Role: "member", BillingBlocked: true}, nil
		}
		return acceptingSessions().answer(token)
	}}
	url := flowServer(t, store, httpapi.Config{StudioSessions: blocked}, streamingEventsHandler(textDelta("x")))

	resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/flows", `{"name":"坏的","definition":`+strings.Replace(chainFlow, "{{draft.text}}", "{{ghost.text}}", 1)+`}`)
	var refusal struct {
		Error  struct{ Code string }
		Issues []flowdef.Issue
	}
	_ = json.Unmarshal(raw, &refusal)
	if resp.StatusCode != http.StatusBadRequest || refusal.Error.Code != "invalid_flow" || len(refusal.Issues) == 0 || refusal.Issues[0].Node != "polish" {
		t.Fatalf("saving a broken flow = %d %s, want 400 invalid_flow located on polish", resp.StatusCode, raw)
	}

	flowID := createFlow(t, url, chainFlow)
	otherModel := createFlow(t, url, strings.Replace(chainFlow, `"model": "qwen3:8b", "system"`, `"model": "gone:1b", "system"`, 1))
	for _, tc := range []struct {
		name, flow, body string
		status           int
		code, node       string
	}{
		{"missing input", flowID, `{"inputs":{}}`, http.StatusBadRequest, "invalid_inputs", ""},
		{"undeclared input", flowID, `{"inputs":{"topic":"x","topik":"y"}}`, http.StatusBadRequest, "invalid_inputs", ""},
		{"model not available", otherModel, `{"inputs":{"topic":"x"}}`, http.StatusBadRequest, "model_unavailable", "polish"},
		{"unknown flow", "flw_404", `{"inputs":{"topic":"x"}}`, http.StatusNotFound, "not_found", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/flows/"+tc.flow+"/runs", tc.body)
			var got struct {
				Error  struct{ Code string }
				Issues []flowdef.Issue
			}
			_ = json.Unmarshal(raw, &got)
			if resp.StatusCode != tc.status || got.Error.Code != tc.code || (tc.node != "" && (len(got.Issues) != 1 || got.Issues[0].Node != tc.node)) {
				t.Fatalf("= %d %s, want %d %s", resp.StatusCode, raw, tc.status, tc.code)
			}
		})
	}

	req, _ := http.NewRequest(http.MethodPost, url+"/studio/v1/flows/"+flowID+"/runs", strings.NewReader(`{"inputs":{"topic":"x"}}`))
	req.Header.Set("Authorization", "Bearer eyJ.blocked")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("run with an exhausted balance = %v %v, want 402", resp.StatusCode, err)
	} else {
		resp.Body.Close()
	}
	req, _ = http.NewRequest(http.MethodGet, url+"/studio/v1/flows/"+flowID, nil)
	req.Header.Set("Authorization", "Bearer eyJ.blocked")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("reading a flow with an exhausted balance = %v %v, want 200", resp.StatusCode, err)
	} else {
		resp.Body.Close()
	}

	store.mu.Lock()
	store.startErr = httpapi.ErrStudioRunLimit
	store.mu.Unlock()
	if resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/flows/"+flowID+"/runs", `{"inputs":{"topic":"x"}}`); resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(raw), "run_limit") {
		t.Errorf("run past the limit = %d %s, want 429 run_limit", resp.StatusCode, raw)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.runs) != 0 {
		t.Errorf("refused runs were stored: %v", store.runs)
	}
}

// TestStudioFlowRunCancel: the owner cancels a run on the replica executing
// it; the model call stops and the run is stored as canceled.
//
// TestStudioFlowRunCancel：所有者在执行运行的副本上取消它；模型调用停止，运行以 canceled
// 存储。
func TestStudioFlowRunCancel(t *testing.T) {
	store := newFakeFlowStore()
	started := make(chan struct{})
	url := flowServer(t, store, httpapi.Config{}, blockingStreamHandler(started))
	runID := startRun(t, url, createFlow(t, url, chainFlow), `{"inputs":{"topic":"cats"}}`)
	<-started

	resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/runs/"+runID+"/cancel", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel = %d %s", resp.StatusCode, raw)
	}
	select {
	case end := <-store.finished:
		if end.Status != "canceled" {
			t.Errorf("stored end = %+v, want canceled", end)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled run's end was not stored")
	}
	run, _ := store.GetRun(context.Background(), httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_1"}, runID)
	if last := run.Steps[len(run.Steps)-1]; last.NodeID != "draft" || last.Status != "canceled" {
		t.Errorf("stored steps = %+v, want draft canceled", run.Steps)
	}
	if resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/runs/"+runID+"/cancel", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "run_finished") {
		t.Errorf("canceling an ended run = %d %s, want 409 run_finished", resp.StatusCode, raw)
	}
}

// TestStudioFlowRunStopsWhenItIsNoLongerHeld: when the control plane refuses
// a write because the run is no longer this replica's, the run stops here
// and no end is written over whatever the control plane decided.
//
// TestStudioFlowRunStopsWhenItIsNoLongerHeld：当控制面因该运行已不属于本副本而拒绝写入时，
// 运行在这里停止，也不会写入终态去覆盖控制面的判定。
func TestStudioFlowRunStopsWhenItIsNoLongerHeld(t *testing.T) {
	store := newFakeFlowStore()
	store.stepErr, store.stepGate = httpapi.ErrStudioRunFinished, make(chan struct{})
	url := flowServer(t, store, httpapi.Config{}, blockingStreamHandler(nil))
	runID := startRun(t, url, createFlow(t, url, chainFlow), `{"inputs":{"topic":"cats"}}`)
	stream := watchRun(t, url, runID)
	readFrames(t, stream, func(f studioFrame) bool { return f.Event == "node_delta" })
	close(store.stepGate)

	frames := readFrames(t, stream, func(f studioFrame) bool { return f.Event == "run_finished" })
	if last := frames[len(frames)-1]; last.Event != "run_finished" || last.Data["status"] != "interrupted" {
		t.Fatalf("frames = %v, want run_finished interrupted", events(frames))
	}
	select {
	case end := <-store.finished:
		t.Errorf("an end was written for a run this replica lost: %+v", end)
	default:
	}
}

// TestStudioFlowEventsOfARunNotHere: a run that is not executing on this
// replica gets its stored snapshot — and its end, when it has ended — and
// cannot be canceled from here.
//
// TestStudioFlowEventsOfARunNotHere：不在本副本执行的运行得到其已存储的快照——已结束时
// 还有结束帧——且不能从这里取消。
func TestStudioFlowEventsOfARunNotHere(t *testing.T) {
	store := newFakeFlowStore()
	owner := httpapi.StudioOwner{TenantID: "tnt_1", UserID: "usr_1"}
	store.runs["frn_done"] = httpapi.StudioRun{ID: "frn_done", FlowID: "flw_x", Status: "succeeded", Outputs: map[string]string{"answer": "42"},
		Steps: []httpapi.StudioRunStep{{NodeID: "start", NodeType: "input", Status: "succeeded"}}}
	store.runs["frn_away"] = httpapi.StudioRun{ID: "frn_away", FlowID: "flw_x", Status: "running", ReplicaID: "other"}
	store.runOwners["frn_done"], store.runOwners["frn_away"] = owner, owner
	url := flowServer(t, store, httpapi.Config{}, streamingEventsHandler(textDelta("x")))

	frames := readFrames(t, watchRun(t, url, "frn_done"), nil)
	if got := strings.Join(events(frames), ","); got != "run_snapshot,run_finished" || frames[1].Data["outputs"].(map[string]any)["answer"] != "42" {
		t.Errorf("ended run frames = %v %v", got, frames)
	}
	if steps := frames[0].Data["run"].(map[string]any)["steps"].([]any); len(steps) != 1 {
		t.Errorf("snapshot steps = %v", steps)
	}
	if got := strings.Join(events(readFrames(t, watchRun(t, url, "frn_away"), nil)), ","); got != "run_snapshot" {
		t.Errorf("run elsewhere frames = %v, want only the snapshot", got)
	}
	if resp, raw := studioDo(t, http.MethodPost, url+"/studio/v1/runs/frn_away/cancel", ""); resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "run_elsewhere") {
		t.Errorf("cancel elsewhere = %d %s", resp.StatusCode, raw)
	}
	if resp, _ := studioDo(t, http.MethodGet, url+"/studio/v1/runs/frn_missing/events", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("events of a missing run = %d, want 404", resp.StatusCode)
	}
}

// TestStudioFlowEndpoints covers saving, reading, listing, renaming with a
// base version, versions and deletion, scoped to the signed-in member.
//
// TestStudioFlowEndpoints 覆盖保存、读取、列表、带基础版本的重命名、版本与删除，均限定在
// 登录成员内。
func TestStudioFlowEndpoints(t *testing.T) {
	store := newFakeFlowStore()
	sessions := &fakeSessions{answer: func(token string) (httpapi.Identity, error) {
		if token == "eyJ.other" {
			return httpapi.Identity{TenantID: "tnt_1", UserID: "usr_2", Role: "member"}, nil
		}
		return acceptingSessions().answer(token)
	}}
	url := flowServer(t, store, httpapi.Config{StudioSessions: sessions}, streamingEventsHandler(textDelta("x")))
	id := createFlow(t, url, chainFlow)

	if resp, raw := studioDo(t, http.MethodGet, url+"/studio/v1/flows/"+id, ""); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"definition"`) {
		t.Errorf("get = %d %s", resp.StatusCode, raw)
	}
	if resp, raw := studioDo(t, http.MethodGet, url+"/studio/v1/flows", ""); resp.StatusCode != http.StatusOK || strings.Contains(string(raw), `"definition"`) || !strings.Contains(string(raw), id) {
		t.Errorf("list = %d %s, want the flow without its definition", resp.StatusCode, raw)
	}
	if resp, raw := studioDo(t, http.MethodPatch, url+"/studio/v1/flows/"+id, `{"definition":`+chainFlow+`,"base_version":1}`); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"version":2`) {
		t.Errorf("save = %d %s", resp.StatusCode, raw)
	}
	if resp, raw := studioDo(t, http.MethodPatch, url+"/studio/v1/flows/"+id, `{"name":"x","base_version":1}`); resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "flow_changed") {
		t.Errorf("stale save = %d %s, want 409 flow_changed", resp.StatusCode, raw)
	}
	if resp, _ := studioDo(t, http.MethodGet, url+"/studio/v1/flows/"+id+"/versions/2", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("version 2 = %d", resp.StatusCode)
	}
	if resp, _ := studioDo(t, http.MethodGet, url+"/studio/v1/flows/"+id+"/versions/zero", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("malformed version = %d, want 404", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, url+"/studio/v1/flows/"+id, nil)
	req.Header.Set("Authorization", "Bearer eyJ.other")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("another member's flow = %v %v, want 404", resp.StatusCode, err)
	} else {
		resp.Body.Close()
	}
	if resp, _ := studioDo(t, http.MethodDelete, url+"/studio/v1/flows/"+id, ""); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d", resp.StatusCode)
	}
	if resp, _ := studioDo(t, http.MethodGet, url+"/studio/v1/flows/bad-id", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("malformed id = %d, want 404", resp.StatusCode)
	}
}

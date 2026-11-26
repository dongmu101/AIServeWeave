package logic_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

// studioFlowJSON is a minimal valid flow: start → draft → end.
const studioFlowJSON = `{
  "inputs": [{"name": "topic", "type": "text", "required": true}],
  "nodes": [
    {"id": "start", "type": "input", "position": {"x": 0, "y": 0}},
    {"id": "draft", "type": "llm", "config": {"model": "qwen3", "prompt": "Write about {{inputs.topic}}"}, "position": {"x": 200, "y": 0}},
    {"id": "end", "type": "output", "position": {"x": 400, "y": 0}}
  ],
  "edges": [{"from": "start", "to": "draft"}, {"from": "draft", "to": "end"}],
  "outputs": [{"name": "answer", "ref": "{{draft.text}}"}]
}`

func newStudioFlowService(t *testing.T) (*logic.Service, *fakeClock, store.StudioOwner) {
	t.Helper()
	clock := newFakeClock()
	return logic.New(memstore.New(), clock), clock, store.StudioOwner{TenantID: model.NewID(model.PrefixTenant), UserID: model.NewID(model.PrefixUser)}
}

func TestStudioFlowSaveValidatesAndVersions(t *testing.T) {
	s, clock, owner := newStudioFlowService(t)
	ctx := context.Background()

	broken := strings.Replace(studioFlowJSON, "{{inputs.topic}}", "{{inputs.subject}}", 1)
	_, _, err := s.CreateStudioFlow(ctx, owner, "摘要", json.RawMessage(broken))
	var invalid *flowdef.ValidationError
	if !errors.As(err, &invalid) || invalid.Issues[0].Node != "draft" || invalid.Issues[0].Code != flowdef.CodeUnknownReference {
		t.Fatalf("saving a broken flow = %v, want the issue located on draft", err)
	}
	if _, _, err := s.CreateStudioFlow(ctx, owner, "  ", json.RawMessage(studioFlowJSON)); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("blank name = %v, want ErrInvalidInput", err)
	}

	f, v, err := s.CreateStudioFlow(ctx, owner, " 摘要 ", json.RawMessage(studioFlowJSON))
	if err != nil || f.Name != "摘要" || f.Version != 1 || v.Version != 1 {
		t.Fatalf("CreateStudioFlow = %+v, %+v, %v", f, v, err)
	}
	var stored flowdef.Definition
	if err := json.Unmarshal([]byte(v.Definition), &stored); err != nil || len(stored.Nodes) != 3 {
		t.Fatalf("stored definition = %q, %v", v.Definition, err)
	}

	// The same flow, spaced differently, is not a new version.
	// 同一个编排换一种排版保存，不是新版本。
	clock.Advance(time.Minute)
	compact, _ := json.Marshal(stored)
	if f, _, err = s.UpdateStudioFlow(ctx, owner, f.ID, logic.StudioFlowPatch{Definition: compact, BaseVersion: 1}); err != nil || f.Version != 1 {
		t.Fatalf("re-saving the same flow = %+v, %v, want version 1 kept", f, err)
	}
	changed := strings.Replace(studioFlowJSON, "Write about", "Summarise", 1)
	if f, v, err = s.UpdateStudioFlow(ctx, owner, f.ID, logic.StudioFlowPatch{Definition: json.RawMessage(changed), BaseVersion: 1}); err != nil || f.Version != 2 || !strings.Contains(v.Definition, "Summarise") {
		t.Fatalf("saving a change = %+v, %v, want version 2", f, err)
	}
	if _, _, err := s.UpdateStudioFlow(ctx, owner, f.ID, logic.StudioFlowPatch{Definition: json.RawMessage(studioFlowJSON), BaseVersion: 1}); !errors.Is(err, logic.ErrStudioFlowChanged) {
		t.Errorf("save from a stale editor = %v, want ErrStudioFlowChanged", err)
	}
	if _, _, err := s.UpdateStudioFlow(ctx, owner, f.ID, logic.StudioFlowPatch{Definition: json.RawMessage(broken)}); !errors.As(err, &invalid) {
		t.Errorf("saving a broken change = %v, want a *flowdef.ValidationError", err)
	}
	if _, _, err := s.UpdateStudioFlow(ctx, owner, f.ID, logic.StudioFlowPatch{}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("empty patch = %v, want ErrInvalidInput", err)
	}
	if old, err := s.GetStudioFlowVersion(ctx, owner, f.ID, 1); err != nil || strings.Contains(old.Definition, "Summarise") {
		t.Errorf("version 1 = %+v, %v, want it unchanged", old, err)
	}
}

func TestStudioFlowListPages(t *testing.T) {
	s, clock, owner := newStudioFlowService(t)
	ctx := context.Background()
	for range 3 {
		if _, _, err := s.CreateStudioFlow(ctx, owner, "f", json.RawMessage(studioFlowJSON)); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
	}
	page, err := s.ListStudioFlows(ctx, owner, "", 2)
	if err != nil || len(page.Flows) != 2 || page.Next == "" {
		t.Fatalf("first page = %d flows, next %q, %v", len(page.Flows), page.Next, err)
	}
	rest, err := s.ListStudioFlows(ctx, owner, page.Next, 2)
	if err != nil || len(rest.Flows) != 1 || rest.Next != "" {
		t.Fatalf("second page = %d flows, next %q, %v", len(rest.Flows), rest.Next, err)
	}
	if _, err := s.ListStudioFlows(ctx, owner, "not-a-cursor", 2); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("bad cursor = %v, want ErrInvalidInput", err)
	}
}

func TestStudioRunLifecycle(t *testing.T) {
	s, clock, owner := newStudioFlowService(t)
	ctx := context.Background()
	f, _, err := s.CreateStudioFlow(ctx, owner, "摘要", json.RawMessage(studioFlowJSON))
	if err != nil {
		t.Fatal(err)
	}
	start := func(id string) (model.StudioFlowRun, error) {
		return s.StartStudioRun(ctx, owner, logic.StudioRunParams{
			ID: id, FlowID: f.ID, FlowVersion: 1, Inputs: json.RawMessage(`{"topic": "cats"}`), ReplicaID: "gw-1", Lease: time.Minute,
		})
	}
	run, err := start(model.NewID(model.PrefixFlowRun))
	if err != nil || run.Status != model.StudioRunRunning || run.Inputs != `{"topic":"cats"}` || !run.LeaseUntil.Equal(clock.Now().Add(time.Minute)) {
		t.Fatalf("StartStudioRun = %+v, %v", run, err)
	}
	if again, err := start(run.ID); err != nil || again.ID != run.ID || !again.CreatedAt.Equal(run.CreatedAt) {
		t.Errorf("retried start = %+v, %v, want the first record", again, err)
	}
	for _, bad := range []logic.StudioRunParams{
		{ID: "run_1", FlowID: f.ID, FlowVersion: 1, ReplicaID: "gw-1", Lease: time.Minute},
		{ID: model.NewID(model.PrefixFlowRun), FlowID: f.ID, FlowVersion: 1, ReplicaID: "gw-1", Lease: time.Hour},
		{ID: model.NewID(model.PrefixFlowRun), FlowID: f.ID, FlowVersion: 1, ReplicaID: "gw-1", Lease: time.Minute, Inputs: json.RawMessage(`["x"]`)},
	} {
		if _, err := s.StartStudioRun(ctx, owner, bad); !errors.Is(err, logic.ErrInvalidInput) {
			t.Errorf("StartStudioRun(%+v) = %v, want ErrInvalidInput", bad, err)
		}
	}

	step := logic.StudioStepParams{
		ReplicaID: "gw-1", NodeID: "draft", NodeType: "llm", Status: "succeeded", Model: "qwen3",
		Inputs: map[string]string{"prompt": "Write about cats"}, Outputs: map[string]string{"text": "Cats purr."},
		PromptTokens: 5, CompletionTokens: 3, StartedAt: clock.Now(), FinishedAt: clock.Now().Add(time.Second),
	}
	if err := s.RecordStudioRunStep(ctx, owner, run.ID, step); err != nil {
		t.Fatalf("RecordStudioRunStep = %v", err)
	}
	tooBig := step
	tooBig.NodeID, tooBig.Outputs = "big", map[string]string{"text": strings.Repeat("x", logic.MaxStudioStepBytes)}
	if err := s.RecordStudioRunStep(ctx, owner, run.ID, tooBig); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("oversized step = %v, want ErrInvalidInput: the Gateway must cut it first", err)
	}

	// A replica that stops renewing loses the run: it reads as interrupted
	// and every later write is refused.
	// 停止续租的副本失去该运行：它读作 interrupted，之后的每次写入都被拒绝。
	clock.Advance(30 * time.Second)
	if err := s.RenewStudioRun(ctx, owner, run.ID, "gw-1", time.Minute); err != nil {
		t.Fatalf("RenewStudioRun = %v", err)
	}
	clock.Advance(61 * time.Second)
	got, steps, err := s.GetStudioRun(ctx, owner, run.ID)
	if err != nil || got.Status != model.StudioRunInterrupted || got.FinishedAt == nil || !got.FinishedAt.Equal(got.LeaseUntil) {
		t.Fatalf("run after its lease passed = %+v, %v, want interrupted at the lease's end", got, err)
	}
	if len(steps) != 1 || steps[0].Outputs != `{"text":"Cats purr."}` {
		t.Errorf("steps = %+v", steps)
	}
	finish := logic.StudioFinishParams{ReplicaID: "gw-1", Status: model.StudioRunSucceeded, Outputs: map[string]string{"answer": "Cats purr."}}
	if err := s.FinishStudioRun(ctx, owner, run.ID, finish); !errors.Is(err, logic.ErrStudioRunFinished) {
		t.Errorf("finish after the lease passed = %v, want ErrStudioRunFinished", err)
	}
	if page, err := s.ListStudioRuns(ctx, owner, f.ID, "", 0); err != nil || len(page.Runs) != 1 || page.Runs[0].Status != model.StudioRunInterrupted {
		t.Errorf("ListStudioRuns = %+v, %v, want the run listed as interrupted", page.Runs, err)
	}

	// An interrupted run no longer counts toward the active limit, and a run
	// that is renewed in time finishes normally.
	// 已中断的运行不再计入进行中上限；按时续租的运行正常结束。
	next, err := start(model.NewID(model.PrefixFlowRun))
	if err != nil {
		t.Fatalf("starting after an interruption = %v", err)
	}
	if err := s.FinishStudioRun(ctx, owner, next.ID, finish); err != nil {
		t.Fatalf("FinishStudioRun = %v", err)
	}
	if got, _, _ := s.GetStudioRun(ctx, owner, next.ID); got.Status != model.StudioRunSucceeded || got.Outputs != `{"answer":"Cats purr."}` {
		t.Errorf("finished run = %+v", got)
	}
	if err := s.FinishStudioRun(ctx, owner, next.ID, logic.StudioFinishParams{ReplicaID: "gw-1", Status: model.StudioRunInterrupted}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("finishing as interrupted = %v, want ErrInvalidInput: only the lease decides that", err)
	}
}

func TestStudioRunActiveLimit(t *testing.T) {
	s, _, owner := newStudioFlowService(t)
	ctx := context.Background()
	f, _, err := s.CreateStudioFlow(ctx, owner, "摘要", json.RawMessage(studioFlowJSON))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= logic.MaxStudioActiveRunsPerUser; i++ {
		_, err := s.StartStudioRun(ctx, owner, logic.StudioRunParams{ID: model.NewID(model.PrefixFlowRun), FlowID: f.ID, FlowVersion: 1, ReplicaID: "gw-1", Lease: time.Minute})
		if i < logic.MaxStudioActiveRunsPerUser && err != nil {
			t.Fatalf("run %d = %v", i, err)
		}
		if i == logic.MaxStudioActiveRunsPerUser && !errors.Is(err, logic.ErrStudioRunLimit) {
			t.Fatalf("run past the limit = %v, want ErrStudioRunLimit", err)
		}
	}
}

package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// StudioFlows checks the Studio flow contract: owner scoping, list order and
// paging, versioning on save (no-op saves, base-version conflicts, the
// version limit), run admission and its active-run limits, lease fencing of
// renewals, steps and finishes, idempotent retries, and deletion of a flow
// with everything under it.
//
// StudioFlows 检查 Studio 编排契约：按所有者限定、列表顺序与分页、保存时的版本规则
// （无改动的保存、基础版本冲突、版本上限）、运行准入与进行中运行上限、续租、步骤与结束
// 的租约隔离、幂等重试，以及删除编排时连带删除其下的一切。
func StudioFlows(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	tenant := model.NewID(model.PrefixTenant)
	alice := store.StudioOwner{TenantID: tenant, UserID: model.NewID(model.PrefixUser)}
	bob := store.StudioOwner{TenantID: tenant, UserID: model.NewID(model.PrefixUser)}

	create := func(owner store.StudioOwner, name, definition string, updated time.Time) model.StudioFlow {
		t.Helper()
		f := model.StudioFlow{ID: model.NewID(model.PrefixFlow), TenantID: owner.TenantID, UserID: owner.UserID, Name: name, Version: 1, CreatedAt: updated, UpdatedAt: updated}
		v := model.StudioFlowVersion{FlowID: f.ID, Version: 1, Definition: definition, CreatedAt: updated}
		if err := st.CreateStudioFlow(ctx, &f, &v, 3); err != nil {
			t.Fatalf("CreateStudioFlow(%q) = %v", name, err)
		}
		return f
	}
	first := create(alice, "摘要", `{"v":1}`, at)
	second := create(alice, "翻译", `{"v":1}`, at.Add(time.Minute))
	third := create(alice, "改写", `{"v":1}`, at.Add(time.Minute))
	bobs := create(bob, "bob", `{"v":1}`, at)
	extra := model.StudioFlow{ID: model.NewID(model.PrefixFlow), TenantID: alice.TenantID, UserID: alice.UserID, Name: "x", Version: 1, CreatedAt: at, UpdatedAt: at}
	if err := st.CreateStudioFlow(ctx, &extra, &model.StudioFlowVersion{FlowID: extra.ID, Version: 1, Definition: "{}", CreatedAt: at}, 3); !errors.Is(err, store.ErrStudioLimit) {
		t.Fatalf("fourth flow = %v, want ErrStudioLimit", err)
	}

	// Scoping and listing.
	if _, _, err := st.GetStudioFlow(ctx, bob, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's GetStudioFlow = %v, want ErrNotFound", err)
	}
	if _, err := st.GetStudioFlowVersion(ctx, bob, first.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's GetStudioFlowVersion = %v, want ErrNotFound", err)
	}
	page, err := st.ListStudioFlows(ctx, store.StudioFlowFilter{Owner: alice, Limit: 2})
	if err != nil || len(page) != 2 || page[0].UpdatedAt.Before(page[1].UpdatedAt) {
		t.Fatalf("first page = %v, %v, want two flows, newest first", flowNames(page), err)
	}
	rest, err := st.ListStudioFlows(ctx, store.StudioFlowFilter{Owner: alice, Before: &store.StudioCursor{At: page[1].UpdatedAt, ID: page[1].ID}})
	if err != nil || len(rest) != 1 || rest[0].ID != first.ID {
		t.Fatalf("second page = %v, %v, want only the oldest", flowNames(rest), err)
	}
	if page[0].ID == page[1].ID || (page[0].ID != second.ID && page[0].ID != third.ID) {
		t.Errorf("first page = %v, want the two flows updated last", flowNames(page))
	}

	// Saving.
	update := func(owner store.StudioOwner, id string, u store.StudioFlowUpdate) (model.StudioFlow, model.StudioFlowVersion, error) {
		u.MaxVersions = 3
		return st.UpdateStudioFlow(ctx, owner, id, u)
	}
	same := `{"v":1}`
	f, v, err := update(alice, first.ID, store.StudioFlowUpdate{Definition: &same, At: at.Add(time.Hour)})
	if err != nil || f.Version != 1 || v.Version != 1 || !f.UpdatedAt.Equal(at) {
		t.Fatalf("saving an unchanged definition = %+v, %+v, %v, want nothing changed", f, v, err)
	}
	name := "长文摘要"
	if f, _, err = update(alice, first.ID, store.StudioFlowUpdate{Name: &name, At: at.Add(time.Hour)}); err != nil || f.Name != name || f.Version != 1 || !f.UpdatedAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("rename = %+v, %v, want the name and update time changed, no new version", f, err)
	}
	two := `{"v":2}`
	if f, v, err = update(alice, first.ID, store.StudioFlowUpdate{Definition: &two, BaseVersion: 1, At: at.Add(2 * time.Hour)}); err != nil || f.Version != 2 || v.Version != 2 || v.Definition != two {
		t.Fatalf("saving a new definition = %+v, %+v, %v, want version 2", f, v, err)
	}
	three := `{"v":3}`
	if _, _, err := update(alice, first.ID, store.StudioFlowUpdate{Definition: &three, BaseVersion: 1, At: at}); !errors.Is(err, store.ErrStudioFlowChanged) {
		t.Errorf("save on a stale base version = %v, want ErrStudioFlowChanged", err)
	}
	if _, _, err := update(alice, first.ID, store.StudioFlowUpdate{Definition: &three, At: at.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	four := `{"v":4}`
	if _, _, err := update(alice, first.ID, store.StudioFlowUpdate{Definition: &four, At: at}); !errors.Is(err, store.ErrStudioLimit) {
		t.Errorf("save past MaxVersions = %v, want ErrStudioLimit", err)
	}
	if _, _, err := update(bob, first.ID, store.StudioFlowUpdate{Name: &name, At: at}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's UpdateStudioFlow = %v, want ErrNotFound", err)
	}
	if old, err := st.GetStudioFlowVersion(ctx, alice, first.ID, 1); err != nil || old.Definition != `{"v":1}` {
		t.Errorf("version 1 after later saves = %+v, %v, want it unchanged", old, err)
	}
	if f, v, err := st.GetStudioFlow(ctx, alice, first.ID); err != nil || f.Version != 3 || v.Definition != three {
		t.Errorf("GetStudioFlow = %+v, %+v, %v, want version 3", f, v, err)
	}

	// Starting runs.
	run := func(owner store.StudioOwner, flow model.StudioFlow, version int64, created time.Time) *model.StudioFlowRun {
		return &model.StudioFlowRun{
			ID: model.NewID(model.PrefixFlowRun), FlowID: flow.ID, TenantID: owner.TenantID, UserID: owner.UserID, FlowVersion: version,
			Status: model.StudioRunRunning, Inputs: `{"topic":"cats"}`, Outputs: "{}", ReplicaID: "gw-1", LeaseUntil: created.Add(time.Minute), CreatedAt: created,
		}
	}
	limits := store.StudioRunLimits{PerUser: 1, PerTenant: 2}
	r1 := run(alice, first, 2, at)
	if err := st.StartStudioRun(ctx, alice, r1, limits, at); err != nil {
		t.Fatalf("StartStudioRun = %v", err)
	}
	if err := st.StartStudioRun(ctx, alice, r1, limits, at); err != nil {
		t.Errorf("retried StartStudioRun = %v, want nil", err)
	}
	if err := st.StartStudioRun(ctx, bob, r1, limits, at); !errors.Is(err, store.ErrConflict) {
		t.Errorf("another owner reusing a run id = %v, want ErrConflict", err)
	}
	if err := st.StartStudioRun(ctx, alice, run(alice, first, 4, at), store.StudioRunLimits{}, at); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run of a version that does not exist = %v, want ErrNotFound", err)
	}
	if err := st.StartStudioRun(ctx, alice, run(alice, bobs, 1, at), store.StudioRunLimits{}, at); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run of another user's flow = %v, want ErrNotFound", err)
	}
	if err := st.StartStudioRun(ctx, alice, run(alice, second, 1, at), limits, at); !errors.Is(err, store.ErrStudioRunLimit) {
		t.Errorf("second active run of a user = %v, want ErrStudioRunLimit", err)
	}
	if err := st.StartStudioRun(ctx, bob, run(bob, bobs, 1, at), limits, at); err != nil {
		t.Fatalf("bob's run = %v", err)
	}
	if err := st.StartStudioRun(ctx, alice, run(alice, second, 1, at), store.StudioRunLimits{PerTenant: 2}, at); !errors.Is(err, store.ErrStudioRunLimit) {
		t.Errorf("third active run of a tenant = %v, want ErrStudioRunLimit", err)
	}
	later := at.Add(2 * time.Minute) // past every lease so far
	r2 := run(alice, second, 1, later)
	if err := st.StartStudioRun(ctx, alice, r2, limits, later); err != nil {
		t.Errorf("run after the others' leases passed = %v, want nil: an expired run is not active", err)
	}

	// Lease fencing.
	if err := st.RenewStudioRun(ctx, alice, r1.ID, "gw-1", later.Add(time.Minute), later); !errors.Is(err, store.ErrStudioRunFinished) {
		t.Errorf("renewing an expired run = %v, want ErrStudioRunFinished", err)
	}
	if err := st.RenewStudioRun(ctx, alice, r2.ID, "gw-2", later.Add(time.Minute), later); !errors.Is(err, store.ErrStudioRunFinished) {
		t.Errorf("renewing from another replica = %v, want ErrStudioRunFinished", err)
	}
	if err := st.RenewStudioRun(ctx, bob, r2.ID, "gw-1", later.Add(time.Minute), later); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's RenewStudioRun = %v, want ErrNotFound", err)
	}
	renewed := later.Add(5 * time.Minute)
	if err := st.RenewStudioRun(ctx, alice, r2.ID, "gw-1", renewed, later); err != nil {
		t.Fatalf("RenewStudioRun = %v", err)
	}

	// Steps.
	step := func(node string, finished time.Time) *model.StudioFlowRunStep {
		return &model.StudioFlowRunStep{
			RunID: r2.ID, NodeID: node, TenantID: alice.TenantID, UserID: alice.UserID, NodeType: "llm", Status: "succeeded",
			Model: "qwen3", Inputs: `{"prompt":"x"}`, Outputs: `{"text":"y"}`, PromptTokens: 3, CompletionTokens: 4, StartedAt: later, FinishedAt: finished,
		}
	}
	afterLease := renewed.Add(-time.Second)
	for _, s := range []*model.StudioFlowRunStep{step("draft", later.Add(2*time.Second)), step("start", later.Add(time.Second))} {
		if err := st.RecordStudioRunStep(ctx, alice, s, "gw-1", afterLease); err != nil {
			t.Fatalf("RecordStudioRunStep(%s) = %v", s.NodeID, err)
		}
	}
	if err := st.RecordStudioRunStep(ctx, alice, step("draft", later), "gw-1", afterLease); err != nil {
		t.Errorf("retried RecordStudioRunStep = %v, want nil", err)
	}
	if err := st.RecordStudioRunStep(ctx, bob, step("other", later), "gw-1", afterLease); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's RecordStudioRunStep = %v, want ErrNotFound", err)
	}
	if err := st.RecordStudioRunStep(ctx, alice, step("late", later), "gw-1", renewed.Add(time.Second)); !errors.Is(err, store.ErrStudioRunFinished) {
		t.Errorf("step after the renewed lease passed = %v, want ErrStudioRunFinished", err)
	}

	// Finishing.
	finish := store.StudioRunFinish{Status: model.StudioRunSucceeded, Outputs: `{"answer":"y"}`, PromptTokens: 3, CompletionTokens: 4}
	if err := st.FinishStudioRun(ctx, alice, r2.ID, "gw-2", finish, afterLease); !errors.Is(err, store.ErrStudioRunFinished) {
		t.Errorf("finish from another replica = %v, want ErrStudioRunFinished", err)
	}
	if err := st.FinishStudioRun(ctx, alice, r2.ID, "gw-1", finish, afterLease); err != nil {
		t.Fatalf("FinishStudioRun = %v", err)
	}
	if err := st.FinishStudioRun(ctx, alice, r2.ID, "gw-1", finish, renewed.Add(time.Hour)); err != nil {
		t.Errorf("repeated FinishStudioRun = %v, want nil", err)
	}
	failed := finish
	failed.Status = model.StudioRunFailed
	if err := st.FinishStudioRun(ctx, alice, r2.ID, "gw-1", failed, afterLease); !errors.Is(err, store.ErrStudioRunFinished) {
		t.Errorf("finishing again with another status = %v, want ErrStudioRunFinished", err)
	}
	if err := st.RecordStudioRunStep(ctx, alice, step("after", later), "gw-1", afterLease); !errors.Is(err, store.ErrStudioRunFinished) {
		t.Errorf("step after the finish = %v, want ErrStudioRunFinished", err)
	}
	if err := st.FinishStudioRun(ctx, alice, r1.ID, "gw-1", finish, later); !errors.Is(err, store.ErrStudioRunFinished) {
		t.Errorf("finishing an expired run = %v, want ErrStudioRunFinished", err)
	}

	got, steps, err := st.GetStudioRun(ctx, alice, r2.ID)
	if err != nil || got.Status != model.StudioRunSucceeded || got.Outputs != finish.Outputs || got.PromptTokens != 3 || got.FinishedAt == nil || !got.FinishedAt.Equal(afterLease) {
		t.Fatalf("GetStudioRun = %+v, %v", got, err)
	}
	if got.Inputs != `{"topic":"cats"}` || got.FlowVersion != 1 || got.ReplicaID != "gw-1" {
		t.Errorf("run kept %+v, want its inputs, version and replica", got)
	}
	if len(steps) != 2 || steps[0].NodeID != "start" || steps[1].NodeID != "draft" || steps[1].Outputs != `{"text":"y"}` || steps[1].CompletionTokens != 4 {
		t.Fatalf("steps = %+v, want start then draft, in the order they finished", steps)
	}
	if _, _, err := st.GetStudioRun(ctx, bob, r2.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's GetStudioRun = %v, want ErrNotFound", err)
	}

	// Listing runs.
	r3 := run(alice, first, 3, later.Add(time.Second))
	if err := st.StartStudioRun(ctx, alice, r3, store.StudioRunLimits{}, later); err != nil {
		t.Fatal(err)
	}
	runs, err := st.ListStudioRuns(ctx, store.StudioRunFilter{Owner: alice, FlowID: first.ID, Limit: 1})
	if err != nil || len(runs) != 1 || runs[0].ID != r3.ID {
		t.Fatalf("ListStudioRuns = %v, %v, want the newest run of first", runIDs(runs), err)
	}
	runs, err = st.ListStudioRuns(ctx, store.StudioRunFilter{Owner: alice, FlowID: first.ID, Before: &store.StudioCursor{At: runs[0].CreatedAt, ID: runs[0].ID}})
	if err != nil || len(runs) != 1 || runs[0].ID != r1.ID {
		t.Fatalf("ListStudioRuns page 2 = %v, %v, want the older run", runIDs(runs), err)
	}
	if _, err := st.ListStudioRuns(ctx, store.StudioRunFilter{Owner: bob, FlowID: first.ID}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's ListStudioRuns = %v, want ErrNotFound", err)
	}

	// Deleting.
	if err := st.DeleteStudioFlow(ctx, bob, second.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's DeleteStudioFlow = %v, want ErrNotFound", err)
	}
	if err := st.DeleteStudioFlow(ctx, alice, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GetStudioRun(ctx, alice, r2.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run of a deleted flow = %v, want ErrNotFound", err)
	}
	if _, err := st.GetStudioFlowVersion(ctx, alice, second.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("version of a deleted flow = %v, want ErrNotFound", err)
	}
	if _, _, err := st.GetStudioRun(ctx, alice, r1.ID); err != nil {
		t.Errorf("run of a flow that was kept = %v", err)
	}
	if err := st.CreateStudioFlow(ctx, &extra, &model.StudioFlowVersion{FlowID: extra.ID, Version: 1, Definition: "{}", CreatedAt: at}, 3); err != nil {
		t.Errorf("creating after a delete freed room = %v", err)
	}
}

func flowNames(list []model.StudioFlow) []string {
	out := make([]string, len(list))
	for i, f := range list {
		out[i] = f.Name + "#" + f.ID
	}
	return out
}

func runIDs(list []model.StudioFlowRun) []string {
	out := make([]string, len(list))
	for i, r := range list {
		out[i] = r.ID
	}
	return out
}

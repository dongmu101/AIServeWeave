package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// studioFlowDefinition is a minimal valid flow: start → draft → end.
const studioFlowDefinition = `{
  "inputs": [{"name": "topic", "type": "text", "required": true}],
  "nodes": [
    {"id": "start", "type": "input", "position": {"x": 0, "y": 0}},
    {"id": "draft", "type": "llm", "config": {"model": "qwen3", "prompt": "Write about {{inputs.topic}}"}, "position": {"x": 200, "y": 0}},
    {"id": "end", "type": "output", "position": {"x": 400, "y": 0}}
  ],
  "edges": [{"from": "start", "to": "draft"}, {"from": "draft", "to": "end"}],
  "outputs": [{"name": "answer", "ref": "{{draft.text}}"}]
}`

// studioCall sends one internal Studio request as owner and returns the status
// and raw body, which error responses need too.
func studioCall(h *harness, method, path, tenant, user string, body any) (int, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	target := h.base + path + sep + url.Values{"tenant_id": {tenant}, "user_id": {user}}.Encode()
	req, err := http.NewRequestWithContext(context.Background(), method, target, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+internalToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func decodeInto(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
}

// TestStudioFlowsInternalAPI drives the internal flow and run endpoints the
// Gateway will call: save with validation and versions, run admission, steps,
// finish, the active-run limit, and owner isolation.
//
// TestStudioFlowsInternalAPI 驱动 Gateway 将要调用的内部编排与运行端点：带校验与版本的
// 保存、运行准入、步骤、结束、进行中运行上限，以及所有者隔离。
func TestStudioFlowsInternalAPI(t *testing.T) {
	h := newHarness(t)
	const tenant, alice, bob = "tnt_studio", "usr_alice", "usr_bob"

	// A broken definition comes back with its issues located.
	broken := strings.Replace(studioFlowDefinition, `{"from": "draft", "to": "end"}`, `{"from": "draft", "to": "end"}, {"from": "end", "to": "draft"}`, 1)
	status, raw := studioCall(h, http.MethodPost, "/internal/v1/studio/flows", tenant, alice, map[string]any{"name": "摘要", "definition": json.RawMessage(broken)})
	var invalid types.StudioFlowInvalidResponse
	decodeInto(t, raw, &invalid)
	if status != http.StatusBadRequest || invalid.Code != "invalid_flow" || len(invalid.Issues) == 0 || invalid.Issues[0].Node != "end" {
		t.Fatalf("saving a broken flow = %d %s, want 400 with issues on end", status, raw)
	}

	status, raw = studioCall(h, http.MethodPost, "/internal/v1/studio/flows", tenant, alice, map[string]any{"name": "摘要", "definition": json.RawMessage(studioFlowDefinition)})
	var flow types.StudioFlowResponse
	decodeInto(t, raw, &flow)
	if status != http.StatusCreated || !strings.HasPrefix(flow.ID, "flw_") || flow.Version != 1 || !bytes.Contains(flow.Definition, []byte(`"draft"`)) {
		t.Fatalf("create = %d %s", status, raw)
	}
	if status, _ := studioCall(h, http.MethodGet, "/internal/v1/studio/flows/"+flow.ID, tenant, bob, nil); status != http.StatusNotFound {
		t.Errorf("another user's flow = %d, want 404", status)
	}

	changed := strings.Replace(studioFlowDefinition, "Write about", "Summarise", 1)
	status, raw = studioCall(h, http.MethodPatch, "/internal/v1/studio/flows/"+flow.ID, tenant, alice, map[string]any{"definition": json.RawMessage(changed), "base_version": 1})
	decodeInto(t, raw, &flow)
	if status != http.StatusOK || flow.Version != 2 {
		t.Fatalf("save = %d %s, want version 2", status, raw)
	}
	status, raw = studioCall(h, http.MethodPatch, "/internal/v1/studio/flows/"+flow.ID, tenant, alice, map[string]any{"name": "旧编辑器", "base_version": 1})
	if status != http.StatusConflict || !bytes.Contains(raw, []byte("studio_flow_changed")) {
		t.Errorf("save from a stale editor = %d %s, want 409 studio_flow_changed", status, raw)
	}
	status, raw = studioCall(h, http.MethodGet, "/internal/v1/studio/flows/"+flow.ID+"/versions/1", tenant, alice, nil)
	var version types.StudioFlowVersionResponse
	decodeInto(t, raw, &version)
	if status != http.StatusOK || version.Version != 1 || !bytes.Contains(version.Definition, []byte("Write about")) {
		t.Errorf("version 1 = %d %s", status, raw)
	}
	status, raw = studioCall(h, http.MethodGet, "/internal/v1/studio/flows", tenant, alice, nil)
	var flows types.StudioFlowListResponse
	decodeInto(t, raw, &flows)
	if status != http.StatusOK || len(flows.Data) != 1 || flows.Data[0].Definition != nil {
		t.Errorf("list = %d %s, want one flow without its definition", status, raw)
	}

	// A run: start, a step, finish, read back.
	runID := "frn_e2e_1"
	start := map[string]any{"id": runID, "flow_id": flow.ID, "flow_version": 2, "inputs": map[string]string{"topic": "cats"}, "replica_id": "gw-1", "lease_seconds": 60}
	status, raw = studioCall(h, http.MethodPost, "/internal/v1/studio/runs", tenant, alice, start)
	var run types.StudioRunResponse
	decodeInto(t, raw, &run)
	if status != http.StatusCreated || run.Status != "running" || run.ReplicaID != "gw-1" {
		t.Fatalf("start = %d %s", status, raw)
	}
	now := time.Now().UTC()
	step := map[string]any{
		"replica_id": "gw-1", "node_id": "draft", "node_type": "llm", "status": "succeeded", "model": "qwen3",
		"inputs": map[string]string{"prompt": "Summarise cats"}, "outputs": map[string]string{"text": "Cats purr."},
		"prompt_tokens": 5, "completion_tokens": 3, "started_at": now, "finished_at": now.Add(time.Second),
	}
	if status, raw := studioCall(h, http.MethodPost, "/internal/v1/studio/runs/"+runID+"/steps", tenant, alice, step); status != http.StatusNoContent {
		t.Fatalf("step = %d %s", status, raw)
	}
	if status, raw := studioCall(h, http.MethodPost, "/internal/v1/studio/runs/"+runID+"/lease", tenant, alice, map[string]any{"replica_id": "gw-1", "lease_seconds": 60}); status != http.StatusNoContent {
		t.Fatalf("lease = %d %s", status, raw)
	}
	finish := map[string]any{"replica_id": "gw-1", "status": "succeeded", "outputs": map[string]string{"answer": "Cats purr."}, "prompt_tokens": 5, "completion_tokens": 3}
	if status, raw := studioCall(h, http.MethodPost, "/internal/v1/studio/runs/"+runID+"/finish", tenant, alice, finish); status != http.StatusNoContent {
		t.Fatalf("finish = %d %s", status, raw)
	}
	status, raw = studioCall(h, http.MethodPost, "/internal/v1/studio/runs/"+runID+"/steps", tenant, alice, map[string]any{
		"replica_id": "gw-1", "node_id": "late", "node_type": "llm", "status": "succeeded", "started_at": now, "finished_at": now,
	})
	if status != http.StatusConflict || !bytes.Contains(raw, []byte("studio_run_finished")) {
		t.Errorf("step after finish = %d %s, want 409 studio_run_finished", status, raw)
	}
	status, raw = studioCall(h, http.MethodGet, "/internal/v1/studio/runs/"+runID, tenant, alice, nil)
	decodeInto(t, raw, &run)
	if status != http.StatusOK || run.Status != "succeeded" || string(run.Outputs) != `{"answer":"Cats purr."}` || string(run.Inputs) != `{"topic":"cats"}` ||
		len(run.Steps) != 1 || run.Steps[0].Outputs["text"] != "Cats purr." || run.FinishedAt == nil {
		t.Fatalf("run = %d %s", status, raw)
	}
	if status, _ := studioCall(h, http.MethodGet, "/internal/v1/studio/runs/"+runID, tenant, bob, nil); status != http.StatusNotFound {
		t.Errorf("another user's run = %d, want 404", status)
	}
	status, raw = studioCall(h, http.MethodGet, "/internal/v1/studio/flows/"+flow.ID+"/runs", tenant, alice, nil)
	var runs types.StudioRunListResponse
	decodeInto(t, raw, &runs)
	if status != http.StatusOK || len(runs.Data) != 1 || runs.Data[0].Steps != nil || runs.Data[0].Inputs != nil {
		t.Errorf("run list = %d %s, want one run without inputs or steps", status, raw)
	}

	// The active-run limit answers 429.
	var last int
	for i := range 4 {
		start["id"] = "frn_e2e_limit_" + string(rune('a'+i))
		last, raw = studioCall(h, http.MethodPost, "/internal/v1/studio/runs", tenant, alice, start)
	}
	if last != http.StatusTooManyRequests || !bytes.Contains(raw, []byte("studio_run_limit")) {
		t.Errorf("run past the active limit = %d %s, want 429 studio_run_limit", last, raw)
	}

	if status, _ := studioCall(h, http.MethodDelete, "/internal/v1/studio/flows/"+flow.ID, tenant, alice, nil); status != http.StatusNoContent {
		t.Fatalf("delete = %d", status)
	}
	if status, _ := studioCall(h, http.MethodGet, "/internal/v1/studio/runs/"+runID, tenant, alice, nil); status != http.StatusNotFound {
		t.Errorf("run of a deleted flow = %d, want 404", status)
	}
}

func TestStudioFlowsRequireTheInternalToken(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "studio-flows@example.com")
	for _, path := range []string{"/internal/v1/studio/flows?tenant_id=t&user_id=u", "/internal/v1/studio/runs/frn_x?tenant_id=t&user_id=u"} {
		if status := h.call(http.MethodGet, path, session, nil, nil); status != http.StatusUnauthorized {
			t.Errorf("GET %s with a session = %d, want 401", path, status)
		}
	}
}

// TestStudioFlowsRoundTripThroughTheGatewayClient drives the internal flow
// and run API with the Gateway's real client, so the two sides agree on
// every field and refusal: a refused definition comes back with its issues,
// saves version, a run is started, stepped, renewed and finished, and the
// run-limit, stale-editor and no-longer-held refusals map to their errors.
//
// TestStudioFlowsRoundTripThroughTheGatewayClient 用 Gateway 真实的客户端驱动内部编排与
// 运行 API，确保两侧对每个字段和每种拒绝的理解一致：被拒绝的定义连同问题列表返回、保存
// 产生版本、运行被启动、写入步骤、续租并结束，运行上限、编辑器过期与不再持有这几种拒绝
// 都映射到各自的错误。
func TestStudioFlowsRoundTripThroughTheGatewayClient(t *testing.T) {
	h := newHarness(t)
	client := studioClient(h)
	ctx := context.Background()
	owner := httpapi.StudioOwner{TenantID: "tnt_flows", UserID: "usr_alice"}
	other := httpapi.StudioOwner{TenantID: "tnt_flows", UserID: "usr_bob"}

	broken := strings.Replace(studioFlowDefinition, "{{inputs.topic}}", "{{inputs.subject}}", 1)
	_, err := client.CreateFlow(ctx, owner, "摘要", json.RawMessage(broken))
	var invalid *flowdef.ValidationError
	if !errors.As(err, &invalid) || invalid.Issues[0].Node != "draft" || invalid.Issues[0].Code != flowdef.CodeUnknownReference {
		t.Fatalf("CreateFlow(broken) = %v, want the issue located on draft", err)
	}

	flow, err := client.CreateFlow(ctx, owner, "摘要", json.RawMessage(studioFlowDefinition))
	if err != nil || !strings.HasPrefix(flow.ID, "flw_") || flow.Version != 1 || !bytes.Contains(flow.Definition, []byte(`"draft"`)) {
		t.Fatalf("CreateFlow = %+v, %v", flow, err)
	}
	if _, err := client.GetFlow(ctx, other, flow.ID); !errors.Is(err, httpapi.ErrStudioNotFound) {
		t.Errorf("another user's GetFlow = %v, want ErrStudioNotFound", err)
	}
	changed := strings.Replace(studioFlowDefinition, "Write about", "Summarise", 1)
	if flow, err = client.UpdateFlow(ctx, owner, flow.ID, httpapi.StudioFlowPatch{Definition: json.RawMessage(changed), BaseVersion: 1}); err != nil || flow.Version != 2 {
		t.Fatalf("UpdateFlow = %+v, %v", flow, err)
	}
	name := "旧编辑器"
	if _, err := client.UpdateFlow(ctx, owner, flow.ID, httpapi.StudioFlowPatch{Name: &name, BaseVersion: 1}); !errors.Is(err, httpapi.ErrStudioFlowChanged) {
		t.Errorf("stale UpdateFlow = %v, want ErrStudioFlowChanged", err)
	}
	if v, err := client.GetFlowVersion(ctx, owner, flow.ID, 1); err != nil || !bytes.Contains(v.Definition, []byte("Write about")) {
		t.Errorf("GetFlowVersion(1) = %+v, %v", v, err)
	}
	if page, err := client.ListFlows(ctx, owner, "", 0); err != nil || len(page.Flows) != 1 || page.Flows[0].Definition != nil {
		t.Errorf("ListFlows = %+v, %v", page, err)
	}

	start := httpapi.StudioRunStart{ID: "frn_client_1", FlowID: flow.ID, FlowVersion: 2, Inputs: json.RawMessage(`{"topic":"cats"}`), ReplicaID: "gw-1#abc", Lease: time.Minute}
	run, err := client.StartRun(ctx, owner, start)
	if err != nil || run.Status != "running" || run.ReplicaID != "gw-1#abc" || string(run.Inputs) != `{"topic":"cats"}` {
		t.Fatalf("StartRun = %+v, %v", run, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	step := httpapi.StudioRunStep{
		NodeID: "draft", NodeType: "llm", Status: "succeeded", Model: "qwen3",
		Inputs: map[string]string{"prompt": "Summarise cats"}, Outputs: map[string]string{"text": "Cats purr."},
		PromptTokens: 5, CompletionTokens: 3, StartedAt: now, FinishedAt: now.Add(time.Second),
	}
	if err := client.RecordStep(ctx, owner, run.ID, "gw-1#abc", step); err != nil {
		t.Fatalf("RecordStep = %v", err)
	}
	if err := client.RenewRun(ctx, owner, run.ID, "gw-1#abc", time.Minute); err != nil {
		t.Fatalf("RenewRun = %v", err)
	}
	if err := client.RenewRun(ctx, owner, run.ID, "gw-2#def", time.Minute); !errors.Is(err, httpapi.ErrStudioRunFinished) {
		t.Errorf("RenewRun from another replica = %v, want ErrStudioRunFinished", err)
	}
	finish := httpapi.StudioRunFinish{Status: "succeeded", Outputs: map[string]string{"answer": "Cats purr."}, PromptTokens: 5, CompletionTokens: 3}
	if err := client.FinishRun(ctx, owner, run.ID, "gw-1#abc", finish); err != nil {
		t.Fatalf("FinishRun = %v", err)
	}
	if err := client.RecordStep(ctx, owner, run.ID, "gw-1#abc", httpapi.StudioRunStep{NodeID: "late", NodeType: "llm", Status: "succeeded", StartedAt: now, FinishedAt: now}); !errors.Is(err, httpapi.ErrStudioRunFinished) {
		t.Errorf("RecordStep after the finish = %v, want ErrStudioRunFinished", err)
	}
	got, err := client.GetRun(ctx, owner, run.ID)
	if err != nil || got.Status != "succeeded" || got.Outputs["answer"] != "Cats purr." || got.FinishedAt == nil || len(got.Steps) != 1 ||
		got.Steps[0].Outputs["text"] != "Cats purr." || got.Steps[0].Inputs["prompt"] != "Summarise cats" || !got.Steps[0].StartedAt.Equal(now) {
		t.Fatalf("GetRun = %+v, %v", got, err)
	}
	if page, err := client.ListRuns(ctx, owner, flow.ID, "", 0); err != nil || len(page.Runs) != 1 || page.Runs[0].Steps != nil {
		t.Errorf("ListRuns = %+v, %v", page, err)
	}

	var limitErr error
	for i := range 4 {
		start.ID = fmt.Sprintf("frn_client_limit_%d", i)
		_, limitErr = client.StartRun(ctx, owner, start)
	}
	if !errors.Is(limitErr, httpapi.ErrStudioRunLimit) {
		t.Errorf("run past the active limit = %v, want ErrStudioRunLimit", limitErr)
	}

	if err := client.DeleteFlow(ctx, owner, flow.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetRun(ctx, owner, run.ID); !errors.Is(err, httpapi.ErrStudioNotFound) {
		t.Errorf("run of a deleted flow = %v, want ErrStudioNotFound", err)
	}
}

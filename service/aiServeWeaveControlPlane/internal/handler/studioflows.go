package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// respondStudioFlowErr extends respondStudioErr with the flow refusals: a
// definition that fails validation answers 400 with all its issues, and the
// run refusals carry codes the Gateway matches on.
//
// respondStudioFlowErr 在 respondStudioErr 之上补充编排的拒绝：未通过校验的定义以 400
// 返回全部问题，运行相关的拒绝带有供 Gateway 匹配的错误码。
func respondStudioFlowErr(w http.ResponseWriter, err error) {
	var invalid *flowdef.ValidationError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, types.StudioFlowInvalidResponse{
			Error: "invalid_flow", Code: "invalid_flow", Issues: invalid.Issues, Truncated: invalid.Truncated,
		})
	case errors.Is(err, logic.ErrStudioFlowChanged):
		writeError(w, http.StatusConflict, "studio_flow_changed")
	case errors.Is(err, logic.ErrStudioRunLimit):
		writeError(w, http.StatusTooManyRequests, "studio_run_limit")
	case errors.Is(err, logic.ErrStudioRunFinished):
		writeError(w, http.StatusConflict, "studio_run_finished")
	default:
		respondStudioErr(w, err)
	}
}

func renderStudioFlow(f model.StudioFlow, v *model.StudioFlowVersion) types.StudioFlowResponse {
	out := types.StudioFlowResponse{ID: f.ID, Name: f.Name, Version: f.Version, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt}
	if v != nil {
		out.Definition = json.RawMessage(v.Definition)
	}
	return out
}

// renderStudioRun renders a run; full adds its inputs, outputs and steps.
func renderStudioRun(r model.StudioFlowRun, steps []model.StudioFlowRunStep, full bool) types.StudioRunResponse {
	out := types.StudioRunResponse{
		ID: r.ID, FlowID: r.FlowID, FlowVersion: r.FlowVersion, Status: r.Status, ErrorCode: r.ErrorCode, FailedNode: r.FailedNode,
		PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens, ReplicaID: r.ReplicaID, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}
	if !full {
		return out
	}
	out.Inputs, out.Outputs = json.RawMessage(r.Inputs), json.RawMessage(r.Outputs)
	out.Steps = make([]types.StudioRunStepResponse, len(steps))
	for i, s := range steps {
		step := types.StudioRunStepResponse{
			NodeID: s.NodeID, NodeType: s.NodeType, Status: s.Status, ErrorCode: s.ErrorCode, Model: s.Model,
			PromptTokens: s.PromptTokens, CompletionTokens: s.CompletionTokens, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt,
		}
		_ = json.Unmarshal([]byte(s.Inputs), &step.Inputs)
		_ = json.Unmarshal([]byte(s.Outputs), &step.Outputs)
		out.Steps[i] = step
	}
	return out
}

func leaseOf(seconds int) time.Duration { return time.Duration(seconds) * time.Second }

// studioRoute registers one internal Studio endpoint the way the conversation
// routes are registered: guarded by the InternalToken and instrumented under
// its path pattern.
//
// studioRoute 以与对话路由相同的方式注册一个内部 Studio 端点：由 InternalToken 保护，
// 并以其路径模式计量。
func studioRoute(ctx *svc.ServiceContext, method, path string, handler func(*svc.ServiceContext) http.HandlerFunc) rest.Route {
	return rest.Route{
		Method:  method,
		Path:    path,
		Handler: instrumented(ctx.MetricsRegistry, path, requireSharedSecret(ctx.Config.InternalToken, handler(ctx))),
	}
}

// createStudioFlow handles POST /internal/v1/studio/flows.
func createStudioFlow(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateStudioFlowRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		f, v, err := ctx.Logic.CreateStudioFlow(r.Context(), studioOwner(r), req.Name, req.Definition)
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderStudioFlow(f, &v))
	}
}

// listStudioFlows handles GET /internal/v1/studio/flows.
func listStudioFlows(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		page, err := ctx.Logic.ListStudioFlows(r.Context(), studioOwner(r), q.Get("cursor"), limit)
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		out := types.StudioFlowListResponse{Data: make([]types.StudioFlowResponse, len(page.Flows)), Next: page.Next}
		for i, f := range page.Flows {
			out.Data[i] = renderStudioFlow(f, nil)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// getStudioFlow handles GET /internal/v1/studio/flows/:id.
func getStudioFlow(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, v, err := ctx.Logic.GetStudioFlow(r.Context(), studioOwner(r), pathvar.Vars(r)["id"])
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderStudioFlow(f, &v))
	}
}

// updateStudioFlow handles PATCH /internal/v1/studio/flows/:id.
func updateStudioFlow(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.UpdateStudioFlowRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		f, v, err := ctx.Logic.UpdateStudioFlow(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], logic.StudioFlowPatch{
			Name: req.Name, Definition: req.Definition, BaseVersion: req.BaseVersion,
		})
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderStudioFlow(f, &v))
	}
}

// deleteStudioFlow handles DELETE /internal/v1/studio/flows/:id.
func deleteStudioFlow(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ctx.Logic.DeleteStudioFlow(r.Context(), studioOwner(r), pathvar.Vars(r)["id"]); err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getStudioFlowVersion handles GET /internal/v1/studio/flows/:id/versions/:version.
func getStudioFlowVersion(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := pathvar.Vars(r)
		version, err := strconv.ParseInt(vars["version"], 10, 64)
		if err != nil {
			respondStudioFlowErr(w, logic.ErrNotFound)
			return
		}
		v, err := ctx.Logic.GetStudioFlowVersion(r.Context(), studioOwner(r), vars["id"], version)
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.StudioFlowVersionResponse{Version: v.Version, Definition: json.RawMessage(v.Definition), CreatedAt: v.CreatedAt})
	}
}

// listStudioRuns handles GET /internal/v1/studio/flows/:id/runs.
func listStudioRuns(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		page, err := ctx.Logic.ListStudioRuns(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], q.Get("cursor"), limit)
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		out := types.StudioRunListResponse{Data: make([]types.StudioRunResponse, len(page.Runs)), Next: page.Next}
		for i, run := range page.Runs {
			out.Data[i] = renderStudioRun(run, nil, false)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// startStudioRun handles POST /internal/v1/studio/runs.
func startStudioRun(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.StartStudioRunRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		run, err := ctx.Logic.StartStudioRun(r.Context(), studioOwner(r), logic.StudioRunParams{
			ID: req.ID, FlowID: req.FlowID, FlowVersion: req.FlowVersion, Inputs: req.Inputs, ReplicaID: req.ReplicaID, Lease: leaseOf(req.LeaseSeconds),
		})
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderStudioRun(run, nil, true))
	}
}

// getStudioRun handles GET /internal/v1/studio/runs/:id.
func getStudioRun(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		run, steps, err := ctx.Logic.GetStudioRun(r.Context(), studioOwner(r), pathvar.Vars(r)["id"])
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderStudioRun(run, steps, true))
	}
}

// renewStudioRun handles POST /internal/v1/studio/runs/:id/lease.
func renewStudioRun(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.RenewStudioRunRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		if err := ctx.Logic.RenewStudioRun(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], req.ReplicaID, leaseOf(req.LeaseSeconds)); err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// recordStudioRunStep handles POST /internal/v1/studio/runs/:id/steps.
func recordStudioRunStep(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.RecordStudioRunStepRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		err := ctx.Logic.RecordStudioRunStep(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], logic.StudioStepParams{
			ReplicaID: req.ReplicaID, NodeID: req.NodeID, NodeType: req.NodeType, Status: req.Status, ErrorCode: req.ErrorCode, Model: req.Model,
			Inputs: req.Inputs, Outputs: req.Outputs, PromptTokens: req.PromptTokens, CompletionTokens: req.CompletionTokens,
			StartedAt: req.StartedAt, FinishedAt: req.FinishedAt,
		})
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// finishStudioRun handles POST /internal/v1/studio/runs/:id/finish.
func finishStudioRun(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.FinishStudioRunRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		err := ctx.Logic.FinishStudioRun(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], logic.StudioFinishParams{
			ReplicaID: req.ReplicaID, Status: req.Status, ErrorCode: req.ErrorCode, FailedNode: req.FailedNode,
			Outputs: req.Outputs, PromptTokens: req.PromptTokens, CompletionTokens: req.CompletionTokens,
		})
		if err != nil {
			respondStudioFlowErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

package handler

import (
	"net/http"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// The node provider handlers serve both the tenant routes (/admin/v1/nodes/
// :node_id/...) and the operator routes (/operator/v1/nodes/:id/...);
// nodeParam names the path variable, and logic decides which nodes the actor
// reaches.
//
// 节点供应商 handler 同时服务商户路由（/admin/v1/nodes/:node_id/...）与运维路由
// （/operator/v1/nodes/:id/...）；nodeParam 是路径变量名，actor 能触及哪些节点由 logic
// 决定。

// listNodeProviders returns a node's provisioned providers and the Agent's
// last report. Keys never appear, only their last four characters.
//
// listNodeProviders 返回节点的下发供应商与 Agent 的最后回报。Key 从不出现，只给末四位。
func listNodeProviders(ctx *svc.ServiceContext, nodeParam string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		status, err := ctx.Logic.ListNodeProviders(r.Context(), actor, pathvar.Vars(r)[nodeParam])
		if err != nil {
			respondErr(w, err)
			return
		}
		states := map[string]string{}
		resp := types.NodeProvidersResponse{Version: status.Version, Connected: status.Connected, Items: make([]types.NodeProvider, 0, len(status.Providers))}
		if rep := status.Report; rep != nil {
			resp.Report = &types.NodeProviderReport{AppliedVersion: rep.AppliedVersion, AttemptedVersion: rep.AttemptedVersion, Outcome: rep.Outcome}
			if !rep.UpdatedAt.IsZero() {
				at := rep.UpdatedAt
				resp.Report.UpdatedAt = &at
			}
			for _, rt := range rep.Runtimes {
				states[rt.ID] = rt.State
			}
		}
		for _, p := range status.Providers {
			item := renderNodeProvider(p)
			item.State = states[p.RuntimeID]
			resp.Items = append(resp.Items, item)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// putNodeProvider creates or replaces one provider and sends the node its
// sealed list.
//
// putNodeProvider 新建或替换一个供应商，并把封装后的列表发给节点。
func putNodeProvider(ctx *svc.ServiceContext, nodeParam string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.NodeProviderRequest
		if !decode(w, r, &req) {
			return
		}
		vars := pathvar.Vars(r)
		result, err := ctx.Logic.PutNodeProvider(r.Context(), actor, vars[nodeParam], vars["runtime_id"], logic.NodeProviderInput{
			Kind: req.Kind, BaseURL: req.BaseURL, APIKey: req.APIKey, Models: req.Models,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		item := renderNodeProvider(result.Provider)
		writeJSON(w, http.StatusOK, types.NodeProviderWriteResponse{Item: &item, Version: result.Version, Delivered: result.Delivered})
	}
}

// deleteNodeProvider removes one provider and sends the node the rest.
//
// deleteNodeProvider 移除一个供应商，并把其余列表发给节点。
func deleteNodeProvider(ctx *svc.ServiceContext, nodeParam string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		vars := pathvar.Vars(r)
		result, err := ctx.Logic.DeleteNodeProvider(r.Context(), actor, vars[nodeParam], vars["runtime_id"])
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.NodeProviderWriteResponse{Version: result.Version, Delivered: result.Delivered})
	}
}

func renderNodeProvider(p logic.NodeProviderView) types.NodeProvider {
	return types.NodeProvider{
		ID: p.RuntimeID, Kind: p.Kind, BaseURL: p.BaseURL, Models: p.Models,
		KeyLast4: p.KeyLast4, UpdatedBy: p.UpdatedBy, UpdatedAt: p.UpdatedAt,
	}
}

package handler

import (
	"net/http"
	"time"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// listTenantNodes returns the caller's tenant's own nodes with each one's
// fleet status. A fleet that cannot be read does not fail the listing: the
// rows are the tenant's own records and the status is reported as unknown.
//
// listTenantNodes 返回调用方商户的自有节点及各自的机群状态。读不到机群不会让列表失败：
// 这些行是商户自己的记录，状态报告为 unknown。
func listTenantNodes(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		rows, err := ctx.Logic.ListTenantNodes(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		live := tenantNodeFleet(ctx, r, rows)
		maxPerTenant, maxCreatesPerDay := ctx.Logic.TenantNodeLimits()
		out := types.TenantNodeListResponse{Items: make([]types.TenantNode, 0, len(rows)), MaxPerTenant: maxPerTenant, MaxCreatesPerDay: maxCreatesPerDay}
		for _, row := range rows {
			out.Items = append(out.Items, renderTenantNode(row, live))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// tenantNodeView is the part of a fleet node a tenant may see of its own node.
//
// tenantNodeView 是商户可以看到的、自有节点在机群中的那部分信息。
type tenantNodeView struct {
	live          bool
	agentVersion  string
	lastHeartbeat *time.Time
}

// tenantNodeFleet reads the fleet once when any row is still active, keyed by
// node id. It returns nil when the fleet is unavailable or answered only in
// part, which renderTenantNode reports as unknown rather than offline.
//
// tenantNodeFleet 在仍有未删除的行时读取一次机群，以节点 id 为键。机群不可用或只部分
// 作答时返回 nil，renderTenantNode 把它报告为 unknown 而不是 offline。
func tenantNodeFleet(ctx *svc.ServiceContext, r *http.Request, rows []model.TenantNode) map[string]tenantNodeView {
	wanted := map[string]bool{}
	for _, row := range rows {
		if row.RevokedAt == nil {
			wanted[row.NodeID] = true
		}
	}
	if len(wanted) == 0 || ctx.Fleet == nil {
		return nil
	}
	snapshot, err := ctx.Fleet.Nodes(r.Context())
	recordFleetCall(ctx, err)
	if err != nil || snapshot.Partial {
		return nil
	}
	out := make(map[string]tenantNodeView, len(wanted))
	for _, node := range snapshot.Nodes {
		if wanted[node.NodeID] {
			out[node.NodeID] = tenantNodeView{live: node.Live, agentVersion: node.AgentVersion, lastHeartbeat: node.LastHeartbeat}
		}
	}
	return out
}

// renderTenantNode converts a row to its wire form through a field
// allowlist: nothing else the fleet knows about the node leaves here.
//
// renderTenantNode 通过字段允许列表把一行转换为对外形式：机群关于该节点的其他信息都不会
// 从这里流出。
func renderTenantNode(row model.TenantNode, live map[string]tenantNodeView) types.TenantNode {
	out := types.TenantNode{NodeID: row.NodeID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, EnrollBy: row.EnrollBy, RevokedAt: row.RevokedAt}
	switch view, seen := live[row.NodeID]; {
	case row.RevokedAt != nil:
		out.Status = "revoked"
	case live == nil:
		out.Status = "unknown"
	case !seen:
		out.Status = "offline"
	default:
		out.Status = "offline"
		if view.live {
			out.Status = "online"
		}
		out.AgentVersion, out.LastHeartbeat = view.agentVersion, view.lastHeartbeat
	}
	return out
}

// createTenantNode issues one enrollment bundle for a new node of the
// caller's tenant. A failed issue is never retried here: the token, if one
// was minted, is unknown to anyone and expires on its own.
//
// createTenantNode 为调用方商户的一台新节点签发一个接入包。签发失败不在这里重试：即便
// 已签出令牌，也没有任何人知道它，它会自行过期。
func createTenantNode(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.CreateTenantNodeRequest
		if !decode(w, r, &req) {
			return
		}
		if req.TTLMinutes < 15 || req.TTLMinutes > 1440 {
			writeError(w, http.StatusBadRequest, "ttl_minutes must be between 15 and 1440")
			return
		}
		bundle, err := ctx.Logic.CreateTenantNode(r.Context(), actor, time.Duration(req.TTLMinutes)*time.Minute)
		recordRegistryClientCall(ctx, err)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, types.AgentEnrollmentResponse{NodeID: bundle.NodeID, ExpiresAt: bundle.ExpiresAt, Filename: bundle.Filename, Archive: bundle.Archive})
	}
}

// revokeTenantNode deletes one of the caller's tenant's nodes.
//
// revokeTenantNode 删除调用方商户的一台节点。
func revokeTenantNode(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		err := ctx.Logic.RevokeTenantNode(r.Context(), actor, pathvar.Vars(r)["node_id"])
		recordRegistryClientCall(ctx, err)
		if err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

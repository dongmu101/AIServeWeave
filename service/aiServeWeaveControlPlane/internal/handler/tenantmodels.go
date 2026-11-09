package handler

import (
	"net/http"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// listTenantModels returns the sanitized Agent-reported model inventory,
// narrowed to the models the caller's tenant may see under common/modelaccess
// and to the nodes it may use under common/nodeaccess. Services on the
// tenant's own nodes are not narrowed by model access.
// listTenantModels 返回已裁剪的 Agent 上报模型清单，并按 common/modelaccess 收窄到
// 调用方租户可见的模型、按 common/nodeaccess 收窄到它可以使用的节点。商户自有节点上的
// 服务不受模型授权收窄。
func listTenantModels(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		visible, err := ctx.Logic.TenantModelFilter(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		allowedNode, err := ctx.Logic.TenantNodeFilter(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		catalog, err := ctx.Fleet.TenantModels(r.Context())
		recordFleetCall(ctx, err)
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		if allowedNode != nil {
			catalog = catalog.FilterNodes(allowedNode)
		}
		allowedAccount, err := ctx.Logic.TenantAccountFilter(r.Context(), actor.TenantID)
		if err != nil {
			respondErr(w, err)
			return
		}
		services := catalog.Services[:0]
		for _, service := range catalog.Services {
			if allowedAccount(service.AgentID, service.ServiceID) {
				services = append(services, service)
			}
		}
		catalog.Services = services
		if visible != nil {
			// The tenant's own nodes are exempt, matching the Gateway's
			// scheduler.admitModel. / 商户自有节点豁免，与 Gateway 的
			// scheduler.admitModel 一致。
			tenantID := actor.TenantID
			catalog = catalog.FilterExcept(visible, func(agentID string) bool {
				owner, owned := nodeaccess.OwnerOf(agentID)
				return owned && owner != "" && owner == tenantID
			})
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, catalog)
	}
}

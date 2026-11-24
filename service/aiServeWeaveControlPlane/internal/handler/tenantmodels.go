package handler

import (
	"context"
	"net/http"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
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
		catalog, err := ctx.Fleet.TenantModels(r.Context())
		recordFleetCall(ctx, err)
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		catalog, err = filterTenantModelCatalog(r.Context(), ctx.Logic, actor, catalog)
		if err != nil {
			respondErr(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, catalog)
	}
}

// filterTenantModelCatalog applies the same inventory permissions to model
// discovery and to discovery of directly priced models.
//
// filterTenantModelCatalog 为模型目录和直连模型价格共用同一份清单权限。
func filterTenantModelCatalog(ctx context.Context, service *logic.Service, actor logic.Actor, catalog fleet.TenantModelCatalog) (fleet.TenantModelCatalog, error) {
	visible, err := service.TenantModelFilter(ctx, actor)
	if err != nil {
		return fleet.TenantModelCatalog{}, err
	}
	allowedNode, err := service.TenantNodeFilter(ctx, actor)
	if err != nil {
		return fleet.TenantModelCatalog{}, err
	}
	if allowedNode != nil {
		catalog = catalog.FilterNodes(allowedNode)
	}
	allowedAccount, heldAccount, err := service.TenantAccountAccess(ctx, actor.TenantID)
	if err != nil {
		return fleet.TenantModelCatalog{}, err
	}
	ownNode := func(agentID string) bool {
		owner, owned := nodeaccess.OwnerOf(agentID)
		return owned && owner == actor.TenantID
	}
	services := catalog.Services[:0]
	for _, item := range catalog.Services {
		if !allowedAccount(item.AgentID, item.ServiceID) {
			continue
		}
		if ownNode(item.AgentID) || heldAccount(item.AgentID, item.ServiceID) {
			item.Scope = fleet.ScopeDedicated
		} else {
			item.Scope = fleet.ScopeCloud
		}
		services = append(services, item)
	}
	catalog.Services = services
	if visible != nil {
		// The tenant's own nodes are exempt, as in Gateway scheduling.
		// 商户自有节点豁免，与 Gateway 的调度规则一致。
		catalog = catalog.FilterExcept(visible, ownNode)
	}
	return catalog, nil
}

package handler

import (
	"net/http"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// listTenantModels returns the sanitized Agent-reported model inventory,
// narrowed to the models the caller's tenant may see under common/modelaccess.
// listTenantModels 返回已裁剪的 Agent 上报模型清单，并按 common/modelaccess 收窄到
// 调用方租户可见的模型。
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
		catalog, err := ctx.Fleet.TenantModels(r.Context())
		recordFleetCall(ctx, err)
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		if visible != nil {
			catalog = catalog.Filter(visible)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, catalog)
	}
}

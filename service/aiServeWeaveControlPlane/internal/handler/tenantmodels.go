package handler

import (
	"net/http"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// listTenantModels returns a shared, sanitized Agent-reported model inventory.
// listTenantModels 返回共享且已裁剪的 Agent 上报模型清单。
func listTenantModels(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		catalog, err := ctx.Fleet.TenantModels(r.Context())
		recordFleetCall(ctx, err)
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, catalog)
	}
}

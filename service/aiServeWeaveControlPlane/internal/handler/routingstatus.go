package handler

import (
	"net/http"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// routingStatus compares the current publication with fresh reads of every configured Gateway.
// routingStatus 将当前发布与全部已配置 Gateway 的新鲜读取结果比较。
func routingStatus(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		desired, err := ctx.Logic.CurrentRoutes(r.Context())
		if err != nil {
			respondErr(w, err)
			return
		}
		report := ctx.Fleet.RoutingStatus(r.Context(), desired)
		// A concurrent publication invalidates completion for the captured version.
		// 并发发布会使先前捕获版本的完成判断失效。
		latest, err := ctx.Logic.CurrentRoutes(r.Context())
		if err != nil {
			respondErr(w, err)
			return
		}
		if latest.Revision != desired.Revision || latest.Digest != desired.Digest {
			report.DesiredRevision = latest.Revision
			report.DesiredDigest = latest.Digest
			report.Complete = false
		}
		writeJSON(w, http.StatusOK, report)
	}
}

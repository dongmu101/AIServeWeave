package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/modelaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// listTenants handles GET /operator/v1/tenants: every tenant, for an operator
// choosing whose model access to set.
//
// listTenants 处理 GET /operator/v1/tenants：全部租户，供运维挑选为谁设置模型授权。
func listTenants(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		query := r.URL.Query()
		page, err := ctx.Logic.ListTenants(r.Context(), actor, listQuery(query), store.TenantFilter{
			Status: query.Get("status"), Query: query.Get("q"),
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		items := make([]types.Tenant, len(page.Items))
		for i, tenant := range page.Items {
			items[i] = types.Tenant{ID: tenant.ID, Name: tenant.Name, Status: tenant.Status, CreatedAt: tenant.CreatedAt}
		}
		writeJSON(w, http.StatusOK, types.TenantListResponse{Items: items, NextCursor: page.NextCursor})
	}
}

// getModelAccess handles GET /operator/v1/model-access/:scope, where scope is
// "platform" or a tenant id.
//
// getModelAccess 处理 GET /operator/v1/model-access/:scope，scope 为 "platform"
// 或某个租户 id。
func getModelAccess(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		list, err := ctx.Logic.GetModelAccess(r.Context(), actor, pathvar.Vars(r)["scope"])
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderModelAccess(list))
	}
}

// setModelAccess handles PUT /operator/v1/model-access/:scope. Its body may
// name up to modelaccess.MaxModels aliases, more than the shared 64 KiB bound
// fits, so it is decoded under modelaccess.MaxDocumentBytes in a route group
// of its own.
//
// setModelAccess 处理 PUT /operator/v1/model-access/:scope。它的请求体最多可点名
// modelaccess.MaxModels 个别名，超出共用的 64 KiB 上限，因此在单独的路由组里按
// modelaccess.MaxDocumentBytes 解码。
func setModelAccess(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.SetModelAccessRequest
		if !decodeModelAccess(w, r, &req) {
			return
		}
		if req.Models == nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		list, err := ctx.Logic.SetModelAccess(r.Context(), actor, pathvar.Vars(r)["scope"], req.Models)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderModelAccess(list))
	}
}

// decodeModelAccess reads one bounded JSON document and refuses trailing data,
// as decodeRoutes does.
//
// decodeModelAccess 读取一个有大小上限的 JSON 文档并拒绝尾随数据，与 decodeRoutes 相同。
func decodeModelAccess(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, modelaccess.MaxDocumentBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(out)
	if err == nil {
		if err = decoder.Decode(new(any)); err == io.EOF {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "model access document exceeds limit")
	} else {
		writeError(w, http.StatusBadRequest, "the request body is not valid JSON for this endpoint")
	}
	return false
}

func renderModelAccess(list logic.ModelAccessList) types.ModelAccessResponse {
	out := types.ModelAccessResponse{Scope: list.Scope, Models: list.Models, UpdatedBy: list.UpdatedBy}
	if !list.UpdatedAt.IsZero() {
		at := list.UpdatedAt
		out.UpdatedAt = &at
	}
	return out
}

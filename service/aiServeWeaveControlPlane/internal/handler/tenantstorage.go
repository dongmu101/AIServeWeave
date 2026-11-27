package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// getTenantStorage handles GET /admin/v1/storage.
// getTenantStorage 处理 GET /admin/v1/storage。
func getTenantStorage(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		status, err := ctx.Logic.GetTenantStorage(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		resp := types.TenantStorageResponse{Enabled: status.Enabled, AllowPrivateEndpoints: status.AllowPrivateEndpoints}
		if status.Storage != nil {
			view := renderTenantStorage(*status.Storage)
			resp.Storage = &view
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// putTenantStorage handles PUT /admin/v1/storage.
// putTenantStorage 处理 PUT /admin/v1/storage。
func putTenantStorage(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.TenantStorageRequest
		if !decode(w, r, &req) {
			return
		}
		in := logic.TenantStorageInput{Kind: req.Kind, RetentionDays: req.RetentionDays}
		if req.S3 != nil {
			in.S3 = &logic.TenantStorageS3Input{
				Endpoint: req.S3.Endpoint, Region: req.S3.Region, Bucket: req.S3.Bucket, Prefix: req.S3.Prefix, PathStyle: req.S3.PathStyle,
				AccessKeyID: req.S3.AccessKeyID, SecretAccessKey: req.S3.SecretAccessKey,
			}
		}
		if req.WebDAV != nil {
			in.WebDAV = &logic.TenantStorageWebDAVInput{URL: req.WebDAV.URL, Dir: req.WebDAV.Dir, Username: req.WebDAV.Username, Password: req.WebDAV.Password}
		}
		view, err := ctx.Logic.PutTenantStorage(r.Context(), actor, in)
		var refused *logic.TenantStorageError
		if errors.As(err, &refused) {
			writeJSON(w, http.StatusBadRequest, types.TenantStorageErrorResponse{Error: "the storage was not saved", Code: refused.Code, Reason: refused.Reason})
			return
		}
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderTenantStorage(view))
	}
}

// deleteTenantStorage handles DELETE /admin/v1/storage.
// deleteTenantStorage 处理 DELETE /admin/v1/storage。
func deleteTenantStorage(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := ctx.Logic.DeleteTenantStorage(r.Context(), actor); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// internalTenantStorage handles GET /internal/v1/tenants/:tenant_id/storage:
// the Gateway reads a tenant's storage with its credentials.
//
// internalTenantStorage 处理 GET /internal/v1/tenants/:tenant_id/storage：Gateway 读取
// 带凭据的租户存储。
func internalTenantStorage(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, allowPrivate := ctx.Logic.TenantStorageEnabled()
		secret, ok, err := ctx.Logic.InternalTenantStorage(r.Context(), pathvar.Vars(r)["tenant_id"])
		if err != nil {
			respondErr(w, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, types.InternalTenantStorage{AllowPrivateEndpoints: allowPrivate})
			return
		}
		writeJSON(w, http.StatusOK, renderInternalTenantStorage(secret, allowPrivate))
	}
}

// listRetainedTenantStorage handles GET /internal/v1/tenant-storage/retained.
// listRetainedTenantStorage 处理 GET /internal/v1/tenant-storage/retained。
func listRetainedTenantStorage(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = n
		}
		_, allowPrivate := ctx.Logic.TenantStorageEnabled()
		items, next, err := ctx.Logic.ListRetainedTenantStorage(r.Context(), r.URL.Query().Get("after"), limit)
		if err != nil {
			respondErr(w, err)
			return
		}
		resp := types.InternalRetainedTenantStorageResponse{Items: make([]types.InternalTenantStorage, 0, len(items)), Next: next}
		for _, item := range items {
			resp.Items = append(resp.Items, renderInternalTenantStorage(item, allowPrivate))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func renderTenantStorage(v logic.TenantStorageView) types.TenantStorage {
	out := types.TenantStorage{Kind: v.Kind, RetentionDays: v.RetentionDays, UpdatedBy: v.UpdatedBy, UpdatedAt: v.UpdatedAt}
	if v.S3 != nil {
		out.S3 = &types.TenantStorageS3{Endpoint: v.S3.Endpoint, Region: v.S3.Region, Bucket: v.S3.Bucket, Prefix: v.S3.Prefix,
			PathStyle: v.S3.PathStyle, AccessKeyIDLast4: v.S3.AccessKeyIDLast4}
	}
	if v.WebDAV != nil {
		out.WebDAV = &types.TenantStorageWebDAV{URL: v.WebDAV.URL, Dir: v.WebDAV.Dir, Username: v.WebDAV.Username}
	}
	return out
}

func renderInternalTenantStorage(s logic.TenantStorageSecret, allowPrivate bool) types.InternalTenantStorage {
	out := types.InternalTenantStorage{
		TenantID: s.TenantID, Configured: true, AllowPrivateEndpoints: allowPrivate,
		Version: s.Version, Kind: s.Kind, RetentionDays: s.RetentionDays,
	}
	if s.S3 != nil {
		out.S3 = &types.InternalTenantStorageS3{Endpoint: s.S3.Endpoint, Region: s.S3.Region, Bucket: s.S3.Bucket, Prefix: s.S3.Prefix,
			PathStyle: s.S3.UsePathStyle, AccessKeyID: s.S3.AccessKeyID, SecretAccessKey: s.S3.SecretAccessKey}
	}
	if s.WebDAV != nil {
		out.WebDAV = &types.InternalTenantStorageWebDAV{URL: s.WebDAV.URL, Dir: s.WebDAV.Dir, Username: s.WebDAV.Username, Password: s.WebDAV.Password}
	}
	return out
}

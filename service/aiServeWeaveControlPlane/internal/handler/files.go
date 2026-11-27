package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// registerFile handles PUT /internal/v1/files/:id: a Gateway about to write
// a file to object storage.
//
// registerFile 处理 PUT /internal/v1/files/:id：Gateway 即将把一个文件写进对象存储。
func registerFile(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.RegisterFileRequest
		if !decode(w, r, &req) {
			return
		}
		f, err := ctx.Logic.RegisterFile(r.Context(), logic.RegisterFileParams{
			ID: pathvar.Vars(r)["id"], TenantID: req.TenantID, Storage: req.Storage, ObjectKey: req.ObjectKey,
			BizType: req.BizType, BizID: req.BizID, Kind: req.Kind, Filename: req.Filename, ContentType: req.ContentType,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderInternalFile(f))
	}
}

// completeFile handles POST /internal/v1/files/:id/complete: the file's
// bytes are written.
//
// completeFile 处理 POST /internal/v1/files/:id/complete：文件字节已写完。
func completeFile(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CompleteFileRequest
		if !decode(w, r, &req) {
			return
		}
		if err := ctx.Logic.CompleteFile(r.Context(), pathvar.Vars(r)["id"], req.SizeBytes, req.SHA256, req.ContentType); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// deleteFile handles DELETE /internal/v1/files/:id. A file already gone
// answers 204 as well.
//
// deleteFile 处理 DELETE /internal/v1/files/:id。文件已不在时同样回 204。
func deleteFile(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ctx.Logic.DeleteFile(r.Context(), pathvar.Vars(r)["id"]); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// listFiles handles GET /internal/v1/files: the Gateway's cleanup asking
// which files registered before a time match a filter. Like the expired job
// artifact listing it is not scoped to a tenant unless tenant_id is given.
//
// listFiles 处理 GET /internal/v1/files：Gateway 的清理询问哪些在某时刻之前登记的文件符合
// 条件。与过期 job 产物列举一样，除非给出 tenant_id，否则不限定租户。
func listFiles(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		before, err := time.Parse(time.RFC3339Nano, query.Get("before"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be an RFC3339 timestamp")
			return
		}
		limit := 0
		if raw := query.Get("limit"); raw != "" {
			if limit, err = strconv.Atoi(raw); err != nil || limit <= 0 {
				writeError(w, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
		}
		files, err := ctx.Logic.ListFiles(r.Context(), logic.FileListParams{
			Before: before, Storage: query.Get("storage"), TenantID: query.Get("tenant_id"),
			BizType: query.Get("biz_type"), Kind: query.Get("kind"), Status: query.Get("status"), Limit: limit,
		})
		if err != nil {
			respondErr(w, err)
			return
		}
		resp := types.ListFilesResponse{Items: make([]types.InternalFile, 0, len(files))}
		for _, f := range files {
			resp.Items = append(resp.Items, renderInternalFile(f))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func renderInternalFile(f model.File) types.InternalFile {
	return types.InternalFile{
		ID: f.ID, TenantID: f.TenantID, Storage: f.Storage, ObjectKey: f.ObjectKey,
		BizType: f.BizType, BizID: f.BizID, Kind: f.Kind, Filename: f.Filename, ContentType: f.ContentType,
		SizeBytes: f.SizeBytes, SHA256: f.SHA256, Status: f.Status, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

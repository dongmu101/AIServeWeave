package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/apidocs"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// listAPIDocs handles GET /admin/v1/api-docs: every published page's
// navigation entry, for any signed-in tenant user.
//
// listAPIDocs 处理 GET /admin/v1/api-docs：所有已发布页面的导航条目，任何已登录的
// 租户用户均可读取。
func listAPIDocs(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := ctx.Logic.ListAPIDocs(r.Context())
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Items []logic.APIDocSummary `json:"items"`
		}{items})
	}
}

// getAPIDoc handles GET /admin/v1/api-docs/:slug.
//
// getAPIDoc 处理 GET /admin/v1/api-docs/:slug。
func getAPIDoc(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		doc, err := ctx.Logic.GetAPIDoc(r.Context(), pathvar.Vars(r)["slug"])
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

// operatorListAPIDocs handles GET /operator/v1/api-docs, drafts included.
//
// operatorListAPIDocs 处理 GET /operator/v1/api-docs，包括草稿。
func operatorListAPIDocs(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, _ := actorFrom(r.Context())
		items, err := ctx.Logic.OperatorListAPIDocs(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Items []logic.OperatorAPIDocSummary `json:"items"`
		}{items})
	}
}

// operatorGetAPIDoc handles GET /operator/v1/api-docs/:slug.
//
// operatorGetAPIDoc 处理 GET /operator/v1/api-docs/:slug。
func operatorGetAPIDoc(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, _ := actorFrom(r.Context())
		doc, err := ctx.Logic.OperatorGetAPIDoc(r.Context(), actor, pathvar.Vars(r)["slug"])
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

// createAPIDoc handles POST /operator/v1/api-docs.
//
// createAPIDoc 处理 POST /operator/v1/api-docs。
func createAPIDoc(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Slug string `json:"slug"`
			logic.APIDocInput
		}
		if !decodeAPIDoc(w, r, &req) {
			return
		}
		actor, _ := actorFrom(r.Context())
		doc, err := ctx.Logic.CreateAPIDoc(r.Context(), actor, req.Slug, req.APIDocInput)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, doc)
	}
}

// updateAPIDoc handles PUT /operator/v1/api-docs/:slug, a full replace
// guarded by expected_version.
//
// updateAPIDoc 处理 PUT /operator/v1/api-docs/:slug，一次以 expected_version 守护的
// 整体替换。
func updateAPIDoc(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ExpectedVersion int64 `json:"expected_version"`
			logic.APIDocInput
		}
		if !decodeAPIDoc(w, r, &req) {
			return
		}
		actor, _ := actorFrom(r.Context())
		doc, err := ctx.Logic.UpdateAPIDoc(r.Context(), actor, pathvar.Vars(r)["slug"], req.ExpectedVersion, req.APIDocInput)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

// resetAPIDoc handles POST /operator/v1/api-docs/:slug/reset.
//
// resetAPIDoc 处理 POST /operator/v1/api-docs/:slug/reset。
func resetAPIDoc(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ExpectedVersion int64 `json:"expected_version"`
		}
		if !decode(w, r, &req) {
			return
		}
		actor, _ := actorFrom(r.Context())
		doc, err := ctx.Logic.ResetAPIDoc(r.Context(), actor, pathvar.Vars(r)["slug"], req.ExpectedVersion)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

// deleteAPIDoc handles DELETE /operator/v1/api-docs/:slug?expected_version=N.
//
// deleteAPIDoc 处理 DELETE /operator/v1/api-docs/:slug?expected_version=N。
func deleteAPIDoc(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected, err := strconv.ParseInt(r.URL.Query().Get("expected_version"), 10, 64)
		if err != nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		actor, _ := actorFrom(r.Context())
		if err := ctx.Logic.DeleteAPIDoc(r.Context(), actor, pathvar.Vars(r)["slug"], expected); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// decodeAPIDoc is decode with the larger apidocs.MaxRequestBytes bound and a
// distinct 413 for an oversized page.
//
// decodeAPIDoc 与 decode 相同，但使用更大的 apidocs.MaxRequestBytes 上限，并对超大
// 页面单独返回 413。
func decodeAPIDoc(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, apidocs.MaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(out)
	if err == nil {
		if trailing := decoder.Decode(new(any)); trailing != io.EOF {
			err = errors.New("trailing data")
		}
	}
	if err == nil {
		return true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "api doc exceeds limit")
	} else {
		writeError(w, http.StatusBadRequest, "the request body is not valid JSON for this endpoint")
	}
	return false
}

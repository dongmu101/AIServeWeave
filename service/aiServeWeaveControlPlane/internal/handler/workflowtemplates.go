package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

func listWorkflowTemplates(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := ctx.Logic.ListWorkflowTemplates(r.Context())
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Items []logic.WorkflowTemplateSummary `json:"items"`
		}{items})
	}
}
func getWorkflowTemplate(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := pathvar.Vars(r)["id"]
		snapshot, err := ctx.Logic.WorkflowTemplate(r.Context(), id)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}
func currentWorkflowTemplatesBundle(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bundle, err := ctx.Logic.CurrentWorkflowTemplatesBundle(r.Context())
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, bundle)
	}
}
func validateWorkflowTemplate(_ *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := pathvar.Vars(r)["id"]
		var req struct {
			Content          workflowtemplate.Content `json:"content"`
			VisibleTenantIDs []string                 `json:"visible_tenant_ids"`
		}
		if !decodeWorkflowTemplate(w, r, &req) {
			return
		}
		digest, err := workflowtemplate.Digest(id, req.Content, req.VisibleTenantIDs)
		if err != nil {
			// The error names a field, never graph content — see
			// workflowtemplate.Validate's own doc comment on that guarantee.
			//
			// 错误只点名字段，绝不含图的内容——见 workflowtemplate.Validate 自身
			// 文档注释里的这条保证。
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Valid  bool   `json:"valid"`
			Digest string `json:"digest"`
		}{true, digest})
	}
}
func publishWorkflowTemplate(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := pathvar.Vars(r)["id"]
		var req struct {
			ExpectedRevision *int64                   `json:"expected_revision"`
			Content          workflowtemplate.Content `json:"content"`
			VisibleTenantIDs []string                 `json:"visible_tenant_ids"`
		}
		if !decodeWorkflowTemplate(w, r, &req) {
			return
		}
		if req.ExpectedRevision == nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		actor, _ := actorFrom(r.Context())
		snapshot, err := ctx.Logic.PublishWorkflowTemplate(r.Context(), actor, id, *req.ExpectedRevision, req.Content, req.VisibleTenantIDs)
		if err != nil {
			respondWorkflowTemplateErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, snapshot)
	}
}
func rollbackWorkflowTemplate(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := pathvar.Vars(r)["id"]
		var req struct {
			ExpectedRevision *int64 `json:"expected_revision"`
			Revision         int64  `json:"revision"`
		}
		if !decodeWorkflowTemplate(w, r, &req) {
			return
		}
		if req.ExpectedRevision == nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		actor, _ := actorFrom(r.Context())
		snapshot, err := ctx.Logic.RollbackWorkflowTemplate(r.Context(), actor, id, *req.ExpectedRevision, req.Revision)
		if err != nil {
			respondWorkflowTemplateErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, snapshot)
	}
}
func workflowTemplateHistory(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := pathvar.Vars(r)["id"]
		var before int64
		limit := 50
		var err error
		if value := r.URL.Query().Get("before"); value != "" {
			before, err = strconv.ParseInt(value, 10, 64)
			if err != nil || before < 1 {
				respondErr(w, logic.ErrInvalidInput)
				return
			}
		}
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil {
				respondErr(w, logic.ErrInvalidInput)
				return
			}
		}
		page, err := ctx.Logic.WorkflowTemplateHistoryPage(r.Context(), id, before, limit)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	}
}
func workflowTemplateRevisionAt(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := pathvar.Vars(r)
		revision, err := strconv.ParseInt(vars["revision"], 10, 64)
		if err != nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		snapshot, err := ctx.Logic.WorkflowTemplateRevision(r.Context(), vars["id"], revision)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}

// decodeWorkflowTemplate accepts a bounded publication and rejects trailing
// or unknown data, the same shape decodeRoutes enforces for routing.
//
// decodeWorkflowTemplate 接受有界发布正文，拒绝尾随及未知数据，与 decodeRoutes
// 对路由的约束同构。
func decodeWorkflowTemplate(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, workflowtemplate.MaxContentBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return workflowTemplateDecodeError(w, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return workflowTemplateDecodeError(w, err)
	}
	return true
}
func workflowTemplateDecodeError(w http.ResponseWriter, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "workflow template document exceeds limit")
	} else {
		writeError(w, http.StatusBadRequest, "the request body is not valid JSON for this endpoint")
	}
	return false
}
func respondWorkflowTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, logic.ErrWorkflowTemplateCapacity):
		writeError(w, http.StatusConflict, "workflow template revision history capacity reached")
	case errors.Is(err, logic.ErrWorkflowTemplateLimit):
		writeError(w, http.StatusConflict, "platform workflow template limit reached")
	case errors.Is(err, logic.ErrConflict):
		writeError(w, http.StatusConflict, "workflow template revision changed; refresh before publishing")
	default:
		respondErr(w, err)
	}
}

package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/arkvideo"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// defaultArkTaskPage is the Ark task list's page size when none is asked. / defaultArkTaskPage 是未指定时方舟任务列表的页大小。
const (
	defaultArkTaskPage = 20
)

// getArk handles GET /admin/v1/ark and GET /operator/v1/ark: the actor's
// scope's saved key, without the key itself.
//
// getArk 处理 GET /admin/v1/ark 与 GET /operator/v1/ark：actor 所在 scope 已保存的 Key，
// 不含 Key 本身。
func getArk(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		status, err := ctx.Logic.GetArk(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		resp := types.ArkResponse{Enabled: status.Enabled, PlatformAvailable: status.PlatformAvailable}
		if status.Credential != nil {
			view := renderArk(*status.Credential)
			resp.Credential = &view
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// putArk handles PUT /admin/v1/ark and PUT /operator/v1/ark.
// putArk 处理 PUT /admin/v1/ark 与 PUT /operator/v1/ark。
func putArk(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.ArkRequest
		if !decode(w, r, &req) {
			return
		}
		view, err := ctx.Logic.PutArk(r.Context(), actor, logic.ArkInput{APIKey: req.APIKey, BaseURL: req.BaseURL, Models: req.Models})
		if writeArkError(w, err, "the key was not saved") {
			return
		}
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderArk(view))
	}
}

// deleteArk handles DELETE /admin/v1/ark and DELETE /operator/v1/ark.
// deleteArk 处理 DELETE /admin/v1/ark 与 DELETE /operator/v1/ark。
func deleteArk(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := ctx.Logic.DeleteArk(r.Context(), actor); err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// listArkTasks handles GET /admin/v1/ark/tasks and GET /operator/v1/ark/tasks
// (?page_num=&page_size=&status=&model=): one page of the tasks under the
// actor's scope's own key, read from Ark.
//
// listArkTasks 处理 GET /admin/v1/ark/tasks 与 GET /operator/v1/ark/tasks
// （?page_num=&page_size=&status=&model=）：从方舟读取 actor 所在 scope 自己 Key 下任务的一页。
func listArkTasks(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		q := r.URL.Query()
		pageNum, okNum := positiveQuery(q.Get("page_num"), 1)
		pageSize, okSize := positiveQuery(q.Get("page_size"), defaultArkTaskPage)
		if !okNum || !okSize || pageSize > logic.MaxArkTaskPage {
			writeError(w, http.StatusBadRequest, "page_num and page_size must be positive integers, page_size at most 100")
			return
		}
		page, err := ctx.Logic.ListArkTasks(r.Context(), actor, arkvideo.ListQuery{
			PageNum: pageNum, PageSize: pageSize, Status: arkvideo.Status(q.Get("status")), Model: q.Get("model"),
		})
		if writeArkError(w, err, "the task list could not be read") {
			return
		}
		if err != nil {
			respondErr(w, err)
			return
		}
		out := types.ArkTaskList{Total: page.Total, PageNum: pageNum, PageSize: pageSize, Items: make([]types.ArkTask, 0, len(page.Items))}
		for _, t := range page.Items {
			item := types.ArkTask{
				ID: t.ID, Model: t.Model, Status: string(t.Status), ErrorCode: t.ErrorCode,
				Resolution: t.Resolution, Ratio: t.Ratio, Duration: t.Duration, CompletionTokens: t.CompletionTokens,
			}
			if !t.CreatedAt.IsZero() {
				created := t.CreatedAt
				item.CreatedAt = &created
			}
			if !t.UpdatedAt.IsZero() {
				updated := t.UpdatedAt
				item.UpdatedAt = &updated
			}
			out.Items = append(out.Items, item)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// deleteArkTask handles DELETE /admin/v1/ark/tasks/:task_id and its operator
// twin: cancel a queued task, or delete the record of a finished one.
//
// deleteArkTask 处理 DELETE /admin/v1/ark/tasks/:task_id 及其运维版本：取消排队中的任务，
// 或删除已结束任务的记录。
func deleteArkTask(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		err := ctx.Logic.DeleteArkTask(r.Context(), actor, pathvar.Vars(r)["task_id"])
		if writeArkError(w, err, "the task was not cancelled or deleted") {
			return
		}
		if err != nil {
			respondErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// internalArk handles GET /internal/v1/tenants/:tenant_id/ark: the Gateway
// reads the key a tenant's video node uses.
//
// internalArk 处理 GET /internal/v1/tenants/:tenant_id/ark：Gateway 读取租户视频节点
// 使用的 Key。
func internalArk(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		secret, ok, err := ctx.Logic.InternalArk(r.Context(), pathvar.Vars(r)["tenant_id"])
		if err != nil {
			respondErr(w, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, types.InternalArk{})
			return
		}
		writeJSON(w, http.StatusOK, types.InternalArk{
			Configured: true, Source: secret.Source, Version: secret.Version,
			APIKey: secret.APIKey, BaseURL: secret.BaseURL, Models: secret.Models,
		})
	}
}

// writeArkError answers a *logic.ArkError with its code: 503 for
// ark_unreachable, 400 for the others. It reports whether it answered.
//
// writeArkError 以错误码答复 *logic.ArkError：ark_unreachable 为 503，其余为 400。返回
// 是否已经答复。
func writeArkError(w http.ResponseWriter, err error, message string) bool {
	var refused *logic.ArkError
	if !errors.As(err, &refused) {
		return false
	}
	status := http.StatusBadRequest
	if refused.Code == logic.ArkUnreachable {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, types.ArkErrorResponse{Error: message, Code: refused.Code, Reason: refused.Reason})
	return true
}

func renderArk(v logic.ArkView) types.ArkCredential {
	return types.ArkCredential{KeyLast4: v.KeyLast4, BaseURL: v.BaseURL, Models: v.Models, UpdatedBy: v.UpdatedBy, UpdatedAt: v.UpdatedAt}
}

// positiveQuery parses an optional positive integer query value.
func positiveQuery(raw string, fallback int) (int, bool) {
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

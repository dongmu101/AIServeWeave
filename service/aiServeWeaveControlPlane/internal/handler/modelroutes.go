package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

func currentRoutes(ctx *svc.ServiceContext, internal bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := ctx.Logic.CurrentRoutes(r.Context())
		if err != nil {
			respondErr(w, err)
			return
		}
		if internal && snapshot.Revision == 0 {
			respondErr(w, logic.ErrNotFound)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}
func validateRoutes(_ *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Routes []modelroute.Route `json:"routes"`
		}
		if !decodeRoutes(w, r, &req) {
			return
		}
		if req.Routes == nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		digest, err := modelroute.Digest(req.Routes)
		if err != nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Valid  bool   `json:"valid"`
			Digest string `json:"digest"`
		}{true, digest})
	}
}
func publishRoutes(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ExpectedRevision *int64             `json:"expected_revision"`
			Routes           []modelroute.Route `json:"routes"`
		}
		if !decodeRoutes(w, r, &req) {
			return
		}
		if req.ExpectedRevision == nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		actor, _ := actorFrom(r.Context())
		snapshot, err := ctx.Logic.PublishRoutes(r.Context(), actor, *req.ExpectedRevision, req.Routes)
		if err != nil {
			respondRouteErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, snapshot)
	}
}
func rollbackRoutes(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ExpectedRevision *int64 `json:"expected_revision"`
			Revision         int64  `json:"revision"`
		}
		if !decodeRoutes(w, r, &req) {
			return
		}
		if req.ExpectedRevision == nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		actor, _ := actorFrom(r.Context())
		snapshot, err := ctx.Logic.RollbackRoutes(r.Context(), actor, *req.ExpectedRevision, req.Revision)
		if err != nil {
			respondRouteErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, snapshot)
	}
}
func routeHistory(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		page, err := ctx.Logic.RoutesHistory(r.Context(), before, limit)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	}
}
func routeRevision(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		revision, err := strconv.ParseInt(pathvar.Vars(r)["revision"], 10, 64)
		if err != nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		snapshot, err := ctx.Logic.RouteRevision(r.Context(), revision)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}

// decodeRoutes allows a bounded publication and rejects trailing or unknown data. / decodeRoutes 接受有界发布正文，拒绝尾随及未知数据。
func decodeRoutes(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, modelroute.MaxDocumentBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return routeDecodeError(w, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return routeDecodeError(w, err)
	}
	return true
}
func routeDecodeError(w http.ResponseWriter, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "routing document exceeds limit")
	} else {
		writeError(w, http.StatusBadRequest, "the request body is not valid JSON for this endpoint")
	}
	return false
}
func respondRouteErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, logic.ErrRouteCapacity):
		writeError(w, http.StatusConflict, "routing revision history capacity reached")
	case errors.Is(err, logic.ErrConflict):
		writeError(w, http.StatusConflict, "routing revision changed; refresh before publishing")
	default:
		respondErr(w, err)
	}
}

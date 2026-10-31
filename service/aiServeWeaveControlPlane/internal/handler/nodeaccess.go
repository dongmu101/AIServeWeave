package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// listNodeAccess handles GET /operator/v1/node-access: every dedicated node.
//
// listNodeAccess 处理 GET /operator/v1/node-access：全部专属节点。
func listNodeAccess(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		entries, err := ctx.Logic.ListNodeAccess(r.Context(), actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		out := types.NodeAccessListResponse{Items: make([]types.NodeAccess, 0, len(entries))}
		for _, entry := range entries {
			out.Items = append(out.Items, renderNodeAccess(entry))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// setNodeAccess handles PUT /operator/v1/node-access/:node_id.
//
// setNodeAccess 处理 PUT /operator/v1/node-access/:node_id。
func setNodeAccess(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.SetNodeAccessRequest
		if !decodeNodeAccess(w, r, &req) {
			return
		}
		if req.Tenants == nil {
			respondErr(w, logic.ErrInvalidInput)
			return
		}
		entry, err := ctx.Logic.SetNodeAccess(r.Context(), actor, pathvar.Vars(r)["node_id"], req.Tenants)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderNodeAccess(entry))
	}
}

func renderNodeAccess(entry logic.NodeAccessEntry) types.NodeAccess {
	out := types.NodeAccess{NodeID: entry.NodeID, Tenants: entry.Tenants, UpdatedBy: entry.UpdatedBy}
	if !entry.UpdatedAt.IsZero() {
		at := entry.UpdatedAt
		out.UpdatedAt = &at
	}
	return out
}

// decodeNodeAccess reads one bounded JSON document and refuses trailing data,
// as decodeModelAccess does, so an oversized body answers 413.
//
// decodeNodeAccess 读取一个有大小上限的 JSON 文档并拒绝尾随数据，与
// decodeModelAccess 相同，使过大的请求体得到 413。
func decodeNodeAccess(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, nodeaccess.MaxDocumentBytes)
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
		writeError(w, http.StatusRequestEntityTooLarge, "node access document exceeds limit")
	} else {
		writeError(w, http.StatusBadRequest, "the request body is not valid JSON for this endpoint")
	}
	return false
}

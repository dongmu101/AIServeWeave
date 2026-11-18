package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/zeromicro/go-zero/rest/pathvar"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// maxStudioBodyBytes bounds an internal Studio request body: a reply of
// MaxStudioReplyContentBytes content plus as much reasoning, JSON-escaped.
//
// maxStudioBodyBytes 限定内部 Studio 请求体：MaxStudioReplyContentBytes 的内容加同样多的
// 思考过程，并计入 JSON 转义。
const maxStudioBodyBytes = 6 * logic.MaxStudioReplyContentBytes

// studioOwner reads the owner the Gateway asserts.
// studioOwner 读取 Gateway 断言的所有者。
func studioOwner(r *http.Request) store.StudioOwner {
	q := r.URL.Query()
	return store.StudioOwner{TenantID: q.Get("tenant_id"), UserID: q.Get("user_id")}
}

func decodeStudio(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxStudioBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "studio request exceeds size limit")
		} else {
			writeError(w, http.StatusBadRequest, "the request body is not valid JSON for this endpoint")
		}
		return false
	}
	return true
}

// respondStudioErr gives the Studio refusals their own messages, which the
// Gateway matches on; everything else maps as respondErr does.
//
// respondStudioErr 为 Studio 的拒绝给出各自的消息，供 Gateway 匹配；其他错误与
// respondErr 的映射一致。
func respondStudioErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, logic.ErrStudioLimit):
		writeError(w, http.StatusConflict, "studio_limit")
	case errors.Is(err, logic.ErrStudioStaleTurn):
		writeError(w, http.StatusConflict, "studio_stale_turn")
	default:
		respondErr(w, err)
	}
}

func renderStudioConversation(c model.StudioConversation) types.StudioConversationResponse {
	return types.StudioConversationResponse{ID: c.ID, Title: c.Title, Model: c.Model, SystemPrompt: c.SystemPrompt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func renderStudioMessages(messages []model.StudioMessage) []types.StudioMessageResponse {
	out := make([]types.StudioMessageResponse, len(messages))
	for i, m := range messages {
		out[i] = types.StudioMessageResponse{
			ID: m.ID, Seq: m.Seq, Role: m.Role, Content: m.Content, Reasoning: m.Reasoning, Status: m.Status, Model: m.Model,
			PromptTokens: m.PromptTokens, CompletionTokens: m.CompletionTokens, ErrorCode: m.ErrorCode, CreatedAt: m.CreatedAt,
		}
	}
	return out
}

// createStudioConversation handles POST /internal/v1/studio/conversations.
func createStudioConversation(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateStudioConversationRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		c, err := ctx.Logic.CreateStudioConversation(r.Context(), studioOwner(r), req.Title, req.Model, req.SystemPrompt)
		if err != nil {
			respondStudioErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderStudioConversation(c))
	}
}

// listStudioConversations handles GET /internal/v1/studio/conversations.
func listStudioConversations(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		page, err := ctx.Logic.ListStudioConversations(r.Context(), studioOwner(r), q.Get("q"), q.Get("cursor"), limit)
		if err != nil {
			respondStudioErr(w, err)
			return
		}
		out := types.StudioConversationListResponse{Data: make([]types.StudioConversationResponse, len(page.Conversations)), Next: page.Next}
		for i, c := range page.Conversations {
			out.Data[i] = renderStudioConversation(c)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// getStudioConversation handles GET /internal/v1/studio/conversations/:id.
func getStudioConversation(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := ctx.Logic.GetStudioConversation(r.Context(), studioOwner(r), pathvar.Vars(r)["id"])
		if err != nil {
			respondStudioErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderStudioConversation(c))
	}
}

// updateStudioConversation handles PATCH /internal/v1/studio/conversations/:id.
func updateStudioConversation(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.UpdateStudioConversationRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		patch := store.StudioConversationPatch{Title: req.Title, Model: req.Model, SystemPrompt: req.SystemPrompt}
		c, err := ctx.Logic.UpdateStudioConversation(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], patch)
		if err != nil {
			respondStudioErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, renderStudioConversation(c))
	}
}

// deleteStudioConversation handles DELETE /internal/v1/studio/conversations/:id.
func deleteStudioConversation(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ctx.Logic.DeleteStudioConversation(r.Context(), studioOwner(r), pathvar.Vars(r)["id"]); err != nil {
			respondStudioErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// listStudioMessages handles GET /internal/v1/studio/conversations/:id/messages.
func listStudioMessages(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var after int64
		if raw := q.Get("after_seq"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "after_seq must be an integer")
				return
			}
			after = parsed
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		messages, err := ctx.Logic.ListStudioMessages(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], after, limit)
		if err != nil {
			respondStudioErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.StudioMessageListResponse{Data: renderStudioMessages(messages)})
	}
}

// beginStudioTurn handles POST /internal/v1/studio/conversations/:id/turns.
func beginStudioTurn(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.BeginStudioTurnRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		c, history, err := ctx.Logic.BeginStudioTurn(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], logic.StudioTurnParams{
			Regenerate: req.Regenerate, ReplaceFromID: req.ReplaceFromMessageID, Content: req.Content, Model: req.Model,
		})
		if err != nil {
			respondStudioErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.BeginStudioTurnResponse{Conversation: renderStudioConversation(c), Messages: renderStudioMessages(history)})
	}
}

// appendStudioReply handles POST /internal/v1/studio/conversations/:id/replies.
func appendStudioReply(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.AppendStudioReplyRequest
		if !decodeStudio(w, r, &req) {
			return
		}
		reply, err := ctx.Logic.AppendStudioReply(r.Context(), studioOwner(r), pathvar.Vars(r)["id"], logic.StudioReplyParams{
			ReplyTo: req.ReplyTo, ID: req.ID, Content: req.Content, Reasoning: req.Reasoning, Status: req.Status, Model: req.Model,
			PromptTokens: req.PromptTokens, CompletionTokens: req.CompletionTokens, ErrorCode: req.ErrorCode,
		})
		if err != nil {
			respondStudioErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, renderStudioMessages([]model.StudioMessage{reply})[0])
	}
}

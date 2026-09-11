package handler

import (
	"net/http"
	"strconv"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

const revocationWatchWait = 2 * time.Second

// watchAPIKeyRevocations returns the current verification generation after it
// changes or the bounded heartbeat wait elapses.
//
// watchAPIKeyRevocations 在校验 generation 变化或有界心跳等待结束后返回当前值。
func watchAPIKeyRevocations(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values, exists := r.URL.Query()["after"]
		if !exists || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "exactly one non-negative after value is required")
			return
		}
		after, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || after < 0 {
			writeError(w, http.StatusBadRequest, "exactly one non-negative after value is required")
			return
		}
		if ctx.Revocations == nil {
			writeError(w, http.StatusServiceUnavailable, "revocation notification unavailable")
			return
		}
		generation, err := ctx.Revocations.WatchGeneration(r.Context(), after, revocationWatchWait)
		if err != nil || generation < 0 {
			writeError(w, http.StatusServiceUnavailable, "revocation notification unavailable")
			return
		}
		writeJSON(w, http.StatusOK, types.RevocationGenerationResponse{Generation: generation})
	}
}

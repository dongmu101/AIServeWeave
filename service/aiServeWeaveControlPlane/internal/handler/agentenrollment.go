package handler

import (
	"net/http"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

func agentEnrollmentCA(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ca, err := ctx.Logic.AgentEnrollmentCA(actor)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, types.AgentEnrollmentCAResponse{CAPEM: ca.PEM, CASHA256: ca.SHA256})
	}
}

func createAgentEnrollment(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := actorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req types.AgentEnrollmentRequest
		if !decode(w, r, &req) {
			return
		}
		if req.TTLMinutes < 15 || req.TTLMinutes > 1440 {
			writeError(w, http.StatusBadRequest, "ttl_minutes must be between 15 and 1440")
			return
		}
		bundle, err := ctx.Logic.CreateAgentEnrollment(r.Context(), actor, req.RegistryAddr, req.GatewayAddr, time.Duration(req.TTLMinutes)*time.Minute)
		recordRegistryClientCall(ctx, err)
		if err != nil {
			respondErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, types.AgentEnrollmentResponse{NodeID: bundle.NodeID, ExpiresAt: bundle.ExpiresAt, Filename: bundle.Filename, Archive: bundle.Archive})
	}
}

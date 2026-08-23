package httpapi

import (
	"encoding/json"
	"net/http"
)

type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelJSON `json:"data"`
}

type modelJSON struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// models implements GET /v1/models: every model, deduplicated, that at
// least one connected node currently reports chat capability for.
func (h *handlers) models(w http.ResponseWriter, r *http.Request) {
	models := h.sched.Models(r.Context())
	resp := modelsResponse{Object: "list", Data: make([]modelJSON, len(models))}
	for i, m := range models {
		resp.Data[i] = modelJSON{ID: m.ID, Object: "model", OwnedBy: "aiserveweave"}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

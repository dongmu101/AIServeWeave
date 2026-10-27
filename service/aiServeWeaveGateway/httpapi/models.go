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
	ID          string `json:"id"`
	Object      string `json:"object"`
	OwnedBy     string `json:"owned_by"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// models implements GET /v1/models: every model, deduplicated, that at
// least one connected node currently reports Chat or Messages capability for.
// models 实现 GET /v1/models，返回可用模型的 ID、显示名称和目录顺序。
func (h *handlers) models(w http.ResponseWriter, r *http.Request) {
	models := h.sched.Models(r.Context())
	resp := modelsResponse{Object: "list", Data: make([]modelJSON, len(models))}
	for i, m := range models {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		resp.Data[i] = modelJSON{ID: m.ID, Object: "model", OwnedBy: "aiserveweave", Name: name, DisplayName: name}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

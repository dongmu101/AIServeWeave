package httpapi

import (
	"encoding/json"
	"net/http"

	"AIServeWeave/common/runtime"
)

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      *int     `json:"top_n"`
}

type rerankResponse struct {
	Model   string             `json:"model"`
	Results []rerankResultJSON `json:"results"`
}

type rerankResultJSON struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// rerank implements POST /v1/rerank, following the Cohere/Jina-style
// contract vLLM and other self-hosted OpenAI-ecosystem servers use for this
// capability — there is no OpenAI-defined rerank endpoint to mirror the way
// /v1/embeddings mirrors one.
func (h *handlers) rerank(w http.ResponseWriter, r *http.Request) {
	var req rerankRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "the request body is not valid JSON")
		return
	}
	if req.Model == "" || req.Query == "" || len(req.Documents) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"model, query and a non-empty documents array are required")
		return
	}

	resp, _, err := h.sched.Rerank(r.Context(), runtime.RerankRequest{
		Model:     req.Model,
		Query:     req.Query,
		Documents: req.Documents,
		TopN:      req.TopN,
	})
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}

	results := make([]rerankResultJSON, len(resp.Results))
	for i, res := range resp.Results {
		results[i] = rerankResultJSON{Index: res.Index, RelevanceScore: res.Score}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rerankResponse{Model: resp.Model, Results: results})
}

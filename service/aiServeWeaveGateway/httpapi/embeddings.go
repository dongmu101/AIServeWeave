package httpapi

import (
	"encoding/json"
	"net/http"

	"AIServeWeave/common/runtime"
)

type embeddingsRequest struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

// inputs decodes Input, which OpenAI's API allows as either a single string
// or an array of strings.
func (r embeddingsRequest) inputs() ([]string, bool) {
	var one string
	if err := json.Unmarshal(r.Input, &one); err == nil {
		return []string{one}, true
	}
	var many []string
	if err := json.Unmarshal(r.Input, &many); err == nil {
		return many, true
	}
	return nil, false
}

type embeddingsResponse struct {
	Object string          `json:"object"`
	Data   []embeddingJSON `json:"data"`
	Model  string          `json:"model"`
	Usage  usageJSON       `json:"usage"`
}

type embeddingJSON struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// embeddings implements POST /v1/embeddings.
func (h *handlers) embeddings(w http.ResponseWriter, r *http.Request) {
	var req embeddingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "the request body is not valid JSON")
		return
	}
	inputs, ok := req.inputs()
	if req.Model == "" || !ok || len(inputs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"model and input are required, and input must be a string or an array of strings")
		return
	}

	resp, _, err := h.sched.Embed(r.Context(), runtime.EmbeddingRequest{Model: req.Model, Input: inputs})
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}

	data := make([]embeddingJSON, len(resp.Data))
	for i, e := range resp.Data {
		data[i] = embeddingJSON{Object: "embedding", Index: e.Index, Embedding: e.Vector}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(embeddingsResponse{
		Object: "list",
		Data:   data,
		Model:  resp.Model,
		Usage: usageJSON{
			PromptTokens: resp.Usage.PromptTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		},
	})
}

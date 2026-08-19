package openai

import (
	"context"
	"net/http"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

type embeddingRequestDTO struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions *int     `json:"dimensions,omitempty"`
}

type embeddingObjectDTO struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type embeddingResponseDTO struct {
	Model string               `json:"model"`
	Data  []embeddingObjectDTO `json:"data"`
	Usage usageDTO             `json:"usage"`
}

// Embed calls POST /v1/embeddings and returns the resulting vectors in the
// order the backend reported them, tagged with their original Index.
func Embed(ctx context.Context, c *Client, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	dtoReq := embeddingRequestDTO{
		Model:      req.Model,
		Input:      req.Input,
		Dimensions: req.Dimensions,
	}

	var dtoResp embeddingResponseDTO
	if err := c.Do(ctx, "embed", http.MethodPost, "/v1/embeddings", dtoReq, &dtoResp); err != nil {
		return runtime.EmbeddingResponse{}, err
	}

	data := make([]runtime.Embedding, 0, len(dtoResp.Data))
	for _, e := range dtoResp.Data {
		data = append(data, runtime.Embedding{Index: e.Index, Vector: e.Embedding})
	}
	return runtime.EmbeddingResponse{
		Model: dtoResp.Model,
		Data:  data,
		Usage: runtime.Usage{
			PromptTokens:     dtoResp.Usage.PromptTokens,
			CompletionTokens: dtoResp.Usage.CompletionTokens,
			TotalTokens:      dtoResp.Usage.TotalTokens,
		},
	}, nil
}

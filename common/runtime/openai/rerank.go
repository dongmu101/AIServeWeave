package openai

import (
	"context"
	"net/http"

	"AIServeWeave/common/runtime"
)

type rerankRequestDTO struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      *int     `json:"top_n,omitempty"`
}

type rerankResultDTO struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type rerankResponseDTO struct {
	Model   string            `json:"model"`
	Results []rerankResultDTO `json:"results"`
}

// Rerank calls POST /v1/rerank, the de-facto OpenAI-ecosystem convention for
// this capability (vLLM, TEI and others all serve it at this path) — there
// is no OpenAI-defined rerank endpoint to mirror the way Chat and Embed
// mirror one.
func Rerank(ctx context.Context, c *Client, req runtime.RerankRequest) (runtime.RerankResponse, error) {
	dtoReq := rerankRequestDTO{
		Model:     req.Model,
		Query:     req.Query,
		Documents: req.Documents,
		TopN:      req.TopN,
	}

	var dtoResp rerankResponseDTO
	if err := c.Do(ctx, "rerank", http.MethodPost, "/v1/rerank", dtoReq, &dtoResp); err != nil {
		return runtime.RerankResponse{}, err
	}

	results := make([]runtime.RerankResult, 0, len(dtoResp.Results))
	for _, r := range dtoResp.Results {
		results = append(results, runtime.RerankResult{Index: r.Index, Score: r.RelevanceScore})
	}
	return runtime.RerankResponse{
		Model:   dtoResp.Model,
		Results: results,
	}, nil
}

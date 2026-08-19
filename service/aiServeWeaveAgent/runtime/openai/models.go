package openai

import (
	"context"
	"net/http"
)

type modelObject struct {
	ID string `json:"id"`
}

type modelListResponse struct {
	Data []modelObject `json:"data"`
}

// ListModels calls GET /v1/models and returns the model IDs the backend
// reports. It is used for both Probe (existence of the endpoint) and
// Discover (model inventory).
func ListModels(ctx context.Context, c *Client) ([]string, error) {
	var resp modelListResponse
	if err := c.Do(ctx, "list_models", http.MethodGet, "/v1/models", nil, &resp); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

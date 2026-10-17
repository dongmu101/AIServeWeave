package openai

import (
	"context"
	"net/http"
)

type modelObject struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
}

type modelListResponse struct {
	Data []modelObject `json:"data"`
}

// ModelCard is one entry of GET /v1/models, reduced to the fields an
// adapter may use as evidence. OwnedBy is whatever the server put in the
// optional owned_by field, empty when absent; it is a server's self-report,
// useful to tell one OpenAI-compatible implementation from another, never a
// security boundary.
//
// ModelCard 是 GET /v1/models 中的一项，只保留适配器可以用作证据的字段。OwnedBy 是
// 服务器在可选字段 owned_by 中填写的内容，缺省为空；它是服务器的自报，可用于区分
// 不同的 OpenAI 兼容实现，但绝不是安全边界。
type ModelCard struct {
	ID      string
	OwnedBy string
}

// ListModels calls GET /v1/models and returns the model IDs the backend
// reports. It is used for both Probe (existence of the endpoint) and
// Discover (model inventory).
func ListModels(ctx context.Context, c *Client) ([]string, error) {
	cards, err := ListModelCards(ctx, c)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(cards))
	for _, m := range cards {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// ListModelCards calls GET /v1/models and returns each entry with its
// owned_by field, for adapters whose Probe needs more than the IDs.
//
// ListModelCards 调用 GET /v1/models，返回每一项及其 owned_by 字段，供探测时需要
// 比模型 ID 更多信息的适配器使用。
func ListModelCards(ctx context.Context, c *Client) ([]ModelCard, error) {
	var resp modelListResponse
	if err := c.Do(ctx, "list_models", http.MethodGet, "/v1/models", nil, &resp); err != nil {
		return nil, err
	}
	cards := make([]ModelCard, 0, len(resp.Data))
	for _, m := range resp.Data {
		cards = append(cards, ModelCard{ID: m.ID, OwnedBy: m.OwnedBy})
	}
	return cards, nil
}

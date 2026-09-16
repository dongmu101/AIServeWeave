package controlplaneclient

import (
	"context"
	"encoding/json"
	"errors"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// ResponsesPersister adapts a ResponsesClient to httpapi.ResponsesPersistClient's
// minimal, primitive-typed interface — the same reason GatewayPersister
// exists for Jobs: this package already imports httpapi (for Identity and
// KeyVerifier), so httpapi cannot import this package back, and
// httpapi.ResponsesPersistClient is declared there in plain types.
//
// ResponsesPersister 把一个 ResponsesClient 适配成 httpapi.ResponsesPersistClient
// 那个最小的、只用原始类型的接口——与 GatewayPersister 之于 Jobs 存在的理由
// 相同：本包已经导入了 httpapi（用于 Identity 与 KeyVerifier），httpapi 不能
// 反过来导入本包，因此 httpapi.ResponsesPersistClient 在那边是用朴素类型
// 声明的。
type ResponsesPersister struct {
	client *ResponsesClient
}

// NewResponsesPersister wraps client for use as an httpapi.ResponsesPersistClient.
//
// NewResponsesPersister 把 client 包装成一个 httpapi.ResponsesPersistClient
// 使用。
func NewResponsesPersister(client *ResponsesClient) *ResponsesPersister {
	return &ResponsesPersister{client: client}
}

// CreateResponseTurn implements httpapi.ResponsesPersistClient.
func (p *ResponsesPersister) CreateResponseTurn(ctx context.Context, responseID, tenantID, previousResponseID, model string, messages json.RawMessage) error {
	_, err := p.client.CreateTurn(ctx, CreateTurnRequest{
		ResponseID:         responseID,
		TenantID:           tenantID,
		PreviousResponseID: previousResponseID,
		Model:              model,
		Messages:           messages,
	})
	return err
}

// GetResponseTurn implements httpapi.ResponsesPersistClient. ErrNotFound is
// translated to httpapi.ErrResponseTurnNotFound — the one error httpapi's
// responses_handler.go distinguishes from every other failure, turning it
// into a 400 instead of a 503.
//
// GetResponseTurn 实现 httpapi.ResponsesPersistClient。ErrNotFound 被转译成
// httpapi.ErrResponseTurnNotFound——这是 httpapi 的 responses_handler.go 唯一
// 会与其他所有失败区分开的错误，把它变成 400 而不是 503。
func (p *ResponsesPersister) GetResponseTurn(ctx context.Context, tenantID, responseID string) (string, json.RawMessage, error) {
	turn, err := p.client.GetTurn(ctx, tenantID, responseID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil, httpapi.ErrResponseTurnNotFound
		}
		return "", nil, err
	}
	return turn.PreviousResponseID, turn.Messages, nil
}

var _ httpapi.ResponsesPersistClient = (*ResponsesPersister)(nil)

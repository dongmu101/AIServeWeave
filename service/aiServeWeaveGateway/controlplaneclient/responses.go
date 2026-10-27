// responses.go is the Gateway's side of the control plane's Response-turn
// persistence API, per STATUS.md's P2 "Responses 持久会话". It mirrors
// jobs.go's shape (a plain net/http client authenticated by the shared
// InternalToken, translating ErrNotFound/ErrConflict/ErrOutcomeUnknown) for
// the same reason: a caller reading back a specific turn needs to be able
// to tell "no such turn" from "the control plane could not be reached", the
// same distinction JobsClient's GetJob already draws.
//
// responses.go 是 Gateway 一侧的控制面轮次持久化 API，对应 STATUS.md 的 P2
// 「Responses 持久会话」。它形态上与 jobs.go 相仿（一个由共享 InternalToken
// 认证的普通 net/http 客户端，转译 ErrNotFound/ErrConflict/ErrOutcomeUnknown），
// 理由相同：一个回读某一轮的调用方，需要能分辨「没有这一轮」与「联系不上
// 控制面」，与 JobsClient 的 GetJob 已经划开的是同一种区分。
package controlplaneclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultResponsesTimeout bounds one Response-turn persistence call.
//
// DefaultResponsesTimeout 限制单次 Response 轮次持久化调用。
const DefaultResponsesTimeout = 3 * time.Second

// ResponsesClientConfig configures a ResponsesClient.
//
// ResponsesClientConfig 配置一个 ResponsesClient。
type ResponsesClientConfig struct {
	// Endpoint is the control plane's base URL, e.g. http://127.0.0.1:8090.
	//
	// Endpoint 是控制面的基础 URL，例如 http://127.0.0.1:8090。
	Endpoint string
	// Token authenticates this Gateway to the control plane's internal
	// endpoints. It must match the control plane's InternalToken.
	//
	// Token 用于本 Gateway 向控制面的内部端点表明身份。它必须与控制面的
	// InternalToken 一致。
	Token string
	// Timeout bounds one call. Zero uses DefaultResponsesTimeout.
	//
	// Timeout 限制单次调用。为零时使用 DefaultResponsesTimeout。
	Timeout time.Duration
	// HTTPClient is used for the calls. Nil builds one with Timeout.
	//
	// HTTPClient 用于发起这些调用。为 nil 时会用 Timeout 构造一个。
	HTTPClient *http.Client
}

// ResponsesClient is the Gateway's side of the control plane's Response-turn
// persistence API.
//
// ResponsesClient 是 Gateway 一侧的控制面 Response 轮次持久化 API。
type ResponsesClient struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewResponsesClient returns a ResponsesClient, validating what a typo would
// otherwise turn into an outage discovered by the first stored conversation.
//
// NewResponsesClient 返回一个 ResponsesClient，校验那些一旦写错、就会变成
// 「由第一次存储的对话发现」的东西。
func NewResponsesClient(cfg ResponsesClientConfig) (*ResponsesClient, error) {
	endpoint := strings.TrimSuffix(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("controlplaneclient: an endpoint is required")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, errors.New("controlplaneclient: the endpoint must include a scheme, e.g. http://127.0.0.1:8090")
	}
	if cfg.Token == "" {
		return nil, errors.New("controlplaneclient: a token is required; it must match the control plane's InternalToken")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultResponsesTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &ResponsesClient{endpoint: endpoint, token: cfg.Token, client: client}, nil
}

// ResponseTurn is the control plane's persisted record of one turn, as this
// client reads it back.
//
// ResponseTurn 是控制面对一轮的持久化记录，本客户端读回的就是这份记录。
type ResponseTurn struct {
	ResponseID         string
	TenantID           string
	PreviousResponseID string
	Model              string
	Messages           json.RawMessage
	CreatedAt          time.Time
}

// CreateTurnRequest is one turn to persist.
//
// CreateTurnRequest 是要持久化的一轮。
type CreateTurnRequest struct {
	ResponseID         string
	TenantID           string
	PreviousResponseID string
	Model              string
	Messages           json.RawMessage
}

// CreateTurn persists one turn. A duplicate ResponseID for the same tenant
// is not an error on the control plane side and is not one here either:
// this call returns the existing row.
//
// CreateTurn 持久化一轮。同一租户下重复的 ResponseID 在控制面那一侧不是
// 错误，在这里也不是：这次调用会返回已有的那一行。
func (c *ResponsesClient) CreateTurn(ctx context.Context, req CreateTurnRequest) (ResponseTurn, error) {
	var wire struct {
		ResponseID         string          `json:"response_id"`
		TenantID           string          `json:"tenant_id"`
		PreviousResponseID string          `json:"previous_response_id,omitempty"`
		Model              string          `json:"model,omitempty"`
		Messages           json.RawMessage `json:"messages"`
	}
	wire.ResponseID, wire.TenantID, wire.PreviousResponseID = req.ResponseID, req.TenantID, req.PreviousResponseID
	wire.Model, wire.Messages = req.Model, req.Messages

	var turn responseTurnWire
	if err := c.call(ctx, http.MethodPost, "/internal/v1/responses", wire, &turn); err != nil {
		return ResponseTurn{}, err
	}
	return turn.toTurn(), nil
}

// GetTurn reads one turn by id, scoped to tenantID.
//
// GetTurn 按 id 读取一轮，限定在 tenantID 范围内。
func (c *ResponsesClient) GetTurn(ctx context.Context, tenantID, responseID string) (ResponseTurn, error) {
	var turn responseTurnWire
	path := "/internal/v1/responses/" + url.PathEscape(responseID) + "?tenant_id=" + url.QueryEscape(tenantID)
	if err := c.call(ctx, http.MethodGet, path, nil, &turn); err != nil {
		return ResponseTurn{}, err
	}
	return turn.toTurn(), nil
}

// responseTurnWire is the internal API's turn shape. It stays unexported for
// the same reason jobWire does: a field rename on either side of the JSON
// boundary touches one conversion function instead of every call site.
//
// responseTurnWire 是内部 API 的轮次形状。它保持未导出，理由与 jobWire
// 相同：JSON 边界任一侧的字段改名，只需改一个转换函数，而不是每个调用点。
type responseTurnWire struct {
	ResponseID         string          `json:"response_id"`
	TenantID           string          `json:"tenant_id"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Model              string          `json:"model,omitempty"`
	Messages           json.RawMessage `json:"messages"`
	CreatedAt          time.Time       `json:"created_at"`
}

func (t responseTurnWire) toTurn() ResponseTurn {
	return ResponseTurn{
		ResponseID: t.ResponseID, TenantID: t.TenantID, PreviousResponseID: t.PreviousResponseID,
		Model: t.Model, Messages: t.Messages, CreatedAt: t.CreatedAt,
	}
}

// call performs one internal API request and decodes its JSON response into
// out. It maps status codes exactly like JobsClient's call — see that
// method's doc comment for the vocabulary (ErrNotFound, ErrConflict,
// ErrInvalidRequest, ErrOutcomeUnknown) this shares by package-level
// convention rather than by embedding, since the two clients have no
// receiver in common.
//
// call 执行一次内部 API 请求，并把其 JSON 响应解码进 out。它对状态码的映射
// 与 JobsClient 的 call 完全一致——这套词汇（ErrNotFound、ErrConflict、
// ErrInvalidRequest、ErrOutcomeUnknown）见该方法的文档注释，两者共用它是
// 靠包级别的约定而非嵌入，因为这两个客户端没有共同的接收者。
func (c *ResponsesClient) call(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: reaching the control plane: %v", ErrOutcomeUnknown, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("%w: decoding the response: %v", ErrOutcomeUnknown, err)
		}
		return nil

	case http.StatusNotFound:
		return ErrNotFound

	case http.StatusConflict:
		return ErrConflict

	case http.StatusBadRequest:
		return ErrInvalidRequest

	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: this Gateway's internal token was refused by the control plane", ErrOutcomeUnknown)

	default:
		return fmt.Errorf("%w: the control plane answered %d", ErrOutcomeUnknown, resp.StatusCode)
	}
}

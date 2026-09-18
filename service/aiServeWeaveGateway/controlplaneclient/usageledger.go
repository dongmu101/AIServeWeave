// usageledger.go is the Gateway's side of the control plane's usage-ledger
// push API, per STATUS.md's P2 usage ledger. It mirrors requestlogs.go's
// shape (a plain net/http client authenticated by the shared InternalToken,
// no read-back, no retry) for the same reason: the caller (httpapi's
// usageLedgerPusher) never acts on a partial failure beyond logging it — a
// dropped batch of usage records is not a condition anything upstream needs
// to distinguish from "the control plane momentarily answered slowly".
//
// usageledger.go 是 Gateway 一侧的控制面用量账本推送 API，对应 STATUS.md 的
// P2 用量账本。它形态上与 requestlogs.go 相仿（一个由共享 InternalToken
// 认证的普通 net/http 客户端，没有读回、没有重试），理由相同：调用方
// （httpapi 的 usageLedgerPusher）除了记日志之外从不对一次部分失败采取任何
// 行动——一批用量记录被丢弃，不是任何上游需要把它与"控制面这一刻答得慢了
// 一点"区分开的情形。
package controlplaneclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// DefaultUsageLedgerTimeout bounds one push call.
//
// DefaultUsageLedgerTimeout 限制单次推送调用。
const DefaultUsageLedgerTimeout = 3 * time.Second

// UsageLedgerClientConfig configures a UsageLedgerClient.
//
// UsageLedgerClientConfig 配置一个 UsageLedgerClient。
type UsageLedgerClientConfig struct {
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
	// Timeout bounds one call. Zero uses DefaultUsageLedgerTimeout.
	//
	// Timeout 限制单次调用。为零时使用 DefaultUsageLedgerTimeout。
	Timeout time.Duration
	// HTTPClient is used for the calls. Nil builds one with Timeout.
	//
	// HTTPClient 用于发起这些调用。为 nil 时会用 Timeout 构造一个。
	HTTPClient *http.Client
}

// UsageLedgerClient is the Gateway's side of the control plane's
// usage-ledger push API. It implements httpapi.UsageLedgerClient.
//
// UsageLedgerClient 是 Gateway 一侧的控制面用量账本推送 API。它实现
// httpapi.UsageLedgerClient。
type UsageLedgerClient struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewUsageLedgerClient returns a UsageLedgerClient, validating what a typo
// would otherwise turn into a silently-empty usage ledger.
//
// NewUsageLedgerClient 返回一个 UsageLedgerClient，校验那些一旦写错、就会
// 变成"用量账本悄悄空着"的东西。
func NewUsageLedgerClient(cfg UsageLedgerClientConfig) (*UsageLedgerClient, error) {
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
		timeout = DefaultUsageLedgerTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &UsageLedgerClient{endpoint: endpoint, token: cfg.Token, client: client}, nil
}

// usageRecordWire is the internal API's usage-record shape. It stays
// unexported and separate from httpapi.UsageRecord for the same reason
// requestLogRecordWire stays separate from httpapi.RequestLogRecord: a
// field rename on either side of the JSON boundary touches one conversion
// site, not every call site.
//
// usageRecordWire 是内部 API 的用量记录形状。它保持未导出，并与
// httpapi.UsageRecord 分开，理由与 requestLogRecordWire 之于
// httpapi.RequestLogRecord 相同：JSON 边界任一侧的字段改名，只需改一个
// 转换点，而不是每个调用点。
type usageRecordWire struct {
	RequestID        string    `json:"request_id"`
	TenantID         string    `json:"tenant_id"`
	Model            string    `json:"model"`
	Endpoint         string    `json:"endpoint"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CreatedAt        time.Time `json:"created_at"`
}

// PushUsageRecords implements httpapi.UsageLedgerClient. An empty batch is
// a no-op that never reaches the network — the pusher's own flush already
// guards against calling with nothing to send, but a second guard here
// costs nothing and keeps this method safe to call directly.
//
// PushUsageRecords 实现 httpapi.UsageLedgerClient。空批次是一个从不触网的
// 空操作——推送器自己的 flush 已经防住了"无内容可发送时调用"的情形，但这里
// 再加一道防护不花什么代价，也让这个方法在被直接调用时依然安全。
func (c *UsageLedgerClient) PushUsageRecords(ctx context.Context, records []httpapi.UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	var wire struct {
		Records []usageRecordWire `json:"records"`
	}
	wire.Records = make([]usageRecordWire, len(records))
	for i, r := range records {
		wire.Records[i] = usageRecordWire{
			RequestID:        r.RequestID,
			TenantID:         r.TenantID,
			Model:            r.Model,
			Endpoint:         r.Endpoint,
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			TotalTokens:      r.TotalTokens,
			CreatedAt:        r.CreatedAt,
		}
	}

	encoded, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/internal/v1/usagerecords", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("controlplaneclient: reaching the control plane: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("controlplaneclient: the control plane answered %d", resp.StatusCode)
	}
	return nil
}

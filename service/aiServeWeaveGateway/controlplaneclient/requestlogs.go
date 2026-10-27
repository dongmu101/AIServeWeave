// requestlogs.go is the Gateway's side of the control plane's request-log
// push API, per STATUS.md's P09/C28. It mirrors jobs.go's shape (a plain
// net/http client authenticated by the shared InternalToken) but is
// intentionally simpler: there is no read-back, no conflict translation, no
// ErrOutcomeUnknown vocabulary, because the caller (httpapi's
// requestLogPusher) never acts on a partial failure beyond logging it — a
// dropped batch of diagnostic records is not a condition anything upstream
// needs to distinguish from "the control plane momentarily answered
// slowly".
//
// requestlogs.go 是 Gateway 一侧的控制面请求日志推送 API，对应 STATUS.md 的
// P09/C28。它形态上与 jobs.go 相仿(一个由共享 InternalToken 认证的普通
// net/http 客户端)，但刻意更简单：没有读回、没有冲突转译、没有
// ErrOutcomeUnknown 这套词汇，因为调用方(httpapi 的 requestLogPusher)除了
// 记日志之外从不对一次部分失败采取任何行动——一批诊断性记录被丢弃，不是任何
// 上游需要把它与"控制面这一刻答得慢了一点"区分开的情形。
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

// DefaultRequestLogsTimeout bounds one push call.
//
// DefaultRequestLogsTimeout 限制单次推送调用。
const DefaultRequestLogsTimeout = 3 * time.Second

// RequestLogsClientConfig configures a RequestLogsClient.
//
// RequestLogsClientConfig 配置一个 RequestLogsClient。
type RequestLogsClientConfig struct {
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
	// Timeout bounds one call. Zero uses DefaultRequestLogsTimeout.
	//
	// Timeout 限制单次调用。为零时使用 DefaultRequestLogsTimeout。
	Timeout time.Duration
	// HTTPClient is used for the calls. Nil builds one with Timeout.
	//
	// HTTPClient 用于发起这些调用。为 nil 时会用 Timeout 构造一个。
	HTTPClient *http.Client
}

// RequestLogsClient is the Gateway's side of the control plane's
// request-log push API. It implements httpapi.RequestLogClient.
//
// RequestLogsClient 是 Gateway 一侧的控制面请求日志推送 API。它实现
// httpapi.RequestLogClient。
type RequestLogsClient struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewRequestLogsClient returns a RequestLogsClient, validating what a typo
// would otherwise turn into a silently-empty search table.
//
// NewRequestLogsClient 返回一个 RequestLogsClient，校验那些一旦写错、就会
// 变成"检索表悄悄空着"的东西。
func NewRequestLogsClient(cfg RequestLogsClientConfig) (*RequestLogsClient, error) {
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
		timeout = DefaultRequestLogsTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &RequestLogsClient{endpoint: endpoint, token: cfg.Token, client: client}, nil
}

// requestLogRecordWire is the internal API's request-log record shape. It
// stays unexported and separate from httpapi.RequestLogRecord for the same
// reason jobWire stays separate from Job: a field rename on either side of
// the JSON boundary touches one conversion site, not every call site.
//
// requestLogRecordWire 是内部 API 的请求日志记录形状。它保持未导出，并与
// httpapi.RequestLogRecord 分开，理由与 jobWire 之于 Job 相同：JSON 边界
// 任一侧的字段改名，只需改一个转换点，而不是每个调用点。
type requestLogRecordWire struct {
	RequestID  string    `json:"request_id"`
	TenantID   string    `json:"tenant_id"`
	KeyDisplay string    `json:"key_display,omitempty"`
	Endpoint   string    `json:"endpoint"`
	StatusCode int       `json:"status_code"`
	Outcome    string    `json:"outcome"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// PushRequestLogs implements httpapi.RequestLogClient. An empty batch is a
// no-op that never reaches the network — the pusher's own flush already
// guards against calling with nothing to send, but a second guard here
// costs nothing and keeps this method safe to call directly.
//
// PushRequestLogs 实现 httpapi.RequestLogClient。空批次是一个从不触网的
// 空操作——推送器自己的 flush 已经防住了"无内容可发送时调用"的情形，但这里
// 再加一道防护不花什么代价，也让这个方法在被直接调用时依然安全。
func (c *RequestLogsClient) PushRequestLogs(ctx context.Context, records []httpapi.RequestLogRecord) error {
	if len(records) == 0 {
		return nil
	}
	var wire struct {
		Records []requestLogRecordWire `json:"records"`
	}
	wire.Records = make([]requestLogRecordWire, len(records))
	for i, r := range records {
		wire.Records[i] = requestLogRecordWire{
			RequestID:  r.RequestID,
			TenantID:   r.TenantID,
			KeyDisplay: r.KeyDisplay,
			Endpoint:   r.Endpoint,
			StatusCode: r.StatusCode,
			Outcome:    r.Outcome,
			DurationMS: r.DurationMS,
			CreatedAt:  r.CreatedAt,
		}
	}

	encoded, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/internal/v1/requestlogs", bytes.NewReader(encoded))
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

// webhook.go delivers one alert state-transition event to an operator-
// configured URL over plain net/http, with a fixed 3-attempt bounded
// retry and no persistent delivery queue — a final failure is recorded by
// the caller (evaluator.go) and dropped, never retried later, matching
// this repo's existing stance on notification reliability (P06's
// timeouts, P07's outbox) scaled down: the downstream here is an
// operator's own infrastructure, not a tenant-visible authorization path.
//
// webhook.go 通过普通 net/http 向运维配置的 URL 投递一次告警状态转换事件，
// 固定 3 次有界重试，不做持久投递队列——最终失败由调用方(evaluator.go)
// 记录后即被丢弃，此后不再重试，与本仓库对通知可靠性已有的立场
// (P06 的超时、P07 的 outbox)一致但规模更小：这里的下游是运维自己的基础
// 设施，不是租户可感知的鉴权路径。
package alertengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"AIServeWeave/common/runtime"
)

// DefaultWebhookTimeout bounds one delivery attempt.
//
// DefaultWebhookTimeout 限制单次投递尝试的时长。
const DefaultWebhookTimeout = 5 * time.Second

// DefaultWebhookMaxAttempts is how many times Notify tries before giving
// up.
//
// DefaultWebhookMaxAttempts 是 Notify 放弃之前尝试的次数。
const DefaultWebhookMaxAttempts = 3

// Sender implements Notifier over plain HTTP.
//
// Sender 用普通 HTTP 实现 Notifier。
type Sender struct {
	client *http.Client
	clock  runtime.Clock
}

// NewSender builds a Sender. A nil clock defaults to the system clock.
//
// NewSender 构造一个 Sender。clock 为 nil 时使用系统时钟。
func NewSender(clock runtime.Clock, client *http.Client) *Sender {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if client == nil {
		client = &http.Client{Timeout: DefaultWebhookTimeout}
	}
	return &Sender{client: client, clock: clock}
}

// Notify implements alertengine.Notifier: POST payload as JSON to
// webhookURL, retrying up to DefaultWebhookMaxAttempts times with
// exponential backoff (1s, 2s, ...) on any failure (network error or a
// non-2xx status). Returns the number of attempts made either way.
//
// Notify 实现 alertengine.Notifier：把 payload 序列化为 JSON POST 给
// webhookURL，任何失败(网络错误或非 2xx 状态)都以指数退避(1s、2s、……)
// 重试，最多 DefaultWebhookMaxAttempts 次。无论成功与否都返回实际尝试的
// 次数。
func (s *Sender) Notify(ctx context.Context, webhookURL string, payload WebhookPayload) (int, error) {
	if err := validateWebhookURL(webhookURL); err != nil {
		return 0, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	var lastErr error
	backoff := time.Second
	for attempt := 1; attempt <= DefaultWebhookMaxAttempts; attempt++ {
		if lastErr = s.attempt(ctx, webhookURL, body); lastErr == nil {
			return attempt, nil
		}
		if attempt < DefaultWebhookMaxAttempts {
			ch, stop := s.clock.NewTimer(backoff)
			<-ch
			stop()
			backoff *= 2
		}
	}
	return DefaultWebhookMaxAttempts, lastErr
}

func (s *Sender) attempt(ctx context.Context, webhookURL string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("alertengine: webhook answered %d", resp.StatusCode)
	}
	return nil
}

// validateWebhookURL rejects anything that is not a plain http(s) URL with
// no embedded credentials — the same shape of check
// common/runtime/openai.NewClient already applies to a configuration-
// supplied base URL, applied here to an operator-supplied one.
//
// validateWebhookURL 拒绝任何不是纯 http(s)、且不带内嵌凭据的 URL——与
// common/runtime/openai.NewClient 已经对配置提供的 base URL 施加的检查
// 同一种形状，这里用在运维提供的 URL 上。
func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("alertengine: webhook URL must use http or https")
	}
	if u.User != nil {
		return errors.New("alertengine: webhook URL must not contain embedded credentials")
	}
	return nil
}

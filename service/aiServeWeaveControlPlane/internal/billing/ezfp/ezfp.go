// Package ezfp implements billing.Ezfp over the 易支付 (EZFP) V2 API, the
// 彩虹易支付 protocol, following its published documentation and reference PHP
// SDK 2.0. It is the only package that talks to EZFP. Requests are signed
// with the merchant's RSA private key, which never leaves this process; the
// answers and notifications the ledger acts on are verified against the
// platform's public key. Upstream messages are kept out of errors and logs.
//
// ezfp 包基于易支付（EZFP）V2 接口（彩虹易支付协议）实现 billing.Ezfp，遵循其公开文档
// 与参考 PHP SDK 2.0。它是唯一与易支付通信的包。请求以商户 RSA 私钥签名，私钥从不离开
// 本进程；账目据以行动的答复与通知都用平台公钥验签。上游消息不会进入错误或日志。
package ezfp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
)

// DefaultBaseURL is the production API base.
//
// DefaultBaseURL 是正式环境的接口基址。
const DefaultBaseURL = "https://www.ezfp.cn/"

// DefaultTimeout bounds one call. It is shorter than the control plane's
// billing route timeout, so a slow answer is recorded as unknown and resumed
// later rather than cut off mid-write.
//
// DefaultTimeout 限制单次调用。它短于控制面计费路由的超时，使缓慢的答复被记为未知并在
// 稍后恢复，而不是在写入途中被截断。
const DefaultTimeout = 7 * time.Second

// maxResponseBytes bounds one answer; the documented bodies are well under
// a KiB.
//
// maxResponseBytes 限制单个答复的大小；文档中的响应体远小于 1 KiB。
const maxResponseBytes = 64 << 10

// MaxClockSkew is how far a signed API answer's timestamp may be from this
// server's clock, as in the reference SDK.
//
// MaxClockSkew 是签名接口答复的时间戳与本服务器时钟之间允许的最大偏差，与参考 SDK 一致。
const MaxClockSkew = 300 * time.Second

// maxNameBytes is the most bytes of a product name EZFP keeps.
//
// maxNameBytes 是易支付保留的商品名称最大字节数。
const maxNameBytes = 127

// ErrUnverified is returned when an answer that claims success does not
// verify, or verifies but does not match what was asked: its outcome is
// unknown, never a definite answer.
//
// ErrUnverified 在声称成功的答复验签不通过、或验签通过却与请求不符时返回：其结果未知，
// 从不视为确定的答复。
var ErrUnverified = errors.New("ezfp: the answer does not verify")

// Config configures a Client. Now defaults to time.Now; HTTPClient may be
// nil.
//
// Config 配置一个 Client。Now 默认为 time.Now；HTTPClient 可为 nil。
type Config struct {
	PID        string
	PrivateKey string
	PublicKey  string
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
}

// Client is an EZFP API client.
//
// Client 是易支付 API 客户端。
type Client struct {
	pid     string
	private *rsa.PrivateKey
	public  *rsa.PublicKey
	base    string
	http    *http.Client
	now     func() time.Time
}

var _ billing.Ezfp = (*Client)(nil)

// New returns a client for cfg. The keys are parsed here, so a bad key
// fails startup instead of the first payment. The base must be HTTPS and
// redirects are never followed.
//
// New 返回 cfg 对应的客户端。密钥在此解析，使错误的密钥让启动失败，而不是让第一笔付款
// 失败。基址必须是 HTTPS，且从不跟随重定向。
func New(cfg Config) (*Client, error) {
	if pid, err := strconv.ParseInt(cfg.PID, 10, 64); err != nil || pid <= 0 {
		return nil, errors.New("ezfp: the merchant id must be a positive integer")
	}
	private, err := ParsePrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	public, err := ParsePublicKey(cfg.PublicKey)
	if err != nil {
		return nil, err
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("ezfp: the API base must be an absolute HTTPS URL")
	}
	hc := &http.Client{Timeout: DefaultTimeout}
	if cfg.HTTPClient != nil {
		copied := *cfg.HTTPClient
		hc = &copied
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		pid: cfg.PID, private: private, public: public,
		base: strings.TrimRight(base, "/") + "/", http: hc, now: now,
	}, nil
}

// call signs params, posts them to path and returns the verified answer.
// A transport failure, a non-2xx status or a body that is not a JSON object
// is returned as an error of unknown outcome. A non-zero code is a
// *billing.EzfpError, a definite answer; a zero code whose signature or
// timestamp does not verify is ErrUnverified.
func (c *Client) call(ctx context.Context, op, path string, params map[string]string) (map[string]string, error) {
	params["pid"] = c.pid
	params["timestamp"] = strconv.FormatInt(c.now().Unix(), 10)
	signature, err := sign(c.private, params)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	form.Set("sign", signature)
	form.Set("sign_type", signType)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("ezfp: building the request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// net/http includes the request URL; keep the error to its cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("ezfp: %s: %w", op, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ezfp: %s: reading the answer: %w", op, err)
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("ezfp: %s: the answer is too large", op)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("ezfp: %s: HTTP %d", op, resp.StatusCode)
	}
	answer, err := decodeAnswer(raw)
	if err != nil {
		return nil, fmt.Errorf("ezfp: %s: %w", op, err)
	}
	code, err := strconv.Atoi(answer["code"])
	if err != nil {
		return nil, fmt.Errorf("ezfp: %s: the answer has no code", op)
	}
	if code != 0 {
		return nil, &billing.EzfpError{Op: op, Code: code}
	}
	if !verify(c.public, answer) || !c.fresh(answer["timestamp"]) || answer["pid"] != "" && answer["pid"] != c.pid {
		return nil, fmt.Errorf("%w (%s)", ErrUnverified, op)
	}
	return answer, nil
}

// fresh reports whether a signed timestamp is within MaxClockSkew of now.
func (c *Client) fresh(stamp string) bool {
	sec, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return false
	}
	skew := c.now().Sub(time.Unix(sec, 0))
	return skew <= MaxClockSkew && skew >= -MaxClockSkew
}

// decodeAnswer flattens a JSON object into the strings PHP would sign:
// strings as they are, numbers as written, true as "1". false, null, arrays
// and objects become empty, so the signature leaves them out as the
// reference SDK does.
func decodeAnswer(raw []byte) (map[string]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("the answer is not a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, errors.New("the answer is not a JSON object")
	}
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		v = bytes.TrimSpace(v)
		switch {
		case len(v) == 0:
		case v[0] == '"':
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, errors.New("the answer is not valid JSON")
			}
			out[k] = s
		case string(v) == "true":
			out[k] = "1"
		case string(v) == "false", string(v) == "null", v[0] == '[', v[0] == '{':
			out[k] = ""
		default:
			out[k] = string(v)
		}
	}
	return out, nil
}

// truncateName cuts a product name to maxNameBytes on a character boundary,
// so EZFP's own byte cut cannot split a character.
func truncateName(name string) string {
	if len(name) <= maxNameBytes {
		return name
	}
	cut := maxNameBytes
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

// CreateOrder implements billing.Ezfp with method=jump, so the answer is
// only ever a page to send the tenant to.
//
// CreateOrder 以 method=jump 实现 billing.Ezfp，使答复只会是一个引导租户前往的页面。
func (c *Client) CreateOrder(ctx context.Context, req billing.EzfpOrderRequest) (billing.EzfpOrder, error) {
	if req.OutTradeNo == "" || req.MoneyMinor <= 0 || req.PayType == "" {
		return billing.EzfpOrder{}, errors.New("ezfp: create: incomplete request")
	}
	answer, err := c.call(ctx, "create", "api/pay/create", map[string]string{
		"method":       "jump",
		"type":         req.PayType,
		"out_trade_no": req.OutTradeNo,
		"notify_url":   req.NotifyURL,
		"return_url":   req.ReturnURL,
		"name":         truncateName(req.Name),
		"money":        FormatMoney(req.MoneyMinor),
		"clientip":     req.ClientIP,
		"param":        req.Param,
	})
	if err != nil {
		return billing.EzfpOrder{}, err
	}
	tradeNo, payURL := answer["trade_no"], answer["pay_info"]
	if tradeNo == "" || answer["pay_type"] != "jump" || !httpsURL(payURL) {
		return billing.EzfpOrder{}, fmt.Errorf("%w (create: no trade or https payment page)", ErrUnverified)
	}
	return billing.EzfpOrder{TradeNo: tradeNo, PayURL: payURL}, nil
}

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// QueryOrder implements billing.Ezfp.
//
// QueryOrder 实现 billing.Ezfp。
func (c *Client) QueryOrder(ctx context.Context, outTradeNo string) (billing.EzfpPayment, error) {
	answer, err := c.call(ctx, "query", "api/pay/query", map[string]string{"out_trade_no": outTradeNo})
	if err != nil {
		return billing.EzfpPayment{}, err
	}
	payment, err := paymentFrom(answer)
	if err != nil || payment.OutTradeNo != outTradeNo || payment.TradeNo == "" {
		return billing.EzfpPayment{}, fmt.Errorf("%w (query: the answer is for another order)", ErrUnverified)
	}
	status, err := strconv.Atoi(answer["status"])
	if err != nil {
		return billing.EzfpPayment{}, fmt.Errorf("%w (query: no status)", ErrUnverified)
	}
	payment.Status = status
	return payment, nil
}

// paymentFrom reads the payment fields a query and a notification share.
func paymentFrom(fields map[string]string) (billing.EzfpPayment, error) {
	p := billing.EzfpPayment{
		PID: fields["pid"], TradeNo: fields["trade_no"], OutTradeNo: fields["out_trade_no"],
		Type: fields["type"], TradeStatus: fields["trade_status"], Param: fields["param"], Status: -1,
	}
	money, err := ParseMoney(fields["money"])
	if err != nil {
		return billing.EzfpPayment{}, err
	}
	p.MoneyMinor = money
	if stamp, err := strconv.ParseInt(fields["timestamp"], 10, 64); err == nil {
		p.Timestamp = stamp
	}
	return p, nil
}

// ParseNotify implements billing.Ezfp. Only the signature is checked, not
// how old the timestamp is: settling a payment is idempotent, so a replay
// gains nothing, while a retry that kept its first timestamp must not be
// lost. A repeated or bracketed parameter, which PHP would read differently
// from Go, does not verify.
//
// ParseNotify 实现 billing.Ezfp。只校验签名，不校验时间戳的新旧：结算付款是幂等的，
// 重放得不到任何东西，而保留了首次时间戳的重投不能丢失。重复或带方括号的参数（PHP 与 Go
// 读法不同）一律校验不通过。
func (c *Client) ParseNotify(params url.Values) (billing.EzfpPayment, error) {
	fields := make(map[string]string, len(params))
	for k, values := range params {
		if len(values) != 1 || strings.ContainsAny(k, "[]") {
			return billing.EzfpPayment{}, billing.ErrInvalidSignature
		}
		fields[k] = values[0]
	}
	if !verify(c.public, fields) || fields["pid"] != c.pid {
		return billing.EzfpPayment{}, billing.ErrInvalidSignature
	}
	payment, err := paymentFrom(fields)
	if err != nil || payment.TradeNo == "" || payment.OutTradeNo == "" {
		return billing.EzfpPayment{}, billing.ErrInvalidSignature
	}
	if payment.TradeStatus == billing.EzfpTradeSuccess {
		payment.Status = billing.EzfpStatusPaid
	}
	return payment, nil
}

// Refund implements billing.Ezfp. It is never retried here: a retry belongs
// to the caller, who must first learn whether the refund exists.
//
// Refund 实现 billing.Ezfp。这里从不重试：重试由调用方决定，调用方须先确认退款是否存在。
func (c *Client) Refund(ctx context.Context, req billing.EzfpRefundRequest) (billing.EzfpRefund, error) {
	if req.OutRefundNo == "" || req.TradeNo == "" || req.MoneyMinor <= 0 {
		return billing.EzfpRefund{}, errors.New("ezfp: refund: incomplete request")
	}
	answer, err := c.call(ctx, "refund", "api/pay/refund", map[string]string{
		"trade_no":      req.TradeNo,
		"money":         FormatMoney(req.MoneyMinor),
		"out_refund_no": req.OutRefundNo,
	})
	var refused *billing.EzfpError
	if errors.As(err, &refused) {
		return billing.EzfpRefund{}, fmt.Errorf("%w: %w", billing.ErrRefundRejected, err)
	}
	if err != nil {
		return billing.EzfpRefund{}, err
	}
	refund, err := refundFrom(answer, req.MoneyMinor)
	if err != nil || refund.RefundNo == "" || (refund.TradeNo != "" && refund.TradeNo != req.TradeNo) ||
		(refund.OutRefundNo != "" && refund.OutRefundNo != req.OutRefundNo) {
		return billing.EzfpRefund{}, fmt.Errorf("%w (refund: the answer is for another refund)", ErrUnverified)
	}
	refund.OutRefundNo, refund.TradeNo, refund.Status = req.OutRefundNo, req.TradeNo, billing.EzfpRefundSucceeded
	return refund, nil
}

// QueryRefund implements billing.Ezfp.
//
// QueryRefund 实现 billing.Ezfp。
func (c *Client) QueryRefund(ctx context.Context, outRefundNo string) (billing.EzfpRefund, error) {
	answer, err := c.call(ctx, "refundquery", "api/pay/refundquery", map[string]string{"out_refund_no": outRefundNo})
	if err != nil {
		return billing.EzfpRefund{}, err
	}
	refund, err := refundFrom(answer, 0)
	if err != nil || refund.RefundNo == "" || refund.OutRefundNo != outRefundNo {
		return billing.EzfpRefund{}, fmt.Errorf("%w (refundquery: the answer is for another refund)", ErrUnverified)
	}
	status, err := strconv.Atoi(answer["status"])
	if err != nil || (status != billing.EzfpRefundFailed && status != billing.EzfpRefundSucceeded) {
		return billing.EzfpRefund{}, fmt.Errorf("%w (refundquery: no status)", ErrUnverified)
	}
	refund.Status = status
	return refund, nil
}

// refundFrom reads a refund answer; fallback is the amount when the answer
// leaves it out.
func refundFrom(fields map[string]string, fallback int64) (billing.EzfpRefund, error) {
	r := billing.EzfpRefund{
		RefundNo: fields["refund_no"], OutRefundNo: fields["out_refund_no"], TradeNo: fields["trade_no"],
		MoneyMinor: fallback, Status: billing.EzfpRefundStatusNone,
	}
	if raw := fields["money"]; raw != "" {
		money, err := ParseMoney(raw)
		if err != nil {
			return billing.EzfpRefund{}, err
		}
		r.MoneyMinor = money
	}
	return r, nil
}

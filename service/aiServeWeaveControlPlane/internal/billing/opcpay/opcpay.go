// Package opcpay implements billing.Alipay over the 一人付 (opcpay) server
// API, following the contract of the npm package opcpay and its published
// OpenAPI. It is the only package that talks to 一人付. The API key is a
// bearer credential: it is sent only to the configured HTTPS base, never
// logged, and never returned in an error.
//
// opcpay 包基于一人付（opcpay）服务端 API 实现 billing.Alipay，遵循 npm 包 opcpay 及其
// 公开 OpenAPI 的契约。它是唯一与一人付通信的包。API Key 是 Bearer 凭据：只发往配置的
// HTTPS 基址，从不写入日志，也从不出现在错误里。
package opcpay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
)

// APIKeyEnv is the environment variable the API key is read from. It is the
// name the opcpay SDK reads, so one deployment secret serves both.
//
// APIKeyEnv 是读取 API Key 的环境变量，与 opcpay SDK 读取的名称相同，一个部署密钥两边通用。
const APIKeyEnv = "opcpay_api_key"

// DefaultBaseURL is the production API base.
//
// DefaultBaseURL 是正式环境的接口基址。
const DefaultBaseURL = "https://ai6666.com/opcpay/api/pay/v1"

// maxResponseBytes bounds one answer; the documented bodies are a few KiB.
//
// maxResponseBytes 限制单个答复的大小；文档中的响应体只有几 KiB。
const maxResponseBytes = 1 << 20

// DefaultTimeout bounds one call. It is shorter than the control plane's
// billing route timeout, so a slow answer is recorded as unknown and resumed
// later rather than cut off mid-write.
//
// DefaultTimeout 限制单次调用。它短于控制面计费路由的超时，使缓慢的答复被记为未知并在
// 稍后恢复，而不是在写入途中被截断。
const DefaultTimeout = 7 * time.Second

// Client is a 一人付 API client.
//
// Client 是一人付 API 客户端。
type Client struct {
	key  string
	base string
	http *http.Client
}

var _ billing.Alipay = (*Client)(nil)

// New returns a client for apiKey at baseURL (DefaultBaseURL when empty),
// which must be HTTPS. httpClient may be nil; redirects are never followed,
// so the key cannot be carried to another host.
//
// New 返回指向 baseURL（为空时用 DefaultBaseURL）的 apiKey 客户端，基址必须是 HTTPS。
// httpClient 可为 nil；从不跟随重定向，因此 Key 不会被带往其他主机。
func New(apiKey, baseURL string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("opcpay: the API key is empty; set " + APIKeyEnv + " in the server environment")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("opcpay: the API base must be an absolute HTTPS URL")
	}
	hc := &http.Client{Timeout: DefaultTimeout}
	if httpClient != nil {
		copied := *httpClient
		hc = &copied
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{key: apiKey, base: strings.TrimRight(baseURL, "/"), http: hc}, nil
}

// errorBody is the documented error answer.
type errorBody struct {
	ErrorCode string `json:"error_code"`
	Retryable bool   `json:"retryable"`
	RequestID string `json:"request_id"`
}

// call sends one request and decodes a 2xx JSON object into out. Any other
// status is an *billing.AlipayError; a transport failure is returned as is,
// without the request URL, which net/http would otherwise include.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return errors.New("opcpay: building the request failed")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("opcpay: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("opcpay: %s %s: reading the answer: %w", method, path, err)
	}
	requestID := resp.Header.Get("X-Request-ID")
	if len(raw) > maxResponseBytes {
		return &billing.AlipayError{Status: resp.StatusCode, Code: "INVALID_RESPONSE", Retryable: true, RequestID: requestID}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var eb errorBody
		_ = json.Unmarshal(raw, &eb)
		if eb.RequestID == "" {
			eb.RequestID = requestID
		}
		code := eb.ErrorCode
		if code == "" {
			code = "HTTP_" + strconv.Itoa(resp.StatusCode)
		}
		return &billing.AlipayError{Status: resp.StatusCode, Code: code, Retryable: eb.Retryable || resp.StatusCode == 429 || resp.StatusCode >= 500, RequestID: eb.RequestID}
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, out) != nil {
		return &billing.AlipayError{Status: resp.StatusCode, Code: "INVALID_RESPONSE", Retryable: true, RequestID: requestID}
	}
	return nil
}

// flexInt decodes a JSON integer, or an integer in a string, which the
// record endpoints use for amounts.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(n)
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v != float64(int64(v)) {
		return errors.New("opcpay: not an integer")
	}
	*f = flexInt(int64(v))
	return nil
}

// flexString decodes a string, or a number written as one (price,
// price_version and status_version vary between the two).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	if string(b) == "null" {
		*f = ""
		return nil
	}
	*f = flexString(b)
	return nil
}

// Capabilities implements billing.Alipay.
//
// Capabilities 实现 billing.Alipay。
func (c *Client) Capabilities(ctx context.Context) (billing.AlipayCapabilities, error) {
	var out struct {
		ProviderConfigured bool `json:"provider_configured"`
		Capabilities       struct {
			PurchaseEntry    bool `json:"purchase_entry"`
			EntitlementQuery bool `json:"entitlement_query"`
			Consumption      bool `json:"consumption"`
		} `json:"capabilities"`
	}
	if err := c.call(ctx, http.MethodGet, "/capabilities", nil, &out); err != nil {
		return billing.AlipayCapabilities{}, err
	}
	return billing.AlipayCapabilities{
		ProviderConfigured: out.ProviderConfigured, PurchaseEntry: out.Capabilities.PurchaseEntry,
		EntitlementQuery: out.Capabilities.EntitlementQuery, Consumption: out.Capabilities.Consumption,
	}, nil
}

type wireOption struct {
	SkuID          string     `json:"sku_id"`
	DisplayName    string     `json:"display_name"`
	BillingMode    string     `json:"billing_mode"`
	Price          flexString `json:"price"`
	Currency       string     `json:"currency"`
	QuotaUnit      string     `json:"quota_unit"`
	QuotaAmount    flexInt    `json:"quota_amount"`
	DurationPeriod string     `json:"duration_period"`
}

func (o wireOption) option() billing.AlipayOption {
	return billing.AlipayOption{
		SkuID: o.SkuID, DisplayName: o.DisplayName, BillingMode: o.BillingMode, Price: string(o.Price),
		Currency: o.Currency, QuotaUnit: o.QuotaUnit, QuotaAmount: int64(o.QuotaAmount), Duration: o.DurationPeriod,
	}
}

// Product implements billing.Alipay.
//
// Product 实现 billing.Alipay。
func (c *Client) Product(ctx context.Context) (billing.AlipayProduct, error) {
	var out struct {
		CapabilityStatus string       `json:"capability_status"`
		ProductName      string       `json:"product_name"`
		BillingMode      string       `json:"billing_mode"`
		QuotaUnit        string       `json:"quota_unit"`
		ChargingOptions  []wireOption `json:"charging_options"`
		ReasonMessage    string       `json:"reason_message"`
	}
	if err := c.call(ctx, http.MethodGet, "/product", nil, &out); err != nil {
		return billing.AlipayProduct{}, err
	}
	p := billing.AlipayProduct{
		CapabilityStatus: out.CapabilityStatus, ProductName: out.ProductName, BillingMode: out.BillingMode,
		QuotaUnit: out.QuotaUnit, ReasonMessage: out.ReasonMessage, Options: make([]billing.AlipayOption, 0, len(out.ChargingOptions)),
	}
	for _, o := range out.ChargingOptions {
		p.Options = append(p.Options, o.option())
	}
	return p, nil
}

// Access implements billing.Alipay.
//
// Access 实现 billing.Alipay。
func (c *Client) Access(ctx context.Context, userID string, amount int64) (billing.AlipayAccess, error) {
	var out struct {
		AccessGranted bool    `json:"access_granted"`
		Decision      string  `json:"decision"`
		ReasonCode    string  `json:"reason_code"`
		QuotaUnit     string  `json:"quota_unit"`
		Total         flexInt `json:"total"`
		Used          flexInt `json:"used"`
		Remaining     flexInt `json:"remaining"`
		ValidUntil    string  `json:"valid_until"`
	}
	body := map[string]any{"user_id": userID}
	if amount > 0 {
		body["amount"] = amount
	}
	if err := c.call(ctx, http.MethodPost, "/access", body, &out); err != nil {
		return billing.AlipayAccess{}, err
	}
	return billing.AlipayAccess{
		AccessGranted: out.AccessGranted, Decision: out.Decision, ReasonCode: out.ReasonCode, QuotaUnit: out.QuotaUnit,
		Total: int64(out.Total), Used: int64(out.Used), Remaining: int64(out.Remaining), ValidUntil: out.ValidUntil,
	}, nil
}

// Purchase implements billing.Alipay.
//
// Purchase 实现 billing.Alipay。
func (c *Client) Purchase(ctx context.Context, req billing.AlipayPurchaseRequest) (billing.AlipayPurchase, error) {
	body := map[string]any{"user_id": req.UserID, "idempotency_key": req.IdempotencyKey}
	if req.SkuID != "" {
		body["sku_id"] = req.SkuID
	}
	if req.CallbackURL != "" {
		body["callback_url"] = req.CallbackURL
	}
	var out struct {
		Status      string `json:"status"`
		PurchaseURL string `json:"purchase_url"`
		QRCodeURL   string `json:"purchase_qr_code"`
		ExpireTime  string `json:"expire_time"`
		ReasonCode  string `json:"reason_code"`
	}
	if err := c.call(ctx, http.MethodPost, "/purchase", body, &out); err != nil {
		return billing.AlipayPurchase{}, err
	}
	// purchase_url is an alipays:// app link, which a browser cannot open;
	// purchase_qr_code is the https page that works there.
	//
	// purchase_url 是 alipays:// 的 App 唤起链接，浏览器打不开；purchase_qr_code 是
	// 浏览器可用的 https 页面。
	link := out.PurchaseURL
	if !strings.HasPrefix(link, "https://") && strings.HasPrefix(out.QRCodeURL, "https://") {
		link = out.QRCodeURL
	}
	return billing.AlipayPurchase{Status: out.Status, URL: link, ExpireTime: out.ExpireTime, ReasonCode: out.ReasonCode}, nil
}

type wireConsumption struct {
	Status         string  `json:"status"`
	Confirmed      bool    `json:"confirmed"`
	IdempotencyKey string  `json:"idempotency_key"`
	QuotaUnit      string  `json:"quota_unit"`
	Consumed       flexInt `json:"consumed"`
	ConsumeOrderID string  `json:"consume_order_id"`
	ReasonCode     string  `json:"reason_code"`
}

func (w wireConsumption) consumption() billing.AlipayConsumption {
	return billing.AlipayConsumption{
		Status: w.Status, Confirmed: w.Confirmed, IdempotencyKey: w.IdempotencyKey, QuotaUnit: w.QuotaUnit,
		Consumed: int64(w.Consumed), ConsumeOrderID: w.ConsumeOrderID, ReasonCode: w.ReasonCode,
	}
}

// Consume implements billing.Alipay.
//
// Consume 实现 billing.Alipay。
func (c *Client) Consume(ctx context.Context, req billing.AlipayConsumeRequest) (billing.AlipayConsumption, error) {
	var out wireConsumption
	if err := c.call(ctx, http.MethodPost, "/consume", map[string]any{
		"user_id": req.UserID, "idempotency_key": req.IdempotencyKey, "amount": req.Amount, "reason": req.Reason,
	}, &out); err != nil {
		return billing.AlipayConsumption{}, err
	}
	return out.consumption(), nil
}

// Consumption implements billing.Alipay.
//
// Consumption 实现 billing.Alipay。
func (c *Client) Consumption(ctx context.Context, userID, idempotencyKey string) (billing.AlipayConsumption, error) {
	var out wireConsumption
	if err := c.call(ctx, http.MethodPost, "/consumption", map[string]any{
		"user_id": userID, "idempotency_key": idempotencyKey,
	}, &out); err != nil {
		return billing.AlipayConsumption{}, err
	}
	return out.consumption(), nil
}

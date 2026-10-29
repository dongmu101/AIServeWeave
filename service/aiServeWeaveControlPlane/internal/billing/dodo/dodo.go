// Package dodo implements billing.Provider over Dodo Payments
// (github.com/dodopayments/dodopayments-go). It is the only package in the
// repository that imports that SDK: the logic layer sees billing.Provider,
// and the SDK's types stop here.
//
// dodo 包基于 Dodo Payments（github.com/dodopayments/dodopayments-go）实现
// billing.Provider。它是仓库中唯一 import 该 SDK 的包：logic 层只看到
// billing.Provider，SDK 的类型止步于此。
package dodo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	dodopayments "github.com/dodopayments/dodopayments-go"
	"github.com/dodopayments/dodopayments-go/option"
	"github.com/dodopayments/dodopayments-go/shared"
	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Environments a Config may name.
//
// Config 可以指定的环境。
const (
	EnvironmentTest = "test"
	EnvironmentLive = "live"
)

// Metadata keys the checkout carries and its payment events return.
//
// 结账携带、其付款事件返回的 metadata 键。
const (
	metadataOrderID  = "aisw_order_id"
	metadataTenantID = "aisw_tenant_id"
)

// requestTimeout bounds one attempt of an API call and maxRetries the
// attempts after it. The whole call is further bounded by the request's own
// context, which the routes that call the provider cap at
// handler.BillingCallTimeout.
//
// requestTimeout 限制一次 API 调用中每次尝试的时长，maxRetries 限制其后的重试次数。
// 整个调用还受请求自身 context 的约束，调用服务商的路由把它限制在
// handler.BillingCallTimeout。
const (
	requestTimeout = 4 * time.Second
	maxRetries     = 1
)

// Config is the Dodo account the provider talks to.
//
// Config 是 provider 所对接的 Dodo 账户。
type Config struct {
	APIKey        string
	WebhookSecret string
	Environment   string
	// BaseURL overrides the environment's API address. It exists for tests
	// that point the client at a local server.
	//
	// BaseURL 覆盖环境对应的 API 地址，供把客户端指向本地服务器的测试使用。
	BaseURL string
}

// Provider is billing.Provider over Dodo Payments.
//
// Provider 是基于 Dodo Payments 的 billing.Provider。
type Provider struct {
	client  *dodopayments.Client
	webhook *standardwebhooks.Webhook
}

var _ billing.Provider = (*Provider)(nil)

// New returns a Provider for cfg. It makes no network call, but it does
// parse the webhook secret, so a malformed one fails start-up instead of
// every webhook.
//
// New 为 cfg 返回一个 Provider。它不发起任何网络调用，但会解析 webhook 密钥，因此格式
// 错误的密钥会让启动失败，而不是让每一个 webhook 都失败。
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" || cfg.WebhookSecret == "" {
		return nil, errors.New("dodo: an API key and a webhook secret are required")
	}
	webhook, err := standardwebhooks.NewWebhook(cfg.WebhookSecret)
	if err != nil {
		return nil, fmt.Errorf("dodo: the webhook secret is malformed: %w", err)
	}
	opts := []option.RequestOption{
		option.WithBearerToken(cfg.APIKey),
		option.WithRequestTimeout(requestTimeout),
		option.WithMaxRetries(maxRetries),
	}
	switch cfg.Environment {
	case EnvironmentTest:
		opts = append(opts, option.WithEnvironmentTestMode())
	case EnvironmentLive:
		opts = append(opts, option.WithEnvironmentLiveMode())
	default:
		return nil, fmt.Errorf("dodo: unknown environment %q", cfg.Environment)
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Provider{client: dodopayments.NewClient(opts...), webhook: webhook}, nil
}

// CreateCustomer implements billing.Provider.
//
// CreateCustomer 实现 billing.Provider。
func (p *Provider) CreateCustomer(ctx context.Context, email, name, tenantID string) (string, error) {
	customer, err := p.client.Customers.New(ctx, dodopayments.CustomerNewParams{
		Email:    dodopayments.F(email),
		Name:     dodopayments.F(name),
		Metadata: dodopayments.F(dodopayments.MetadataParam{metadataTenantID: shared.UnionString(tenantID)}),
	})
	if err != nil {
		return "", fmt.Errorf("dodo: creating customer: %w", err)
	}
	return customer.CustomerID, nil
}

// CreateCheckout implements billing.Provider. The checkout charges in the
// order's currency with no discount codes, so what the tenant pays is what
// the order's credits were priced at; the payment event is checked against
// that again before anything is credited.
//
// CreateCheckout 实现 billing.Provider。结账以订单币种收款且不接受折扣码，因此租户支付
// 的正是订单点数的定价；入账前还会依据付款事件再核对一次。
func (p *Provider) CreateCheckout(ctx context.Context, req billing.CheckoutRequest) (billing.Checkout, error) {
	item := dodopayments.ProductItemReqParam{
		ProductID: dodopayments.F(req.ProductID),
		Quantity:  dodopayments.F(int64(1)),
	}
	if req.AmountMinor > 0 {
		item.Amount = dodopayments.F(req.AmountMinor)
	}
	params := dodopayments.CheckoutSessionRequestParam{
		ProductCart: dodopayments.F([]dodopayments.ProductItemReqParam{item}),
		ReturnURL:   dodopayments.F(req.ReturnURL),
		CancelURL:   dodopayments.F(req.ReturnURL),
		Metadata: dodopayments.F(dodopayments.MetadataParam{
			metadataOrderID:  shared.UnionString(req.OrderID),
			metadataTenantID: shared.UnionString(req.TenantID),
		}),
		FeatureFlags: dodopayments.F(dodopayments.CheckoutSessionFlagsParam{
			AllowDiscountCode:      dodopayments.F(false),
			AllowCurrencySelection: dodopayments.F(false),
		}),
	}
	if req.Currency != "" {
		params.BillingCurrency = dodopayments.F(dodopayments.Currency(strings.ToUpper(req.Currency)))
	}
	if req.CustomerID != "" {
		params.Customer = dodopayments.F[dodopayments.CustomerRequestUnionParam](
			dodopayments.AttachExistingCustomerParam{CustomerID: dodopayments.F(req.CustomerID)})
	}
	res, err := p.client.CheckoutSessions.New(ctx, dodopayments.CheckoutSessionNewParams{CheckoutSessionRequest: params})
	if err != nil {
		return billing.Checkout{}, fmt.Errorf("dodo: creating checkout session: %w", err)
	}
	if res.CheckoutURL == "" {
		return billing.Checkout{}, errors.New("dodo: checkout session has no checkout URL")
	}
	return billing.Checkout{SessionID: res.SessionID, URL: res.CheckoutURL}, nil
}

// PortalLink implements billing.Provider.
//
// PortalLink 实现 billing.Provider。
func (p *Provider) PortalLink(ctx context.Context, customerID, returnURL string) (string, error) {
	session, err := p.client.Customers.CustomerPortal.New(ctx, customerID, dodopayments.CustomerCustomerPortalNewParams{
		ReturnURL: dodopayments.F(returnURL),
	})
	if err != nil {
		return "", fmt.Errorf("dodo: creating customer portal session: %w", err)
	}
	return session.Link, nil
}

// ParseWebhook implements billing.Provider. The signature is verified first,
// by Standard Webhooks over webhook-id, webhook-timestamp and the raw body; a
// timestamp outside the tolerance fails like a bad signature. Only then is
// the body decoded, and a verified body this SDK cannot decode — an event
// type newer than it — is acknowledged as an event of no interest rather
// than refused, since refusing would only make the provider redeliver it.
//
// ParseWebhook 实现 billing.Provider。先按 Standard Webhooks 对 webhook-id、
// webhook-timestamp 与原始请求体校验签名，超出容差的时间戳与错误签名同样失败。之后才
// 解码请求体；已验签但本 SDK 无法解码的请求体（比 SDK 更新的事件类型）按不关心的事件
// 确认，而不是拒绝——拒绝只会让服务商重新投递。
func (p *Provider) ParseWebhook(payload []byte, headers http.Header) (store.BillingEvent, error) {
	if err := p.webhook.Verify(payload, headers); err != nil {
		return store.BillingEvent{}, fmt.Errorf("%w: %w", billing.ErrInvalidSignature, err)
	}
	out := store.BillingEvent{WebhookID: headers.Get("webhook-id")}
	event, err := p.client.Webhooks.UnsafeUnwrap(payload)
	if err != nil {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(payload, &head)
		out.Type = head.Type
		slog.Warn("a verified billing webhook did not decode; acknowledging it unhandled",
			slog.String("webhook_id", out.WebhookID), slog.String("type", head.Type))
		return out, nil
	}
	out.Type = string(event.Type)
	switch e := event.AsUnion().(type) {
	case dodopayments.PaymentSucceededWebhookEvent:
		fillPayment(&out, store.BillingEventPaymentSucceeded, e.Data)
	case dodopayments.PaymentFailedWebhookEvent:
		fillPayment(&out, store.BillingEventPaymentFailed, e.Data)
	case dodopayments.PaymentCancelledWebhookEvent:
		fillPayment(&out, store.BillingEventPaymentFailed, e.Data)
	case dodopayments.RefundSucceededWebhookEvent:
		out.Kind = store.BillingEventRefundSucceeded
		out.PaymentID, out.RefundID, out.AmountMinor = e.Data.PaymentID, e.Data.RefundID, e.Data.Amount
		out.Currency, out.IsPartial = string(e.Data.Currency), e.Data.IsPartial
	case dodopayments.DisputeOpenedWebhookEvent:
		out.Kind = store.BillingEventDisputeOpened
		out.PaymentID, out.DisputeID = e.Data.PaymentID, e.Data.DisputeID
	case dodopayments.DisputeWonWebhookEvent:
		out.Kind = store.BillingEventDisputeWon
		out.PaymentID, out.DisputeID = e.Data.PaymentID, e.Data.DisputeID
	case dodopayments.DisputeCancelledWebhookEvent:
		// The customer withdrew the dispute: the merchant keeps the money, as
		// with a won one. 顾客撤回了争议：商户保住款项，与胜诉相同。
		out.Kind = store.BillingEventDisputeWon
		out.PaymentID, out.DisputeID = e.Data.PaymentID, e.Data.DisputeID
	}
	return out, nil
}

func fillPayment(out *store.BillingEvent, kind string, payment dodopayments.Payment) {
	out.Kind = kind
	out.PaymentID = payment.PaymentID
	out.CheckoutSessionID = payment.CheckoutSessionID
	out.AmountMinor = payment.TotalAmount
	out.Currency = string(payment.Currency)
	out.OrderID = metadataString(payment.Metadata, metadataOrderID)
	out.TenantID = metadataString(payment.Metadata, metadataTenantID)
}

func metadataString(metadata dodopayments.Metadata, key string) string {
	if value, ok := metadata[key].(shared.UnionString); ok {
		return string(value)
	}
	return ""
}

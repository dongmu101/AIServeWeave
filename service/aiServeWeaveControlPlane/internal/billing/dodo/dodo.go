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
	"errors"
	"fmt"
	"net/http"
	"time"

	dodopayments "github.com/dodopayments/dodopayments-go"
	"github.com/dodopayments/dodopayments-go/option"
	"github.com/dodopayments/dodopayments-go/shared"

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

// requestTimeout bounds one API call, retries included, so a slow provider
// costs a Console request seconds rather than a hung connection.
//
// requestTimeout 限制一次 API 调用（含重试）的时长，让一个缓慢的服务商只让 Console
// 请求多等几秒，而不是挂住一条连接。
const requestTimeout = 15 * time.Second

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
	client *dodopayments.Client
}

var _ billing.Provider = (*Provider)(nil)

// New returns a Provider for cfg. It makes no network call.
//
// New 为 cfg 返回一个 Provider。它不发起任何网络调用。
func New(cfg Config) (*Provider, error) {
	if cfg.APIKey == "" || cfg.WebhookSecret == "" {
		return nil, errors.New("dodo: an API key and a webhook secret are required")
	}
	opts := []option.RequestOption{
		option.WithBearerToken(cfg.APIKey),
		option.WithWebhookKey(cfg.WebhookSecret),
		option.WithRequestTimeout(requestTimeout),
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
	return &Provider{client: dodopayments.NewClient(opts...)}, nil
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

// CreateCheckout implements billing.Provider.
//
// CreateCheckout 实现 billing.Provider。
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

// ParseWebhook implements billing.Provider. Verification follows Standard
// Webhooks through the SDK's Unwrap: the signature covers webhook-id,
// webhook-timestamp and the raw body, and a timestamp outside the tolerance
// fails like a bad signature.
//
// ParseWebhook 实现 billing.Provider。校验经 SDK 的 Unwrap 遵循 Standard Webhooks：
// 签名覆盖 webhook-id、webhook-timestamp 与原始请求体，超出容差的时间戳与错误签名
// 同样失败。
func (p *Provider) ParseWebhook(payload []byte, headers http.Header) (store.BillingEvent, error) {
	event, err := p.client.Webhooks.Unwrap(payload, headers)
	if err != nil {
		return store.BillingEvent{}, fmt.Errorf("%w: %w", billing.ErrInvalidSignature, err)
	}
	out := store.BillingEvent{WebhookID: headers.Get("webhook-id"), Type: string(event.Type)}
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
	case dodopayments.DisputeOpenedWebhookEvent:
		out.Kind = store.BillingEventDisputeOpened
		out.PaymentID, out.DisputeID = e.Data.PaymentID, e.Data.DisputeID
	case dodopayments.DisputeWonWebhookEvent:
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
	out.OrderID = metadataString(payment.Metadata, metadataOrderID)
	out.TenantID = metadataString(payment.Metadata, metadataTenantID)
}

func metadataString(metadata dodopayments.Metadata, key string) string {
	if value, ok := metadata[key].(shared.UnionString); ok {
		return string(value)
	}
	return ""
}

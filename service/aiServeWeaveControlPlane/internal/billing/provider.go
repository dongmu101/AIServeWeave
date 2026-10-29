// Package billing defines what the control plane needs from a payment
// provider, so the logic layer and its tests never import a provider SDK.
// The one implementation is billing/dodo, over Dodo Payments; it is the only
// package in the repository that imports that SDK.
//
// billing 包定义控制面对支付服务商的需求，使 logic 层及其测试永远不必 import 服务商
// SDK。唯一的实现是基于 Dodo Payments 的 billing/dodo，它是仓库中唯一 import 该 SDK
// 的包。
package billing

import (
	"context"
	"errors"
	"net/http"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ErrInvalidSignature is returned by ParseWebhook for a payload whose
// signature, timestamp or headers do not verify.
//
// ErrInvalidSignature 由 ParseWebhook 在载荷的签名、时间戳或请求头校验不通过时返回。
var ErrInvalidSignature = errors.New("billing: webhook signature does not verify")

// CheckoutRequest is one checkout to open. AmountMinor is zero for a
// fixed-price product and the chosen amount for a pay-what-you-want one.
// OrderID and TenantID ride the checkout as metadata and come back on its
// payment events.
//
// CheckoutRequest 是一次要发起的结账。固定价格产品的 AmountMinor 为零，按意愿付费
// 产品则为所选金额。OrderID 与 TenantID 作为 metadata 随结账传出，并随其付款事件返回。
type CheckoutRequest struct {
	CustomerID  string
	ProductID   string
	AmountMinor int64
	ReturnURL   string
	OrderID     string
	TenantID    string
}

// Checkout is an opened checkout: the session id the order records and the
// page the tenant is sent to.
//
// Checkout 是一次已发起的结账：订单记录的会话 id，以及租户被引导前往的页面。
type Checkout struct {
	SessionID string
	URL       string
}

// Provider is a payment provider.
//
// Provider 是一个支付服务商。
type Provider interface {
	// CreateCustomer registers the customer a tenant's checkouts attach to
	// and returns its provider id.
	//
	// CreateCustomer 登记租户结账所关联的客户，并返回其服务商 id。
	CreateCustomer(ctx context.Context, email, name, tenantID string) (string, error)
	// CreateCheckout opens a hosted checkout for one product.
	//
	// CreateCheckout 为一个产品发起托管结账。
	CreateCheckout(ctx context.Context, req CheckoutRequest) (Checkout, error)
	// PortalLink returns a one-time link to the provider's customer portal.
	//
	// PortalLink 返回服务商客户门户的一次性链接。
	PortalLink(ctx context.Context, customerID, returnURL string) (string, error)
	// ParseWebhook verifies a webhook against its raw body and headers and
	// reduces it to a store.BillingEvent. An event the ledger does not act on
	// comes back with an empty Kind. A failed verification is
	// ErrInvalidSignature.
	//
	// ParseWebhook 依据原始请求体与请求头校验一个 webhook，并把它归约为
	// store.BillingEvent。账目不处理的事件返回空的 Kind。校验失败返回
	// ErrInvalidSignature。
	ParseWebhook(payload []byte, headers http.Header) (store.BillingEvent, error)
}

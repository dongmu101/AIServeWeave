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

// ErrRefundRejected is returned by CreateRefund when the provider answered
// and refused the refund, so no refund exists. Any other error leaves the
// outcome unknown: the refund may have been created.
//
// ErrRefundRejected 由 CreateRefund 在服务商已答复并拒绝退款时返回，此时不存在任何
// 退款。其他任何错误都意味着结果未知：退款可能已经创建。
var ErrRefundRejected = errors.New("billing: the provider refused the refund")

// Refund statuses a provider reports for a refund it accepted.
//
// 服务商对已受理退款报告的状态。
const (
	RefundStatusPending   = "pending"
	RefundStatusReview    = "review"
	RefundStatusSucceeded = "succeeded"
	RefundStatusFailed    = "failed"
)

// RefundRequest is one refund to ask for: AmountMinor of the payment,
// tax included, in Currency, the payment's currency, against ProductID, the
// item the payment bought. RequestID, OrderID and TenantID ride the refund as
// metadata and come back on its refund events.
//
// RefundRequest 是一次要发起的退款：从付款中退回 AmountMinor（含税，币种为付款币种
// Currency），针对付款所购买的那一项 ProductID。RequestID、OrderID 与 TenantID 作为
// metadata 随退款传出，并随其退款事件返回。
type RefundRequest struct {
	PaymentID   string
	ProductID   string
	AmountMinor int64
	Currency    string
	Reason      string
	RequestID   string
	OrderID     string
	TenantID    string
}

// Refund is a refund the provider accepted.
//
// Refund 是服务商已受理的一笔退款。
type Refund struct {
	ID     string
	Status string
}

// CheckoutRequest is one checkout to open. AmountMinor is zero for a
// fixed-price product and the chosen amount for a pay-what-you-want one.
// Currency is the order's currency; the checkout must charge in it, since
// the order's credits were priced in it. OrderID and TenantID ride the
// checkout as metadata and come back on its payment events.
//
// CheckoutRequest 是一次要发起的结账。固定价格产品的 AmountMinor 为零，按意愿付费
// 产品则为所选金额。Currency 是订单的币种；结账必须以它收款，因为订单点数是按它定价的。
// OrderID 与 TenantID 作为 metadata 随结账传出，并随其付款事件返回。
type CheckoutRequest struct {
	CustomerID  string
	ProductID   string
	AmountMinor int64
	Currency    string
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
	// CreateRefund asks the provider to refund part or all of a payment. It
	// is never retried, as the provider offers no idempotency key: a retry
	// after a lost answer could refund twice. ErrRefundRejected means no
	// refund exists; any other error leaves that unknown.
	//
	// CreateRefund 请求服务商退回一笔付款的部分或全部。它从不重试，因为服务商不提供
	// 幂等键：丢失答复之后的重试可能退款两次。ErrRefundRejected 表示不存在任何退款；
	// 其他错误表示这一点未知。
	CreateRefund(ctx context.Context, req RefundRequest) (Refund, error)
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

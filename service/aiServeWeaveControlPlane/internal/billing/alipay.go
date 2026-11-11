package billing

import (
	"context"
	"errors"
	"fmt"
)

// Alipay is what the control plane needs from 一人付 (opcpay), the Alipay
// entitlement service: the quota a tenant buys lives in Alipay, the control
// plane reads it and redeems it into credits with a formal consumption. It
// has no per-payment confirmation and no refunds, so nothing here confirms
// that a payment happened; only a confirmed consumption moves a balance. The
// one implementation is billing/opcpay.
//
// Alipay 是控制面对一人付（opcpay）支付宝权益服务的需求：租户购买的额度保存在支付宝，
// 控制面读取额度，并经正式核销兑换为点数。它没有逐笔付款确认，也没有退款，因此这里没有
// 任何东西能确认一笔付款发生过；只有已确认的核销才会变动余额。唯一的实现是 billing/opcpay。
type Alipay interface {
	Capabilities(ctx context.Context) (AlipayCapabilities, error)
	Product(ctx context.Context) (AlipayProduct, error)
	// Access reads a user's entitlement for amount units (1 for a
	// count package). It never consumes.
	//
	// Access 读取用户对 amount 个单位的权益（次数包为 1），从不扣减。
	Access(ctx context.Context, userID string, amount int64) (AlipayAccess, error)
	// Purchase opens, or with the same key and parameters reopens, the
	// Alipay purchase entry. READY only means the entry is usable.
	//
	// Purchase 创建购买入口；同一编号与参数重复调用时返回原入口。READY 只表示入口有效。
	Purchase(ctx context.Context, req AlipayPurchaseRequest) (AlipayPurchase, error)
	// Consume formally consumes quota under an idempotency key. A retry must
	// carry the same key and parameters.
	//
	// Consume 以幂等编号正式扣减额度；重试必须携带相同编号与参数。
	Consume(ctx context.Context, req AlipayConsumeRequest) (AlipayConsumption, error)
	// Consumption reads the outcome of an earlier Consume without consuming.
	//
	// Consumption 读取先前一次 Consume 的结果，不发起新扣减。
	Consumption(ctx context.Context, userID, idempotencyKey string) (AlipayConsumption, error)
}

// Billing modes, quota units and statuses 一人付 reports.
//
// 一人付报告的收费模式、额度单位与状态。
const (
	AlipayModeOneTime  = "ONE_TIME"
	AlipayModeDuration = "ONE_TIME_DURATION"
	AlipayModeQuantity = "QUANTITY"

	AlipayUnitCount = "COUNT"
	AlipayUnitPoint = "POINT"

	AlipayCapabilityEnabled = "ENABLED"

	AlipayPurchaseReady   = "READY"
	AlipayPurchaseFailed  = "FAILED"
	AlipayPurchaseUnknown = "UNKNOWN"

	AlipayConsumeSucceeded  = "SUCCEEDED"
	AlipayConsumeFailed     = "FAILED"
	AlipayConsumeProcessing = "PROCESSING"
	AlipayConsumeUnknown    = "UNKNOWN"

	AlipayDecisionAllow = "ALLOW"
)

// MaxAlipayPointAmount is the most points one consumption may take.
//
// MaxAlipayPointAmount 是单次核销最多可扣的积分。
const MaxAlipayPointAmount = 99_999_999

// AlipayCapabilities is which entries the service implements. A true flag
// still needs the project's configuration to be enabled.
//
// AlipayCapabilities 是服务已实现的入口。为 true 时仍需项目配置已启用。
type AlipayCapabilities struct {
	ProviderConfigured bool
	PurchaseEntry      bool
	EntitlementQuery   bool
	Consumption        bool
}

// AlipayOption is one plan configured in Alipay. Price is the decimal string
// Alipay reports; it is shown, never computed with.
//
// AlipayOption 是支付宝中配置的一个套餐。Price 是支付宝给出的十进制字符串，只展示，
// 不参与计算。
type AlipayOption struct {
	SkuID       string
	DisplayName string
	BillingMode string
	Price       string
	Currency    string
	QuotaUnit   string
	QuotaAmount int64
	Duration    string
}

// AlipayProduct is the project's charging configuration.
//
// AlipayProduct 是项目的收费配置。
type AlipayProduct struct {
	CapabilityStatus string
	ProductName      string
	BillingMode      string
	QuotaUnit        string
	Options          []AlipayOption
	ReasonMessage    string
}

// Quantity reports whether the product sells counts or points, the only
// modes credits can be redeemed from.
//
// Quantity 报告该产品是否出售次数或积分，只有这两种模式能兑换点数。
func (p AlipayProduct) Quantity() bool {
	return p.BillingMode == AlipayModeQuantity && (p.QuotaUnit == AlipayUnitCount || p.QuotaUnit == AlipayUnitPoint)
}

// AlipayAccess is a user's entitlement.
//
// AlipayAccess 是一个用户的权益。
type AlipayAccess struct {
	AccessGranted bool
	Decision      string
	ReasonCode    string
	QuotaUnit     string
	Total         int64
	Used          int64
	Remaining     int64
	ValidUntil    string
}

// AlipayPurchaseRequest is one purchase entry to open.
//
// AlipayPurchaseRequest 是一次要创建的购买入口。
type AlipayPurchaseRequest struct {
	UserID         string
	IdempotencyKey string
	SkuID          string
	CallbackURL    string
}

// AlipayPurchase is the answer to Purchase.
//
// AlipayPurchase 是 Purchase 的答复。
type AlipayPurchase struct {
	Status     string
	URL        string
	ExpireTime string
	ReasonCode string
}

// AlipayConsumeRequest is one consumption.
//
// AlipayConsumeRequest 是一次核销。
type AlipayConsumeRequest struct {
	UserID         string
	IdempotencyKey string
	Amount         int64
	Reason         string
}

// AlipayConsumption is the outcome of a consumption.
//
// AlipayConsumption 是一次核销的结果。
type AlipayConsumption struct {
	Status         string
	Confirmed      bool
	IdempotencyKey string
	QuotaUnit      string
	Consumed       int64
	ConsumeOrderID string
	ReasonCode     string
}

// AlipayError is an HTTP error answer from 一人付. Code is its error_code;
// the upstream message is kept out, so it can never reach a log or a page.
//
// AlipayError 是一人付的 HTTP 错误答复。Code 为其 error_code；不保留上游消息文本，
// 使它永远到不了日志或页面。
type AlipayError struct {
	Status    int
	Code      string
	Retryable bool
	RequestID string
}

func (e *AlipayError) Error() string {
	return fmt.Sprintf("opcpay: HTTP %d %s (request %s)", e.Status, e.Code, e.RequestID)
}

// AlipayErrorCode returns err's error_code and HTTP status when it is an
// *AlipayError, or empty and zero otherwise.
//
// AlipayErrorCode 在 err 为 *AlipayError 时返回其 error_code 与 HTTP 状态，否则返回空与零。
func AlipayErrorCode(err error) (string, int) {
	var e *AlipayError
	if errors.As(err, &e) {
		return e.Code, e.Status
	}
	return "", 0
}

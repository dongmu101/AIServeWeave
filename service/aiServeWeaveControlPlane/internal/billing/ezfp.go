package billing

import (
	"context"
	"fmt"
	"net/url"
)

// Ezfp is what the control plane needs from 易支付 (EZFP, the 彩虹易支付 V2
// protocol): a hosted CNY payment for one order, its signed status, its
// signed asynchronous notification, and refunds. Requests are signed with
// the merchant's RSA key and every answer the ledger acts on is verified
// against the platform's public key; the browser return is never trusted.
// The one implementation is billing/ezfp.
//
// Ezfp 是控制面对易支付（EZFP，彩虹易支付 V2 协议）的需求：为一个订单发起的托管人民币
// 付款、其签名的状态、签名的异步通知以及退款。请求以商户 RSA 私钥签名，账目据以行动的
// 每个答复都用平台公钥验签；浏览器回跳从不被信任。唯一的实现是 billing/ezfp。
type Ezfp interface {
	// CreateOrder opens a payment for one order and returns the page the
	// tenant is sent to.
	//
	// CreateOrder 为一个订单发起付款，并返回租户被引导前往的页面。
	CreateOrder(ctx context.Context, req EzfpOrderRequest) (EzfpOrder, error)
	// QueryOrder reads the signed status of the payment for outTradeNo, the
	// order id.
	//
	// QueryOrder 读取订单号 outTradeNo 对应付款的签名状态。
	QueryOrder(ctx context.Context, outTradeNo string) (EzfpPayment, error)
	// ParseNotify verifies an asynchronous notification against its
	// parameters. A failed verification is ErrInvalidSignature.
	//
	// ParseNotify 依据参数校验一个异步通知。校验失败返回 ErrInvalidSignature。
	ParseNotify(params url.Values) (EzfpPayment, error)
	// Refund refunds part or all of a payment under outRefundNo. A refusal
	// is ErrRefundRejected and means no refund exists; any other error
	// leaves that unknown.
	//
	// Refund 以 outRefundNo 退回一笔付款的部分或全部。被拒绝时返回 ErrRefundRejected，
	// 表示不存在任何退款；其他错误表示这一点未知。
	Refund(ctx context.Context, req EzfpRefundRequest) (EzfpRefund, error)
	// QueryRefund reads the signed outcome of the refund under outRefundNo.
	//
	// QueryRefund 读取 outRefundNo 对应退款的签名结果。
	QueryRefund(ctx context.Context, outRefundNo string) (EzfpRefund, error)
}

// EZFP payment types a tenant may choose.
//
// 租户可以选择的易支付付款方式。
const (
	EzfpTypeAlipay = "alipay"
	EzfpTypeWxpay  = "wxpay"
	EzfpTypeUsdt   = "usdt"
)

// EzfpTradeSuccess is the only trade_status a notification carries.
//
// EzfpTradeSuccess 是通知携带的唯一 trade_status。
const EzfpTradeSuccess = "TRADE_SUCCESS"

// Payment statuses api/pay/query reports.
//
// api/pay/query 报告的付款状态。
const (
	EzfpStatusUnpaid     = 0
	EzfpStatusPaid       = 1
	EzfpStatusRefunded   = 2
	EzfpStatusFrozen     = 3
	EzfpStatusPreauthed  = 4
	EzfpRefundFailed     = 0
	EzfpRefundSucceeded  = 1
	EzfpRefundStatusNone = -1
)

// EzfpOrderRequest is one payment to open. MoneyMinor is in fen. Param
// rides the payment and comes back on its notification.
//
// EzfpOrderRequest 是一次要发起的付款。MoneyMinor 以分计。Param 随付款传出，并随其
// 通知返回。
type EzfpOrderRequest struct {
	OutTradeNo string
	PayType    string
	Name       string
	MoneyMinor int64
	ClientIP   string
	Param      string
	NotifyURL  string
	ReturnURL  string
}

// EzfpOrder is an opened payment: EZFP's trade number and the page to pay on.
//
// EzfpOrder 是一次已发起的付款：易支付的交易号与付款页面。
type EzfpOrder struct {
	TradeNo string
	PayURL  string
}

// EzfpPayment is the verified state of one payment, from a notification or
// a query. Status is a query's status; a notification carries TradeStatus
// instead and reports Status as EzfpStatusPaid when it is TRADE_SUCCESS.
//
// EzfpPayment 是一笔付款经验签的状态，来自通知或查询。Status 是查询的状态；通知改为携带
// TradeStatus，为 TRADE_SUCCESS 时 Status 报告为 EzfpStatusPaid。
type EzfpPayment struct {
	PID         string
	TradeNo     string
	OutTradeNo  string
	Type        string
	TradeStatus string
	Status      int
	MoneyMinor  int64
	Param       string
	// Timestamp is the signed send time, in Unix seconds.
	//
	// Timestamp 是签名中的发送时间，Unix 秒。
	Timestamp int64
}

// EzfpRefundRequest is one refund: MoneyMinor fen of the payment TradeNo,
// under OutRefundNo, this service's refund request id.
//
// EzfpRefundRequest 是一次退款：以本服务的退款申请 id OutRefundNo，从交易 TradeNo 中
// 退回 MoneyMinor 分。
type EzfpRefundRequest struct {
	OutRefundNo string
	TradeNo     string
	MoneyMinor  int64
}

// EzfpRefund is a refund EZFP answered for. Status is EzfpRefundSucceeded
// for a refund that went through, EzfpRefundFailed for one that failed and
// EzfpRefundStatusNone when the answer carries no status (api/pay/refund
// answers code 0 only for a completed refund).
//
// EzfpRefund 是易支付已答复的一笔退款。退款完成时 Status 为 EzfpRefundSucceeded，失败
// 时为 EzfpRefundFailed，答复没有状态时为 EzfpRefundStatusNone（api/pay/refund 只在
// 退款完成时答复 code 0）。
type EzfpRefund struct {
	RefundNo    string
	OutRefundNo string
	TradeNo     string
	MoneyMinor  int64
	Status      int
}

// EzfpError is a non-zero code EZFP answered with. Msg is EZFP's own reason,
// meant for the merchant (a bad signature, a channel not enabled), cut short
// and stripped of control characters. Error leaves it out, so it never
// reaches a page; only an operator log reads it.
//
// EzfpError 是易支付答复的非零 code。Msg 是易支付给商户看的原因（签名错误、通道未开通
// 等），已截短并去掉控制字符。Error 不包含它，使它永远到不了页面；只有运维日志读取它。
type EzfpError struct {
	Op   string
	Code int
	Msg  string
}

func (e *EzfpError) Error() string {
	return fmt.Sprintf("ezfp: %s answered code %d", e.Op, e.Code)
}

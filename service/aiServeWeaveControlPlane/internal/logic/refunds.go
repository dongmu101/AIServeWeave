// refunds.go implements refunds of credit purchases: a tenant asking for one
// within the refund window and an operator approving or rejecting it, an
// operator refunding an order directly, and the provider call both end in.
//
// A tenant may get back only what its unused credits stand for. Approval
// re-judges that against the balance of the moment and holds the credits off
// the balance, so they cannot be spent while the provider works; the
// provider's refund events then settle the hold (store.ApplyBillingEventRules).
//
// refunds.go 实现充值的退款：租户在退款期限内申请、运维批准或驳回，运维直接对订单
// 退款，以及两者最终都要走到的服务商调用。
//
// 租户只能拿回其未用点数所对应的金额。批准时按当时的余额重新核定，并把相应点数从余额中
// 冻结，使其在服务商处理期间无法被花掉；之后由服务商的退款事件结算这笔冻结
// （store.ApplyBillingEventRules）。
package logic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// refundRecordTimeout bounds the store writes that record a provider's
// answer. They run detached from the request, so a client that gives up
// mid-call cannot leave a refund the provider accepted unrecorded.
//
// refundRecordTimeout 限制记录服务商答复的 store 写入。这些写入脱离请求的
// context 运行，使中途放弃的客户端无法让一笔服务商已受理的退款漏记。
const refundRecordTimeout = 5 * time.Second

// Failure texts a refund request records. They are fixed: a provider's
// error text is never stored or shown.
//
// 退款申请记录的失败说明。它们是固定文本：服务商的错误文本从不保存或展示。
const (
	refundFailureRejected = "the provider refused the refund"
	refundFailureReported = "the provider reported the refund failed"
	refundFailureNoTarget = "the order has no payment or product to refund"
)

// RefundableOrders returns, for each of orders, how much its tenant could
// ask to refund right now: within the refund window, without a refund in
// progress, and no more than its unused credits stand for. An order that
// cannot be refunded is absent. It is advisory, for the billing page; the
// request itself is judged again in one transaction. Every order must belong
// to actor's tenant.
//
// RefundableOrders 为 orders 中的每一个返回其租户此刻可申请退款的金额：在退款期限内、
// 没有进行中的退款、且不超过其未用点数所对应的金额。不能退款的订单不出现在结果中。它只
// 供计费页面参考；申请本身会在一个事务中重新判断。所有订单都必须属于 actor 的租户。
func (s *Service) RefundableOrders(ctx context.Context, actor Actor, orders []model.CheckoutOrder) (map[string]int64, error) {
	out := map[string]int64{}
	if !s.billingEnabled() || s.billing.RefundWindowDays <= 0 || isPlatformActor(actor) {
		return out, nil
	}
	row, err := s.tenantBilling(ctx, actor.TenantID)
	if err != nil {
		return nil, err
	}
	active, err := s.store.ActiveRefundOrders(ctx, actor.TenantID)
	if err != nil {
		return nil, err
	}
	notBefore := s.refundWindowStart()
	for _, o := range orders {
		if o.TenantID != actor.TenantID || o.Status != model.OrderStatusPaid || active[o.ID] || o.CreatedAt.Before(notBefore) {
			continue
		}
		if amount := o.UnusedRefundMinor(row.BalanceMicro); amount > 0 {
			out[o.ID] = amount
		}
	}
	return out, nil
}

// RequestRefund records a tenant's request to refund one of its orders,
// pending an operator's review. amountMinor zero asks for everything the
// unused-credits rule allows. Only an owner or admin may ask, as only they
// may buy; self-service refunds are off when the refund window is zero.
//
// RequestRefund 记录租户对其某个订单的退款申请，等待运维审核。amountMinor 为零表示
// 申请「只退未用点数」规则所允许的全部金额。只有 owner 或 admin 可以申请，与只有他们
// 能购买一致；退款期限为零时关闭自助退款。
func (s *Service) RequestRefund(ctx context.Context, actor Actor, orderID string, amountMinor int64, reason string) (model.RefundRequest, error) {
	if !s.billingEnabled() {
		return model.RefundRequest{}, ErrNotFound
	}
	if !actor.canPurchase() || s.billing.RefundWindowDays <= 0 {
		return model.RefundRequest{}, ErrForbidden
	}
	reason, ok := refundText(reason, true)
	if !ok || amountMinor < 0 {
		return model.RefundRequest{}, ErrInvalidInput
	}
	req, _, _, err := s.store.OpenRefundRequest(ctx, model.RefundRequest{
		ID: model.NewID(model.PrefixRefundRequest), TenantID: actor.TenantID, OrderID: orderID,
		Source: model.RefundSourceTenant, Status: model.RefundStatusPending, AmountMinor: amountMinor,
		Reason: reason, RequestedBy: actor.UserID,
	}, store.RefundPolicy{NotBefore: s.refundWindowStart(), UnusedOnly: true}, s.clock.Now())
	if err != nil {
		return model.RefundRequest{}, translateRefund(err)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionBillingRefundRequest, req.ID,
		fmt.Sprintf("order %s amount %d %s", req.OrderID, req.AmountMinor, req.Currency), actor.IP)
	return req, nil
}

// CancelRefundRequest withdraws a tenant's own pending request.
//
// CancelRefundRequest 撤回租户自己的一个待审核申请。
func (s *Service) CancelRefundRequest(ctx context.Context, actor Actor, id string) (model.RefundRequest, error) {
	if !s.billingEnabled() {
		return model.RefundRequest{}, ErrNotFound
	}
	if !actor.canPurchase() {
		return model.RefundRequest{}, ErrForbidden
	}
	req, _, err := s.store.TransitionRefundRequest(ctx, store.RefundTransition{
		ID: id, TenantID: actor.TenantID, To: model.RefundStatusCancelled, Actor: actor.UserID,
	}, s.clock.Now())
	if err != nil {
		return model.RefundRequest{}, translateRefund(err)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionBillingRefundCancel, req.ID, "order "+req.OrderID, actor.IP)
	return req, nil
}

// ListRefundRequests reads refund requests, scoped as ListBillingLedger is.
//
// ListRefundRequests 读取退款申请，范围限定同 ListBillingLedger。
func (s *Service) ListRefundRequests(ctx context.Context, actor Actor, query store.ListQuery, filter store.RefundRequestFilter) (store.Page[model.RefundRequest], error) {
	if !s.billingEnabled() {
		return store.Page[model.RefundRequest]{}, ErrNotFound
	}
	if !isPlatformActor(actor) {
		filter.TenantID = actor.TenantID
	}
	page, err := s.store.ListRefundRequests(ctx, query, filter)
	return page, translate(err)
}

// ApproveRefundRequest approves a tenant's pending request and asks the
// provider for the refund. The amount is re-judged under the unused-credits
// rule at today's balance and may come out lower than asked; amountMinor,
// when not zero, lowers it further. The returned request is processing while
// the provider works or when its answer was lost, and failed when it refused.
//
// ApproveRefundRequest 批准租户的一个待审核申请，并向服务商发起退款。金额按「只退未用
// 点数」规则以当前余额重新核定，可能低于申请金额；amountMinor 不为零时可进一步调低。
// 服务商处理中或其答复丢失时，返回的申请为 processing；服务商拒绝时为 failed。
func (s *Service) ApproveRefundRequest(ctx context.Context, actor Actor, id string, amountMinor int64, note string) (model.RefundRequest, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return model.RefundRequest{}, err
	}
	note, ok := refundText(note, false)
	if !ok || amountMinor < 0 {
		return model.RefundRequest{}, ErrInvalidInput
	}
	req, order, changed, err := s.store.ApproveRefundRequest(ctx, id, actor.UserID, note, amountMinor,
		store.RefundPolicy{UnusedOnly: true}, s.clock.Now())
	if err != nil {
		return model.RefundRequest{}, translateRefund(err)
	}
	s.invalidateIf(ctx, changed)
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingRefundApprove, req.ID,
		fmt.Sprintf("tenant %s order %s amount %d %s", req.TenantID, req.OrderID, req.AmountMinor, req.Currency), actor.IP)
	return s.submitRefund(ctx, req, order)
}

// CreateOperatorRefund refunds an order on an operator's own decision, for
// support cases the tenant rule does not cover. It is bounded only by what
// the payment has not had refunded, so it may take back credits already
// spent and leave the balance negative. amountMinor zero refunds the rest.
//
// CreateOperatorRefund 按运维自己的决定对一个订单退款，用于租户规则覆盖不到的售后
// 情形。它只受付款中尚未退款的金额限制，因此可能扣回已经花掉的点数、使余额为负。
// amountMinor 为零时退回剩余全部。
func (s *Service) CreateOperatorRefund(ctx context.Context, actor Actor, orderID string, amountMinor int64, reason string) (model.RefundRequest, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return model.RefundRequest{}, err
	}
	reason, ok := refundText(reason, true)
	if !ok || amountMinor < 0 {
		return model.RefundRequest{}, ErrInvalidInput
	}
	req, order, changed, err := s.store.OpenRefundRequest(ctx, model.RefundRequest{
		ID: model.NewID(model.PrefixRefundRequest), OrderID: orderID, Source: model.RefundSourceOperator,
		Status: model.RefundStatusProcessing, AmountMinor: amountMinor, Reason: reason,
		RequestedBy: actor.UserID, ReviewedBy: actor.UserID,
	}, store.RefundPolicy{}, s.clock.Now())
	if err != nil {
		return model.RefundRequest{}, translateRefund(err)
	}
	s.invalidateIf(ctx, changed)
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingRefundCreate, req.ID,
		fmt.Sprintf("tenant %s order %s amount %d %s", req.TenantID, req.OrderID, req.AmountMinor, req.Currency), actor.IP)
	return s.submitRefund(ctx, req, order)
}

// RejectRefundRequest rejects a tenant's pending request, with note as the
// answer the tenant sees.
//
// RejectRefundRequest 驳回租户的一个待审核申请，note 是租户看到的答复。
func (s *Service) RejectRefundRequest(ctx context.Context, actor Actor, id, note string) (model.RefundRequest, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return model.RefundRequest{}, err
	}
	note, ok := refundText(note, false)
	if !ok {
		return model.RefundRequest{}, ErrInvalidInput
	}
	req, _, err := s.store.TransitionRefundRequest(ctx, store.RefundTransition{
		ID: id, To: model.RefundStatusRejected, Actor: actor.UserID, Note: note,
	}, s.clock.Now())
	if err != nil {
		return model.RefundRequest{}, translateRefund(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingRefundReject, req.ID,
		"tenant "+req.TenantID+" order "+req.OrderID, actor.IP)
	return req, nil
}

// MarkRefundFailed settles a processing refund as failed and returns its held
// credits. It is for a refund whose provider answer was lost, once the
// operator has checked the provider's dashboard and found no refund there;
// should the provider report it succeeded after all, its event still takes
// the credits back.
//
// MarkRefundFailed 把一个处理中的退款结算为失败并退回其冻结的点数。它用于服务商答复
// 丢失的退款：运维在服务商后台核实其中确实没有这笔退款之后使用。即便服务商之后仍报告
// 退款成功，其事件照样会扣回点数。
func (s *Service) MarkRefundFailed(ctx context.Context, actor Actor, id, note string) (model.RefundRequest, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return model.RefundRequest{}, err
	}
	note, ok := refundText(note, true)
	if !ok {
		return model.RefundRequest{}, ErrInvalidInput
	}
	req, changed, err := s.store.TransitionRefundRequest(ctx, store.RefundTransition{
		ID: id, To: model.RefundStatusFailed, Actor: actor.UserID, Note: truncate("marked failed by an operator: "+note, 256),
	}, s.clock.Now())
	if err != nil {
		return model.RefundRequest{}, translateRefund(err)
	}
	s.invalidateIf(ctx, changed)
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingRefundFail, req.ID,
		"tenant "+req.TenantID+" order "+req.OrderID, actor.IP)
	return req, nil
}

// submitRefund asks the provider for an approved refund and records the
// answer. A refusal fails the request and returns its credits; a lost answer
// leaves it processing, since the refund may exist, for the refund events or
// an operator to settle. The answer is recorded on a context detached from
// the request, so an impatient client cannot drop it.
//
// submitRefund 向服务商发起一笔已批准的退款并记录答复。被拒绝时把申请标记为失败并
// 退回点数；答复丢失时保持 processing（退款可能已经存在），交由退款事件或运维结算。
// 答复在脱离请求的 context 上记录，使不耐烦的客户端无法把它丢掉。
func (s *Service) submitRefund(ctx context.Context, req model.RefundRequest, order model.CheckoutOrder) (model.RefundRequest, error) {
	call := billing.RefundRequest{
		AmountMinor: req.AmountMinor, Currency: req.Currency, Reason: req.Reason,
		RequestID: req.ID, OrderID: order.ID, TenantID: order.TenantID, ProductID: order.DodoProductID,
	}
	if order.DodoPaymentID != nil {
		call.PaymentID = *order.DodoPaymentID
	}
	if call.ProductID == "" {
		// Orders created before the product was recorded on them fall back
		// to their package's product. 记录产品之前创建的订单回落到其充值包的产品。
		if pkg, err := s.store.GetCreditPackage(ctx, order.PackageID); err == nil {
			call.ProductID = pkg.DodoProductID
		}
	}
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), refundRecordTimeout)
	defer cancel()
	if call.PaymentID == "" || call.ProductID == "" {
		return s.failSubmittedRefund(record, req, refundFailureNoTarget)
	}
	refund, err := s.billing.Provider.CreateRefund(ctx, call)
	switch {
	case errors.Is(err, billing.ErrRefundRejected):
		slog.Error("the payment provider refused a refund", slog.String("refund_request_id", req.ID),
			slog.String("order_id", order.ID), slog.String("error", err.Error()))
		return s.failSubmittedRefund(record, req, refundFailureRejected)
	case err != nil:
		slog.Error("a refund's provider answer was lost; it stays processing until a refund event or an operator settles it",
			slog.String("refund_request_id", req.ID), slog.String("order_id", order.ID))
		return req, nil
	}
	if err := s.store.RecordRefundSubmitted(record, req.ID, refund.ID, s.clock.Now()); err != nil {
		slog.Error("recording a submitted refund failed", slog.String("refund_request_id", req.ID), slog.String("refund_id", refund.ID))
	}
	if refund.Status == billing.RefundStatusFailed {
		return s.failSubmittedRefund(record, req, refundFailureReported)
	}
	if fresh, err := s.store.GetRefundRequest(record, req.ID); err == nil {
		return fresh, nil
	}
	return req, nil
}

// failSubmittedRefund fails a processing request the provider will not
// refund, returning its held credits.
//
// failSubmittedRefund 把服务商不会退款的处理中申请标记为失败，并退回其冻结的点数。
func (s *Service) failSubmittedRefund(ctx context.Context, req model.RefundRequest, failure string) (model.RefundRequest, error) {
	failed, changed, err := s.store.TransitionRefundRequest(ctx, store.RefundTransition{
		ID: req.ID, To: model.RefundStatusFailed, Note: failure,
	}, s.clock.Now())
	var refused *store.RefundRefusedError
	if errors.As(err, &refused) {
		// A refund event settled it meanwhile. 期间已有退款事件结算了它。
		return s.store.GetRefundRequest(ctx, req.ID)
	}
	if err != nil {
		return model.RefundRequest{}, translate(err)
	}
	s.invalidateIf(ctx, changed)
	return failed, nil
}

// refundWindowStart is the creation time before which an order is past the
// tenant's refund window.
//
// refundWindowStart 是一个时间点：早于它创建的订单已超出租户的退款期限。
func (s *Service) refundWindowStart() time.Time {
	return s.clock.Now().AddDate(0, 0, -s.billing.RefundWindowDays)
}

func (s *Service) invalidateIf(ctx context.Context, changed bool) {
	if changed && s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
}

// refundText trims a reason or note and checks it fits its column: at most
// 500 characters and 1024 bytes. required refuses an empty one.
//
// refundText 去除原因或备注两端的空白，并检查它能放进对应的列：至多 500 个字符、
// 1024 字节。required 时拒绝空值。
func refundText(text string, required bool) (string, bool) {
	text = strings.TrimSpace(text)
	if (required && text == "") || utf8.RuneCountInString(text) > 500 || len(text) > 1024 || !utf8.ValidString(text) {
		return "", false
	}
	return text, true
}

// translateRefund keeps a refund refusal intact, so the handler can say why,
// and translates everything else.
//
// translateRefund 原样保留退款拒绝错误，使 handler 能说明原因；其他错误照常转译。
func translateRefund(err error) error {
	var refused *store.RefundRefusedError
	if errors.As(err, &refused) {
		return refused
	}
	return translate(err)
}

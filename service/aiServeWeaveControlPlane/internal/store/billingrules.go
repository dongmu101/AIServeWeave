package store

import (
	"errors"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// BillingTx is the transaction-scoped access ApplyBillingEventRules needs. Each
// store implements it over its own transaction, so both stores apply provider
// events by the same rules rather than by two copies that could drift.
// Methods returning an order report ErrNotFound for a missing one.
//
// BillingTx 是 ApplyBillingEventRules 所需的、限定在事务内的访问能力。每个 store 在
// 自己的事务上实现它，两个 store 因此按同一套规则应用服务商事件，而不是靠两份可能
// 漂移的副本。返回订单的方法在订单不存在时报告 ErrNotFound。
type BillingTx interface {
	// OrderByID reads and locks one order by id.
	//
	// OrderByID 按 id 读取并锁定一个订单。
	OrderByID(id string) (model.CheckoutOrder, error)
	// OrderByPayment reads and locks the order a provider payment paid.
	//
	// OrderByPayment 读取并锁定某笔服务商付款所支付的订单。
	OrderByPayment(paymentID string) (model.CheckoutOrder, error)
	// SaveOrder writes an order's payment id, paid amount and currency,
	// refunded amount, status and update time.
	//
	// SaveOrder 写入订单的付款 id、实付金额与币种、已退款金额、状态与更新时间。
	SaveOrder(order model.CheckoutOrder) error
	// Credit applies a ledger entry to its tenant's balance. changed reports
	// a usable/exhausted flip; duplicate reports an idempotency key already
	// in the ledger, in which case nothing was written.
	//
	// Credit 把一条账目记入其租户的余额。changed 报告可用与耗尽之间的翻转；duplicate
	// 报告幂等键已在账目中，此时什么都没写。
	Credit(entry *model.CreditLedgerEntry) (changed, duplicate bool, err error)
	// LedgerAmount reads the amount of the entry carrying an idempotency key,
	// and whether there is one.
	//
	// LedgerAmount 读取携带某个幂等键的账目金额，以及是否存在这样一条账目。
	LedgerAmount(key string) (int64, bool, error)
	// TenantBilling reads and locks a tenant's billing row, creating an
	// exempt zero-balance row when there is none.
	//
	// TenantBilling 读取并锁定租户的计费行；没有时创建一行零余额的 exempt 记录。
	TenantBilling(tenantID string, now time.Time) (model.TenantBilling, error)
	// RefundRequestOrderID reads, without locking, the order a refund
	// request belongs to, so the caller can lock that order first: every
	// refund path locks the order before the request, which keeps them from
	// deadlocking one another.
	//
	// RefundRequestOrderID 不加锁地读取退款申请所属的订单，使调用方能先锁定该订单：所有
	// 退款路径都先锁订单、再锁申请，彼此因此不会死锁。
	RefundRequestOrderID(id string) (string, error)
	// RefundRequestByID reads and locks one refund request.
	//
	// RefundRequestByID 读取并锁定一个退款申请。
	RefundRequestByID(id string) (model.RefundRequest, error)
	// RefundRequestByRefund reads and locks the request a provider refund
	// settles.
	//
	// RefundRequestByRefund 读取并锁定某个服务商退款所结算的申请。
	RefundRequestByRefund(refundID string) (model.RefundRequest, error)
	// OrderHasActiveRefund reports whether the order has a pending or
	// processing refund request other than exceptID.
	//
	// OrderHasActiveRefund 报告订单是否有除 exceptID 之外的待审核或处理中退款申请。
	OrderHasActiveRefund(orderID, exceptID string) (bool, error)
	// InsertRefundRequest inserts a refund request.
	//
	// InsertRefundRequest 插入一个退款申请。
	InsertRefundRequest(req model.RefundRequest) error
	// SaveRefundRequest writes a refund request's mutable fields.
	//
	// SaveRefundRequest 写入退款申请的可变字段。
	SaveRefundRequest(req model.RefundRequest) error
}

// ApplyBillingEventRules applies one provider event through tx. A payment
// credits its order's credits once, keyed by payment id, and only when it
// charged at least the order's amount in the order's currency; a refund
// takes back the credits in proportion to the share of the paid amount
// refunded, keyed by refund id; a dispute takes back what the order still
// holds and a won dispute returns exactly that. A refund or dispute that
// outruns the event it follows returns ErrBillingEventEarly so the provider
// redelivers it. Balances may go negative: credits already spent cannot be
// taken back from usage, so a negative balance is the debt the next top-up
// settles first.
//
// ApplyBillingEventRules 通过 tx 应用一个服务商事件。付款按付款 id 为键，为其订单
// 入账一次，且仅当它以订单币种收取了不少于订单金额的款项时才入账；退款按退款 id 为键，
// 按退款占实付金额的比例扣回点数；争议扣回订单仍持有的点数，争议胜诉时原样退回。先于
// 前置事件到达的退款或争议返回 ErrBillingEventEarly，让服务商重新投递。余额可以为负：
// 已经用掉的点数无法从用量中收回，因此负余额就是下一次充值首先要还上的欠款。
func ApplyBillingEventRules(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	switch event.Kind {
	case BillingEventPaymentSucceeded:
		return paymentSucceeded(tx, event, now)
	case BillingEventPaymentFailed:
		return paymentFailed(tx, event, now)
	case BillingEventRefundSucceeded:
		return refundSucceeded(tx, event, now)
	case BillingEventRefundFailed:
		return refundFailed(tx, event, now)
	case BillingEventDisputeOpened:
		return disputeOpened(tx, event, now)
	case BillingEventDisputeWon:
		return disputeWon(tx, event, now)
	default:
		return BillingEventResult{Outcome: BillingOutcomeIgnored}, nil
	}
}

func paymentSucceeded(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	if event.PaymentID == "" {
		return BillingEventResult{Outcome: BillingOutcomeMismatch}, nil
	}
	order, result, ok, err := findOrder(tx, event.OrderID, "")
	if !ok || err != nil {
		return result, err
	}
	if event.TenantID != order.TenantID ||
		(event.CheckoutSessionID != "" && order.DodoSessionID != "" && event.CheckoutSessionID != order.DodoSessionID) {
		result.Outcome = BillingOutcomeMismatch
		return result, nil
	}
	switch order.Status {
	case model.OrderStatusPending, model.OrderStatusFailed:
	default:
		result.Outcome = BillingOutcomeIgnored
		if order.DodoPaymentID != nil && *order.DodoPaymentID == event.PaymentID {
			result.Outcome = BillingOutcomeDuplicate
		}
		return result, nil
	}
	// The order's credits were priced in its own currency and amount, so a
	// payment in another currency or for less — a discount code, a product
	// misconfigured for the package — does not earn them.
	// 订单点数按订单自身的币种与金额定价，因此币种不同或金额更少的付款（折扣码、与
	// 充值包配置不符的产品）拿不到这些点数。
	if !strings.EqualFold(event.Currency, order.Currency) || event.AmountMinor < order.AmountMinor {
		result.Outcome = BillingOutcomeMismatch
		return result, nil
	}
	paymentID := event.PaymentID
	order.DodoPaymentID = &paymentID
	order.PaidAmountMinor = event.AmountMinor
	order.PaidCurrency = strings.ToUpper(event.Currency)
	order.Status = model.OrderStatusPaid
	order.UpdatedAt = now
	if err := tx.SaveOrder(order); err != nil {
		return BillingEventResult{}, err
	}
	return credit(tx, result, order.TenantID, model.LedgerKindTopup, order.CreditsMicro, order.ID, "topup:"+paymentID, now)
}

func paymentFailed(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	order, result, ok, err := findOrder(tx, event.OrderID, "")
	if !ok || err != nil {
		return result, err
	}
	if order.Status != model.OrderStatusPending {
		result.Outcome = BillingOutcomeIgnored
		return result, nil
	}
	order.Status = model.OrderStatusFailed
	order.UpdatedAt = now
	if err := tx.SaveOrder(order); err != nil {
		return BillingEventResult{}, err
	}
	result.Outcome = BillingOutcomeApplied
	return result, nil
}

func refundSucceeded(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	order, result, ok, err := findPaidOrder(tx, event.PaymentID)
	if !ok || err != nil {
		return result, err
	}
	key := "refund:" + event.RefundID
	if event.RefundID == "" ||
		(event.Currency != "" && order.PaidCurrency != "" && !strings.EqualFold(event.Currency, order.PaidCurrency)) {
		result.Outcome = BillingOutcomeMismatch
		return result, nil
	}
	req, linked, err := refundRequestFor(tx, order, event)
	if err != nil {
		return BillingEventResult{}, err
	}
	if linked && req.Status == model.RefundStatusSucceeded {
		result.Outcome = BillingOutcomeDuplicate
		return result, nil
	}
	if _, seen, err := tx.LedgerAmount(key); err != nil || seen {
		result.Outcome = BillingOutcomeDuplicate
		return result, err
	}
	basis := order.RefundBasisMinor()
	amount := event.AmountMinor
	if amount <= 0 {
		// The provider may omit the amount; only a full refund is then
		// unambiguous, and it returns whatever is left.
		// 服务商可能不给出金额；此时只有全额退款含义明确，它退回剩余的全部金额。
		if event.IsPartial {
			result.Outcome = BillingOutcomeMismatch
			return result, nil
		}
		amount = basis - order.RefundedMinor
	}
	credits := order.RefundDelta(amount)
	order.RefundedMinor = min(order.RefundedMinor+max(amount, 0), basis)
	if order.RefundedMinor >= basis {
		order.Status = model.OrderStatusRefunded
	}
	order.UpdatedAt = now
	if err := tx.SaveOrder(order); err != nil {
		return BillingEventResult{}, err
	}
	// A refund this service approved already holds its credits off the
	// balance; only the difference between what was held and what the
	// refund actually takes back moves the balance now.
	// 本服务批准的退款在批准时已冻结了点数；此刻只按冻结量与实际扣回量之差变动余额。
	var held int64
	if linked {
		if req.Status == model.RefundStatusProcessing {
			held = req.CreditsMicro
		}
		refundID := event.RefundID
		req.Status, req.DodoRefundID, req.Failure, req.UpdatedAt = model.RefundStatusSucceeded, &refundID, "", now
		if err := tx.SaveRefundRequest(req); err != nil {
			return BillingEventResult{}, err
		}
	}
	net := credits - held
	if net == 0 {
		result.Outcome = BillingOutcomeApplied
		return result, nil
	}
	kind := model.LedgerKindRefund
	if net < 0 {
		kind = model.LedgerKindRefundReversal
	}
	return credit(tx, result, order.TenantID, kind, -net, order.ID, key, now)
}

// refundFailed settles a refund this service asked for as failed and returns
// the credits its approval held. A failed refund this service did not ask
// for changes nothing.
//
// refundFailed 把本服务发起的退款结算为失败，并退回批准时冻结的点数。不是本服务发起
// 的失败退款不改变任何东西。
func refundFailed(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	if event.PaymentID == "" {
		return BillingEventResult{Outcome: BillingOutcomeMismatch}, nil
	}
	order, result, ok, err := findOrder(tx, "", event.PaymentID)
	if !ok || err != nil {
		return result, err
	}
	req, linked, err := refundRequestFor(tx, order, event)
	if err != nil {
		return BillingEventResult{}, err
	}
	switch {
	case !linked:
		result.Outcome = BillingOutcomeIgnored
		return result, nil
	case req.Status == model.RefundStatusFailed:
		result.Outcome = BillingOutcomeDuplicate
		return result, nil
	case req.Status != model.RefundStatusProcessing:
		result.Outcome = BillingOutcomeIgnored
		return result, nil
	}
	if event.RefundID != "" {
		refundID := event.RefundID
		req.DodoRefundID = &refundID
	}
	changed, err := failRefund(tx, &req, "the provider reported the refund failed", now)
	if err != nil {
		return BillingEventResult{}, err
	}
	result.BlockedChanged = changed
	result.Outcome = BillingOutcomeApplied
	return result, nil
}

// refundRequestFor finds the refund request a refund event settles: by the
// request id its metadata carries, else by the provider refund id. linked is
// false for a refund made elsewhere, or one whose request belongs to another
// order.
//
// refundRequestFor 查找一个退款事件所结算的退款申请：先按其 metadata 携带的申请 id，
// 再按服务商退款 id。在别处发起的退款，或申请属于其他订单时，linked 为 false。
func refundRequestFor(tx BillingTx, order model.CheckoutOrder, event BillingEvent) (model.RefundRequest, bool, error) {
	req, err := model.RefundRequest{}, ErrNotFound
	if event.RefundRequestID != "" {
		req, err = tx.RefundRequestByID(event.RefundRequestID)
	}
	if errors.Is(err, ErrNotFound) && event.RefundID != "" {
		req, err = tx.RefundRequestByRefund(event.RefundID)
	}
	if errors.Is(err, ErrNotFound) {
		return model.RefundRequest{}, false, nil
	}
	if err != nil {
		return model.RefundRequest{}, false, err
	}
	return req, req.OrderID == order.ID, nil
}

// OpenRefundRules records a new refund request through tx; see
// Billing.OpenRefundRequest. An empty req.TenantID takes the order's, for an
// operator who names only the order; a set one must own it.
//
// OpenRefundRules 通过 tx 记录一个新的退款申请，见 Billing.OpenRefundRequest。
// req.TenantID 为空时取订单的租户，供只指明订单的运维使用；不为空时必须是订单的所属
// 租户。
func OpenRefundRules(tx BillingTx, req model.RefundRequest, policy RefundPolicy, now time.Time) (model.RefundRequest, model.CheckoutOrder, bool, error) {
	order, err := tx.OrderByID(req.OrderID)
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, err
	}
	if req.TenantID == "" {
		req.TenantID = order.TenantID
	}
	if req.TenantID != order.TenantID {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, ErrNotFound
	}
	limit, err := refundLimit(tx, order, policy, "", now)
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, err
	}
	switch {
	case req.AmountMinor < 0 || req.AmountMinor > limit:
		return model.RefundRequest{}, model.CheckoutOrder{}, false, &RefundRefusedError{Reason: RefundRefusedAmount}
	case req.AmountMinor == 0:
		req.AmountMinor = limit
	}
	req.Currency = refundCurrency(order)
	req.CreatedAt, req.UpdatedAt = now, now
	changed := false
	if req.Status == model.RefundStatusProcessing {
		req.CreditsMicro = order.RefundDelta(req.AmountMinor)
		if changed, err = holdRefund(tx, req, now); err != nil {
			return model.RefundRequest{}, model.CheckoutOrder{}, false, err
		}
	}
	if err := tx.InsertRefundRequest(req); err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, err
	}
	return req, order, changed, nil
}

// ApproveRefundRules approves a pending request through tx; see
// Billing.ApproveRefundRequest. policy.NotBefore is not applied again: the
// window is judged when the tenant asks, not when an operator gets to it.
//
// ApproveRefundRules 通过 tx 批准一个待审核的申请，见 Billing.ApproveRefundRequest。
// 不再重新应用 policy.NotBefore：期限在租户申请时判断，而不是在运维处理时判断。
func ApproveRefundRules(tx BillingTx, id, reviewer, note string, amountMinor int64, policy RefundPolicy, now time.Time) (model.RefundRequest, model.CheckoutOrder, bool, error) {
	req, order, err := lockRefundRequest(tx, id)
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, err
	}
	if req.Status != model.RefundStatusPending {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, &RefundRefusedError{Reason: RefundRefusedStatus}
	}
	policy.NotBefore = time.Time{}
	limit, err := refundLimit(tx, order, policy, req.ID, now)
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, err
	}
	limit = min(limit, req.AmountMinor)
	switch {
	case amountMinor < 0 || amountMinor > limit:
		return model.RefundRequest{}, model.CheckoutOrder{}, false, &RefundRefusedError{Reason: RefundRefusedAmount}
	case amountMinor == 0:
		amountMinor = limit
	}
	req.AmountMinor = amountMinor
	req.Currency = refundCurrency(order)
	req.CreditsMicro = order.RefundDelta(amountMinor)
	req.Status, req.ReviewedBy, req.ReviewNote, req.UpdatedAt = model.RefundStatusProcessing, reviewer, note, now
	changed, err := holdRefund(tx, req, now)
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, err
	}
	if err := tx.SaveRefundRequest(req); err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, false, err
	}
	return req, order, changed, nil
}

// TransitionRefundRules applies t through tx; see RefundTransition.
//
// TransitionRefundRules 通过 tx 应用 t，见 RefundTransition。
func TransitionRefundRules(tx BillingTx, t RefundTransition, now time.Time) (model.RefundRequest, bool, error) {
	req, _, err := lockRefundRequest(tx, t.ID)
	if err != nil {
		return model.RefundRequest{}, false, err
	}
	if t.TenantID != "" && req.TenantID != t.TenantID {
		return model.RefundRequest{}, false, ErrNotFound
	}
	from := model.RefundStatusPending
	if t.To == model.RefundStatusFailed {
		from = model.RefundStatusProcessing
	}
	if req.Status != from {
		return model.RefundRequest{}, false, &RefundRefusedError{Reason: RefundRefusedStatus}
	}
	changed := false
	switch t.To {
	case model.RefundStatusRejected:
		req.Status, req.ReviewedBy, req.ReviewNote, req.UpdatedAt = t.To, t.Actor, t.Note, now
	case model.RefundStatusCancelled:
		req.Status, req.UpdatedAt = t.To, now
	case model.RefundStatusFailed:
		// failRefund saves the request. failRefund 会保存申请。
		changed, err = failRefund(tx, &req, t.Note, now)
		return req, changed, err
	default:
		return model.RefundRequest{}, false, &RefundRefusedError{Reason: RefundRefusedStatus}
	}
	return req, changed, tx.SaveRefundRequest(req)
}

// lockRefundRequest locks a refund request's order, then the request, in the
// order every refund path takes them.
//
// lockRefundRequest 按所有退款路径共同的顺序，先锁定退款申请的订单，再锁定申请本身。
func lockRefundRequest(tx BillingTx, id string) (model.RefundRequest, model.CheckoutOrder, error) {
	orderID, err := tx.RefundRequestOrderID(id)
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, err
	}
	order, err := tx.OrderByID(orderID)
	if err != nil {
		return model.RefundRequest{}, model.CheckoutOrder{}, err
	}
	req, err := tx.RefundRequestByID(id)
	return req, order, err
}

// refundLimit is the most a refund of order may return under policy, or a
// *RefundRefusedError saying why nothing may be.
//
// refundLimit 是在 policy 下这个订单的一次退款最多能退回的金额；一分也不能退时返回
// 说明原因的 *RefundRefusedError。
func refundLimit(tx BillingTx, order model.CheckoutOrder, policy RefundPolicy, exceptID string, now time.Time) (int64, error) {
	if order.Status != model.OrderStatusPaid {
		return 0, &RefundRefusedError{Reason: RefundRefusedOrderState}
	}
	if !policy.NotBefore.IsZero() && order.CreatedAt.Before(policy.NotBefore) {
		return 0, &RefundRefusedError{Reason: RefundRefusedWindow}
	}
	active, err := tx.OrderHasActiveRefund(order.ID, exceptID)
	if err != nil {
		return 0, err
	}
	if active {
		return 0, &RefundRefusedError{Reason: RefundRefusedInProgress}
	}
	limit := order.RemainingRefundMinor()
	if policy.UnusedOnly {
		row, err := tx.TenantBilling(order.TenantID, now)
		if err != nil {
			return 0, err
		}
		limit = order.UnusedRefundMinor(row.BalanceMicro)
	}
	if limit <= 0 {
		return 0, &RefundRefusedError{Reason: RefundRefusedNothing}
	}
	return limit, nil
}

func refundCurrency(order model.CheckoutOrder) string {
	if order.PaidCurrency != "" {
		return order.PaidCurrency
	}
	return strings.ToUpper(order.Currency)
}

// holdRefund takes an approved refund's credits off the balance until the
// provider settles it, so the tenant cannot spend what is being refunded.
//
// holdRefund 在服务商结算之前把已批准退款的点数从余额中扣下，使租户无法花掉正在退款的
// 点数。
func holdRefund(tx BillingTx, req model.RefundRequest, now time.Time) (bool, error) {
	if req.CreditsMicro <= 0 {
		return false, nil
	}
	return creditOnce(tx, req.TenantID, model.LedgerKindRefund, -req.CreditsMicro, req.OrderID, "refund_hold:"+req.ID, now)
}

// failRefund marks a processing request failed with failure as the reason,
// saves it, and returns the credits its approval held.
//
// failRefund 把一个处理中的申请标记为失败并以 failure 为原因，保存它，并退回批准时冻结
// 的点数。
func failRefund(tx BillingTx, req *model.RefundRequest, failure string, now time.Time) (bool, error) {
	req.Status, req.Failure, req.UpdatedAt = model.RefundStatusFailed, failure, now
	if err := tx.SaveRefundRequest(*req); err != nil {
		return false, err
	}
	if req.CreditsMicro <= 0 {
		return false, nil
	}
	return creditOnce(tx, req.TenantID, model.LedgerKindRefundReversal, req.CreditsMicro, req.OrderID, "refund_release:"+req.ID, now)
}

func creditOnce(tx BillingTx, tenantID, kind string, amount int64, ref, key string, now time.Time) (bool, error) {
	entry := model.CreditLedgerEntry{
		ID: model.NewID(model.PrefixCreditLedger), TenantID: tenantID, Kind: kind,
		AmountMicro: amount, Ref: ref, IdempotencyKey: &key, CreatedAt: now,
	}
	changed, _, err := tx.Credit(&entry)
	return changed, err
}

func disputeOpened(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	order, result, ok, err := findPaidOrder(tx, event.PaymentID)
	if !ok || err != nil {
		return result, err
	}
	if event.DisputeID == "" {
		result.Outcome = BillingOutcomeMismatch
		return result, nil
	}
	key := "dispute:" + event.DisputeID
	if _, seen, err := tx.LedgerAmount(key); err != nil || seen {
		result.Outcome = BillingOutcomeDuplicate
		return result, err
	}
	held := order.CreditsMicro - order.RefundCredits(order.RefundedMinor)
	order.Status = model.OrderStatusDisputed
	order.UpdatedAt = now
	if err := tx.SaveOrder(order); err != nil {
		return BillingEventResult{}, err
	}
	return credit(tx, result, order.TenantID, model.LedgerKindDispute, -held, order.ID, key, now)
}

func disputeWon(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	order, result, ok, err := findPaidOrder(tx, event.PaymentID)
	if !ok || err != nil {
		return result, err
	}
	if event.DisputeID == "" {
		result.Outcome = BillingOutcomeMismatch
		return result, nil
	}
	taken, found, err := tx.LedgerAmount("dispute:" + event.DisputeID)
	if err != nil {
		return BillingEventResult{}, err
	}
	if !found {
		return BillingEventResult{}, ErrBillingEventEarly
	}
	if order.Status == model.OrderStatusDisputed {
		order.Status = model.OrderStatusPaid
		if order.RefundedMinor >= order.RefundBasisMinor() {
			order.Status = model.OrderStatusRefunded
		}
		order.UpdatedAt = now
		if err := tx.SaveOrder(order); err != nil {
			return BillingEventResult{}, err
		}
	}
	return credit(tx, result, order.TenantID, model.LedgerKindDisputeReversal, -taken, order.ID, "dispute_won:"+event.DisputeID, now)
}

// findPaidOrder finds the order a payment paid, for the refund and dispute
// events that follow a payment. ok is false for an event without a payment
// id, with result already saying so. An unknown payment is
// ErrBillingEventEarly: its success may still be on the way, and
// acknowledging the event now would lose it for good.
//
// findPaidOrder 查找某笔付款所支付的订单，供付款之后的退款与争议事件使用。事件没有
// 付款 id 时 ok 为 false，result 已说明这一点。未知付款返回 ErrBillingEventEarly：它的
// 付款成功事件可能仍在路上，现在确认这个事件会让它永久丢失。
func findPaidOrder(tx BillingTx, paymentID string) (model.CheckoutOrder, BillingEventResult, bool, error) {
	if paymentID == "" {
		return model.CheckoutOrder{}, BillingEventResult{Outcome: BillingOutcomeMismatch}, false, nil
	}
	order, result, ok, err := findOrder(tx, "", paymentID)
	if err != nil {
		return model.CheckoutOrder{}, BillingEventResult{}, false, err
	}
	if !ok {
		return model.CheckoutOrder{}, BillingEventResult{}, false, ErrBillingEventEarly
	}
	return order, result, true, nil
}

// findOrder looks an order up by id, or by payment id when id is empty. ok is
// false when there is no such order, with result already set to say so.
//
// findOrder 按 id 查找订单，id 为空时按付款 id 查找。不存在这样的订单时 ok 为 false，
// result 已被设置为说明这一点。
func findOrder(tx BillingTx, id, paymentID string) (model.CheckoutOrder, BillingEventResult, bool, error) {
	var order model.CheckoutOrder
	err := ErrNotFound
	switch {
	case id != "":
		order, err = tx.OrderByID(id)
	case paymentID != "":
		order, err = tx.OrderByPayment(paymentID)
	}
	if errors.Is(err, ErrNotFound) {
		return model.CheckoutOrder{}, BillingEventResult{Outcome: BillingOutcomeNoOrder}, false, nil
	}
	if err != nil {
		return model.CheckoutOrder{}, BillingEventResult{}, false, err
	}
	return order, BillingEventResult{TenantID: order.TenantID, OrderID: order.ID}, true, nil
}

func credit(tx BillingTx, result BillingEventResult, tenantID, kind string, amount int64, ref, key string, now time.Time) (BillingEventResult, error) {
	key = strings.TrimSpace(key)
	entry := model.CreditLedgerEntry{
		ID: model.NewID(model.PrefixCreditLedger), TenantID: tenantID, Kind: kind,
		AmountMicro: amount, Ref: ref, IdempotencyKey: &key, CreatedAt: now,
	}
	changed, duplicate, err := tx.Credit(&entry)
	if err != nil {
		return BillingEventResult{}, err
	}
	result.BlockedChanged = changed
	result.Outcome = BillingOutcomeApplied
	if duplicate {
		result.Outcome = BillingOutcomeDuplicate
	}
	return result, nil
}

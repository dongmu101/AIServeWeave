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
	// SaveOrder writes an order's payment id, refunded amount, status and
	// update time.
	//
	// SaveOrder 写入订单的付款 id、已退款金额、状态与更新时间。
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
}

// ApplyBillingEventRules applies one provider event through tx. A payment
// credits its order's credits once, keyed by payment id; a refund takes back
// the credits in proportion to the amount refunded, keyed by refund id; a
// dispute takes back what the order still holds and a won dispute returns
// exactly that. Balances may go negative: credits already spent cannot be
// taken back from usage, so a negative balance is the debt the next top-up
// settles first.
//
// ApplyBillingEventRules 通过 tx 应用一个服务商事件。付款按付款 id 为键，为其订单
// 入账一次；退款按退款 id 为键，按退款金额比例扣回点数；争议扣回订单仍持有的点数，
// 争议胜诉时原样退回。余额可以为负：已经用掉的点数无法从用量中收回，因此负余额就是
// 下一次充值首先要还上的欠款。
func ApplyBillingEventRules(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	switch event.Kind {
	case BillingEventPaymentSucceeded:
		return paymentSucceeded(tx, event, now)
	case BillingEventPaymentFailed:
		return paymentFailed(tx, event, now)
	case BillingEventRefundSucceeded:
		return refundSucceeded(tx, event, now)
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
	paymentID := event.PaymentID
	order.DodoPaymentID = &paymentID
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
	order, result, ok, err := findOrder(tx, "", event.PaymentID)
	if !ok || err != nil {
		return result, err
	}
	key := "refund:" + event.RefundID
	if event.RefundID == "" || event.AmountMinor <= 0 {
		result.Outcome = BillingOutcomeMismatch
		return result, nil
	}
	if _, seen, err := tx.LedgerAmount(key); err != nil || seen {
		result.Outcome = BillingOutcomeDuplicate
		return result, err
	}
	refunded := min(order.RefundedMinor+event.AmountMinor, order.AmountMinor)
	credits := order.RefundCredits(refunded) - order.RefundCredits(order.RefundedMinor)
	order.RefundedMinor = refunded
	if refunded >= order.AmountMinor {
		order.Status = model.OrderStatusRefunded
	}
	order.UpdatedAt = now
	if err := tx.SaveOrder(order); err != nil {
		return BillingEventResult{}, err
	}
	return credit(tx, result, order.TenantID, model.LedgerKindRefund, -credits, order.ID, key, now)
}

func disputeOpened(tx BillingTx, event BillingEvent, now time.Time) (BillingEventResult, error) {
	order, result, ok, err := findOrder(tx, "", event.PaymentID)
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
	order, result, ok, err := findOrder(tx, "", event.PaymentID)
	if !ok || err != nil {
		return result, err
	}
	taken, found, err := tx.LedgerAmount("dispute:" + event.DisputeID)
	if err != nil {
		return BillingEventResult{}, err
	}
	if event.DisputeID == "" || !found {
		result.Outcome = BillingOutcomeIgnored
		return result, nil
	}
	if order.Status == model.OrderStatusDisputed {
		order.Status = model.OrderStatusPaid
		if order.RefundedMinor >= order.AmountMinor {
			order.Status = model.OrderStatusRefunded
		}
		order.UpdatedAt = now
		if err := tx.SaveOrder(order); err != nil {
			return BillingEventResult{}, err
		}
	}
	return credit(tx, result, order.TenantID, model.LedgerKindDisputeReversal, -taken, order.ID, "dispute_won:"+event.DisputeID, now)
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

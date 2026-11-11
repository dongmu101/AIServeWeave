package store

import (
	"context"
	"errors"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// AlipayBilling keeps the Alipay (一人付) purchase intents and redemptions.
// A redemption is saved before 一人付 is asked to consume, and its credits
// are booked in the same transaction that marks it succeeded, under its own
// ledger key, so a resumed or repeated settlement books them once.
//
// AlipayBilling 保存支付宝（一人付）的购买意图与兑换。兑换在请求一人付核销之前保存，
// 其点数在把它标为成功的同一事务中以它自己的账目键入账，因此恢复或重复的结算只入账一次。
type AlipayBilling interface {
	// CreateAlipayPurchase saves a purchase intent, or returns the one the
	// tenant already saved under the same action id. A saved intent with a
	// different SKU is ErrConflict.
	//
	// CreateAlipayPurchase 保存一个购买意图；租户已用同一 action id 保存过时返回原记录。
	// 原记录的 SKU 不同则返回 ErrConflict。
	CreateAlipayPurchase(ctx context.Context, row model.AlipayPurchase) (model.AlipayPurchase, error)
	// SaveAlipayPurchase writes a purchase's status, entry, expiry and
	// reason code.
	//
	// SaveAlipayPurchase 写入购买记录的状态、入口、到期时间与原因码。
	SaveAlipayPurchase(ctx context.Context, row model.AlipayPurchase) error
	// CreateAlipayRedemption saves a pending redemption under
	// CreateAlipayRedemptionRules.
	//
	// CreateAlipayRedemption 按 CreateAlipayRedemptionRules 保存一个待确认的兑换。
	CreateAlipayRedemption(ctx context.Context, row model.AlipayRedemption) (model.AlipayRedemption, error)
	// CreatePresaleAlipayRedemption saves a presale order and the pending
	// redemption that pays for it, under CreatePresaleAlipayRules.
	//
	// CreatePresaleAlipayRedemption 按 CreatePresaleAlipayRules 保存一个预售订单及支付它的
	// 待确认兑换。
	CreatePresaleAlipayRedemption(ctx context.Context, order model.CheckoutOrder, row model.AlipayRedemption) (model.AlipayRedemption, error)
	// SettleAlipayRedemption applies outcome under SettleAlipayRedemptionRules.
	//
	// SettleAlipayRedemption 按 SettleAlipayRedemptionRules 应用 outcome。
	SettleAlipayRedemption(ctx context.Context, outcome AlipayOutcome, now time.Time) (model.AlipayRedemption, bool, error)
	// GetAlipayRedemption reads one redemption. ErrNotFound when there is none.
	//
	// GetAlipayRedemption 读取一个兑换；不存在时返回 ErrNotFound。
	GetAlipayRedemption(ctx context.Context, id string) (model.AlipayRedemption, error)
	// ListAlipayRedemptions lists redemptions newest first.
	//
	// ListAlipayRedemptions 按时间倒序列出兑换。
	ListAlipayRedemptions(ctx context.Context, query ListQuery, filter AlipayRedemptionFilter) (Page[model.AlipayRedemption], error)
}

// AlipayRedemptionFilter narrows a redemption list. An empty field does not
// filter; a zero CreatedBefore does not either.
//
// AlipayRedemptionFilter 收窄兑换列表。字段为空表示不筛选；CreatedBefore 为零也不筛选。
type AlipayRedemptionFilter struct {
	TenantID      string
	Status        string
	CreatedBefore time.Time
}

// AlipayOutcome is a definite answer about one pending redemption: it
// succeeded (Succeeded) or definitely failed (!Succeeded).
//
// AlipayOutcome 是关于一个待确认兑换的确定答复：成功（Succeeded）或明确失败（!Succeeded）。
type AlipayOutcome struct {
	ID             string
	Succeeded      bool
	ReasonCode     string
	ConsumeOrderID string
}

// AlipayTx is the transaction-scoped access the redemption rules need, in
// addition to the ledger.
//
// AlipayTx 是兑换规则在账目之外所需的事务内访问能力。
type AlipayTx interface {
	// Credit is BillingTx.Credit.
	Credit(entry *model.CreditLedgerEntry) (changed, duplicate bool, err error)
	// TenantBilling is BillingTx.TenantBilling; the rules take it as the
	// tenant's lock.
	TenantBilling(tenantID string, now time.Time) (model.TenantBilling, error)
	// AlipayRedemptionByID reads and locks one redemption.
	//
	// AlipayRedemptionByID 读取并锁定一个兑换。
	AlipayRedemptionByID(id string) (model.AlipayRedemption, error)
	// AlipayRedemptionByAction reads a tenant's redemption by action id.
	//
	// AlipayRedemptionByAction 按 action id 读取租户的一个兑换。
	AlipayRedemptionByAction(tenantID, actionID string) (model.AlipayRedemption, error)
	// AlipayPendingRedemption reports whether the tenant has a pending
	// redemption.
	//
	// AlipayPendingRedemption 报告租户是否有待确认的兑换。
	AlipayPendingRedemption(tenantID string) (bool, error)
	InsertAlipayRedemption(row model.AlipayRedemption) error
	SaveAlipayRedemption(row model.AlipayRedemption) error
	// OrderByID and SaveOrder are BillingTx's, for the presale order a
	// redemption pays for.
	//
	// OrderByID 与 SaveOrder 即 BillingTx 的同名方法，用于兑换所支付的预售订单。
	OrderByID(id string) (model.CheckoutOrder, error)
	SaveOrder(order model.CheckoutOrder) error
	// InsertOrder adds a new order.
	//
	// InsertOrder 新增一个订单。
	InsertOrder(order model.CheckoutOrder) error
	// LockPresale reads and locks one presale, so checkouts of the same
	// presale are counted one at a time.
	//
	// LockPresale 读取并锁定一个预售，使同一预售的结账逐个计数。
	LockPresale(id string) (model.AccountPresale, error)
	// PresaleOutstanding counts the presale's orders PresaleOrderOutstanding counts at now.
	//
	// PresaleOutstanding 统计预售在 now 时按 PresaleOrderOutstanding 计入的订单数。
	PresaleOutstanding(presaleID string, now time.Time) (int, error)
}

// CreateAlipayRedemptionRules saves row as a pending redemption through tx,
// holding the tenant's billing lock. A redemption already saved under the
// same action id is returned as it stands, or ErrConflict when its amount
// differs. Otherwise a tenant with a pending redemption gets ErrConflict:
// a redemption whose outcome is unknown must be resumed, not raced by a new
// one that could consume the same quota twice.
//
// CreateAlipayRedemptionRules 在持有租户计费锁的情况下经 tx 把 row 保存为待确认兑换。
// 同一 action id 已保存的兑换原样返回；其数量不同时返回 ErrConflict。否则，租户已有待确认
// 兑换时返回 ErrConflict：结果未知的兑换必须恢复，而不是被一个可能重复扣减同一额度的新兑换抢先。
func CreateAlipayRedemptionRules(tx AlipayTx, row model.AlipayRedemption, now time.Time) (model.AlipayRedemption, error) {
	if _, err := tx.TenantBilling(row.TenantID, now); err != nil {
		return model.AlipayRedemption{}, err
	}
	existing, err := tx.AlipayRedemptionByAction(row.TenantID, row.ActionID)
	switch {
	case err == nil:
		if existing.Amount != row.Amount {
			return model.AlipayRedemption{}, ErrConflict
		}
		return existing, nil
	case !errors.Is(err, ErrNotFound):
		return model.AlipayRedemption{}, err
	}
	pending, err := tx.AlipayPendingRedemption(row.TenantID)
	if err != nil {
		return model.AlipayRedemption{}, err
	}
	if pending {
		return model.AlipayRedemption{}, ErrConflict
	}
	row.Status = model.AlipayRedemptionPending
	if err := tx.InsertAlipayRedemption(row); err != nil {
		return model.AlipayRedemption{}, err
	}
	return row, nil
}

// PresaleAlipayTerms decides whether one month of presale may be paid with
// amount Alipay units while outstanding orders wait.
//
// PresaleAlipayTerms 判断在已有 outstanding 笔订单等待时，能否用 amount 个支付宝单位支付
// presale 的一个月。
func PresaleAlipayTerms(presale model.AccountPresale, amount int64, outstanding int) error {
	if !presale.Active || presale.AlipayAmount <= 0 {
		return ErrNotFound
	}
	if presale.AlipayAmount != amount {
		return ErrConflict
	}
	if outstanding >= presale.Capacity {
		return ErrConflict
	}
	return nil
}

// CreatePresaleAlipayRules saves order, a pending presale order, and row,
// the pending redemption that pays for it, through tx, holding the tenant's
// billing lock and then the presale's. A redemption already saved under the
// same action id is returned as it stands when it pays for a presale order
// with the same amount, and is ErrConflict otherwise. A tenant with a
// pending redemption gets ErrConflict, as in CreateAlipayRedemptionRules.
// The presale is then checked under PresaleAlipayTerms.
//
// CreatePresaleAlipayRules 经 tx 在持有租户计费锁、随后持有预售锁的情况下保存待付款的
// 预售订单 order 与支付它的待确认兑换 row。同一 action id 已保存的兑换，若所付的是同数量的
// 预售订单则原样返回，否则返回 ErrConflict。租户已有待确认兑换时与
// CreateAlipayRedemptionRules 一样返回 ErrConflict。随后按 PresaleAlipayTerms 检查预售。
func CreatePresaleAlipayRules(tx AlipayTx, order model.CheckoutOrder, row model.AlipayRedemption, now time.Time) (model.AlipayRedemption, error) {
	if _, err := tx.TenantBilling(row.TenantID, now); err != nil {
		return model.AlipayRedemption{}, err
	}
	existing, err := tx.AlipayRedemptionByAction(row.TenantID, row.ActionID)
	switch {
	case err == nil:
		if existing.PresaleOrderID == "" || existing.Amount != row.Amount {
			return model.AlipayRedemption{}, ErrConflict
		}
		// One click pays for one presale: the same action id for another
		// presale is not the same request.
		// 一次点击只支付一个预售：同一 action id 用于另一个预售并不是同一个请求。
		prior, err := tx.OrderByID(existing.PresaleOrderID)
		if err != nil {
			return model.AlipayRedemption{}, err
		}
		if prior.PresaleID != order.PresaleID {
			return model.AlipayRedemption{}, ErrConflict
		}
		return existing, nil
	case !errors.Is(err, ErrNotFound):
		return model.AlipayRedemption{}, err
	}
	pending, err := tx.AlipayPendingRedemption(row.TenantID)
	if err != nil {
		return model.AlipayRedemption{}, err
	}
	if pending {
		return model.AlipayRedemption{}, ErrConflict
	}
	presale, err := tx.LockPresale(order.PresaleID)
	if err != nil {
		return model.AlipayRedemption{}, err
	}
	outstanding, err := tx.PresaleOutstanding(presale.ID, now)
	if err != nil {
		return model.AlipayRedemption{}, err
	}
	if err := PresaleAlipayTerms(presale, row.Amount, outstanding); err != nil {
		return model.AlipayRedemption{}, err
	}
	if err := tx.InsertOrder(order); err != nil {
		return model.AlipayRedemption{}, err
	}
	row.Status, row.PresaleOrderID = model.AlipayRedemptionPending, order.ID
	if err := tx.InsertAlipayRedemption(row); err != nil {
		return model.AlipayRedemption{}, err
	}
	return row, nil
}

// SettleAlipayRedemptionRules settles a pending redemption through tx. A
// success books its credits under model.LedgerKeyAlipay and marks it
// succeeded; a failure only marks it failed. A redemption that pays for a
// presale order books no credits: its success marks the pending order paid,
// which then waits for an account, and its failure marks it failed. A
// redemption no longer pending is returned unchanged. changed reports a
// usable/exhausted flip.
//
// SettleAlipayRedemptionRules 经 tx 结算一个待确认兑换。成功时以 model.LedgerKeyAlipay
// 入账其点数并标为成功；失败时只标为失败。支付预售订单的兑换不入账点数：成功时把待付款
// 订单标为已付款，随后等待分配账号；失败时把订单标为失败。已不在待确认状态的兑换原样返回。
// changed 报告可用与耗尽之间的翻转。
func SettleAlipayRedemptionRules(tx AlipayTx, outcome AlipayOutcome, now time.Time) (model.AlipayRedemption, bool, error) {
	row, err := tx.AlipayRedemptionByID(outcome.ID)
	if err != nil {
		return model.AlipayRedemption{}, false, err
	}
	if row.Status != model.AlipayRedemptionPending {
		return row, false, nil
	}
	changed := false
	row.ReasonCode, row.ConsumeOrderID, row.UpdatedAt = outcome.ReasonCode, outcome.ConsumeOrderID, now
	if row.PresaleOrderID != "" {
		order, err := tx.OrderByID(row.PresaleOrderID)
		if err != nil {
			return model.AlipayRedemption{}, false, err
		}
		if order.Status == model.OrderStatusPending {
			order.Status, order.UpdatedAt = model.OrderStatusFailed, now
			if outcome.Succeeded {
				order.Status = model.OrderStatusPaid
			}
			if err := tx.SaveOrder(order); err != nil {
				return model.AlipayRedemption{}, false, err
			}
		}
		row.Status = model.AlipayRedemptionFailed
		if outcome.Succeeded {
			row.Status = model.AlipayRedemptionSucceeded
		}
		if err := tx.SaveAlipayRedemption(row); err != nil {
			return model.AlipayRedemption{}, false, err
		}
		return row, false, nil
	}
	if outcome.Succeeded {
		key := model.LedgerKeyAlipay(row.ID)
		entry := model.CreditLedgerEntry{
			ID: model.NewID(model.PrefixCreditLedger), TenantID: row.TenantID, Kind: model.LedgerKindAlipayTopup,
			AmountMicro: row.CreditsMicro, Ref: row.ID, IdempotencyKey: &key, ActorID: row.CreatedBy, CreatedAt: now,
		}
		if changed, _, err = tx.Credit(&entry); err != nil {
			return model.AlipayRedemption{}, false, err
		}
		row.Status = model.AlipayRedemptionSucceeded
	} else {
		row.Status = model.AlipayRedemptionFailed
	}
	if err := tx.SaveAlipayRedemption(row); err != nil {
		return model.AlipayRedemption{}, false, err
	}
	return row, changed, nil
}

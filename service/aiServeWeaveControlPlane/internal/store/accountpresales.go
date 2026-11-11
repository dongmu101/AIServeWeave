package store

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"context"
	"strings"
	"time"
)

// PresaleStock is a presale with the number of its orders still waiting for
// an account, as PresaleOrderOutstanding counts them.
//
// PresaleStock 是一个预售及其仍在等待账号的订单数，按 PresaleOrderOutstanding 计数。
type PresaleStock struct {
	model.AccountPresale
	Outstanding int
}

// AccountPresales keeps the presale catalog, opens its fiat checkouts and
// assigns paid presale orders a monthly account. Payment only marks a
// presale order paid, through ApplyBillingEvent; the month is granted by
// AssignPresaleOrder.
//
// AccountPresales 保存预售商品、发起其法币结账，并为已付款的预售订单分配包月账号。
// 付款只经 ApplyBillingEvent 把预售订单标为已付款；一个月的期限由 AssignPresaleOrder
// 开通。
type AccountPresales interface {
	// ListAccountPresales returns every presale with its outstanding count
	// at now, in id order.
	//
	// ListAccountPresales 按 id 顺序返回全部预售及其在 now 时的等待订单数。
	ListAccountPresales(ctx context.Context, now time.Time) ([]PresaleStock, error)
	// SaveAccountPresale creates or updates a presale. ErrConflict for a
	// full catalog, an existing id on create, or a changed kind on update;
	// ErrNotFound for a missing presale on update.
	//
	// SaveAccountPresale 创建或更新一个预售。商品已满、创建时 id 已存在或更新时类型
	// 改变返回 ErrConflict；更新不存在的预售返回 ErrNotFound。
	SaveAccountPresale(ctx context.Context, row model.AccountPresale, create bool) error
	// CreatePresaleCheckoutOrder locks the presale, re-checks it under
	// PresaleCheckoutTerms with its current outstanding count, and inserts
	// the pending order. ErrNotFound for an archived or missing presale,
	// ErrConflict for a changed price or a full capacity.
	//
	// CreatePresaleCheckoutOrder 锁定预售，按 PresaleCheckoutTerms 以当前等待订单数
	// 复核，然后插入待付款订单。预售已下架或不存在返回 ErrNotFound；价格已变或名额已满
	// 返回 ErrConflict。
	CreatePresaleCheckoutOrder(ctx context.Context, order *model.CheckoutOrder, now time.Time) error
	// AssignPresaleOrder gives a paid presale order the account accountID
	// under AssignPresaleRules.
	//
	// AssignPresaleOrder 按 AssignPresaleRules 为已付款的预售订单分配账号 accountID。
	AssignPresaleOrder(ctx context.Context, orderID, accountID string, now time.Time) (model.CheckoutOrder, error)
	// GetCheckoutOrder reads one order. ErrNotFound when there is none.
	//
	// GetCheckoutOrder 读取一个订单；不存在时返回 ErrNotFound。
	GetCheckoutOrder(ctx context.Context, id string) (model.CheckoutOrder, error)
}

// PresaleCheckoutTerms decides whether a checkout for one month of presale
// may open at amountMinor in currency while outstanding orders wait.
//
// PresaleCheckoutTerms 判断在已有 outstanding 笔订单等待时，能否以 currency 计价的
// amountMinor 为 presale 发起一个月的结账。
func PresaleCheckoutTerms(presale model.AccountPresale, amountMinor int64, currency string, outstanding int) error {
	if !presale.Active || presale.PriceMinor <= 0 || presale.Currency == "" || presale.DodoProductID == "" {
		return ErrNotFound
	}
	if presale.PriceMinor != amountMinor || !strings.EqualFold(presale.Currency, currency) {
		return ErrConflict
	}
	if outstanding >= presale.Capacity {
		return ErrConflict
	}
	return nil
}

// PresaleOrderOutstanding reports whether a presale order takes a place in
// its presale's capacity at now: a pending checkout within
// AccountCheckoutHold, or a paid order not yet assigned an account. A
// payment that settles after the hold still lands as waiting, even past
// capacity.
//
// PresaleOrderOutstanding 报告预售订单在 now 时是否占用其预售的名额：
// AccountCheckoutHold 内的待付款结账，或尚未分配账号的已付款订单。超过占用时长才完成
// 的付款仍会进入等待，即使超出名额。
func PresaleOrderOutstanding(order model.CheckoutOrder, now time.Time) bool {
	if order.PresaleID == "" {
		return false
	}
	switch order.Status {
	case model.OrderStatusPending:
		return now.Before(order.CreatedAt.Add(model.AccountCheckoutHold))
	case model.OrderStatusPaid:
		return order.AccountExpiresAt == nil
	default:
		return false
	}
}

// PresaleAwaiting reports whether a presale order is paid and waiting for an
// account.
//
// PresaleAwaiting 报告预售订单是否已付款且在等待分配账号。
func PresaleAwaiting(order model.CheckoutOrder) bool {
	return order.PresaleID != "" && order.Status == model.OrderStatusPaid && order.AccountExpiresAt == nil
}

// AssignPresaleRules gives the paid presale order orderID the monthly
// account accountID through tx, locking order -> tenant billing -> account
// as a payment does. ErrNotFound for a missing order, a non-presale order
// or a missing account. ErrConflict when the order is not waiting (unpaid,
// already assigned, refunded or disputed) or has a refund in progress, the
// account's kind is not the presale's, another tenant's checkout holds it,
// or AccountGrantTerms refuses it. Listing state is not checked: archived
// inventory may be kept for presales. The month starts now, or at this
// tenant's own unexpired expiry on the account.
//
// AssignPresaleRules 经 tx 为已付款的预售订单 orderID 分配包月账号 accountID，加锁
// 顺序与付款一致：订单 -> 商户计费行 -> 账号。订单不存在、不是预售订单或账号不存在
// 返回 ErrNotFound。订单不在等待（未付款、已分配、已退款或争议中）或有进行中的退款、
// 账号类型与预售不符、被其他商户的结账占用，或 AccountGrantTerms 拒绝时返回
// ErrConflict。不检查上架状态：下架库存可以留作预售。一个月从现在起算；本商户在该账号
// 上尚未到期时从原到期时间延长。
func AssignPresaleRules(tx BillingTx, orderID, accountID string, now time.Time) (model.CheckoutOrder, error) {
	order, err := tx.OrderByID(orderID)
	if err != nil {
		return model.CheckoutOrder{}, err
	}
	if order.PresaleID == "" {
		return model.CheckoutOrder{}, ErrNotFound
	}
	if !PresaleAwaiting(order) {
		return model.CheckoutOrder{}, ErrConflict
	}
	if active, err := tx.OrderHasActiveRefund(order.ID, ""); err != nil {
		return model.CheckoutOrder{}, err
	} else if active {
		return model.CheckoutOrder{}, ErrConflict
	}
	presale, err := tx.PresaleByID(order.PresaleID)
	if err != nil {
		return model.CheckoutOrder{}, err
	}
	if _, err := tx.TenantBilling(order.TenantID, now); err != nil {
		return model.CheckoutOrder{}, err
	}
	sale, err := tx.AccountSaleByID(accountID)
	if err != nil {
		return model.CheckoutOrder{}, err
	}
	if sale.Kind == "" || sale.Kind != presale.Kind {
		return model.CheckoutOrder{}, ErrConflict
	}
	if held, err := tx.AccountCheckoutHeldByOther(accountID, order.TenantID, now); err != nil {
		return model.CheckoutOrder{}, err
	} else if held {
		return model.CheckoutOrder{}, ErrConflict
	}
	end, err := AccountGrantTerms(sale, order.TenantID, now)
	if err != nil {
		return model.CheckoutOrder{}, err
	}
	if err := tx.GrantAccount(accountID, order.TenantID, end, now); err != nil {
		return model.CheckoutOrder{}, err
	}
	order.AccountID, order.AccountExpiresAt, order.UpdatedAt = accountID, &end, now
	if err := tx.SaveOrder(order); err != nil {
		return model.CheckoutOrder{}, err
	}
	return order, nil
}

package logic

import (
	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

// PresaleAvailable reports whether a presale can be checked out now: it is
// listed with a fiat price and still has a place.
//
// PresaleAvailable 报告一个预售此刻能否结账：已上架、有法币价格且仍有名额。
func PresaleAvailable(p store.PresaleStock) bool {
	return p.Active && p.PriceMinor > 0 && p.Currency != "" && p.DodoProductID != "" && p.Outstanding < p.Capacity
}

// ListAccountPresales returns the presale catalog. An operator sees every
// presale; a tenant only the listed, priced ones, and callers must render a
// tenant only whether one is available, never its counts or product.
//
// ListAccountPresales 返回预售商品。运维看到全部预售；商户只看到已上架且已标价的预售，
// 调用方只能向商户呈现是否可购，不能呈现其数量或产品。
func (s *Service) ListAccountPresales(ctx context.Context, actor Actor) ([]store.PresaleStock, error) {
	if !s.accountsEnabled() {
		return nil, ErrNotFound
	}
	operator := actor.TenantID == model.PlatformScope
	if operator {
		if err := s.requirePlatformActor(actor); err != nil {
			return nil, err
		}
	} else if actor.TenantID == "" {
		return nil, ErrForbidden
	}
	rows, err := s.store.ListAccountPresales(ctx, s.clock.Now())
	if err != nil {
		return nil, translate(err)
	}
	if len(rows) > model.MaxPresales {
		return nil, errors.New("logic: presale catalog exceeds limit")
	}
	if operator {
		return rows, nil
	}
	out := make([]store.PresaleStock, 0, len(rows))
	for _, row := range rows {
		if row.Active && row.PriceMinor > 0 && row.Currency != "" && row.DodoProductID != "" {
			out = append(out, row)
		}
	}
	return out, nil
}

// SaveAccountPresale creates or updates a presale. Kind is fixed at
// creation; capacity bounds the orders waiting for an account.
//
// SaveAccountPresale 创建或更新一个预售。类型在创建时确定；名额限制等待分配账号的
// 订单数。
func (s *Service) SaveAccountPresale(ctx context.Context, actor Actor, in model.AccountPresale, create bool) (store.PresaleStock, error) {
	if err := s.requireAccountOperator(actor); err != nil {
		return store.PresaleStock{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.DodoProductID = strings.TrimSpace(in.DodoProductID)
	if in.Name == "" || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 128 || !model.ValidAccountKind(in.Kind) ||
		in.PriceMinor <= 0 || in.PriceMinor > model.MaxAccountPriceMinor || !model.ValidCurrency(in.Currency) ||
		in.DodoProductID == "" || len(in.DodoProductID) > 64 || !utf8.ValidString(in.DodoProductID) ||
		in.Capacity < 1 || in.Capacity > model.MaxPresaleCapacity {
		return store.PresaleStock{}, ErrInvalidInput
	}
	if create {
		in.ID = model.NewID(model.PrefixAccountPresale)
	}
	now := s.clock.Now().UTC()
	in.UpdatedAt = now
	if err := s.store.SaveAccountPresale(ctx, in, create); err != nil {
		return store.PresaleStock{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionPresaleSave, in.ID,
		fmt.Sprintf("kind %s amount %d %s capacity %d active %t", in.Kind, in.PriceMinor, in.Currency, in.Capacity, in.Active), actor.IP)
	rows, err := s.store.ListAccountPresales(ctx, now)
	if err != nil {
		return store.PresaleStock{}, translate(err)
	}
	for _, row := range rows {
		if row.ID == in.ID {
			return row, nil
		}
	}
	return store.PresaleStock{}, ErrNotFound
}

// CreatePresaleCheckout opens a provider checkout for one month of a
// presale, priced in fiat. Only an owner or admin may buy. The payment only
// marks the order paid and waiting; the month starts when an operator
// assigns an account. expectedMinor and currency must match the listed
// price; a full capacity is a conflict, as is a price change.
//
// CreatePresaleCheckout 为一个预售的一个月发起法币服务商结账。只有 owner 或 admin
// 可以购买。付款只把订单标为已付款并等待；一个月自运维分配账号时起算。expectedMinor
// 与 currency 必须与标价一致；名额已满与改价一样视为冲突。
func (s *Service) CreatePresaleCheckout(ctx context.Context, actor Actor, presaleID string, expectedMinor int64, currency string) (CheckoutResult, error) {
	if !s.dodoEnabled() {
		return CheckoutResult{}, ErrNotFound
	}
	if !actor.canPurchase() || actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return CheckoutResult{}, ErrForbidden
	}
	if expectedMinor <= 0 || expectedMinor > model.MaxAccountPriceMinor || !model.ValidCurrency(currency) {
		return CheckoutResult{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	rows, err := s.store.ListAccountPresales(ctx, now)
	if err != nil {
		return CheckoutResult{}, translate(err)
	}
	var presale store.PresaleStock
	found := false
	for _, row := range rows {
		if row.ID == presaleID {
			presale, found = row, true
			break
		}
	}
	if !found {
		return CheckoutResult{}, ErrNotFound
	}
	// Checked before registering a customer; the store checks again under lock.
	if err := store.PresaleCheckoutTerms(presale.AccountPresale, expectedMinor, currency, presale.Outstanding); err != nil {
		return CheckoutResult{}, translate(err)
	}
	customerID, err := s.ensureCustomer(ctx, actor)
	if err != nil {
		return CheckoutResult{}, err
	}
	order := model.CheckoutOrder{
		ID: model.NewID(model.PrefixCheckoutOrder), TenantID: actor.TenantID, PresaleID: presale.ID, DodoProductID: presale.DodoProductID,
		AmountMinor: presale.PriceMinor, Currency: presale.Currency, Status: model.OrderStatusPending,
		CreatedBy: actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreatePresaleCheckoutOrder(ctx, &order, now); err != nil {
		return CheckoutResult{}, translate(err)
	}
	checkout, err := s.billing.Provider.CreateCheckout(ctx, billing.CheckoutRequest{
		CustomerID: customerID, ProductID: presale.DodoProductID, AmountMinor: presale.PriceMinor, Currency: presale.Currency,
		ReturnURL: s.billing.ReturnURL, OrderID: order.ID, TenantID: actor.TenantID,
	})
	if err != nil {
		slog.Error("opening a presale checkout failed", slog.String("order_id", order.ID))
		// Release the place: this order will never be paid.
		if failErr := s.store.FailCheckoutOrder(ctx, order.ID, s.clock.Now()); failErr != nil {
			slog.Error("releasing a failed presale checkout failed", slog.String("order_id", order.ID))
		}
		return CheckoutResult{}, ErrUnavailable
	}
	if err := s.store.SetCheckoutOrderSession(ctx, order.ID, checkout.SessionID, s.clock.Now()); err != nil {
		return CheckoutResult{}, translate(err)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionPresaleCheckout, presaleID,
		fmt.Sprintf("order %s amount %d %s", order.ID, order.AmountMinor, order.Currency), actor.IP)
	return CheckoutResult{OrderID: order.ID, CheckoutURL: checkout.URL}, nil
}

// AssignPresaleOrder gives a paid presale order a monthly account of the
// presale's kind, starting its month now (or extending the tenant's own
// unexpired month on that account) and switching the tenant to
// account_monthly. The account's node must be one the tenant may use.
// ErrNotFound for a missing order or account, or an order that is not a
// presale; ErrConflict when the order is not waiting, has a refund in
// progress, or the account cannot be given to this tenant now.
//
// AssignPresaleOrder 为已付款的预售订单分配一个与预售同类型的包月账号：一个月从现在
// 起算（或延长本商户在该账号上尚未到期的期限），并把商户切换为 account_monthly。账号
// 所在节点必须是该商户可以使用的。订单或账号不存在、订单不是预售订单时返回
// ErrNotFound；订单不在等待、有进行中的退款，或该账号此刻不能给这个商户时返回
// ErrConflict。
func (s *Service) AssignPresaleOrder(ctx context.Context, actor Actor, orderID, accountID string) (model.CheckoutOrder, error) {
	if err := s.requireAccountOperator(actor); err != nil {
		return model.CheckoutOrder{}, err
	}
	if orderID == "" || accountID == "" || len(orderID) > 64 || len(accountID) > 64 {
		return model.CheckoutOrder{}, ErrInvalidInput
	}
	order, err := s.store.GetCheckoutOrder(ctx, orderID)
	if err != nil {
		return model.CheckoutOrder{}, translate(err)
	}
	if order.PresaleID == "" {
		return model.CheckoutOrder{}, ErrNotFound
	}
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return model.CheckoutOrder{}, translate(err)
	}
	var sale model.AccountSale
	found := false
	for _, row := range rows {
		if row.ID == accountID {
			sale, found = row, true
			break
		}
	}
	if !found {
		return model.CheckoutOrder{}, ErrNotFound
	}
	// Assignment does not bypass node dedication any more than a purchase does.
	policy, err := s.nodePolicy(ctx, order.TenantID)
	if err != nil {
		return model.CheckoutOrder{}, err
	}
	if !nodeaccess.CanConsider(order.TenantID, policy, sale.NodeID) {
		return model.CheckoutOrder{}, ErrConflict
	}
	got, err := s.store.AssignPresaleOrder(ctx, orderID, accountID, s.clock.Now().UTC())
	if err != nil {
		return model.CheckoutOrder{}, translate(err)
	}
	if s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	expires := ""
	if got.AccountExpiresAt != nil {
		expires = got.AccountExpiresAt.UTC().Format(time.RFC3339)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionPresaleAssign, got.ID,
		fmt.Sprintf("tenant %s account %s expires %s", got.TenantID, got.AccountID, expires), actor.IP)
	return got, nil
}

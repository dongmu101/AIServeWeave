package logic

import (
	"AIServeWeave/common/accountaccess"
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

// WithAccountSales enables the monthly account inventory and its access
// policy without enabling payment or usage billing. Buying a month needs
// WithBilling, as it is paid through the provider's checkout.
//
// WithAccountSales 不开启支付与用量计费，只开启包月账号库存及其授权；购买一个月须经
// 服务商结账付款，因此还需要 WithBilling。
func WithAccountSales() Option {
	return func(s *Service) { s.accountSales = true }
}

func (s *Service) accountsEnabled() bool { return s.accountSales || s.billingEnabled() }

func (s *Service) requireAccountOperator(actor Actor) error {
	if !s.accountsEnabled() {
		return ErrNotFound
	}
	return s.requirePlatformActor(actor)
}

func (s *Service) ListAccountSales(ctx context.Context, actor Actor) ([]model.AccountSale, error) {
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
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return nil, translate(err)
	}
	if len(rows) > accountaccess.MaxAccounts {
		return nil, errors.New("logic: account inventory exceeds limit")
	}
	if operator {
		return rows, nil
	}
	policy, err := s.nodePolicy(ctx, actor.TenantID)
	if err != nil {
		return nil, err
	}
	out := make([]model.AccountSale, 0, len(rows))
	now := s.clock.Now()
	for _, row := range rows {
		if !nodeaccess.CanConsider(actor.TenantID, policy, row.NodeID) {
			continue
		}
		if row.TenantID != actor.TenantID {
			if !row.Active || now.Before(row.ExpiresAt) {
				continue
			}
			row.TenantID, row.ExpiresAt = "", time.Unix(0, 0).UTC()
		}
		out = append(out, row)
	}
	return out, nil
}

// SaveAccountSale never accepts ownership/expiry from the operator's draft.
// A new account must name its kind; an update may fill in a legacy row's
// empty kind, and keeps the stored kind when the draft omits it. Listed
// inventory needs a fiat price; archived inventory may be left unpriced and
// kept for presale assignment.
//
// SaveAccountSale 从不接受运维草稿中的归属与到期时间。新账号必须指明类型；更新可以
// 为旧行补填空类型，草稿未带类型时保留已存类型。上架库存必须有法币价格；下架库存可以
// 不标价，留作预售分配。
func (s *Service) SaveAccountSale(ctx context.Context, actor Actor, in model.AccountSale, create bool) (model.AccountSale, error) {
	if err := s.requireAccountOperator(actor); err != nil {
		return model.AccountSale{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.DodoProductID = strings.TrimSpace(in.DodoProductID)
	priced := in.Active || in.PriceMinor != 0 || in.Currency != "" || in.DodoProductID != ""
	if in.Name == "" || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 128 ||
		!nodeaccess.ValidNodeID(in.NodeID) || !nodeaccess.ValidNodeID(in.RuntimeID) ||
		(in.Kind != "" && !model.ValidAccountKind(in.Kind)) || (create && in.Kind == "") ||
		(priced && (in.PriceMinor <= 0 || in.PriceMinor > model.MaxAccountPriceMinor || !model.ValidCurrency(in.Currency) ||
			in.DodoProductID == "" || len(in.DodoProductID) > 64 || !utf8.ValidString(in.DodoProductID))) {
		return model.AccountSale{}, ErrInvalidInput
	}
	if _, owned := nodeaccess.OwnerOf(in.NodeID); owned {
		return model.AccountSale{}, ErrConflict
	}
	if create {
		in.ID = model.NewID(model.PrefixAccountSale)
	} else if in.Kind == "" {
		rows, err := s.store.ListAccountSales(ctx)
		if err != nil {
			return model.AccountSale{}, translate(err)
		}
		for _, row := range rows {
			if row.ID == in.ID {
				in.Kind = row.Kind
			}
		}
	}
	in.TenantID, in.ExpiresAt, in.UpdatedAt = "", time.Unix(0, 0).UTC(), s.clock.Now().UTC()
	if err := s.store.SaveAccountSale(ctx, in, create); err != nil {
		return model.AccountSale{}, translate(err)
	}
	if s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionAccountSaleSave, in.ID, "", actor.IP)
	// Return the stored ownership, including when this was an edit.
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return model.AccountSale{}, err
	}
	for _, row := range rows {
		if row.ID == in.ID {
			return row, nil
		}
	}
	return model.AccountSale{}, ErrNotFound
}

// CreateAccountCheckout opens a provider checkout for one month of an
// account, priced in fiat. Only an owner or admin may buy. The month is
// granted only when the provider's payment webhook confirms the order;
// nothing here moves credits. expectedMinor and currency must match the
// listed price, so a price change between reading and buying is a conflict.
// The account must use a pay-what-you-want Dodo product: the order amount is
// passed to the checkout.
//
// CreateAccountCheckout 为一个账号的一个月发起法币服务商结账。只有 owner 或 admin
// 可以购买。只有服务商付款 webhook 确认订单后才开通这一个月；这里不变动任何点数。
// expectedMinor 与 currency 必须与标价一致，读取与购买之间的改价视为冲突。账号须使用
// Dodo「按意愿付费」产品：订单金额会传给结账。
func (s *Service) CreateAccountCheckout(ctx context.Context, actor Actor, accountID string, expectedMinor int64, currency string) (CheckoutResult, error) {
	if !s.billingEnabled() {
		return CheckoutResult{}, ErrNotFound
	}
	if !actor.canPurchase() || actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return CheckoutResult{}, ErrForbidden
	}
	if expectedMinor <= 0 || expectedMinor > model.MaxAccountPriceMinor || !model.ValidCurrency(currency) {
		return CheckoutResult{}, ErrInvalidInput
	}
	// Node policy still applies to sales; purchasing does not bypass dedication.
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return CheckoutResult{}, err
	}
	policy, err := s.nodePolicy(ctx, actor.TenantID)
	if err != nil {
		return CheckoutResult{}, err
	}
	var sale model.AccountSale
	found := false
	for _, row := range rows {
		if row.ID == accountID {
			sale, found = row, nodeaccess.CanConsider(actor.TenantID, policy, row.NodeID)
			break
		}
	}
	if !found {
		return CheckoutResult{}, ErrNotFound
	}
	now := s.clock.Now().UTC()
	// Checked before registering a customer; the store checks again under lock.
	if err := store.AccountCheckoutTerms(sale, actor.TenantID, expectedMinor, currency, now); err != nil {
		return CheckoutResult{}, translate(err)
	}
	customerID, err := s.ensureCustomer(ctx, actor)
	if err != nil {
		return CheckoutResult{}, err
	}
	order := model.CheckoutOrder{
		ID: model.NewID(model.PrefixCheckoutOrder), TenantID: actor.TenantID, AccountID: sale.ID, DodoProductID: sale.DodoProductID,
		AmountMinor: sale.PriceMinor, Currency: sale.Currency, Status: model.OrderStatusPending,
		CreatedBy: actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateAccountCheckoutOrder(ctx, &order, now); err != nil {
		return CheckoutResult{}, translate(err)
	}
	checkout, err := s.billing.Provider.CreateCheckout(ctx, billing.CheckoutRequest{
		CustomerID: customerID, ProductID: sale.DodoProductID, AmountMinor: sale.PriceMinor, Currency: sale.Currency,
		ReturnURL: s.billing.ReturnURL, OrderID: order.ID, TenantID: actor.TenantID,
	})
	if err != nil {
		slog.Error("opening an account checkout failed", slog.String("order_id", order.ID))
		// Release the hold: this order will never be paid.
		if failErr := s.store.FailCheckoutOrder(ctx, order.ID, s.clock.Now()); failErr != nil {
			slog.Error("releasing a failed account checkout failed", slog.String("order_id", order.ID))
		}
		return CheckoutResult{}, ErrUnavailable
	}
	if err := s.store.SetCheckoutOrderSession(ctx, order.ID, checkout.SessionID, s.clock.Now()); err != nil {
		return CheckoutResult{}, translate(err)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionAccountCheckout, accountID,
		fmt.Sprintf("order %s amount %d %s", order.ID, order.AmountMinor, order.Currency), actor.IP)
	return CheckoutResult{OrderID: order.ID, CheckoutURL: checkout.URL}, nil
}

// AccountPolicy includes closed inventory even after expiry or archival. It
// carries absolute expiry timestamps so a cached verification cannot renew it.
func (s *Service) AccountPolicy(ctx context.Context, tenantID string) (accountaccess.Policy, error) {
	if !s.accountsEnabled() {
		return accountaccess.Policy{}, nil
	}
	billing, err := s.tenantBilling(ctx, tenantID)
	if err != nil {
		return accountaccess.Policy{}, err
	}
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return accountaccess.Policy{}, err
	}
	if len(rows) > accountaccess.MaxAccounts {
		return accountaccess.Policy{}, errors.New("logic: account inventory exceeds limit")
	}
	p := accountaccess.Policy{Only: billing.Mode == model.BillingModeAccountMonthly}
	for _, row := range rows {
		lease := accountaccess.Lease{NodeID: row.NodeID, RuntimeID: row.RuntimeID}
		if row.TenantID == tenantID {
			lease.ExpiresAt = row.ExpiresAt
		}
		p.Managed = append(p.Managed, lease)
	}
	return p, nil
}

// TenantAccountFilter uses the service clock just as key verification does.
func (s *Service) TenantAccountFilter(ctx context.Context, tenantID string) (func(string, string) bool, error) {
	p, err := s.AccountPolicy(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.nodePolicy(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	return func(nodeID, runtimeID string) bool { return p.Permits(tenantID, nodes, nodeID, runtimeID, now) }, nil
}

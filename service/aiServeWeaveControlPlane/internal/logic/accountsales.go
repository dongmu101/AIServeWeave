package logic

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// WithAccountSales enables monthly accounts without enabling payment or usage billing.
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
func (s *Service) SaveAccountSale(ctx context.Context, actor Actor, in model.AccountSale, create bool) (model.AccountSale, error) {
	if err := s.requireAccountOperator(actor); err != nil {
		return model.AccountSale{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 128 ||
		!nodeaccess.ValidNodeID(in.NodeID) || !nodeaccess.ValidNodeID(in.RuntimeID) || in.PriceMicro <= 0 || in.PriceMicro > model.MaxPriceMicroPerMTok {
		return model.AccountSale{}, ErrInvalidInput
	}
	if _, owned := nodeaccess.OwnerOf(in.NodeID); owned {
		return model.AccountSale{}, ErrConflict
	}
	if create {
		in.ID = model.NewID(model.PrefixAccountSale)
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

func (s *Service) BuyAccount(ctx context.Context, actor Actor, accountID, requestID string, price int64) (model.AccountPurchase, error) {
	if !s.accountsEnabled() {
		return model.AccountPurchase{}, ErrNotFound
	}
	if !actor.canPurchase() || actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return model.AccountPurchase{}, ErrForbidden
	}
	if b, err := hex.DecodeString(requestID); err != nil || len(b) != 16 || strings.ToLower(requestID) != requestID || price <= 0 {
		return model.AccountPurchase{}, ErrInvalidInput
	}
	// Node policy still applies to sales; purchasing does not bypass dedication.
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return model.AccountPurchase{}, err
	}
	policy, err := s.nodePolicy(ctx, actor.TenantID)
	if err != nil {
		return model.AccountPurchase{}, err
	}
	found := false
	for _, row := range rows {
		if row.ID == accountID {
			found = nodeaccess.CanConsider(actor.TenantID, policy, row.NodeID)
			break
		}
	}
	if !found {
		return model.AccountPurchase{}, ErrNotFound
	}
	receipt, err := s.store.BuyAccount(ctx, accountID, actor.TenantID, actor.UserID, requestID, price, s.clock.Now().UTC())
	if err != nil {
		return model.AccountPurchase{}, translate(err)
	}
	if s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionAccountPurchase, accountID, "purchase="+requestID, actor.IP)
	return receipt, nil
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

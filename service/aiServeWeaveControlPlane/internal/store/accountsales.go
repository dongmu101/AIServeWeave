package store

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"context"
	"errors"
	"time"
)

var ErrInsufficientCredit = errors.New("store: insufficient credit")

// AccountSales makes purchase, exclusive allocation, balance debit, receipt and
// verification invalidation atomic. Targets cannot change after creation.
type AccountSales interface {
	ListAccountSales(context.Context) ([]model.AccountSale, error)
	SaveAccountSale(context.Context, model.AccountSale, bool) error
	BuyAccount(context.Context, string, string, string, string, int64, time.Time) (model.AccountPurchase, error)
}

// AccountPurchaseTerms is shared by the transactional and in-memory stores.
func AccountPurchaseTerms(sale model.AccountSale, tenantID string, price int64, now time.Time) (time.Time, error) {
	if !sale.Active {
		return time.Time{}, ErrNotFound
	}
	if sale.PriceMicro != price || price <= 0 {
		return time.Time{}, ErrConflict
	}
	if sale.TenantID != "" && sale.TenantID != tenantID && now.Before(sale.ExpiresAt) {
		return time.Time{}, ErrConflict
	}
	start := now
	if sale.TenantID == tenantID && sale.ExpiresAt.After(start) {
		start = sale.ExpiresAt
	}
	end := accountaccess.NextMonth(start)
	if end.After(now.AddDate(10, 0, 0)) {
		return time.Time{}, ErrConflict
	}
	return end, nil
}

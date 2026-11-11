package logic

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"context"
)

// Catalog is the purchase information anyone may read without signing in.
//
// Catalog 是任何人无需登录即可读取的购买信息。
type Catalog struct {
	Packages []model.CreditPackage
	Prices   []model.ModelPrice
	Accounts []model.AccountSale
}

// Catalog returns the active credit packages, model prices and the monthly
// accounts that are on sale right now. Packages and prices exist only when
// payment billing is enabled; accounts need only account sales. Callers must
// not render provider ids or node targets from it.
//
// Catalog 返回上架的充值包、模型单价，以及当前可购买的包月账号。充值包与单价仅在开启
// 支付计费时存在；账号只需开启账号包月。调用方不得据此渲染服务商 id 或节点目标。
func (s *Service) Catalog(ctx context.Context) (Catalog, error) {
	if !s.accountsEnabled() {
		return Catalog{}, ErrNotFound
	}
	out := Catalog{Packages: []model.CreditPackage{}, Prices: []model.ModelPrice{}, Accounts: []model.AccountSale{}}
	if s.billingEnabled() {
		var err error
		if out.Packages, err = s.store.ListCreditPackages(ctx, true); err != nil {
			return Catalog{}, translate(err)
		}
		if out.Prices, err = s.store.ListModelPrices(ctx); err != nil {
			return Catalog{}, translate(err)
		}
	}
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return Catalog{}, translate(err)
	}
	now := s.clock.Now()
	for _, row := range rows {
		// A held account is back on sale once its month has ended.
		if row.Active && !now.Before(row.ExpiresAt) {
			out.Accounts = append(out.Accounts, row)
		}
	}
	return out, nil
}

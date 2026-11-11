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
// accounts that can be bought right now. All three exist only when payment
// billing is enabled: a month is bought through the provider's checkout, so
// with account sales alone the catalog answers with empty lists. Accounts
// without a fiat price (legacy credit-priced offers) are left out. Callers
// must not render provider ids or node targets from it.
//
// Catalog 返回上架的充值包、模型单价，以及当前可购买的包月账号。三者都仅在开启支付计费
// 时存在：一个月须经服务商结账购买，因此只开启账号包月时返回空列表。没有法币价格的账号
// （旧版按点数定价的商品）不列出。调用方不得据此渲染服务商 id 或节点目标。
func (s *Service) Catalog(ctx context.Context) (Catalog, error) {
	if !s.accountsEnabled() {
		return Catalog{}, ErrNotFound
	}
	out := Catalog{Packages: []model.CreditPackage{}, Prices: []model.ModelPrice{}, Accounts: []model.AccountSale{}}
	if !s.billingEnabled() {
		return out, nil
	}
	var err error
	if out.Packages, err = s.store.ListCreditPackages(ctx, true); err != nil {
		return Catalog{}, translate(err)
	}
	if out.Prices, err = s.store.ListModelPrices(ctx); err != nil {
		return Catalog{}, translate(err)
	}
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return Catalog{}, translate(err)
	}
	now := s.clock.Now()
	for _, row := range rows {
		// A held account is back on sale once its month has ended.
		if row.Active && row.PriceMinor > 0 && row.Currency != "" && row.DodoProductID != "" && !now.Before(row.ExpiresAt) {
			out.Accounts = append(out.Accounts, row)
		}
	}
	return out, nil
}

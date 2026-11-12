package logic

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"context"
)

// Catalog is the purchase information anyone may read without signing in.
//
// Catalog 是任何人无需登录即可读取的购买信息。
type Catalog struct {
	Packages []model.CreditPackage
	Prices   []model.ModelPrice
	Accounts []model.AccountSale
	// Presales are the listed, priced presales; callers render only their
	// kind, name, price and PresaleAvailable.
	//
	// Presales 是已上架且已标价的预售；调用方只呈现其类型、名称、价格与
	// PresaleAvailable。
	Presales []store.PresaleStock
}

// Catalog returns the active credit packages, model prices, the monthly
// accounts that can be bought right now and the listed presales. All of
// them exist only when payment billing is enabled: a month is bought
// through the provider's checkout, so with account sales alone the catalog
// answers with empty lists. Accounts and presales without a fiat price
// (legacy credit-priced offers) are left out. Callers must not render
// provider ids or node targets from it.
//
// Catalog 返回上架的充值包、模型单价、当前可购买的包月账号以及上架的预售。它们都仅在
// 开启支付计费时存在：一个月须经服务商结账购买，因此只开启账号包月时返回空列表。没有
// 法币价格的账号与预售（旧版按点数定价的商品）不列出。调用方不得据此渲染服务商 id 或
// 节点目标。只配置支付宝时只返回模型单价。
func (s *Service) Catalog(ctx context.Context) (Catalog, error) {
	if !s.accountsEnabled() {
		return Catalog{}, ErrNotFound
	}
	out := Catalog{Packages: []model.CreditPackage{}, Prices: []model.ModelPrice{}, Accounts: []model.AccountSale{}, Presales: []store.PresaleStock{}}
	if !s.billingEnabled() {
		return out, nil
	}
	var err error
	if out.Prices, err = s.store.ListModelPrices(ctx); err != nil {
		return Catalog{}, translate(err)
	}
	// Packages, accounts and presales are bought through a fiat checkout,
	// Dodo's or 易支付's; an item is listed when one of them can sell it.
	// 充值包、账号与预售都经法币结账（Dodo 或易支付）购买；其中之一能出售时才列出。
	if !s.fiatEnabled() {
		return out, nil
	}
	packages, err := s.store.ListCreditPackages(ctx, true)
	if err != nil {
		return Catalog{}, translate(err)
	}
	for _, pkg := range packages {
		if s.fiatPayable(pkg.Currency, pkg.DodoProductID) {
			out.Packages = append(out.Packages, pkg)
		}
	}
	rows, err := s.store.ListAccountSales(ctx)
	if err != nil {
		return Catalog{}, translate(err)
	}
	now := s.clock.Now()
	for _, row := range rows {
		// A held account is back on sale once its month has ended.
		if row.Active && row.PriceMinor > 0 && row.Currency != "" && s.fiatPayable(row.Currency, row.DodoProductID) && !now.Before(row.ExpiresAt) {
			out.Accounts = append(out.Accounts, row)
		}
	}
	presales, err := s.store.ListAccountPresales(ctx, now)
	if err != nil {
		return Catalog{}, translate(err)
	}
	for _, row := range presales {
		if row.Active && row.PriceMinor > 0 && row.Currency != "" && s.fiatPayable(row.Currency, row.DodoProductID) {
			out.Presales = append(out.Presales, row)
		}
	}
	return out, nil
}

package handler

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
	"net/http"
	"strings"
)

// getCatalog handles GET /billing/v1/catalog. It is public: it needs no session
// and shows only what a buyer needs to decide, never provider product ids or
// the node a monthly account is bound to.
//
// getCatalog 处理 GET /billing/v1/catalog。它是公开的：无需会话，只展示买家做决定所需
// 的内容，绝不含服务商产品 id，也不含包月账号绑定的节点。
func getCatalog(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := ctx.Logic.Catalog(r.Context())
		if err != nil {
			respondErr(w, err)
			return
		}
		out := types.CatalogResponse{
			Packages: make([]types.CatalogPackage, len(c.Packages)),
			Accounts: make([]types.CatalogAccount, len(c.Accounts)),
			Presales: make([]types.CatalogPresale, len(c.Presales)),
			Prices:   make([]types.CatalogPrice, len(c.Prices)),
		}
		for i, p := range c.Packages {
			out.Packages[i] = types.CatalogPackage{
				ID: p.ID, Name: p.Name, Kind: p.Kind, CreditsMicro: p.CreditsMicro, PriceMinor: p.PriceMinor,
				Currency: strings.ToUpper(p.Currency), CreditsPerMinorUnitMicro: p.CreditsPerMinorUnitMicro,
				MinAmountMinor: p.MinAmountMinor, MaxAmountMinor: p.MaxAmountMinor,
			}
		}
		for i, a := range c.Accounts {
			out.Accounts[i] = types.CatalogAccount{ID: a.ID, Kind: a.Kind, Name: a.Name, PriceMinor: a.PriceMinor, Currency: a.Currency}
		}
		for i, p := range c.Presales {
			out.Presales[i] = types.CatalogPresale{ID: p.ID, Kind: p.Kind, Name: p.Name, PriceMinor: p.PriceMinor, Currency: p.Currency,
				Available: ctx.Logic.PresaleFiatAvailable(p)}
		}
		for i, p := range c.Prices {
			out.Prices[i] = types.CatalogPrice{Model: p.Model, InputMicroPerMTok: p.InputMicroPerMTok, OutputMicroPerMTok: p.OutputMicroPerMTok}
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		writeJSON(w, http.StatusOK, out)
	}
}

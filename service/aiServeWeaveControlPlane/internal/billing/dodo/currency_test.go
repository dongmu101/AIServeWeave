package dodo

import (
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"

	"github.com/dodopayments/dodopayments-go"
)

// Every currency a price may be in must be one Dodo accepts, or a checkout
// in it would be refused. A code the SDK drops after an upgrade fails here.
//
// 标价可用的每个币种都必须是 Dodo 接受的，否则以它结账会被拒。SDK 升级后移除的代码
// 会在这里失败。
func TestSupportedCurrenciesAreKnownToDodo(t *testing.T) {
	for _, code := range model.SupportedCurrencies {
		if !dodopayments.Currency(code).IsKnown() {
			t.Errorf("%s is not a Dodo currency", code)
		}
	}
}

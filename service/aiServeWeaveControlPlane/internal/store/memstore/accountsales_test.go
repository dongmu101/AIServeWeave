package memstore_test

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
	"testing"
)

func TestAccountFiatCheckouts(t *testing.T) {
	storetest.AccountCheckouts(t, memstore.New())
}

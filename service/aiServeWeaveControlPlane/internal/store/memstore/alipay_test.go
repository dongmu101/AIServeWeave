package memstore_test

import (
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

func TestAlipayRedemptions(t *testing.T) {
	storetest.AlipayRedemptions(t, memstore.New())
}

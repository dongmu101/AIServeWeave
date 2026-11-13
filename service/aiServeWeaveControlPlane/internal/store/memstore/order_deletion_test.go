package memstore_test

import (
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

func TestOrderDeletion(t *testing.T) {
	storetest.OrderDeletion(t, memstore.New())
}

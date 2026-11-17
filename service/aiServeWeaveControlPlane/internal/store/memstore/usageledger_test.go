package memstore_test

import (
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

func TestUsageLedger(t *testing.T) {
	storetest.UsageLedger(t, memstore.New())
}

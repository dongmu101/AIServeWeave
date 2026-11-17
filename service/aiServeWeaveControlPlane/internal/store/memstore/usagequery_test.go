package memstore_test

import (
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

func TestUsageQueries(t *testing.T) {
	storetest.UsageQueries(t, memstore.New())
}

package memstore_test

import (
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

func TestStudioFlows(t *testing.T) {
	storetest.StudioFlows(t, memstore.New())
}

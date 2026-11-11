package memstore_test

import (
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
	"testing"
)

func TestAccountPresales(t *testing.T) {
	storetest.PresaleCheckouts(t, memstore.New())
}

package memstore_test

import (
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/storetest"
)

func TestStudioConversations(t *testing.T) {
	storetest.StudioConversations(t, memstore.New())
}

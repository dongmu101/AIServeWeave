package idgen_test

import (
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	"AIServeWeave/common/idgen"
)

func TestNewIsHexUUIDv7(t *testing.T) {
	id, err := idgen.New()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != idgen.Len {
		t.Fatalf("len(New()) = %d, want %d", len(id), idgen.Len)
	}
	raw, err := hex.DecodeString(id)
	if err != nil {
		t.Fatalf("New() = %q is not hex: %v", id, err)
	}
	u, err := uuid.FromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Version() != 7 || u.Variant() != uuid.RFC4122 {
		t.Errorf("New() = %q has version %d variant %v, want 7 RFC4122", id, u.Version(), u.Variant())
	}
}

func TestNewSortsInMintOrder(t *testing.T) {
	prev := idgen.MustNew()
	for range 10000 {
		next := idgen.MustNew()
		if next <= prev {
			t.Fatalf("id %q minted after %q does not sort after it", next, prev)
		}
		prev = next
	}
}

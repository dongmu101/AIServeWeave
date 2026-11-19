package idgen_test

import (
	"testing"
	"uuid"

	"AIServeWeave/common/idgen"
)

func TestNewIsHexUUIDv7(t *testing.T) {
	id := idgen.New()
	if len(id) != idgen.Len {
		t.Fatalf("len(New()) = %d, want %d", len(id), idgen.Len)
	}
	u, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("New() = %q is not a hex UUID: %v", id, err)
	}
	if v := u[6] >> 4; v != 7 {
		t.Errorf("New() = %q has version %d, want 7", id, v)
	}
	if u[8]&0xc0 != 0x80 {
		t.Errorf("New() = %q has variant bits %02b, want 10 (RFC 9562)", id, u[8]>>6)
	}
}

func TestNewSortsInMintOrder(t *testing.T) {
	prev := idgen.New()
	for range 10000 {
		next := idgen.New()
		if next <= prev {
			t.Fatalf("id %q minted after %q does not sort after it", next, prev)
		}
		prev = next
	}
}

package controlplaneclient_test

import (
	"AIServeWeave/common/accountaccess"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestCachedVerificationPreservesAbsoluteAccountExpiry(t *testing.T) {
	cp := newFakeControlPlane(t)
	clock := newFakeClock()
	end := clock.Now().Add(time.Second)
	cp.respond = func(w http.ResponseWriter, _ string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tenant_id": "buyer", "key_id": "key", "accounts": accountaccess.Policy{Only: true, Managed: []accountaccess.Lease{{NodeID: "agent", RuntimeID: "mine", ExpiresAt: end}}}})
	}
	verifier := newCachingVerifier(t, cp, clock)
	key := mustKey(t)
	identity, err := verifier.Verify(context.Background(), key)
	if err != nil || identity.Accounts == nil || !identity.Accounts.Allows("agent", "mine", clock.Now()) {
		t.Fatal(identity, err)
	}
	clock.Advance(time.Second)
	identity, err = verifier.Verify(context.Background(), key)
	if err != nil || identity.Accounts == nil || identity.Accounts.Active(clock.Now()) || !identity.Accounts.Only {
		t.Fatal("cached expiry extended", identity, err)
	}
	if cp.calls.Load() != 1 {
		t.Fatal("test did not use cached authorization")
	}
}

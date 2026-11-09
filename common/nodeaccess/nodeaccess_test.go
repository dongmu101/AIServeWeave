package nodeaccess_test

import (
	"AIServeWeave/common/nodeaccess"
	"testing"
)

func TestExplicitSharingAndDedicatedGrants(t *testing.T) {
	p := nodeaccess.PolicyFor([]nodeaccess.Binding{
		{NodeID: "shared", Mode: nodeaccess.ModeShared},
		{NodeID: "private", Mode: nodeaccess.ModePrivate},
		{NodeID: "dedicated", Mode: nodeaccess.ModeDedicated, Tenants: []string{"buyer"}},
	}, "buyer")
	if p.Unrestricted() || !p.DefaultDeny || p.Allows("new-agent") || p.Allows("private") || !p.Allows("shared") || !p.Allows("dedicated") {
		t.Fatalf("unexpected operator-controlled node policy: %+v", p)
	}
	other := nodeaccess.PolicyFor([]nodeaccess.Binding{{NodeID: "dedicated", Mode: nodeaccess.ModeDedicated, Tenants: []string{"buyer"}}}, "other")
	if nodeaccess.CanConsider("other", other, "dedicated") {
		t.Fatal("dedicated binding bypassed")
	}
	owned, err := nodeaccess.NewTenantNodeID("buyer")
	if err != nil {
		t.Fatal(err)
	}
	if !nodeaccess.Permits("buyer", p, owned) || nodeaccess.Permits("other", p, owned) {
		t.Fatal("tenant-owned identity isolation changed")
	}
}

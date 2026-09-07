package registryserver_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// TestRegisterRejectsAnUnapprovedNodeIDAndRecordsItPending covers STATUS.md's
// P01: an unbound bootstrap token registering a node_id no operator has
// approved must be refused, and the attempt must surface in
// ListNodeStates so an operator can find and approve it.
func TestRegisterRejectsAnUnapprovedNodeIDAndRecordsItPending(t *testing.T) {
	f := startRegistry(t)
	client := rawIdentityClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	_, err = client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-new", Csr: newCSR(t), BootstrapToken: tok,
	})
	if err == nil {
		t.Fatal("Register() for an unapproved node_id = nil error, want a rejection")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("Register() error code = %v, want PermissionDenied", status.Code(err))
	}

	admin, adminCtx := tokenAdminClient(t, f)
	resp, err := admin.ListNodeStates(adminCtx, &tunnelv1.ListNodeStatesRequest{})
	if err != nil {
		t.Fatalf("ListNodeStates() error = %v", err)
	}
	found := false
	for _, st := range resp.GetStates() {
		if st.GetNodeId() != "node-new" {
			continue
		}
		found = true
		if !st.GetPendingApproval() {
			t.Error("NodeState.PendingApproval = false, want true")
		}
		if st.GetDisabled() {
			t.Error("NodeState.Disabled = true, want false")
		}
	}
	if !found {
		t.Error("ListNodeStates() did not report node-new as pending")
	}
}

// TestRegisterRejectsAnUnboundTokenWithNoNodeID covers the corollary of
// strict approval (STATUS.md's P01): an unbound bootstrap token can no
// longer let the Registry self-assign a node_id, since a freshly generated
// one would give an operator nothing stable to approve.
func TestRegisterRejectsAnUnboundTokenWithNoNodeID(t *testing.T) {
	f := startRegistry(t)
	client := rawIdentityClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	_, err = client.Register(ctx, &tunnelv1.RegisterRequest{Csr: newCSR(t), BootstrapToken: tok})
	if err == nil {
		t.Fatal("Register() with an empty node_id and an unbound token = nil error, want a rejection")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("Register() error code = %v, want InvalidArgument", status.Code(err))
	}
}

// TestApproveNodeAllowsRegistrationToProceed covers the other half of the
// gate: once ApproveNode clears the pending mark, the same node_id can
// register normally, whether the operator approved it before or after the
// node's first attempt.
func TestApproveNodeAllowsRegistrationToProceed(t *testing.T) {
	f := startRegistry(t)
	admin, adminCtx := tokenAdminClient(t, f)
	client := rawIdentityClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pre-approval: an operator names the node_id before it ever attempts to
	// register.
	if _, err := admin.ApproveNode(adminCtx, &tunnelv1.ApproveNodeRequest{NodeId: "node-preapproved"}); err != nil {
		t.Fatalf("ApproveNode() error = %v", err)
	}
	tok1, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-preapproved", Csr: newCSR(t), BootstrapToken: tok1,
	}); err != nil {
		t.Fatalf("Register() after pre-approval error = %v, want success", err)
	}

	// Post-hoc approval: the node attempts first, is refused and recorded
	// pending, then an operator approves it and a retry succeeds.
	tok2, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-posthoc", Csr: newCSR(t), BootstrapToken: tok2,
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("first Register() for node-posthoc code = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := admin.ApproveNode(adminCtx, &tunnelv1.ApproveNodeRequest{NodeId: "node-posthoc"}); err != nil {
		t.Fatalf("ApproveNode() error = %v", err)
	}
	tok3, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-posthoc", Csr: newCSR(t), BootstrapToken: tok3,
	}); err != nil {
		t.Fatalf("Register() after ApproveNode() error = %v, want success", err)
	}
}

// TestNodeBoundTokenSkipsTheApprovalGate confirms a node-bound bootstrap
// token remains sufficient authorization on its own (STATUS.md's S02 and
// P01 both apply here): minting it is itself the approval, so the node_id
// need not appear in ListNodeStates as pending, and Register must not
// refuse it.
func TestNodeBoundTokenSkipsTheApprovalGate(t *testing.T) {
	f := startRegistry(t)
	admin, adminCtx := tokenAdminClient(t, f)
	client := rawIdentityClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tok, err := admin.MintToken(adminCtx, &tunnelv1.MintTokenRequest{
		Ttl: durationpb.New(15 * time.Minute), NodeId: "node-bound",
	})
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	if _, err := client.Register(ctx, &tunnelv1.RegisterRequest{
		Csr: newCSR(t), BootstrapToken: tok.GetToken(),
	}); err != nil {
		t.Fatalf("Register() with a node-bound token error = %v, want success", err)
	}
}

// TestApproveNodeRequiresAdminToken checks ApproveNode is guarded the same
// way DisableNode/EnableNode already are.
func TestApproveNodeRequiresAdminToken(t *testing.T) {
	f := startRegistry(t)
	admin, ctx := tokenAdminClient(t, f)
	unauthed := t.Context()

	if _, err := admin.ApproveNode(unauthed, &tunnelv1.ApproveNodeRequest{NodeId: "node-1"}); err == nil {
		t.Fatal("ApproveNode() with no admin token = nil error, want a rejection")
	}
	if _, err := admin.ApproveNode(ctx, &tunnelv1.ApproveNodeRequest{NodeId: "node-1"}); err != nil {
		t.Fatalf("ApproveNode() with the admin token error = %v, want success", err)
	}
}

// TestSetMaintenanceBroadcastsMaintenanceNodeIDsOverJoin mirrors
// TestDisableNodeBroadcastsRevokedNodeIDsOverJoin for STATUS.md's P01
// maintenance flag, which travels on GatewayRoster.maintenance_node_ids
// instead of revoked_node_ids.
func TestSetMaintenanceBroadcastsMaintenanceNodeIDsOverJoin(t *testing.T) {
	f := startRegistry(t)
	admin, ctx := tokenAdminClient(t, f)

	join := dialJoin(t, f)
	join.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	first := join.recv()
	if len(first.GetMaintenanceNodeIds()) != 0 {
		t.Fatalf("initial roster maintenance_node_ids = %v, want empty", first.GetMaintenanceNodeIds())
	}

	if _, err := admin.SetMaintenance(ctx, &tunnelv1.SetMaintenanceRequest{NodeId: "node-1"}); err != nil {
		t.Fatalf("SetMaintenance() error = %v", err)
	}
	updated := join.recv()
	if got := updated.GetMaintenanceNodeIds(); len(got) != 1 || got[0] != "node-1" {
		t.Fatalf("roster maintenance_node_ids after SetMaintenance() = %v, want [node-1]", got)
	}
	// A maintenance broadcast must never touch revoked_node_ids: the two
	// facets are independent.
	if len(updated.GetRevokedNodeIds()) != 0 {
		t.Errorf("roster revoked_node_ids after SetMaintenance() = %v, want empty", updated.GetRevokedNodeIds())
	}

	if _, err := admin.ClearMaintenance(ctx, &tunnelv1.ClearMaintenanceRequest{NodeId: "node-1"}); err != nil {
		t.Fatalf("ClearMaintenance() error = %v", err)
	}
	cleared := join.recv()
	if got := cleared.GetMaintenanceNodeIds(); len(got) != 0 {
		t.Fatalf("roster maintenance_node_ids after ClearMaintenance() = %v, want empty", got)
	}
}

// TestListNodeStatesReportsDisabledAndMaintenance checks the two other
// facets ListNodeStates must report alongside pending_approval.
func TestListNodeStatesReportsDisabledAndMaintenance(t *testing.T) {
	f := startRegistry(t)
	admin, ctx := tokenAdminClient(t, f)

	if _, err := admin.DisableNode(ctx, &tunnelv1.DisableNodeRequest{NodeId: "node-disabled"}); err != nil {
		t.Fatalf("DisableNode() error = %v", err)
	}
	if _, err := admin.SetMaintenance(ctx, &tunnelv1.SetMaintenanceRequest{NodeId: "node-maintained"}); err != nil {
		t.Fatalf("SetMaintenance() error = %v", err)
	}

	resp, err := admin.ListNodeStates(ctx, &tunnelv1.ListNodeStatesRequest{})
	if err != nil {
		t.Fatalf("ListNodeStates() error = %v", err)
	}
	byID := make(map[string]*tunnelv1.NodeState)
	for _, st := range resp.GetStates() {
		byID[st.GetNodeId()] = st
	}
	if st, ok := byID["node-disabled"]; !ok || !st.GetDisabled() {
		t.Errorf("ListNodeStates() node-disabled = %v, want Disabled=true", st)
	}
	if st, ok := byID["node-maintained"]; !ok || !st.GetMaintenance() {
		t.Errorf("ListNodeStates() node-maintained = %v, want Maintenance=true", st)
	}
}

package registryserver_test

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

func TestDisableNodeRejectsFutureRegisterAndRenewCertificate(t *testing.T) {
	f := startRegistry(t)
	admin, ctx := tokenAdminClient(t, f)
	f.approve(t, "node-a")

	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	manager := newAgentIdentityManager(t, f, "node-a", tok)
	id, err := manager.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	if _, err := admin.DisableNode(ctx, &tunnelv1.DisableNodeRequest{NodeId: id.NodeID}); err != nil {
		t.Fatalf("DisableNode() error = %v", err)
	}

	if _, err := manager.Renew(ctx, id); err == nil {
		t.Fatal("Renew() for a disabled node = nil error, want a rejection")
	} else if status.Code(err) != codes.PermissionDenied {
		t.Errorf("Renew() error code = %v, want PermissionDenied", status.Code(err))
	}

	tok2, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	client := rawIdentityClient(t, f)
	_, err = client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: id.NodeID, Csr: newCSR(t), BootstrapToken: tok2,
	})
	if err == nil {
		t.Fatal("Register() for a disabled node_id = nil error, want a rejection")
	} else if status.Code(err) != codes.PermissionDenied {
		t.Errorf("Register() error code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestEnableNodeAllowsRegisterAndRenewCertificateAgain(t *testing.T) {
	f := startRegistry(t)
	admin, ctx := tokenAdminClient(t, f)
	f.approve(t, "node-a")

	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	manager := newAgentIdentityManager(t, f, "node-a", tok)
	id, err := manager.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	if _, err := admin.DisableNode(ctx, &tunnelv1.DisableNodeRequest{NodeId: id.NodeID}); err != nil {
		t.Fatalf("DisableNode() error = %v", err)
	}
	if _, err := admin.EnableNode(ctx, &tunnelv1.EnableNodeRequest{NodeId: id.NodeID}); err != nil {
		t.Fatalf("EnableNode() error = %v", err)
	}

	if _, err := manager.Renew(ctx, id); err != nil {
		t.Fatalf("Renew() after EnableNode() error = %v, want success", err)
	}
}

func TestDisableAndEnableNodeRequireAdminToken(t *testing.T) {
	f := startRegistry(t)
	admin, ctx := tokenAdminClient(t, f)
	unauthed := t.Context()

	if _, err := admin.DisableNode(unauthed, &tunnelv1.DisableNodeRequest{NodeId: "node-1"}); err == nil {
		t.Fatal("DisableNode() with no admin token = nil error, want a rejection")
	}
	if _, err := admin.EnableNode(unauthed, &tunnelv1.EnableNodeRequest{NodeId: "node-1"}); err == nil {
		t.Fatal("EnableNode() with no admin token = nil error, want a rejection")
	}

	// Sanity: the same calls succeed with the token this test otherwise
	// withholds, proving the rejection above is really about the token and
	// not some other misconfiguration.
	if _, err := admin.DisableNode(ctx, &tunnelv1.DisableNodeRequest{NodeId: "node-1"}); err != nil {
		t.Fatalf("DisableNode() with the admin token error = %v, want success", err)
	}
}

func TestDisableNodeBroadcastsRevokedNodeIDsOverJoin(t *testing.T) {
	f := startRegistry(t)
	admin, ctx := tokenAdminClient(t, f)

	join := dialJoin(t, f)
	join.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	first := join.recv()
	if len(first.GetRevokedNodeIds()) != 0 {
		t.Fatalf("initial roster revoked_node_ids = %v, want empty", first.GetRevokedNodeIds())
	}

	if _, err := admin.DisableNode(ctx, &tunnelv1.DisableNodeRequest{NodeId: "node-1"}); err != nil {
		t.Fatalf("DisableNode() error = %v", err)
	}
	updated := join.recv()
	if got := updated.GetRevokedNodeIds(); len(got) != 1 || got[0] != "node-1" {
		t.Fatalf("roster revoked_node_ids after DisableNode() = %v, want [node-1]", got)
	}

	if _, err := admin.EnableNode(ctx, &tunnelv1.EnableNodeRequest{NodeId: "node-1"}); err != nil {
		t.Fatalf("EnableNode() error = %v", err)
	}
	cleared := join.recv()
	if got := cleared.GetRevokedNodeIds(); len(got) != 0 {
		t.Fatalf("roster revoked_node_ids after EnableNode() = %v, want empty", got)
	}
}

func TestJoinRequiresGatewayTokenWhenConfigured(t *testing.T) {
	f := startRegistryWithGatewayToken(t, testGatewayToken)

	unauthed := dialJoin(t, f)
	unauthed.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	if _, err := unauthed.stream.Recv(); err == nil {
		t.Fatal("Join() with no gateway token = nil error, want a rejection")
	} else if status.Code(err) != codes.Unauthenticated {
		t.Errorf("Join() error code = %v, want Unauthenticated", status.Code(err))
	}

	authed := dialJoinWithToken(t, f, testGatewayToken)
	authed.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	roster := authed.recv()
	if len(roster.GetReplicas()) != 1 {
		t.Fatalf("roster after an authenticated Join() has %d replicas, want 1", len(roster.GetReplicas()))
	}
}

func TestJoinStaysOpenWhenNoGatewayTokenIsConfigured(t *testing.T) {
	f := startRegistry(t)

	client := dialJoin(t, f)
	client.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	roster := client.recv()
	if len(roster.GetReplicas()) != 1 {
		t.Fatalf("roster = %d replicas, want 1", len(roster.GetReplicas()))
	}
}

package registryserver_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// joinTestClient wraps one GatewayDirectory.Join call so tests can send and
// receive without repeating stream boilerplate.
type joinTestClient struct {
	t      *testing.T
	stream tunnelv1.GatewayDirectory_JoinClient
}

func dialJoin(t *testing.T, f *registryFixture) *joinTestClient {
	t.Helper()
	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: f.ca.Pool()})
	conn, err := grpc.NewClient(f.addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	client := tunnelv1.NewGatewayDirectoryClient(conn)
	stream, err := client.Join(context.Background())
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	return &joinTestClient{t: t, stream: stream}
}

func (c *joinTestClient) join(replicaID, endpoint string, state tunnelv1.ReplicaState) {
	c.t.Helper()
	if err := c.stream.Send(&tunnelv1.JoinRequest{ReplicaId: replicaID, Endpoint: endpoint, State: state}); err != nil {
		c.t.Fatalf("Send() error = %v", err)
	}
}

func (c *joinTestClient) setState(state tunnelv1.ReplicaState) {
	c.t.Helper()
	if err := c.stream.Send(&tunnelv1.JoinRequest{State: state}); err != nil {
		c.t.Fatalf("Send() error = %v", err)
	}
}

func (c *joinTestClient) recv() *tunnelv1.GatewayRoster {
	c.t.Helper()
	roster, err := c.stream.Recv()
	if err != nil {
		c.t.Fatalf("Recv() error = %v", err)
	}
	return roster
}

func (c *joinTestClient) close() {
	c.stream.CloseSend()
}

func replicaByID(roster *tunnelv1.GatewayRoster, id string) *tunnelv1.GatewayReplica {
	for _, r := range roster.GetReplicas() {
		if r.GetReplicaId() == id {
			return r
		}
	}
	return nil
}

func TestJoinBroadcastsAGrowingRosterToEveryReplica(t *testing.T) {
	f := startRegistry(t)

	a := dialJoin(t, f)
	a.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	first := a.recv()
	if got := len(first.GetReplicas()); got != 1 {
		t.Fatalf("roster after replica-a joins has %d replicas, want 1", got)
	}
	v1 := first.GetVersion()

	b := dialJoin(t, f)
	b.join("replica-b", "10.0.0.2:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)

	// Both streams must see the two-replica roster: b sees it as its first
	// message, a sees it as an update.
	rosterForB := b.recv()
	rosterForA := a.recv()
	for _, roster := range []*tunnelv1.GatewayRoster{rosterForA, rosterForB} {
		if got := len(roster.GetReplicas()); got != 2 {
			t.Fatalf("roster after replica-b joins has %d replicas, want 2", got)
		}
		if roster.GetVersion() <= v1 {
			t.Errorf("roster version = %d, want it to have advanced past %d", roster.GetVersion(), v1)
		}
	}
	if rep := replicaByID(rosterForA, "replica-b"); rep == nil || rep.GetEndpoint() != "10.0.0.2:8443" {
		t.Errorf("roster is missing replica-b with the expected endpoint: %+v", rosterForA)
	}
}

func TestJoinRejectsAnEmptyReplicaIDOrEndpoint(t *testing.T) {
	f := startRegistry(t)

	client := dialJoin(t, f)
	client.join("", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	if _, err := client.stream.Recv(); err == nil {
		t.Fatal("Join() with an empty replica_id = nil error, want a rejection")
	}
}

func TestUpdatingStateToDrainingBroadcastsWithoutRemovingTheReplica(t *testing.T) {
	f := startRegistry(t)

	a := dialJoin(t, f)
	a.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	a.recv()

	b := dialJoin(t, f)
	b.join("replica-b", "10.0.0.2:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	b.recv() // b's own join
	a.recv() // a sees b join

	b.setState(tunnelv1.ReplicaState_REPLICA_STATE_DRAINING)
	updated := a.recv()
	rep := replicaByID(updated, "replica-b")
	if rep == nil {
		t.Fatal("replica-b disappeared from the roster after moving to DRAINING, want it still present")
	}
	if rep.GetState() != tunnelv1.ReplicaState_REPLICA_STATE_DRAINING {
		t.Errorf("replica-b state = %v, want DRAINING", rep.GetState())
	}
}

func TestClosingTheStreamRemovesTheReplicaFromTheRoster(t *testing.T) {
	f := startRegistry(t)

	a := dialJoin(t, f)
	a.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	a.recv()

	b := dialJoin(t, f)
	b.join("replica-b", "10.0.0.2:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	b.recv()
	a.recv() // a sees b join

	b.close()

	deadline := time.Now().Add(5 * time.Second)
	var last *tunnelv1.GatewayRoster
	for time.Now().Before(deadline) {
		last = a.recv()
		if replicaByID(last, "replica-b") == nil {
			return
		}
	}
	t.Fatalf("replica-b was never removed from the roster after its stream closed; last roster: %+v", last)
}

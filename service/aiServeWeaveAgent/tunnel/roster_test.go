package tunnel_test

import (
	"log/slog"
	"strconv"
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// The roster tests are pure: no tunnels, no clock, no fakes. Every rule they
// check is a decision the connection table then acts on, so getting them
// wrong is the difference between a control-plane wobble costing nothing and
// costing the whole node.

func testRoster(t *testing.T, maxGateways int) *tunnel.Roster {
	t.Helper()
	return tunnel.NewRoster(maxGateways, slog.New(slog.DiscardHandler))
}

// replicas builds a roster message from "id@endpoint" pairs, all active.
func replicas(version int64, specs ...string) *tunnelv1.GatewayRoster {
	pb := &tunnelv1.GatewayRoster{Version: version}
	for _, spec := range specs {
		id, endpoint := splitSpec(spec)
		pb.Replicas = append(pb.Replicas, &tunnelv1.GatewayReplica{
			ReplicaId: id,
			Endpoint:  endpoint,
			State:     tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE,
		})
	}
	return pb
}

func splitSpec(spec string) (id, endpoint string) {
	for i := range spec {
		if spec[i] == '@' {
			return spec[:i], spec[i+1:]
		}
	}
	return spec, spec
}

func endpointsOf(entries []tunnel.RosterEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Endpoint)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRosterSeedsThenYieldsToTheFirstBroadcast(t *testing.T) {
	r := testRoster(t, 0)
	r.Seed([]string{"gw-1.example.com:8443", "gw-2.example.com:8443"})

	if got, want := endpointsOf(r.Entries()), []string{"gw-1.example.com:8443", "gw-2.example.com:8443"}; !equalStrings(got, want) {
		t.Fatalf("seeded endpoints = %v, want %v", got, want)
	}
	if _, accepted := r.Version(); accepted {
		t.Error("seeds were reported as an accepted roster; the first broadcast must replace them whatever its version")
	}

	// The roster is authoritative from the first broadcast: a seed it does
	// not list is not a replica, it was only a way in.
	if !r.Apply(replicas(1, "gw-2@gw-2.example.com:8443", "gw-3@gw-3.example.com:8443")) {
		t.Fatal("the first roster was rejected")
	}
	if got, want := endpointsOf(r.Entries()), []string{"gw-2.example.com:8443", "gw-3.example.com:8443"}; !equalStrings(got, want) {
		t.Errorf("endpoints after the first roster = %v, want %v", got, want)
	}
}

func TestRosterSkipsInvalidSeeds(t *testing.T) {
	r := testRoster(t, 0)
	r.Seed([]string{"https://gw-1.example.com:8443", "gw-2.example.com:8443", "gw-3.example.com"})

	// A URL and a port-less host are configuration errors, but one bad entry
	// must not cost the reachable one.
	if got, want := endpointsOf(r.Entries()), []string{"gw-2.example.com:8443"}; !equalStrings(got, want) {
		t.Errorf("seeded endpoints = %v, want %v", got, want)
	}
}

func TestRosterIgnoresAnOlderVersion(t *testing.T) {
	r := testRoster(t, 0)
	if !r.Apply(replicas(7, "gw-1@gw-1.example.com:8443", "gw-2@gw-2.example.com:8443")) {
		t.Fatal("version 7 was rejected")
	}
	if !r.Apply(replicas(8, "gw-1@gw-1.example.com:8443")) {
		t.Fatal("version 8 was rejected")
	}

	tests := []struct {
		name    string
		version int64
	}{
		{name: "an older version", version: 7},
		{name: "the same version again", version: 8},
		{name: "version zero", version: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicas broadcast independently, so a stale frame arriving
			// after a newer one is routine. Applying it would bring gw-2
			// back from the dead.
			if r.Apply(replicas(tt.version, "gw-1@gw-1.example.com:8443", "gw-2@gw-2.example.com:8443")) {
				t.Fatalf("version %d was accepted over version 8", tt.version)
			}
			if got, want := endpointsOf(r.Entries()), []string{"gw-1.example.com:8443"}; !equalStrings(got, want) {
				t.Errorf("endpoints = %v, want %v: a removed replica came back", got, want)
			}
		})
	}
}

func TestRosterKeepsTheLastValidOneWhenDegraded(t *testing.T) {
	good := []string{"gw-1.example.com:8443", "gw-2.example.com:8443"}

	tests := []struct {
		name string
		next *tunnelv1.GatewayRoster
	}{
		{name: "a nil roster", next: nil},
		{name: "no replicas at all", next: &tunnelv1.GatewayRoster{Version: 9}},
		{
			name: "every entry invalid",
			next: &tunnelv1.GatewayRoster{Version: 9, Replicas: []*tunnelv1.GatewayReplica{
				{ReplicaId: "gw-3", Endpoint: "http://gw-3.example.com:8443", State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
				{ReplicaId: "", Endpoint: "gw-4.example.com:8443", State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
				{ReplicaId: "gw-5", Endpoint: "gw-5.example.com:8443"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRoster(t, 0)
			if !r.Apply(replicas(8, "gw-1@gw-1.example.com:8443", "gw-2@gw-2.example.com:8443")) {
				t.Fatal("the initial roster was rejected")
			}

			// Emptying the connection table on a control-plane wobble would
			// take the node fully offline; the last good roster stays.
			if r.Apply(tt.next) {
				t.Fatal("a degraded roster was accepted")
			}
			if got := endpointsOf(r.Entries()); !equalStrings(got, good) {
				t.Errorf("endpoints = %v, want %v", got, good)
			}
			if version, _ := r.Version(); version != 8 {
				t.Errorf("version = %d, want %d", version, 8)
			}
		})
	}
}

func TestRosterKeepsTheValidEntriesOfAPartlyBadRoster(t *testing.T) {
	r := testRoster(t, 0)
	pb := &tunnelv1.GatewayRoster{Version: 3, Replicas: []*tunnelv1.GatewayReplica{
		{ReplicaId: "gw-1", Endpoint: "gw-1.example.com:8443", State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
		{ReplicaId: "gw-2", Endpoint: "gw-2.example.com:8443/path", State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
	}}

	if !r.Apply(pb) {
		t.Fatal("a roster with one usable entry was rejected")
	}
	if got, want := endpointsOf(r.Entries()), []string{"gw-1.example.com:8443"}; !equalStrings(got, want) {
		t.Errorf("endpoints = %v, want %v", got, want)
	}
}

func TestRosterTruncatesToMaxGateways(t *testing.T) {
	r := testRoster(t, 3)

	specs := make([]string, 0, 20)
	for i := range 20 {
		n := strconv.Itoa(i + 10) // two digits, so sorting by endpoint is unsurprising
		specs = append(specs, "gw-"+n+"@gw-"+n+".example.com:8443")
	}
	if !r.Apply(replicas(1, specs...)) {
		t.Fatal("an oversized roster was rejected outright; it must be truncated instead")
	}

	entries := r.Entries()
	if len(entries) != 3 {
		t.Fatalf("replicas = %d, want %d: max_gateways is what keeps a poisoned roster from exhausting the file descriptors", len(entries), 3)
	}
	// Truncation is deterministic, so a roster that arrives twice does not
	// churn the connection table.
	want := []string{"gw-10.example.com:8443", "gw-11.example.com:8443", "gw-12.example.com:8443"}
	if got := endpointsOf(entries); !equalStrings(got, want) {
		t.Errorf("kept = %v, want %v", got, want)
	}
}

func TestRosterActiveCount(t *testing.T) {
	tests := []struct {
		name   string
		states []tunnelv1.ReplicaState
		want   int
	}{
		{
			name:   "all active",
			states: []tunnelv1.ReplicaState{1, 1, 1},
			want:   3,
		},
		{
			name:   "draining replicas do not take a share",
			states: []tunnelv1.ReplicaState{1, 2, 1},
			want:   2,
		},
		{
			name:   "removed replicas do not take a share",
			states: []tunnelv1.ReplicaState{1, 3, 3},
			want:   1,
		},
		{
			name:   "no active replica still reports one",
			states: []tunnelv1.ReplicaState{2, 3},
			want:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testRoster(t, 0)
			pb := &tunnelv1.GatewayRoster{Version: 1}
			for i, state := range tt.states {
				n := strconv.Itoa(i + 1)
				pb.Replicas = append(pb.Replicas, &tunnelv1.GatewayReplica{
					ReplicaId: "gw-" + n,
					Endpoint:  "gw-" + n + ".example.com:8443",
					State:     state,
				})
			}
			if !r.Apply(pb) {
				t.Fatal("the roster was rejected")
			}
			if got := r.ActiveCount(); got != tt.want {
				t.Errorf("ActiveCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

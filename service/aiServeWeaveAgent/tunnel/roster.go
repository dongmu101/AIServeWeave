package tunnel

import (
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"sync"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// This file holds the Gateway roster: the authoritative list of replicas the
// Registry maintains and every replica broadcasts on its own Control stream.
// The Agent opens and closes tunnels to match it, which is what lets the
// Gateway be scaled up or down without anyone touching an Agent.
//
// The roster is trusted input — it arrives over a mutually authenticated
// stream — but it is still validated. A malformed or poisoned roster must not
// be able to point this Agent at an arbitrary address or exhaust a home Mac's
// file descriptors, so every endpoint is checked and the list is truncated to
// max_gateways.
//
// Two degradation rules matter more than they look:
//
//   - An empty or wholly invalid roster is ignored, not applied. Treating it
//     as "there are no replicas" would take the node fully offline the first
//     time the Registry hiccups.
//   - A version that is not newer is ignored. Replicas broadcast
//     independently, so an older frame can arrive after a newer one; applying
//     it would resurrect a replica that was just removed.

// rosterOperation is the Operation recorded on roster errors.
const rosterOperation = "tunnel_roster"

// defaultMaxGateways bounds the connection table, per README.md.
const defaultMaxGateways = 16

// RosterEntry is one Gateway replica as the roster describes it.
type RosterEntry struct {
	// ReplicaID is the replica's identity, as it also reports in HelloAck. It
	// is empty for a seed endpoint, which is known by address alone.
	ReplicaID string
	// Endpoint is the replica's host:port. It is the connection table's key:
	// it is the only field present both before and after a handshake, and it
	// is what actually gets dialled.
	Endpoint string
	// State says whether to keep dispatching to this replica, let it drain,
	// or close the tunnel.
	State tunnelv1.ReplicaState
}

// Roster is the last accepted view of the replica set. It is safe for
// concurrent use: rosters arrive on every tunnel's Control stream at once.
type Roster struct {
	maxGateways int
	logger      *slog.Logger

	mu      sync.Mutex
	version int64
	// accepted distinguishes "no roster yet, running on seeds" from "a roster
	// with version 0", so the first broadcast is never mistaken for a
	// duplicate.
	accepted bool
	entries  map[string]RosterEntry
}

// NewRoster returns an empty roster. A maxGateways of zero takes the default
// of 16; a negative value means no limit, which is only sensible on a host
// that is not file-descriptor bound.
func NewRoster(maxGateways int, logger *slog.Logger) *Roster {
	if maxGateways == 0 {
		maxGateways = defaultMaxGateways
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Roster{
		maxGateways: maxGateways,
		logger:      logger,
		entries:     map[string]RosterEntry{},
	}
}

// Seed installs the configured seed endpoints as the starting connection set.
// They are treated as active but carry no version, so the first real roster
// replaces them whatever its version — including a seed that the roster does
// not list, which is then closed. The seed list only has to get the Agent to
// one reachable replica; the roster is authoritative from then on.
func (r *Roster) Seed(endpoints []string) {
	entries := make(map[string]RosterEntry, len(endpoints))
	for _, endpoint := range endpoints {
		if err := validateEndpoint("gateway_endpoint", endpoint); err != nil {
			r.logger.Error("ignoring an invalid seed endpoint", slog.String("error", err.Error()))
			continue
		}
		entries[endpoint] = RosterEntry{
			Endpoint: endpoint,
			State:    tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE,
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accepted {
		// A roster is already in force; seeds are history.
		return
	}
	r.entries = r.truncateLocked(entries)
}

// Apply takes one broadcast roster, reporting whether it was accepted. A
// rejected roster changes nothing: the previous one stays in force.
func (r *Roster) Apply(pb *tunnelv1.GatewayRoster) bool {
	if pb == nil {
		return false
	}
	version := pb.GetVersion()

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.accepted && version <= r.version {
		// Not an error: every replica broadcasts the same roster, so all but
		// the first copy of a version lands here.
		r.logger.Debug("ignoring a roster that is not newer",
			slog.Int64("version", version), slog.Int64("current", r.version))
		return false
	}

	entries := make(map[string]RosterEntry, len(pb.GetReplicas()))
	for _, replica := range pb.GetReplicas() {
		entry, err := validateReplica(replica)
		if err != nil {
			// One bad row must not cost the whole roster: the other replicas
			// in it are still reachable and still authoritative.
			r.logger.Error("skipping an invalid roster entry",
				slog.Int64("version", version),
				slog.String("replica_id", replica.GetReplicaId()),
				slog.String("error", err.Error()))
			continue
		}
		entries[entry.Endpoint] = entry
	}

	if len(entries) == 0 {
		// Either the Registry sent nothing or every row was unusable. Keeping
		// the last good roster is what stops a control-plane wobble from
		// taking the whole node out of service.
		r.logger.Warn("ignoring an empty roster; keeping the last valid one",
			slog.Int64("version", version), slog.Int("known_replicas", len(r.entries)))
		return false
	}

	r.entries = r.truncateLocked(entries)
	r.version = version
	r.accepted = true
	return true
}

// truncateLocked enforces max_gateways. Truncation is a safety valve against
// a misconfigured or poisoned roster, not a scheduling policy, so it keeps a
// deterministic subset — sorted by endpoint — and says loudly that it did.
func (r *Roster) truncateLocked(entries map[string]RosterEntry) map[string]RosterEntry {
	if r.maxGateways <= 0 || len(entries) <= r.maxGateways {
		return entries
	}

	endpoints := make([]string, 0, len(entries))
	for endpoint := range entries {
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)

	kept := make(map[string]RosterEntry, r.maxGateways)
	for _, endpoint := range endpoints[:r.maxGateways] {
		kept[endpoint] = entries[endpoint]
	}
	r.logger.Error("the roster exceeds max_gateways; truncating",
		slog.Int("replicas", len(entries)),
		slog.Int("max_gateways", r.maxGateways),
		slog.Int("dropped", len(entries)-r.maxGateways))
	return kept
}

// Entries returns the current replica set, sorted by endpoint so a caller
// diffing it twice sees a stable order.
func (r *Roster) Entries() []RosterEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]RosterEntry, 0, len(r.entries))
	for _, entry := range r.entries {
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b RosterEntry) int {
		switch {
		case a.Endpoint < b.Endpoint:
			return -1
		case a.Endpoint > b.Endpoint:
			return 1
		default:
			return 0
		}
	})
	return out
}

// Version reports the version currently in force, and whether any roster has
// been accepted at all.
func (r *Roster) Version() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version, r.accepted
}

// ActiveCount reports how many replicas are dispatching, which is the divisor
// in the per-replica slot share. Draining and removed replicas send no new
// requests, so counting them would shrink every other replica's ceiling for
// no reason. It never reports zero: a node with no active replica still needs
// a defined share for the moment one comes back.
func (r *Roster) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := 0
	for _, entry := range r.entries {
		if entry.State == tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE {
			n++
		}
	}
	return max(n, 1)
}

// validateReplica checks one roster row. The endpoint check is the same
// validateEndpoint the configuration uses, deliberately: a roster entry and a
// configured endpoint are dialled by exactly the same code, so they must be
// held to exactly the same rule.
func validateReplica(pb *tunnelv1.GatewayReplica) (RosterEntry, error) {
	if pb.GetReplicaId() == "" {
		return RosterEntry{}, &runtimeConfigError{"replica_id is required"}
	}
	if err := validateEndpoint("endpoint", pb.GetEndpoint()); err != nil {
		return RosterEntry{}, err
	}
	state := pb.GetState()
	if _, known := tunnelv1.ReplicaState_name[int32(state)]; !known {
		return RosterEntry{}, &runtimeConfigError{"unknown replica state " + strconv.Itoa(int(state))}
	}
	if state == tunnelv1.ReplicaState_REPLICA_STATE_UNSPECIFIED {
		return RosterEntry{}, &runtimeConfigError{"replica state is unspecified"}
	}
	return RosterEntry{
		ReplicaID: pb.GetReplicaId(),
		Endpoint:  pb.GetEndpoint(),
		State:     state,
	}, nil
}

// runtimeConfigError is a bare validation failure. Roster rows are rejected
// individually and only ever logged, so they need a message and nothing else
// of RuntimeError's machinery.
type runtimeConfigError struct{ msg string }

func (e *runtimeConfigError) Error() string { return e.msg }

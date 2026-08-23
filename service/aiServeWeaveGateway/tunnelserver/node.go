package tunnelserver

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// node is everything this replica knows about one connected Agent: its Control
// streams, its parked slots, and the runtime inventory it last reported.
//
// A node entry is keyed by node_id and shared by every stream that
// authenticated as that node, because slots and Control arrive as separate
// gRPC streams with no ordering between them. It is dropped only once the last
// of those streams is gone.
type node struct {
	id  string
	srv *Server

	mu sync.Mutex

	// controls holds the live Control streams. There is normally exactly
	// one; a second appears briefly when an Agent reconnects before this
	// replica has noticed the old stream die, and both are kept so a
	// broadcast reaches whichever one is actually alive.
	controls map[*controlSession]struct{}

	agentVersion string
	resources    *tunnelv1.NodeResources
	// runtimeIDs is the allowlist the Agent declared in Hello. It exists so
	// the scheduler avoids dispatching to a runtime the Agent would reject
	// anyway; it is a shortcut, never the enforcement point, which stays on
	// the Agent.
	runtimeIDs []string

	// snapshots is the runtime inventory, keyed by runtime_id. A report with
	// full set replaces the map wholesale; an incremental report merges, so
	// an instance that stopped changing is not forgotten between full
	// reconciliations.
	snapshots map[string]runtime.Snapshot

	lastHeartbeat time.Time
	inflight      int
	reportedIdle  int
	draining      bool

	// idle holds parked slots per class, most recently parked last. Reuse is
	// LIFO because a slot that just finished a request is the one most likely
	// to still have a warm TCP window and an unexpired NAT mapping.
	idle map[tunnelv1.SlotClass][]*slot
	// live counts every open slot stream, parked or busy, so the node entry
	// is not dropped while a request is still running on it.
	live int
}

func newNode(id string, srv *Server) *node {
	return &node{
		id:        id,
		srv:       srv,
		controls:  make(map[*controlSession]struct{}),
		snapshots: make(map[string]runtime.Snapshot),
		idle:      make(map[tunnelv1.SlotClass][]*slot),
	}
}

// NodeInfo is a point-in-time view of one connected node, for the scheduler.
// It is a copy: holding one never blocks the tunnel.
type NodeInfo struct {
	NodeID       string
	AgentVersion string
	Resources    *tunnelv1.NodeResources
	// RuntimeIDs is the Agent's declared allowlist.
	RuntimeIDs []string
	// Runtimes is the last reported inventory, sorted by runtime_id so two
	// consecutive reads of an unchanged node compare equal.
	Runtimes []runtime.Snapshot
	// IdleSlots counts slots parked and ready to take a request right now,
	// per class. It is the only honest measure of spare capacity on this
	// link: the Agent's heartbeat figure is a snapshot from its side and is
	// already stale by the time it is read.
	IdleSlots map[tunnelv1.SlotClass]int
	// InflightRequests is what the Agent last reported across all replicas,
	// not just this one.
	InflightRequests int
	LastHeartbeat    time.Time
	// Draining is set once the Agent has announced it is shutting down. A
	// draining node must not be given new work, but its in-flight requests
	// are still running.
	Draining bool
	// Live reports whether a Control stream is established and the last
	// heartbeat is recent enough.
	Live bool
}

// Nodes returns a view of every node connected to this replica, sorted by
// node_id.
func (s *Server) Nodes() []NodeInfo {
	s.mu.RLock()
	nodes := make([]*node, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodes = append(nodes, n)
	}
	s.mu.RUnlock()

	now := s.clock.Now()
	infos := make([]NodeInfo, 0, len(nodes))
	for _, n := range nodes {
		infos = append(infos, n.info(now, s.cfg.HeartbeatTimeout))
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].NodeID < infos[j].NodeID })
	return infos
}

// Node returns the view of one node, or false when this replica holds no
// tunnel to it.
func (s *Server) Node(nodeID string) (NodeInfo, bool) {
	n, ok := s.lookup(nodeID)
	if !ok {
		return NodeInfo{}, false
	}
	return n.info(s.clock.Now(), s.cfg.HeartbeatTimeout), true
}

func (n *node) info(now time.Time, heartbeatTimeout time.Duration) NodeInfo {
	n.mu.Lock()
	defer n.mu.Unlock()

	runtimes := make([]runtime.Snapshot, 0, len(n.snapshots))
	for _, snap := range n.snapshots {
		runtimes = append(runtimes, snap)
	}
	sort.Slice(runtimes, func(i, j int) bool {
		return runtimes[i].Descriptor.ID < runtimes[j].Descriptor.ID
	})

	idle := make(map[tunnelv1.SlotClass]int, len(n.idle))
	for class, slots := range n.idle {
		if len(slots) > 0 {
			idle[class] = len(slots)
		}
	}

	// A node with no Control stream is not live no matter how recent its
	// last heartbeat was: without Control there is nothing to send it a
	// roster or a shutdown, and its slots are about to follow.
	live := len(n.controls) > 0 && !n.draining &&
		!n.lastHeartbeat.IsZero() && now.Sub(n.lastHeartbeat) <= heartbeatTimeout

	return NodeInfo{
		NodeID:           n.id,
		AgentVersion:     n.agentVersion,
		Resources:        n.resources,
		RuntimeIDs:       append([]string(nil), n.runtimeIDs...),
		Runtimes:         runtimes,
		IdleSlots:        idle,
		InflightRequests: n.inflight,
		LastHeartbeat:    n.lastHeartbeat,
		Draining:         n.draining,
		Live:             live,
	}
}

// isEmpty reports whether the entry holds neither a Control stream nor a slot,
// and may therefore be forgotten. Callers must hold no node lock.
func (n *node) isEmpty() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.controls) == 0 && n.live == 0
}

// broadcast sends frame on every Control stream of this node. A stream that
// fails is left alone: its own reader will notice and tear it down, and
// duplicating that teardown here would race with it.
func (n *node) broadcast(frame *tunnelv1.GatewayControl) {
	n.mu.Lock()
	sessions := make([]*controlSession, 0, len(n.controls))
	for s := range n.controls {
		sessions = append(sessions, s)
	}
	n.mu.Unlock()

	for _, s := range sessions {
		if err := s.send(frame); err != nil {
			n.srv.logger.Debug("control broadcast failed",
				slog.String("node_id", n.id), slog.String("error", err.Error()))
		}
	}
}

// applyStatus merges a RuntimeStatus report into the inventory.
func (n *node) applyStatus(status *tunnelv1.RuntimeStatus, snaps []runtime.Snapshot) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if status.GetFull() {
		n.snapshots = make(map[string]runtime.Snapshot, len(snaps))
	}
	for _, snap := range snaps {
		n.snapshots[snap.Descriptor.ID] = snap
	}
}

// park puts a slot back into the idle set. It reports false when the node is
// draining or the slot is already dead, in which case the caller closes the
// slot rather than offering work to something on its way out.
func (n *node) park(s *slot) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.draining {
		return false
	}
	n.idle[s.class] = append(n.idle[s.class], s)
	return true
}

// acquire takes one parked slot of the given class, or returns nil when there
// is none. It never blocks and never queues: an empty idle set is backpressure,
// and the answer to backpressure is another node, not a wait.
func (n *node) acquire(class tunnelv1.SlotClass) *slot {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.draining {
		return nil
	}
	stack := n.idle[class]
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s.take() {
			n.idle[class] = stack
			return s
		}
		// The slot died while parked; drop it and try the next one.
	}
	n.idle[class] = stack
	return nil
}

// unpark removes a slot from the idle set, used when its stream ends while it
// was parked.
func (n *node) unpark(s *slot) {
	n.mu.Lock()
	defer n.mu.Unlock()
	stack := n.idle[s.class]
	for i, cur := range stack {
		if cur == s {
			n.idle[s.class] = append(stack[:i], stack[i+1:]...)
			return
		}
	}
}

// durationProto converts a Go duration to its proto form, mapping a
// non-positive duration to an unset field so "no grace period" and "the caller
// did not say" look the same on the wire.
func durationProto(d time.Duration) *durationpb.Duration {
	if d <= 0 {
		return nil
	}
	return durationpb.New(d)
}

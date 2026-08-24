package tunnelserver

import (
	"log/slog"
	"maps"
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
	id      string
	srv     *Server
	metrics *recorder

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
	// labels are what the Agent declared in Hello. They are taken at face
	// value and must never decide authorization — see the proto's note on
	// Hello.labels.
	//
	// labels 是 Agent 在 Hello 中声明的内容。它们被原样采信，且绝不能参与授权判断
	// ——见 proto 中对 Hello.labels 的说明。
	labels map[string]string

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
	// liveByClass is the same count split per class, which is what the slot
	// occupancy gauge needs: busy is what is open minus what is parked, and
	// that subtraction is only meaningful within one class.
	//
	// liveByClass 是同一个计数按 class 拆开的结果，这正是槽位占用量表所需要的：
	// busy 等于已打开减去已停放，而这个减法只在同一个 class 内才有意义。
	liveByClass map[tunnelv1.SlotClass]int
}

func newNode(id string, srv *Server) *node {
	return &node{
		id:          id,
		srv:         srv,
		metrics:     srv.metrics.forNode(id),
		controls:    make(map[*controlSession]struct{}),
		snapshots:   make(map[string]runtime.Snapshot),
		idle:        make(map[tunnelv1.SlotClass][]*slot),
		liveByClass: make(map[tunnelv1.SlotClass]int),
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
	// Labels are the operator-assigned facts the Agent declared, which routing
	// rules select on. They are a preference about where work should go, never
	// permission to receive it: a compromised Agent could claim any label.
	//
	// Labels 是 Agent 声明的、由运维赋予的事实，路由规则据此选择。它们表达的是「工作
	// 应该去哪」的偏好，绝不是「有权接收工作」：被攻破的 Agent 可以声称任何标签。
	Labels map[string]string
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
		Labels:           maps.Clone(n.labels),
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
	if n.draining {
		n.mu.Unlock()
		return false
	}
	n.idle[s.class] = append(n.idle[s.class], s)
	idle, busy := n.occupancyLocked(s.class)
	n.mu.Unlock()

	n.metrics.Slots(s.class, idle, busy)
	return true
}

// acquire takes one parked slot of the given class, or returns nil when there
// is none. It never blocks and never queues: an empty idle set is backpressure,
// and the answer to backpressure is another node, not a wait.
func (n *node) acquire(class tunnelv1.SlotClass) *slot {
	n.mu.Lock()
	if n.draining {
		n.mu.Unlock()
		return nil
	}
	var taken *slot
	stack := n.idle[class]
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s.take() {
			taken = s
			break
		}
		// The slot died while parked; drop it and try the next one.
	}
	n.idle[class] = stack
	idle, busy := n.occupancyLocked(class)
	n.mu.Unlock()

	n.metrics.Slots(class, idle, busy)
	return taken
}

// unpark removes a slot from the idle set, used when its stream ends while it
// was parked.
func (n *node) unpark(s *slot) {
	n.mu.Lock()
	stack := n.idle[s.class]
	for i, cur := range stack {
		if cur == s {
			n.idle[s.class] = append(stack[:i], stack[i+1:]...)
			break
		}
	}
	idle, busy := n.occupancyLocked(s.class)
	n.mu.Unlock()

	n.metrics.Slots(s.class, idle, busy)
}

// addLive adjusts the open-slot counts by delta and republishes the class's
// occupancy. It is how a slot stream's arrival and departure reach the gauge:
// opening a stream changes what is open without changing what is parked, and
// only reporting the park would leave busy overstated for the life of the
// stream.
//
// addLive 按 delta 调整已打开槽的计数，并重新发布该 class 的占用情况。槽流的到来
// 与离开正是这样传到量表上的：打开一条流会改变「已打开」而不改变「已停放」，若只
// 上报停放动作，busy 在该流的整个生命周期里都会偏高。
func (n *node) addLive(class tunnelv1.SlotClass, delta int) {
	n.mu.Lock()
	n.live += delta
	n.liveByClass[class] += delta
	if n.liveByClass[class] <= 0 {
		delete(n.liveByClass, class)
	}
	idle, busy := n.occupancyLocked(class)
	n.mu.Unlock()

	n.metrics.Slots(class, idle, busy)
}

// occupancyLocked returns one class's parked and busy slot counts. Callers
// must hold n.mu.
//
// occupancyLocked 返回某个 class 已停放与忙碌的槽数。调用方必须持有 n.mu。
func (n *node) occupancyLocked(class tunnelv1.SlotClass) (idle, busy int) {
	idle = len(n.idle[class])
	busy = n.liveByClass[class] - idle
	if busy < 0 {
		// A slot removed from the idle set before its stream count caught up
		// would otherwise report a negative gauge, which reads as a defect in
		// whoever consumes it rather than in the moment it describes.
		//
		// 一个先于流计数更新就被移出空闲集合的槽，否则会让量表出现负值——那读起来
		// 像是消费方的缺陷，而不是它所描述的那个瞬间。
		busy = 0
	}
	return idle, busy
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

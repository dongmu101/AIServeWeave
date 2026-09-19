package tunnelserver

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/common/modelpullstatus"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
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
	// modelPulls is this node's last-reported model pull status, keyed by
	// name (STATUS.md's P2 model distribution subtask 2). Unlike snapshots
	// this is always replaced wholesale on every report: the Agent's
	// manifest is small and operator-authored, so there is no incremental-
	// merge case worth the complexity RuntimeStatus's full/partial split
	// carries.
	//
	// modelPulls 是该节点最后一次上报的模型拉取状态，按名字索引（STATUS.md
	// P2 模型分发子任务二）。与 snapshots 不同，它每次上报都整份替换：
	// Agent 的清单是运维手写的、体量有限，不值得为它承担 RuntimeStatus
	// full/partial 拆分那份复杂度。
	modelPulls map[string]modelpullstatus.Status
	// comfyUIManaged is this node's last-reported Managed ComfyUI container
	// status, keyed by container name (STATUS.md's P2 ComfyUI Managed
	// Docker deployment subtask 2). Like modelPulls, it is always replaced
	// wholesale on every report — an Agent manages at most one Managed
	// instance today, so there is nothing to merge incrementally.
	//
	// comfyUIManaged 是该节点最后一次上报的 Managed ComfyUI 容器状态，按容
	// 器名索引（STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务二）。与
	// modelPulls 一样，它每次上报都整份替换——今天一个 Agent 最多管理一个
	// Managed 实例，没有什么需要增量合并。
	comfyUIManaged map[string]comfyuimanagedstatus.Status

	lastHeartbeat time.Time
	inflight      int
	reportedIdle  int
	draining      bool
	// maintenance is set from the Registry's GatewayRoster.maintenance_node_ids
	// (STATUS.md's P01), not by the Agent. Unlike draining it never closes
	// idle slots or the Control stream: a node under operator-forced
	// maintenance stays fully connected and finishes in-flight work, it is
	// merely excluded from new dispatch (scheduler.go).
	maintenance bool

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

	// revoked is closed exactly once, by kill, to interrupt every Control
	// stream's blocking Recv from outside its own read loop — the mechanism
	// STATUS.md's S03 needs so a disable takes effect against a connection
	// that is already open, not just against the next handshake attempt.
	revoked     chan struct{}
	revokedOnce sync.Once
}

func newNode(id string, srv *Server) *node {
	return &node{
		id:             id,
		srv:            srv,
		metrics:        srv.metrics.forNode(id),
		controls:       make(map[*controlSession]struct{}),
		snapshots:      make(map[string]runtime.Snapshot),
		modelPulls:     make(map[string]modelpullstatus.Status),
		comfyUIManaged: make(map[string]comfyuimanagedstatus.Status),
		idle:           make(map[tunnelv1.SlotClass][]*slot),
		liveByClass:    make(map[tunnelv1.SlotClass]int),
		revoked:        make(chan struct{}),
	}
}

// kill forcibly ends this node's connection: every open Control stream's read
// loop observes n.revoked closed and returns, which ends that Control RPC and
// closes the stream from this replica's side — an Agent whose node_id was
// just disabled cannot keep the link open by refusing to cooperate, unlike a
// graceful Draining notice, which only ever asks. Idle slots are closed the
// same way an Agent-announced drain closes them; a request already in flight
// on a busy slot is left to finish or fail on its own, the same restraint
// Server.Close already applies fleet-wide — severing a data-plane stream
// mid-request is a strictly worse failure mode for whoever is waiting on it
// than letting it run out.
//
// kill 强制结束该节点的连接：每一条打开的 Control 流的读循环观察到 n.revoked 被关闭后
// 返回，从而结束那次 Control RPC 并由本副本一侧关闭连接——一个刚被禁用节点的 Agent
// 无法通过拒绝配合来维持这条链路，这与只是请求的 Draining 通告不同。空闲槽的处理方式与
// Agent 主动宣告 draining 时一致；仍在处理中的忙碌槽不做打断，这与 Server.Close 在整个
// 副本范围内已经采用的克制一致——对正等待结果的一方来说，中途掐断数据面的流，是比让它
// 跑完更糟的失败方式。
func (n *node) kill() {
	n.revokedOnce.Do(func() {
		close(n.revoked)

		n.mu.Lock()
		n.draining = true
		idle := n.idle
		n.idle = make(map[tunnelv1.SlotClass][]*slot)
		n.mu.Unlock()

		for _, stack := range idle {
			for _, sl := range stack {
				sl.close(errors.New("node is disabled"))
			}
		}
	})
}

// setMaintenance records the Registry's current maintenance flag for this
// node_id (STATUS.md's P01). It is not idempotent-checked against the
// previous value: the caller (Server.SetRoster) already diffs the roster's
// set, and a redundant call here is a cheap no-op assignment either way.
func (n *node) setMaintenance(maintenance bool) {
	n.mu.Lock()
	n.maintenance = maintenance
	n.mu.Unlock()
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
	// Maintenance is set by an operator via the Registry (STATUS.md's P01),
	// not by the Agent. It excludes the node from new dispatch the same way
	// Draining does, but does not mean the node is on its way out: it stays
	// Live and its Control stream is untouched.
	Maintenance bool
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

// TriggerModelPull asks nodeID to start pulling names, if it holds an active
// Control stream to this replica (STATUS.md's P2 model distribution subtask
// 2). It returns an error otherwise — including when the node is connected
// only to a sibling replica — per the tunnelserver package's "no forwarding"
// boundary (README's 四条约束第四条): a caller that does not already know
// which replica a node is on has to be told by whatever tracks roster
// membership, the same limitation the admin listener's per-replica views
// already carry.
//
// A successful call only means the trigger reached the Agent over the wire;
// per GatewayControl_Config's existing precedent there is no dedicated ack,
// so whether a name was known to the Agent's manifest is only observable
// from the ModelPullReport that follows (ModelPullStatus, below).
func (s *Server) TriggerModelPull(nodeID string, names []string) error {
	n, ok := s.lookup(nodeID)
	if !ok {
		return fmt.Errorf("tunnelserver: node %q is not connected to this replica", nodeID)
	}
	n.mu.Lock()
	connected := len(n.controls) > 0
	n.mu.Unlock()
	if !connected {
		return fmt.Errorf("tunnelserver: node %q has no active control stream on this replica", nodeID)
	}
	n.broadcast(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_ModelPullTrigger{
		ModelPullTrigger: tunnelwire.ModelPullTriggerToProto(names),
	}})
	return nil
}

// ModelPullStatus returns this replica's last-known model pull status for
// nodeID, and whether the node is known to this replica at all. A known node
// with no reports yet (no manifest configured, Agent predates this feature,
// or no report has arrived) returns an empty, true result.
func (s *Server) ModelPullStatus(nodeID string) ([]modelpullstatus.Status, bool) {
	n, ok := s.lookup(nodeID)
	if !ok {
		return nil, false
	}
	return n.modelPullSnapshot(), true
}

// TriggerComfyUIManagedAction asks nodeID to apply action to its one
// locally-declared Managed ComfyUI instance, if it holds an active Control
// stream to this replica (STATUS.md's P2 ComfyUI Managed Docker deployment,
// subtask 2). It returns an error otherwise — including when the node is
// connected only to a sibling replica — the same "no forwarding" boundary
// TriggerModelPull observes.
//
// A successful call only means the action reached the Agent over the wire;
// per GatewayControl_ModelPullTrigger's existing precedent there is no
// dedicated ack, so its effect is only observable from the
// ComfyUIManagedReport that follows (ComfyUIManagedStatus, below).
func (s *Server) TriggerComfyUIManagedAction(nodeID string, action comfyuimanagedstatus.Action) error {
	n, ok := s.lookup(nodeID)
	if !ok {
		return fmt.Errorf("tunnelserver: node %q is not connected to this replica", nodeID)
	}
	n.mu.Lock()
	connected := len(n.controls) > 0
	n.mu.Unlock()
	if !connected {
		return fmt.Errorf("tunnelserver: node %q has no active control stream on this replica", nodeID)
	}
	n.broadcast(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_ComfyuiManagedAction{
		ComfyuiManagedAction: tunnelwire.ComfyUIManagedActionToProto(action),
	}})
	return nil
}

// TriggerComfyUIManagedCustomNodeInstall asks nodeID to install name — its
// own local allowlist decides whether name is known, never a URL this call
// carries — into its one locally-declared Managed ComfyUI instance's
// custom-nodes directory (STATUS.md's P2 ComfyUI Managed Docker deployment,
// subtask 4). Same "connected to this replica or refused" boundary as
// TriggerComfyUIManagedAction; same "no dedicated ack" contract — a
// ComfyUIManagedReport is how the caller observes what happened.
//
// TriggerComfyUIManagedCustomNodeInstall 要求 nodeID 把 name（是否已知由它
// 自己本地的允许列表决定，本调用从不携带 URL）安装进它本地已声明的那一个
// Managed ComfyUI 实例的自定义节点目录（STATUS.md 的 P2 ComfyUI Managed
// Docker 部署子任务四）。与 TriggerComfyUIManagedAction 同一"连到本副本才
// 转发，否则拒绝"边界；同一"不设专门 ack"约定——调用方经由
// ComfyUIManagedReport 观察发生了什么。
func (s *Server) TriggerComfyUIManagedCustomNodeInstall(nodeID, name string) error {
	n, ok := s.lookup(nodeID)
	if !ok {
		return fmt.Errorf("tunnelserver: node %q is not connected to this replica", nodeID)
	}
	n.mu.Lock()
	connected := len(n.controls) > 0
	n.mu.Unlock()
	if !connected {
		return fmt.Errorf("tunnelserver: node %q has no active control stream on this replica", nodeID)
	}
	n.broadcast(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_ComfyuiManagedCustomNodeInstall{
		ComfyuiManagedCustomNodeInstall: tunnelwire.ComfyUIManagedCustomNodeInstallTriggerToProto(name),
	}})
	return nil
}

// ComfyUIManagedStatus returns this replica's last-known Managed ComfyUI
// status for nodeID, and whether the node is known to this replica at all. A
// known node with no reports yet (Managed mode not configured, Agent
// predates this feature, or no report has arrived) returns an empty, true
// result.
func (s *Server) ComfyUIManagedStatus(nodeID string) ([]comfyuimanagedstatus.Status, bool) {
	n, ok := s.lookup(nodeID)
	if !ok {
		return nil, false
	}
	return n.comfyUIManagedSnapshot(), true
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
		Maintenance:      n.maintenance,
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

// applyModelPullReport replaces the node's model pull status wholesale with
// the Agent's latest report (STATUS.md's P2 model distribution subtask 2).
func (n *node) applyModelPullReport(statuses []modelpullstatus.Status) {
	n.mu.Lock()
	defer n.mu.Unlock()
	next := make(map[string]modelpullstatus.Status, len(statuses))
	for _, st := range statuses {
		next[st.Name] = st
	}
	n.modelPulls = next
}

// modelPullSnapshot returns this replica's last-known model pull status for
// the node, sorted by name.
func (n *node) modelPullSnapshot() []modelpullstatus.Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]modelpullstatus.Status, 0, len(n.modelPulls))
	for _, st := range n.modelPulls {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// applyComfyUIManagedReport replaces the node's Managed ComfyUI status
// wholesale with the Agent's latest report (STATUS.md's P2 ComfyUI Managed
// Docker deployment subtask 2).
func (n *node) applyComfyUIManagedReport(statuses []comfyuimanagedstatus.Status) {
	n.mu.Lock()
	defer n.mu.Unlock()
	next := make(map[string]comfyuimanagedstatus.Status, len(statuses))
	for _, st := range statuses {
		next[st.ContainerName] = st
	}
	n.comfyUIManaged = next
}

// comfyUIManagedSnapshot returns this replica's last-known Managed ComfyUI
// status for the node, sorted by container name.
func (n *node) comfyUIManagedSnapshot() []comfyuimanagedstatus.Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]comfyuimanagedstatus.Status, 0, len(n.comfyUIManaged))
	for _, st := range n.comfyUIManaged {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContainerName < out[j].ContainerName })
	return out
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

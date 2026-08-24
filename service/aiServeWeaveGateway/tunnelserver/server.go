// Package tunnelserver implements one Gateway replica's side of the mTLS gRPC
// tunnel. An Agent dials in and stays connected; this package terminates that
// connection, tracks what the node can serve, and hands the scheduler a way to
// run a request on it.
//
// A replica is self-sufficient by design: it never forwards a request to
// another replica, so every node the scheduler can see here is one this
// process holds a live tunnel to. The roster that tells Agents which replicas
// exist is authored by the Registry and only relayed here.
//
// Three rules from the tunnel design shape everything in this package:
//
//   - The node_id on a stream must equal the node_id in the client
//     certificate. A stream that disagrees is closed rather than trusted.
//   - No queueing. With no idle slot the dispatch call returns backpressure
//     immediately, so the scheduler places the request on another node instead
//     of holding a caller behind a queue this replica cannot bound.
//   - No unbounded buffering. Response frames are handed to the caller one at
//     a time; a slow reader stalls the tunnel's gRPC flow control, which is
//     what pushes back on the Agent.
package tunnelserver

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// Config configures a Server. Only ReplicaID is required; every other field
// has a working default so a test or a first deployment does not have to
// answer questions it has no opinion on yet.
type Config struct {
	// ReplicaID identifies this replica to Agents; it is echoed in HelloAck
	// and is what makes a node's logs and metrics attributable to one link.
	ReplicaID string

	// Logger receives connection lifecycle events. Nil discards them.
	Logger *slog.Logger

	// Clock supplies time. Nil uses the system clock; tests inject a fake so
	// heartbeat expiry is exercised without sleeping.
	Clock runtime.Clock

	// SlotHint is advice sent to every Agent right after HelloAck. The Agent
	// clamps it to its own configured limits, so this is a scheduling
	// preference, never a guarantee. Nil sends no hint.
	SlotHint *tunnelv1.SlotHint

	// HeartbeatTimeout is how long a node may go without a Heartbeat before
	// Nodes() stops reporting it as live. It must exceed the Agent's
	// heartbeat interval by enough to absorb one lost heartbeat. Zero uses
	// DefaultHeartbeatTimeout.
	HeartbeatTimeout time.Duration

	// MaxFrameBytes caps a single DataChunk this replica will accept from an
	// Agent. Zero uses DefaultMaxFrameBytes. It is a protocol guard, not a
	// tuning knob: a frame over the limit ends the request as a protocol
	// error rather than being buffered.
	MaxFrameBytes int

	// Metrics receives this replica's tunnel instruments, described by
	// Descriptions. Nil discards them, which is what a Gateway with no
	// metrics backend configured gets.
	//
	// Metrics 接收本副本的隧道仪器，其描述见 Descriptions。为 nil 时全部丢弃，
	// 这也是未配置指标后端的 Gateway 所得到的行为。
	Metrics runtime.Metrics
}

// Defaults for the optional Config fields.
const (
	// DefaultHeartbeatTimeout tolerates two missed heartbeats at the Agent's
	// default 15s interval before a node stops counting as live.
	DefaultHeartbeatTimeout = 45 * time.Second
	// DefaultMaxFrameBytes matches the Agent's default frame limit, so
	// neither side rejects a frame the other considered legal.
	DefaultMaxFrameBytes = 4 << 20
)

// ErrNoIdleSlot is the cause carried by the backpressure error Dispatch
// returns when a node holds no parked slot of the requested class. It is a
// sentinel so a scheduler can tell "this node is saturated right now" from
// "this node cannot serve this request at all" without parsing messages.
var ErrNoIdleSlot = errors.New("tunnelserver: no idle slot")

// ErrNodeNotConnected is the cause carried when no tunnel to the requested
// node is currently established on this replica.
var ErrNodeNotConnected = errors.New("tunnelserver: node not connected")

// Server implements tunnelv1.TunnelServer for one replica. The zero value is
// not usable; construct one with New.
type Server struct {
	// UnimplementedTunnelServer is embedded for the forward compatibility the
	// generated code requires: a method added to the service in a later
	// version of the proto must not stop this replica from building. It never
	// answers a call this file implements.
	tunnelv1.UnimplementedTunnelServer

	cfg     Config
	logger  *slog.Logger
	clock   runtime.Clock
	metrics *recorder

	mu    sync.RWMutex
	nodes map[string]*node

	// roster is the last roster handed to SetRoster, relayed to every Agent
	// on connect and re-broadcast on change. The Registry authors it; this
	// replica only carries it, which is why it is stored whole rather than
	// merged.
	roster *tunnelv1.GatewayRoster

	// closed stops new streams from being accepted after Close, so a
	// shutting-down replica does not take on work it is about to drop.
	closed bool

	// connected counts nodes holding at least one Control stream. It is not
	// len(nodes): an entry survives briefly with only a slot on it, and
	// counting that as a connected node would report capacity this replica
	// cannot actually reach with a roster or a shutdown.
	//
	// connected 统计至少持有一条 Control 流的节点数。它不等于 len(nodes)：一个条目
	// 可能短暂地只剩一个槽，把那种情况算作已连接节点，就是在上报一份本副本其实既
	// 发不出名册也发不出 shutdown 的容量。
	connected int
}

// New returns a Server ready to be registered with a gRPC server. It returns
// an error only for a Config that cannot work at all.
func New(cfg Config) (*Server, error) {
	if cfg.ReplicaID == "" {
		return nil, errors.New("tunnelserver: ReplicaID is required: an Agent has no other way to name this link in its logs and metrics")
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = DefaultHeartbeatTimeout
	}
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = DefaultMaxFrameBytes
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	return &Server{
		cfg:     cfg,
		logger:  logger.With(slog.String("replica_id", cfg.ReplicaID)),
		clock:   clock,
		metrics: newRecorder(cfg.Metrics, cfg.ReplicaID),
		nodes:   make(map[string]*node),
	}, nil
}

// ReplicaID reports the identity this replica announces in HelloAck.
func (s *Server) ReplicaID() string { return s.cfg.ReplicaID }

// SetRoster installs the replica roster relayed to Agents and broadcasts it to
// every connected node immediately. Agents ignore a version they have already
// seen, so re-sending an unchanged roster is safe but pointless; SetRoster
// therefore broadcasts whatever it is given and leaves de-duplication to the
// caller that owns the Registry subscription.
func (s *Server) SetRoster(roster *tunnelv1.GatewayRoster) {
	s.mu.Lock()
	s.roster = roster
	targets := make([]*node, 0, len(s.nodes))
	for _, n := range s.nodes {
		targets = append(targets, n)
	}
	s.mu.Unlock()

	s.metrics.RosterVersion(roster.GetVersion())

	frame := &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Roster{Roster: roster}}
	for _, n := range targets {
		n.broadcast(frame)
	}
}

// Close stops the replica from accepting new streams and asks every connected
// Agent to drain and disconnect. In-flight requests are not interrupted: the
// Agent finishes them before it leaves, which is what makes a rolling replica
// upgrade invisible to callers.
func (s *Server) Close(reason string, grace time.Duration) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	targets := make([]*node, 0, len(s.nodes))
	for _, n := range s.nodes {
		targets = append(targets, n)
	}
	s.mu.Unlock()

	frame := &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Shutdown{Shutdown: &tunnelv1.Shutdown{
		Reason:      reason,
		GracePeriod: durationProto(grace),
	}}}
	for _, n := range targets {
		n.broadcast(frame)
	}
}

// node returns the entry for id, creating it if this is the node's first
// stream. Entries outlive individual streams so a slot that reconnects a
// moment before its Control stream does still finds its node.
func (s *Server) node(id string) (*node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("tunnelserver: replica is shutting down")
	}
	n, ok := s.nodes[id]
	if !ok {
		n = newNode(id, s)
		s.nodes[id] = n
	}
	return n, nil
}

// lookup returns the entry for id without creating one.
func (s *Server) lookup(id string) (*node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	return n, ok
}

// forget drops a node entry once it holds neither a Control stream nor a slot.
// It runs under the server lock and re-checks emptiness there, because a new
// stream may have arrived between the decision to drop and the drop itself.
func (s *Server) forget(n *node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.nodes[n.id]; ok && cur == n && n.isEmpty() {
		delete(s.nodes, n.id)
	}
}

// controlsChanged adjusts the connected-node count and republishes it. delta
// is +1 when a node gained its first Control stream and -1 when it lost its
// last, and zero for every change in between, which is why the caller passes a
// delta rather than the stream count.
//
// controlsChanged 调整已连接节点计数并重新发布。节点获得第一条 Control 流时 delta
// 为 +1，失去最后一条时为 -1，两者之间的每次变化都是 0——这正是调用方传增量而不是
// 传流数量的原因。
func (s *Server) controlsChanged(delta int) {
	s.mu.Lock()
	s.connected += delta
	connected := s.connected
	s.mu.Unlock()
	s.metrics.ConnectedNodes(connected)
}

// currentRoster returns the roster to send a freshly connected Agent, or nil
// when this replica has not been given one yet. A nil roster is normal at
// startup and means "keep using your seeds"; the Agent's connection table
// treats an absent roster as no news rather than as an empty one.
func (s *Server) currentRoster() *tunnelv1.GatewayRoster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.roster
}

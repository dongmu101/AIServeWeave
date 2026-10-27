package registryserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// requireGatewayToken authenticates a GatewayDirectory.Join caller the same
// way requireAdmin (token_admin.go) authenticates a TokenAdmin caller — a
// constant-time comparison against a "authorization: Bearer <token>" gRPC
// metadata entry — but against the narrower GatewayToken (STATUS.md's S03).
// An empty GatewayToken leaves Join open, matching this method's behavior
// before S03: unlike TokenAdmin's registration, Join cannot be conditionally
// unregistered, since GatewayDirectory is core functionality a Gateway
// replica cannot run without.
func (s *Server) requireGatewayToken(ctx context.Context) error {
	if s.gatewayToken == "" {
		return nil
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing authorization metadata")
	}
	values := md.Get("authorization")
	if len(values) != 1 {
		return status.Error(codes.Unauthenticated, "missing authorization metadata")
	}
	const prefix = "Bearer "
	header := values[0]
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return status.Error(codes.Unauthenticated, "malformed authorization metadata")
	}
	if subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(s.gatewayToken)) != 1 {
		return status.Error(codes.PermissionDenied, "invalid gateway token")
	}
	return nil
}

// rosterState is the Registry's authoritative view of connected Gateway
// replicas: one entry per replica_id, plus a monotonic version that every
// broadcast bumps. The Agent side (tunnel/roster.go) ignores a version it has
// already seen, so re-broadcasting an unchanged roster is safe but pointless —
// this file always bumps the version when the replica set or a replica's
// state actually changes, and never otherwise.
//
// owners records which Join stream currently speaks for each replica_id. A
// replica that reconnects (replica_id defaults to the hostname, so a restart
// reuses it) can open its new stream before the Registry notices the old one
// died; only the owning stream may change or remove the entry, so the old
// stream's late exit cannot evict the live replica.
//
// rosterState 是 Registry 对已连接 Gateway 副本的权威视图：每个 replica_id 一个
// 条目，外加每次广播都会递增的单调版本号。Agent 一侧（tunnel/roster.go）会忽略
// 已见过的版本，因此重复广播一份没变的名册虽然安全但毫无意义——本文件只在副本
// 集合或某个副本状态真正变化时递增版本号，其他时候从不递增。
//
// owners 记录每个 replica_id 当前由哪条 Join 流代表。重连的副本（replica_id
// 默认取主机名，重启后不变）可能在 Registry 察觉旧流断开之前就打开了新流；
// 只有持有条目的那条流才能修改或删除条目，旧流迟到的退出因此不会把在线副本踢掉。
type rosterState struct {
	mu          sync.Mutex
	replicas    map[string]*tunnelv1.GatewayReplica
	owners      map[string]*joinStream
	revoked     map[string]struct{}
	maintenance map[string]struct{}
	version     int64
	streams     map[*joinStream]struct{}
}

// joinStream is one Gateway replica's open GatewayDirectory.Join call.
//
// Rosters reach it through offer and leave through a single sender goroutine
// (run), never through Send called by whichever goroutine changed the roster.
// That gives two guarantees a direct Send cannot: a stream never receives an
// older version after a newer one, even when two changes broadcast
// concurrently, and a replica that stops reading blocks only its own sender,
// not the broadcast to every other replica nor the admin RPC that triggered
// it. Rosters are full snapshots, so a backlog collapses into the newest one.
//
// joinStream 是某个 Gateway 副本一次打开着的 GatewayDirectory.Join 调用。
//
// 名册经 offer 送进来，再由唯一的发送协程（run）发出去，而不是由改动名册的那个
// 协程直接调用 Send。这带来两条直接 Send 给不了的保证：即使两次变化并发广播，
// 一条流也永远不会在收到新版本之后又收到旧版本；一个不再读取的副本只会卡住自己
// 的发送协程，而不会卡住发往其他副本的广播，也不会卡住触发这次广播的管理 RPC。
// 名册是完整快照，积压的多份因此合并成最新的一份。
type joinStream struct {
	replicaID  string
	grpcStream tunnelv1.GatewayDirectory_JoinServer

	mu     sync.Mutex
	latest *tunnelv1.GatewayRoster // newest roster offered so far / 目前送来的最新名册
	wake   chan struct{}           // capacity 1; signals a new latest / 容量 1，通知有了新的 latest
}

func newJoinStream(replicaID string, stream tunnelv1.GatewayDirectory_JoinServer) *joinStream {
	return &joinStream{replicaID: replicaID, grpcStream: stream, wake: make(chan struct{}, 1)}
}

// offer queues roster for delivery unless a newer one is already queued. It
// never blocks.
//
// offer 把 roster 排入待发送，除非已经排着一份更新的。它从不阻塞。
func (j *joinStream) offer(roster *tunnelv1.GatewayRoster) {
	j.mu.Lock()
	if j.latest == nil || roster.GetVersion() > j.latest.GetVersion() {
		j.latest = roster
	}
	j.mu.Unlock()
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

// run is the stream's only sender. It delivers the newest offered roster
// each time it wakes, skipping one it already sent, until done closes or a
// Send fails — a failed Send means the stream is dying, and Join's receive
// loop notices on its own.
//
// run 是这条流唯一的发送者。每次被唤醒时发出目前最新的那份名册（已发过的跳过），
// 直到 done 关闭或某次 Send 失败——Send 失败意味着流正在断开，Join 的接收循环
// 会自己察觉。
func (j *joinStream) run(done <-chan struct{}) {
	var sent *tunnelv1.GatewayRoster
	for {
		select {
		case <-done:
			return
		case <-j.wake:
		}
		j.mu.Lock()
		next := j.latest
		j.mu.Unlock()
		if next == nil || next == sent {
			continue
		}
		if err := j.grpcStream.Send(next); err != nil {
			return
		}
		sent = next
	}
}

// Join implements tunnelv1.GatewayDirectoryServer. It runs for the life of
// one Gateway replica's registration: the replica is in the roster for as
// long as this call is open, and drops out the moment it ends, by error, by
// the replica hanging up, or by Shutdown.
//
// Join 实现 tunnelv1.GatewayDirectoryServer。它贯穿一个 Gateway 副本登记的整个
// 生命周期：调用开着，副本就在名册里；调用一结束——出错、副本挂断或 Shutdown——
// 它立刻从名册里消失。
func (s *Server) Join(stream tunnelv1.GatewayDirectory_JoinServer) error {
	if s.shuttingDown() {
		return errShuttingDown
	}
	if err := s.requireGatewayToken(stream.Context()); err != nil {
		return err
	}

	// Recv runs on its own goroutine from the very first message, so every
	// wait below — including the one for that first message — also observes
	// Shutdown. It ends once this handler returns: gRPC cancels the stream
	// context, Recv fails, and the error lands in the buffered channel.
	// Recv 从第一条消息起就放在独立协程里，下面的每一次等待——包括等第一条消息
	// ——都能同时响应 Shutdown。处理函数返回后它随之结束：gRPC 取消流上下文，
	// Recv 失败，错误落进带缓冲的 channel。
	requests := make(chan *tunnelv1.JoinRequest)
	recvErr := make(chan error, 1)
	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case requests <- req:
			case <-stream.Context().Done():
				return
			}
		}
	}()

	var first *tunnelv1.JoinRequest
	select {
	case <-s.shutdown:
		return errShuttingDown
	case err := <-recvErr:
		return err
	case first = <-requests:
	}
	replicaID := first.GetReplicaId()
	endpoint := first.GetEndpoint()
	if replicaID == "" || endpoint == "" {
		return status.Error(codes.InvalidArgument, "replica_id and endpoint are required on the first message")
	}
	state := first.GetState()
	if state != tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE && state != tunnelv1.ReplicaState_REPLICA_STATE_DRAINING {
		return status.Error(codes.InvalidArgument, "state must be ACTIVE or DRAINING")
	}

	js := newJoinStream(replicaID, stream)
	// The sender must have exited before this handler returns: gRPC forbids
	// Send on a stream whose handler has returned. Deferred first, so it runs
	// after leave() below has taken js out of every future broadcast.
	// 本处理函数返回之前发送协程必须已退出：gRPC 不允许在处理函数返回后对流调用
	// Send。它最先 defer，因此在下面的 leave() 把 js 移出后续广播之后才执行。
	senderDone := make(chan struct{})
	senderExited := make(chan struct{})
	go func() {
		defer close(senderExited)
		js.run(senderDone)
	}()
	defer func() {
		close(senderDone)
		<-senderExited
	}()

	s.roster.join(js, endpoint, state)
	s.metrics.GatewayJoined()
	defer s.metrics.GatewayLeft() // deferred before leave(js) so LIFO runs leave() first, mirroring join-then-count above
	defer s.roster.leave(js)

	s.logger.Info("gateway replica joined", slog.String("replica_id", replicaID), slog.String("endpoint", endpoint))
	defer s.logger.Info("gateway replica left", slog.String("replica_id", replicaID))

	for {
		select {
		case <-s.shutdown:
			return errShuttingDown
		case err := <-recvErr:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case req := <-requests:
			state := req.GetState()
			if state != tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE && state != tunnelv1.ReplicaState_REPLICA_STATE_DRAINING {
				return status.Error(codes.InvalidArgument, "state must be ACTIVE or DRAINING")
			}
			s.roster.updateState(js, state)
		}
	}
}

// join registers a replica, taking over its replica_id from any older stream
// still holding it, and broadcasts the resulting roster to every currently
// open stream, including js itself.
//
// join 登记一个副本——若还有旧流占着这个 replica_id，就由 js 接管——并把得到的
// 名册广播给当前所有打开着的流，包括 js 自己。
func (r *rosterState) join(js *joinStream, endpoint string, state tunnelv1.ReplicaState) {
	r.mu.Lock()
	if r.streams == nil {
		r.streams = make(map[*joinStream]struct{})
	}
	if r.owners == nil {
		r.owners = make(map[string]*joinStream)
	}
	r.streams[js] = struct{}{}
	r.owners[js.replicaID] = js
	r.replicas[js.replicaID] = &tunnelv1.GatewayReplica{
		ReplicaId: js.replicaID,
		Endpoint:  endpoint,
		State:     state,
	}
	r.version++
	roster, targets := r.snapshotLocked()
	r.mu.Unlock()

	broadcast(roster, targets)
}

// updateState changes the state of the replica js speaks for and broadcasts,
// unless js no longer owns that replica_id — it left, or a newer stream for
// the same replica_id took over — in which case there is nothing to update.
//
// updateState 修改 js 所代表副本的状态并广播；若 js 已不再持有这个 replica_id
// ——它已离开，或同一 replica_id 的新流已接管——则无事可做。
func (r *rosterState) updateState(js *joinStream, state tunnelv1.ReplicaState) {
	r.mu.Lock()
	rep, ok := r.replicas[js.replicaID]
	if !ok || r.owners[js.replicaID] != js || rep.State == state {
		r.mu.Unlock()
		return
	}
	// Replace rather than mutate: snapshots already handed to senders share
	// the old *GatewayReplica, and a sender may be serializing it right now.
	// 替换而不是原地修改：已交给发送协程的快照共享旧的 *GatewayReplica，某个
	// 发送协程此刻可能正在序列化它。
	r.replicas[js.replicaID] = &tunnelv1.GatewayReplica{
		ReplicaId: rep.GetReplicaId(),
		Endpoint:  rep.GetEndpoint(),
		State:     state,
	}
	r.version++
	roster, targets := r.snapshotLocked()
	r.mu.Unlock()

	broadcast(roster, targets)
}

// leave drops js from future broadcasts and, if js still owns its
// replica_id, removes that replica and broadcasts the roster without it to
// everyone still connected. A stream that was already superseded leaves the
// roster untouched.
//
// leave 把 js 移出后续广播；若 js 仍持有它的 replica_id，就删除该副本，并把不含
// 它的名册广播给所有仍连接着的流。已被接管的旧流离开时不改动名册。
func (r *rosterState) leave(js *joinStream) {
	r.mu.Lock()
	delete(r.streams, js)
	if r.owners[js.replicaID] != js {
		r.mu.Unlock()
		return
	}
	delete(r.owners, js.replicaID)
	delete(r.replicas, js.replicaID)
	r.version++
	roster, targets := r.snapshotLocked()
	r.mu.Unlock()

	broadcast(roster, targets)
}

// setRevoked replaces the revoked node_id set and broadcasts the resulting
// roster to every currently open stream, unless the set is unchanged from
// what was already broadcast — TokenAdmin.DisableNode/EnableNode both call
// this after every write, and Enable undoing something that was never
// disabled must not bump the version and force every replica to reprocess an
// identical roster.
func (r *rosterState) setRevoked(nodeIDs []string) {
	next := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		next[id] = struct{}{}
	}

	r.mu.Lock()
	if mapsEqual(r.revoked, next) {
		r.mu.Unlock()
		return
	}
	r.revoked = next
	r.version++
	roster, targets := r.snapshotLocked()
	r.mu.Unlock()

	broadcast(roster, targets)
}

// setMaintenance replaces the maintenance node_id set and broadcasts the
// resulting roster to every currently open stream, unless the set is
// unchanged from what was already broadcast — the same idempotence
// setRevoked gives DisableNode/EnableNode, here for
// SetMaintenance/ClearMaintenance.
func (r *rosterState) setMaintenance(nodeIDs []string) {
	next := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		next[id] = struct{}{}
	}

	r.mu.Lock()
	if mapsEqual(r.maintenance, next) {
		r.mu.Unlock()
		return
	}
	r.maintenance = next
	r.version++
	roster, targets := r.snapshotLocked()
	r.mu.Unlock()

	broadcast(roster, targets)
}

// mapsEqual reports whether two sets of node_ids hold the same members.
func mapsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}

// snapshotLocked builds the current roster and the list of streams to send it
// to. Callers must hold r.mu.
func (r *rosterState) snapshotLocked() (*tunnelv1.GatewayRoster, []*joinStream) {
	replicas := make([]*tunnelv1.GatewayReplica, 0, len(r.replicas))
	for _, rep := range r.replicas {
		replicas = append(replicas, rep)
	}
	revoked := make([]string, 0, len(r.revoked))
	for id := range r.revoked {
		revoked = append(revoked, id)
	}
	maintenance := make([]string, 0, len(r.maintenance))
	for id := range r.maintenance {
		maintenance = append(maintenance, id)
	}
	roster := &tunnelv1.GatewayRoster{
		Replicas:           replicas,
		Version:            r.version,
		RevokedNodeIds:     revoked,
		MaintenanceNodeIds: maintenance,
	}
	targets := make([]*joinStream, 0, len(r.streams))
	for js := range r.streams {
		targets = append(targets, js)
	}
	return roster, targets
}

// broadcast hands roster to every target's sender without waiting for any of
// them to send it.
//
// broadcast 把 roster 交给每个目标的发送协程，不等待任何一个真正发出。
func broadcast(roster *tunnelv1.GatewayRoster, targets []*joinStream) {
	for _, js := range targets {
		js.offer(roster)
	}
}

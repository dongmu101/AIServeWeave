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
// replicas: one entry per open Join stream, plus a monotonic version that
// every broadcast bumps. The Agent side (tunnel/roster.go) ignores a version
// it has already seen, so re-broadcasting an unchanged roster is safe but
// pointless — this file always bumps the version when the replica set or a
// replica's state actually changes, and never otherwise.
type rosterState struct {
	mu          sync.Mutex
	replicas    map[string]*tunnelv1.GatewayReplica
	revoked     map[string]struct{}
	maintenance map[string]struct{}
	version     int64
	streams     map[*joinStream]struct{}
}

// joinStream is one Gateway replica's open GatewayDirectory.Join call.
// sendMu serializes Send the same way tunnelserver's controlSession and slot
// types do: a gRPC stream is not safe for concurrent Send from multiple
// goroutines, and both the read loop's own replies and roster broadcasts
// from other replicas' Join calls can reach this stream at once.
type joinStream struct {
	replicaID  string
	grpcStream tunnelv1.GatewayDirectory_JoinServer

	sendMu sync.Mutex
}

func (j *joinStream) send(roster *tunnelv1.GatewayRoster) error {
	j.sendMu.Lock()
	defer j.sendMu.Unlock()
	return j.grpcStream.Send(roster)
}

// Join implements tunnelv1.GatewayDirectoryServer. It runs for the life of
// one Gateway replica's registration: the replica is in the roster for as
// long as this call is open, and drops out the moment it ends, by error or
// by the replica hanging up.
func (s *Server) Join(stream tunnelv1.GatewayDirectory_JoinServer) error {
	if err := s.requireGatewayToken(stream.Context()); err != nil {
		return err
	}

	first, err := stream.Recv()
	if err != nil {
		return err
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

	js := &joinStream{replicaID: replicaID, grpcStream: stream}
	s.roster.join(js, endpoint, state)
	s.metrics.GatewayJoined()
	defer s.metrics.GatewayLeft() // deferred before leave(js) so LIFO runs leave() first, mirroring join-then-count above
	defer s.roster.leave(js)

	s.logger.Info("gateway replica joined", slog.String("replica_id", replicaID), slog.String("endpoint", endpoint))
	defer s.logger.Info("gateway replica left", slog.String("replica_id", replicaID))

	for {
		req, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		state := req.GetState()
		if state != tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE && state != tunnelv1.ReplicaState_REPLICA_STATE_DRAINING {
			return status.Error(codes.InvalidArgument, "state must be ACTIVE or DRAINING")
		}
		s.roster.updateState(replicaID, state)
	}
}

// join registers a replica and broadcasts the resulting roster to every
// currently open stream, including js itself.
func (r *rosterState) join(js *joinStream, endpoint string, state tunnelv1.ReplicaState) {
	r.mu.Lock()
	if r.streams == nil {
		r.streams = make(map[*joinStream]struct{})
	}
	r.streams[js] = struct{}{}
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

// updateState changes a joined replica's state and broadcasts, unless the
// replica already left (its stream can have closed between Recv returning
// and this call, in which case there is nothing to update).
func (r *rosterState) updateState(replicaID string, state tunnelv1.ReplicaState) {
	r.mu.Lock()
	rep, ok := r.replicas[replicaID]
	if !ok || rep.State == state {
		r.mu.Unlock()
		return
	}
	rep.State = state
	r.version++
	roster, targets := r.snapshotLocked()
	r.mu.Unlock()

	broadcast(roster, targets)
}

// leave removes a replica from the roster and broadcasts the roster without
// it to everyone still connected.
func (r *rosterState) leave(js *joinStream) {
	r.mu.Lock()
	delete(r.streams, js)
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

func broadcast(roster *tunnelv1.GatewayRoster, targets []*joinStream) {
	for _, js := range targets {
		_ = js.send(roster) // a failed send means the stream is dying; its own Recv loop will notice and leave()
	}
}

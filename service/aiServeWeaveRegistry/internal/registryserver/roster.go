package registryserver

import (
	"errors"
	"io"
	"log/slog"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// rosterState is the Registry's authoritative view of connected Gateway
// replicas: one entry per open Join stream, plus a monotonic version that
// every broadcast bumps. The Agent side (tunnel/roster.go) ignores a version
// it has already seen, so re-broadcasting an unchanged roster is safe but
// pointless — this file always bumps the version when the replica set or a
// replica's state actually changes, and never otherwise.
type rosterState struct {
	mu       sync.Mutex
	replicas map[string]*tunnelv1.GatewayReplica
	version  int64
	streams  map[*joinStream]struct{}
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

// snapshotLocked builds the current roster and the list of streams to send it
// to. Callers must hold r.mu.
func (r *rosterState) snapshotLocked() (*tunnelv1.GatewayRoster, []*joinStream) {
	replicas := make([]*tunnelv1.GatewayReplica, 0, len(r.replicas))
	for _, rep := range r.replicas {
		replicas = append(replicas, rep)
	}
	roster := &tunnelv1.GatewayRoster{Replicas: replicas, Version: r.version}
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

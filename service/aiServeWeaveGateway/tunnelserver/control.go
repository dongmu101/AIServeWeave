package tunnelserver

import (
	"errors"
	"io"
	"log/slog"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/tunnelwire"
)

// controlSession is one Control stream. Its only mutable state is the send
// mutex: gRPC allows one writer at a time, and a broadcast may race with the
// handler's own replies.
type controlSession struct {
	stream tunnelv1.Tunnel_ControlServer
	sendMu sync.Mutex
}

func (s *controlSession) send(frame *tunnelv1.GatewayControl) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(frame)
}

// Control implements tunnelv1.TunnelServer. It runs for the life of one
// Agent's control connection to this replica and returns when the Agent
// disconnects, when the stream fails, or when the handshake is rejected.
func (s *Server) Control(stream tunnelv1.Tunnel_ControlServer) error {
	ctx := stream.Context()
	certNodeID, err := peerNodeID(ctx)
	if err != nil {
		return err
	}

	hello, err := recvHello(stream)
	if err != nil {
		return err
	}
	// The certificate is the authority; the declared node_id only has to
	// agree with it. Rejecting the mismatch rather than preferring the
	// certificate keeps the Agent's own view honest: a node that thinks it
	// is someone else has a configuration fault worth surfacing, not one
	// worth silently correcting.
	if hello.GetNodeId() != certNodeID {
		return status.Errorf(codes.PermissionDenied,
			"Hello declared a node_id that the client certificate does not authorize")
	}

	n, err := s.node(certNodeID)
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	session := &controlSession{stream: stream}

	n.mu.Lock()
	n.controls[session] = struct{}{}
	n.agentVersion = hello.GetAgentVersion()
	n.resources = hello.GetResources()
	n.runtimeIDs = append([]string(nil), hello.GetRuntimeIds()...)
	// A reconnecting Agent is not draining any more: Draining describes the
	// stream it was announced on, and that stream is gone.
	n.draining = false
	n.lastHeartbeat = s.clock.Now()
	n.mu.Unlock()

	s.logger.Info("node connected",
		slog.String("node_id", n.id),
		slog.String("agent_version", hello.GetAgentVersion()),
		slog.Int("runtime_ids", len(hello.GetRuntimeIds())))

	defer func() {
		n.mu.Lock()
		delete(n.controls, session)
		n.mu.Unlock()
		s.forget(n)
		s.logger.Info("node disconnected", slog.String("node_id", n.id))
	}()

	if err := session.send(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Ack{Ack: &tunnelv1.HelloAck{
		ReplicaId:    s.cfg.ReplicaID,
		ServerUnixMs: s.clock.Now().UnixMilli(),
	}}}); err != nil {
		return err
	}
	if hint := s.cfg.SlotHint; hint != nil {
		if err := session.send(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_SlotHint{SlotHint: hint}}); err != nil {
			return err
		}
	}
	if roster := s.currentRoster(); roster != nil {
		if err := session.send(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Roster{Roster: roster}}); err != nil {
			return err
		}
	}

	return s.serveControl(n, session)
}

// recvHello reads the first frame of a Control stream and requires it to be
// Hello. Accepting anything else would mean tracking a node whose identity has
// not been stated yet.
func recvHello(stream tunnelv1.Tunnel_ControlServer) (*tunnelv1.Hello, error) {
	frame, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	hello := frame.GetHello()
	if hello == nil {
		return nil, status.Error(codes.InvalidArgument, "first control frame must be Hello")
	}
	if hello.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Hello carried no node_id")
	}
	return hello, nil
}

// serveControl is the read loop: it answers heartbeats, folds status reports
// into the node's inventory, and records a drain announcement.
func (s *Server) serveControl(n *node, session *controlSession) error {
	for {
		frame, err := session.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// The Agent half-closed, which is how it reports that
				// draining finished. Returning nil closes our side too.
				return nil
			}
			return err
		}

		switch body := frame.GetBody().(type) {
		case *tunnelv1.AgentControl_Heartbeat:
			now := s.clock.Now()
			n.mu.Lock()
			n.lastHeartbeat = now
			n.inflight = int(body.Heartbeat.GetInflightRequests())
			n.reportedIdle = int(body.Heartbeat.GetIdleSlots())
			n.mu.Unlock()
			if err := session.send(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_HbAck{HbAck: &tunnelv1.HeartbeatAck{
				SentUnixMs:   body.Heartbeat.GetSentUnixMs(),
				ServerUnixMs: now.UnixMilli(),
			}}}); err != nil {
				return err
			}

		case *tunnelv1.AgentControl_Status:
			snaps := tunnelwire.SnapshotsFromProto(body.Status.GetSnapshots())
			n.applyStatus(body.Status, snaps)

		case *tunnelv1.AgentControl_Draining:
			n.mu.Lock()
			n.draining = true
			// Idle slots are useless once the node is leaving: nothing may
			// be dispatched onto them, and leaving them in the set would
			// make the node look like it still has capacity.
			idle := n.idle
			n.idle = make(map[tunnelv1.SlotClass][]*slot)
			n.mu.Unlock()
			for _, stack := range idle {
				for _, sl := range stack {
					sl.close(errors.New("node is draining"))
				}
			}
			s.logger.Info("node draining",
				slog.String("node_id", n.id),
				slog.String("reason", body.Draining.GetReason()))

		case *tunnelv1.AgentControl_Pong:
			// RTT accounting for replica-initiated pings lives with the
			// heartbeat metrics; nothing to do until those are wired up.

		case *tunnelv1.AgentControl_Hello:
			return status.Error(codes.InvalidArgument, "Hello may only be the first frame on a control stream")

		default:
			// An unknown frame from a newer Agent is ignored rather than
			// fatal: the control plane must stay forward compatible, or a
			// mixed-version fleet cannot be upgraded one side at a time.
		}
	}
}

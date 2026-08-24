package tunnelserver

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// slot is one Serve stream: a data-plane channel the Agent opened and parked,
// which this replica fills with one request at a time and which the Agent
// re-parks afterwards.
//
// The stream's reader is the Serve handler goroutine and nothing else. Every
// frame the Agent sends is routed from there to whoever is currently running a
// request on the slot, one frame at a time and with no intermediate buffer, so
// a caller that stops reading stalls the tunnel's flow control instead of
// growing a queue in this process.
type slot struct {
	srv    *Server
	node   *node
	id     string
	class  tunnelv1.SlotClass
	stream tunnelv1.Tunnel_ServeServer

	sendMu sync.Mutex

	mu     sync.Mutex
	parked bool
	dead   bool
	cur    *call

	closeOnce sync.Once
	done      chan struct{}
	closeErr  error
}

// Serve implements tunnelv1.TunnelServer. Each call is one slot, living until
// the Agent closes it or the stream fails.
func (s *Server) Serve(stream tunnelv1.Tunnel_ServeServer) error {
	certNodeID, err := peerNodeID(stream.Context())
	if err != nil {
		return err
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ready := first.GetReady()
	if ready == nil {
		return status.Error(codes.InvalidArgument, "first slot frame must be Ready")
	}
	if ready.GetNodeId() != certNodeID {
		return status.Error(codes.PermissionDenied,
			"Ready declared a node_id that the client certificate does not authorize")
	}
	if ready.GetClass() == tunnelv1.SlotClass_SLOT_CLASS_UNSPECIFIED {
		return status.Error(codes.InvalidArgument, "Ready carried no slot class")
	}

	n, err := s.node(certNodeID)
	if err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}

	sl := &slot{
		srv:    s,
		node:   n,
		id:     ready.GetSlotId(),
		class:  ready.GetClass(),
		stream: stream,
		done:   make(chan struct{}),
	}

	n.addLive(sl.class, 1)
	defer func() {
		n.addLive(sl.class, -1)
		s.forget(n)
	}()

	if !sl.park() {
		return status.Error(codes.Unavailable, "node is draining")
	}
	defer sl.close(io.EOF)

	return sl.readLoop()
}

// park offers the slot to the node's idle set. It returns false when the node
// will not take it, which is the Agent's signal to close the stream.
func (s *slot) park() bool {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return false
	}
	s.parked = true
	s.mu.Unlock()

	if !s.node.park(s) {
		s.mu.Lock()
		s.parked = false
		s.mu.Unlock()
		return false
	}
	return true
}

// take claims a parked slot for a request. It is called with the node lock
// held, so it must touch nothing but the slot.
func (s *slot) take() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead || !s.parked {
		return false
	}
	s.parked = false
	return true
}

// begin binds a call to the slot. It fails if the slot died between being
// taken from the idle set and the request being written.
func (s *slot) begin(c *call) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return s.closeErr
	}
	if s.cur != nil {
		return errors.New("tunnelserver: slot already has a request")
	}
	s.cur = c
	return nil
}

// finish clears the current call so the Agent's next Ready is legal.
func (s *slot) finish(c *call) {
	s.mu.Lock()
	if s.cur == c {
		s.cur = nil
	}
	s.mu.Unlock()
}

func (s *slot) current() *call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// send writes one frame to the Agent under the stream's single-writer rule.
func (s *slot) send(frame *tunnelv1.GatewayFrame) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.node.metrics.FrameBytes(DirectionOutbound, proto.Size(frame))
	return s.stream.Send(frame)
}

// close marks the slot dead, releases whatever request was running on it, and
// takes it out of the idle set. It is idempotent, and safe to call from the
// read loop, from a dispatch, and from a node-wide drain at the same time.
func (s *slot) close(cause error) {
	s.closeOnce.Do(func() {
		if cause == nil {
			cause = io.EOF
		}
		s.mu.Lock()
		s.dead = true
		s.closeErr = cause
		cur := s.cur
		s.cur = nil
		s.parked = false
		s.mu.Unlock()

		close(s.done)
		if cur != nil {
			cur.abort(slotDeathError(cause))
		}
		s.node.unpark(s)
	})
}

// readLoop is the slot's only reader. It runs until the stream ends.
func (s *slot) readLoop() error {
	for {
		frame, err := s.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		s.node.metrics.FrameBytes(DirectionInbound, proto.Size(frame))

		switch body := frame.GetBody().(type) {
		case *tunnelv1.AgentFrame_Ready:
			// The Agent re-parks the slot after finishing a request. A
			// Ready while a request is still running is a protocol fault:
			// the slot would be offered for a second request while the
			// first one's frames are still coming.
			if s.current() != nil {
				return s.fault(FaultReadyWhileInflight, "Ready arrived while a request was still in flight")
			}
			if !s.park() {
				return status.Error(codes.Unavailable, "node is draining")
			}

		case *tunnelv1.AgentFrame_Pong:
			// Application-level keepalive; the RTT is recorded on the
			// control plane, where heartbeats already live.

		case *tunnelv1.AgentFrame_Headers, *tunnelv1.AgentFrame_Data, *tunnelv1.AgentFrame_End:
			if err := s.route(frame); err != nil {
				return err
			}

		default:
			_ = body
			// Unknown frame from a newer Agent: ignore, so one side can be
			// upgraded before the other.
		}
	}
}

// route hands one response frame to the running request. Delivery blocks until
// the caller takes the frame, the caller abandons the request, or the stream
// ends — which is exactly the backpressure path: a slow consumer holds the
// tunnel's receive window rather than filling memory here.
func (s *slot) route(frame *tunnelv1.AgentFrame) error {
	c := s.current()
	if c == nil {
		return s.fault(FaultFrameOnIdleSlot, "response frame arrived on an idle slot")
	}
	if c.id != frame.GetRequestId() {
		// A frame for a request that is no longer on this slot is dropped
		// for the same reason the Agent drops a late Cancel: the slot has
		// moved on, and acting on it would corrupt the request that
		// replaced it.
		s.srv.logger.Debug("dropping frame for a stale request",
			slog.String("node_id", s.node.id),
			slog.String("slot_id", s.id))
		return nil
	}

	if data := frame.GetData(); data != nil && len(data.GetPayload()) > s.srv.cfg.MaxFrameBytes {
		err := fmt.Errorf("tunnelserver: data chunk of %d bytes exceeds the %d byte frame limit",
			len(data.GetPayload()), s.srv.cfg.MaxFrameBytes)
		c.abort(err)
		s.finish(c)
		return s.fault(FaultFrameTooLarge, err.Error())
	}

	last := frame.GetEnd() != nil
	c.deliver(frame, s.done)
	if last {
		// Clear the slot before the Agent's next Ready can arrive.
		s.finish(c)
	}
	return nil
}

// fault ends the slot because the Agent broke the frame contract. The stream
// is closed rather than resynchronized: a slot whose framing is not understood
// cannot be safely reused for a later request.
//
// The two reason arguments are not redundant: kind is the bounded label the
// metric carries, detail is the human sentence the log carries and may quote
// sizes and ids that must never become label values.
//
// fault 因 Agent 违反帧契约而结束该槽。这里是关闭流而不是重新同步：一个帧结构已经
// 无法理解的槽，不能安全地复用给后面的请求。
//
// 两个 reason 参数并不冗余：kind 是指标携带的有界标签，detail 是日志携带的自然语句，
// 其中可能引用尺寸与 id，而那些绝不能变成标签值。
func (s *slot) fault(kind SlotFaultReason, detail string) error {
	s.node.metrics.SlotFault(kind)
	s.srv.logger.Warn("closing slot on a protocol fault",
		slog.String("node_id", s.node.id),
		slog.String("slot_id", s.id),
		slog.String("fault", string(kind)),
		slog.String("reason", detail))
	err := status.Error(codes.FailedPrecondition, detail)
	s.close(err)
	return err
}

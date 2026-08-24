package gatewaytest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// Timeout bounds every blocking wait a Harness user performs. It is generous
// because it should never be reached: hitting it means a deadlock, and the
// failure message should say which wait deadlocked rather than the whole run
// timing out anonymously.
const Timeout = 5 * time.Second

// Harness runs one tunnelserver.Server and lets a test play one or more
// Agents against it, exactly as tunnelserver's own tests do. It exists here
// — outside tunnelserver/internal — because scheduler and httpapi need the
// same scripted-Agent double and Go's internal-package rule keeps
// tunnelserver's copy out of their reach.
type Harness struct {
	T     *testing.T
	Srv   *tunnelserver.Server
	CA    *CA
	Clock *Clock

	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup

	mu      sync.Mutex
	streams []interface{ Break(error) }
}

// NewHarness starts srv (config already built by the caller) and returns a
// Harness that will tear every stream it opens down on test cleanup.
func NewHarness(t *testing.T, cfg tunnelserver.Config) *Harness {
	t.Helper()
	clock := NewClock()
	if cfg.ReplicaID == "" {
		cfg.ReplicaID = "replica-a"
	}
	cfg.Clock = clock
	srv, err := tunnelserver.New(cfg)
	if err != nil {
		t.Fatalf("tunnelserver.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Harness{T: t, Srv: srv, CA: NewCA(t), Clock: clock, ctx: ctx, cancel: cancel}
	t.Cleanup(h.Close)
	return h
}

// Close tears every stream down and waits for the handlers to return.
func (h *Harness) Close() {
	h.mu.Lock()
	streams := h.streams
	h.streams = nil
	h.mu.Unlock()
	for _, s := range streams {
		s.Break(errors.New("harness closed"))
	}
	h.cancel()
	h.wg.Wait()
}

func (h *Harness) track(s interface{ Break(error) }) {
	h.mu.Lock()
	h.streams = append(h.streams, s)
	h.mu.Unlock()
}

// Control is one Agent's Control stream, with the handshake already done.
type Control struct {
	Stream *ControlStream
	Ack    *tunnelv1.HelloAck
	errc   chan error
}

// Connect starts a Control handler and completes the Hello handshake.
func (h *Harness) Connect(nodeID string, runtimeIDs ...string) *Control {
	h.T.Helper()
	c := h.StartControl(nodeID, &tunnelv1.Hello{
		NodeId:       nodeID,
		AgentVersion: "test",
		RuntimeIds:   runtimeIDs,
	})
	frame := c.Expect(h.T)
	ack := frame.GetAck()
	if ack == nil {
		h.T.Fatalf("first gateway frame = %T, want HelloAck", frame.GetBody())
	}
	c.Ack = ack
	return c
}

// ConnectWithLabels connects a node carrying operator-assigned labels, reports
// snap, and parks one slot per handler. It is the labelled counterpart of the
// per-package connectNode helpers, kept here because building a Hello with
// labels is harness business rather than any one test's.
//
// ConnectWithLabels 连接一个带有运维标签的节点，上报 snap，并为每个 handler park 一个
// 槽。它是各包 connectNode 辅助函数的带标签对应物，放在这里是因为「构造一个带标签的
// Hello」属于测试框架的事，而不是某一个测试的事。
func (h *Harness) ConnectWithLabels(t *testing.T, nodeID, runtimeID string, labels map[string]string, snap runtime.Snapshot, handlers ...SlotHandler) {
	t.Helper()
	c := h.StartControl(nodeID, &tunnelv1.Hello{
		NodeId:       nodeID,
		AgentVersion: "test",
		RuntimeIds:   []string{runtimeID},
		Labels:       labels,
	})
	frame := c.Expect(t)
	if frame.GetAck() == nil {
		t.Fatalf("first gateway frame = %T, want HelloAck", frame.GetBody())
	}
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{snap}),
	}}})
	for i, handle := range handlers {
		h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, fmt.Sprintf("%s-slot-%d", nodeID, i), handle)
	}
	WaitFor(t, "slots to park on "+nodeID, func() bool { return IdleCount(h, nodeID) == len(handlers) })
	WaitFor(t, "inventory to arrive on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return len(info.Runtimes) == 1 && len(info.Labels) == len(labels)
	})
}

// StartControl starts a Control handler and sends hello, without requiring
// the handshake to succeed.
func (h *Harness) StartControl(certNodeID string, hello *tunnelv1.Hello) *Control {
	h.T.Helper()
	stream := NewControlStream(h.ctx, certNodeID, h.CA)
	h.track(stream)
	c := &Control{Stream: stream, errc: make(chan error, 1)}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		c.errc <- h.Srv.Control(stream)
	}()
	if hello != nil {
		_ = stream.FromAgent(&tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Hello{Hello: hello}})
	}
	return c
}

// Expect returns the next frame the replica sent, failing the test on
// timeout.
func (c *Control) Expect(t *testing.T) *tunnelv1.GatewayControl {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	frame, err := c.Stream.ToAgent(ctx)
	if err != nil {
		t.Fatalf("waiting for a gateway control frame: %v", err)
	}
	return frame
}

// Send plays one Agent control frame.
func (c *Control) Send(t *testing.T, frame *tunnelv1.AgentControl) {
	t.Helper()
	if err := c.Stream.FromAgent(frame); err != nil {
		t.Fatalf("sending an agent control frame: %v", err)
	}
}

// Wait returns the handler's error once it has exited.
func (c *Control) Wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-c.errc:
		return err
	case <-time.After(Timeout):
		t.Fatal("control handler did not exit")
		return nil
	}
}

// AgentSlot is one Agent-side slot: a Serve handler plus a goroutine that
// answers dispatched requests with a scripted handler.
type AgentSlot struct {
	Stream *ServeStream
	errc   chan error
	slotID string
}

// SlotHandler answers one dispatched request. It receives the request
// headers and a reply function for response frames, and returns the
// ResponseEnd error (nil for success). Returning without replying at all is
// a valid, if terse, answer: it is what a runtime that produced nothing
// looks like.
type SlotHandler func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error

// OpenSlot starts a Serve handler, parks the slot with Ready, and serves
// requests with handle until the stream ends.
func (h *Harness) OpenSlot(nodeID string, class tunnelv1.SlotClass, slotID string, handle SlotHandler) *AgentSlot {
	h.T.Helper()
	stream := NewServeStream(h.ctx, nodeID, h.CA)
	h.track(stream)
	s := &AgentSlot{Stream: stream, errc: make(chan error, 1), slotID: slotID}

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		s.errc <- h.Srv.Serve(stream)
	}()

	if err := stream.FromAgent(&tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Ready{Ready: &tunnelv1.Ready{
		NodeId: nodeID, SlotId: slotID, Class: class,
	}}}); err != nil {
		h.T.Fatalf("parking slot %s: %v", slotID, err)
	}

	if handle != nil {
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			s.run(h.ctx, nodeID, class, handle)
		}()
	}
	return s
}

// run is the Agent's frame loop for one slot: take a request, run the
// handler, terminate it with ResponseEnd, and re-park.
func (s *AgentSlot) run(ctx context.Context, nodeID string, class tunnelv1.SlotClass, handle SlotHandler) {
	for {
		frame, err := s.Stream.ToAgent(ctx)
		if err != nil {
			return
		}
		headers := frame.GetHeaders()
		if headers == nil {
			continue
		}
		requestID := frame.GetRequestId()

		// Collect the request body up to RequestEnd.
		var body [][]byte
		for {
			next, err := s.Stream.ToAgent(ctx)
			if err != nil {
				return
			}
			if chunk := next.GetData(); chunk != nil {
				body = append(body, chunk.GetPayload())
				continue
			}
			if next.GetEnd() != nil {
				break
			}
		}

		reply := func(f *tunnelv1.AgentFrame) error {
			f.RequestId = requestID
			return s.Stream.FromAgent(f)
		}
		handlerErr := handle(headers, body, reply)
		end := &tunnelv1.AgentFrame{
			RequestId: requestID,
			Body:      &tunnelv1.AgentFrame_End{End: &tunnelv1.ResponseEnd{Error: ToTunnelError(handlerErr)}},
		}
		if err := s.Stream.FromAgent(end); err != nil {
			return
		}
		if err := s.Stream.FromAgent(&tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Ready{Ready: &tunnelv1.Ready{
			NodeId: nodeID, SlotId: s.slotID, Class: class,
		}}}); err != nil {
			return
		}
	}
}

// Wait returns the Serve handler's error once it has exited.
func (s *AgentSlot) Wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-s.errc:
		return err
	case <-time.After(Timeout):
		t.Fatal("serve handler did not exit")
		return nil
	}
}

// DataFrame builds a DataChunk response frame.
func DataFrame(payload []byte) *tunnelv1.AgentFrame {
	return &tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Data{Data: &tunnelv1.DataChunk{Payload: payload}}}
}

// HeaderFrame builds the ResponseHeaders frame that precedes an artifact
// body. Only ARTIFACT_OPEN sends one; every other operation goes straight to
// its DataChunks.
//
// HeaderFrame 构造产物响应体之前的那个 ResponseHeaders 帧。只有 ARTIFACT_OPEN 会发
// 它，其余操作都直接进入自己的 DataChunk。
func HeaderFrame(contentType string, size int64) *tunnelv1.AgentFrame {
	return &tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Headers{
		Headers: &tunnelv1.ResponseHeaders{ContentType: contentType, Size: size},
	}}
}

// WireError lets a scripted handler dictate the exact TunnelError an Agent
// would put on the wire.
type WireError struct {
	Code      string
	Message   string
	Retryable bool
	Cause     string
}

func (e *WireError) Error() string { return e.Code + ": " + e.Message }

// ToTunnelError renders a handler error as the wire error, or nil on
// success. The harness carries the error itself rather than reusing the
// codec, so a bug in the codec cannot make a protocol test pass.
func ToTunnelError(err error) *tunnelv1.TunnelError {
	if err == nil {
		return nil
	}
	var we *WireError
	if errors.As(err, &we) {
		return &tunnelv1.TunnelError{
			Code:      we.Code,
			Message:   we.Message,
			Retryable: we.Retryable,
			Cause:     we.Cause,
		}
	}
	return &tunnelv1.TunnelError{Code: "upstream_error", Message: err.Error()}
}

// WaitFor polls cond until it holds or Timeout expires. It exists for
// assertions that observe state a handler goroutine sets — a node going
// live, a slot re-parking — where there is no frame to synchronize on.
func WaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(Timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// IdleCount reports how many slots of class SLOT_CLASS_INFERENCE are
// currently parked on nodeID, the figure most tests poll on to know a slot
// has finished parking.
func IdleCount(h *Harness, nodeID string) int {
	info, ok := h.Srv.Node(nodeID)
	if !ok {
		return 0
	}
	return info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE]
}

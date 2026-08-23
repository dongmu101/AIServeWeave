package tunnelserver_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// testTimeout bounds every blocking wait in this package's tests. It is
// generous because it should never be reached: a test that hits it has found a
// deadlock, and the failure message should say which wait deadlocked rather
// than the whole run timing out anonymously.
const testTimeout = 5 * time.Second

// harness runs one Server and lets a test play one or more Agents against it.
// Every handler goroutine it starts is joined by Close, so the package's
// goroutine-leak check has something to observe.
type harness struct {
	t     *testing.T
	srv   *tunnelserver.Server
	ca    *gatewaytest.CA
	clock *gatewaytest.Clock

	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup

	mu      sync.Mutex
	streams []interface{ Break(error) }
}

func newHarness(t *testing.T, cfg tunnelserver.Config) *harness {
	t.Helper()
	clock := gatewaytest.NewClock()
	if cfg.ReplicaID == "" {
		cfg.ReplicaID = "replica-a"
	}
	cfg.Clock = clock
	srv, err := tunnelserver.New(cfg)
	if err != nil {
		t.Fatalf("tunnelserver.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, srv: srv, ca: gatewaytest.NewCA(t), clock: clock, ctx: ctx, cancel: cancel}
	t.Cleanup(h.Close)
	return h
}

// Close tears every stream down and waits for the handlers to return.
func (h *harness) Close() {
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

func (h *harness) track(s interface{ Break(error) }) {
	h.mu.Lock()
	h.streams = append(h.streams, s)
	h.mu.Unlock()
}

// control is one Agent's Control stream, with the handshake already done.
type control struct {
	stream *gatewaytest.ControlStream
	ack    *tunnelv1.HelloAck
	// errc receives the handler's return value once it exits.
	errc chan error
}

// connect starts a Control handler and completes the Hello handshake.
func (h *harness) connect(nodeID string, runtimeIDs ...string) *control {
	h.t.Helper()
	c := h.startControl(nodeID, &tunnelv1.Hello{
		NodeId:       nodeID,
		AgentVersion: "test",
		RuntimeIds:   runtimeIDs,
	})
	frame := c.expect(h.t)
	ack := frame.GetAck()
	if ack == nil {
		h.t.Fatalf("first gateway frame = %T, want HelloAck", frame.GetBody())
	}
	c.ack = ack
	return c
}

// startControl starts a Control handler and sends hello, without requiring the
// handshake to succeed.
func (h *harness) startControl(certNodeID string, hello *tunnelv1.Hello) *control {
	h.t.Helper()
	stream := gatewaytest.NewControlStream(h.ctx, certNodeID, h.ca)
	h.track(stream)
	c := &control{stream: stream, errc: make(chan error, 1)}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		c.errc <- h.srv.Control(stream)
	}()
	if hello != nil {
		_ = stream.FromAgent(&tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Hello{Hello: hello}})
	}
	return c
}

// expect returns the next frame the replica sent, failing the test on timeout.
func (c *control) expect(t *testing.T) *tunnelv1.GatewayControl {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	frame, err := c.stream.ToAgent(ctx)
	if err != nil {
		t.Fatalf("waiting for a gateway control frame: %v", err)
	}
	return frame
}

// send plays one Agent control frame.
func (c *control) send(t *testing.T, frame *tunnelv1.AgentControl) {
	t.Helper()
	if err := c.stream.FromAgent(frame); err != nil {
		t.Fatalf("sending an agent control frame: %v", err)
	}
}

// wait returns the handler's error once it has exited.
func (c *control) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-c.errc:
		return err
	case <-time.After(testTimeout):
		t.Fatal("control handler did not exit")
		return nil
	}
}

// agentSlot is one Agent-side slot: a Serve handler plus a goroutine that
// answers dispatched requests with a scripted handler.
type agentSlot struct {
	stream *gatewaytest.ServeStream
	errc   chan error
	slotID string
}

// slotHandler answers one dispatched request. It receives the request headers
// and a reply function for response frames, and returns the ResponseEnd error
// (nil for success). Returning without replying at all is a valid, if terse,
// answer: it is what a runtime that produced nothing looks like.
type slotHandler func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error

// openSlot starts a Serve handler, parks the slot with Ready, and serves
// requests with handle until the stream ends.
func (h *harness) openSlot(nodeID string, class tunnelv1.SlotClass, slotID string, handle slotHandler) *agentSlot {
	h.t.Helper()
	stream := gatewaytest.NewServeStream(h.ctx, nodeID, h.ca)
	h.track(stream)
	s := &agentSlot{stream: stream, errc: make(chan error, 1), slotID: slotID}

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		s.errc <- h.srv.Serve(stream)
	}()

	if err := stream.FromAgent(&tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Ready{Ready: &tunnelv1.Ready{
		NodeId: nodeID, SlotId: slotID, Class: class,
	}}}); err != nil {
		h.t.Fatalf("parking slot %s: %v", slotID, err)
	}

	if handle != nil {
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			s.run(h.ctx, nodeID, handle)
		}()
	}
	return s
}

// run is the Agent's frame loop for one slot: take a request, run the handler,
// terminate it with ResponseEnd, and re-park.
func (s *agentSlot) run(ctx context.Context, nodeID string, handle slotHandler) {
	for {
		frame, err := s.stream.ToAgent(ctx)
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
			next, err := s.stream.ToAgent(ctx)
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
			return s.stream.FromAgent(f)
		}
		handlerErr := handle(headers, body, reply)
		end := &tunnelv1.AgentFrame{
			RequestId: requestID,
			Body:      &tunnelv1.AgentFrame_End{End: &tunnelv1.ResponseEnd{Error: toTunnelError(handlerErr)}},
		}
		if err := s.stream.FromAgent(end); err != nil {
			return
		}
		if err := s.stream.FromAgent(&tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Ready{Ready: &tunnelv1.Ready{
			NodeId: nodeID, SlotId: s.slotID, Class: tunnelv1.SlotClass_SLOT_CLASS_INFERENCE,
		}}}); err != nil {
			return
		}
	}
}

// wait returns the Serve handler's error once it has exited.
func (s *agentSlot) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-s.errc:
		return err
	case <-time.After(testTimeout):
		t.Fatal("serve handler did not exit")
		return nil
	}
}

// dataFrame builds a DataChunk response frame.
func dataFrame(payload []byte) *tunnelv1.AgentFrame {
	return &tunnelv1.AgentFrame{Body: &tunnelv1.AgentFrame_Data{Data: &tunnelv1.DataChunk{Payload: payload}}}
}

// toTunnelError renders a handler error as the wire error, or nil on success.
// The test harness carries the error itself rather than reusing the codec, so
// a bug in the codec cannot make a protocol test pass.
func toTunnelError(err error) *tunnelv1.TunnelError {
	if err == nil {
		return nil
	}
	var we *wireError
	if errors.As(err, &we) {
		return &tunnelv1.TunnelError{
			Code:      we.code,
			Message:   we.message,
			Retryable: we.retryable,
			Cause:     we.cause,
		}
	}
	return &tunnelv1.TunnelError{Code: "upstream_error", Message: err.Error()}
}

// wireError lets a scripted handler dictate the exact TunnelError an Agent
// would put on the wire.
type wireError struct {
	code      string
	message   string
	retryable bool
	cause     string
}

func (e *wireError) Error() string { return e.code + ": " + e.message }

// waitFor polls cond until it holds or the timeout expires. It exists for the
// few assertions that observe state a handler goroutine sets — a node going
// live, a slot re-parking — where there is no frame to synchronize on.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
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

// Package gatewaytest provides in-memory gRPC streams and node certificates for
// testing the Gateway's tunnel server without a listener, a port or a real
// handshake. A test plays the Agent by hand, frame by frame, which is what
// makes protocol faults — a Ready during a request, a frame for a request that
// already ended — expressible at all.
package gatewaytest

import (
	"context"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// ErrBroken is the failure a stream reports once Break has torn it down.
var ErrBroken = errors.New("gatewaytest: stream broken")

// base carries the machinery both stream kinds share: a context, a teardown
// signal, and the "the Agent half-closed" flag.
type base struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	err    error
	broken chan struct{}
	// agentEOF is closed by CloseSend. It is a channel rather than a flag
	// because a Recv already blocked when the Agent half-closes must wake up
	// and report io.EOF, exactly as gRPC does.
	agentEOF  chan struct{}
	closeOnce sync.Once
}

func newBase(ctx context.Context) *base {
	ctx, cancel := context.WithCancel(ctx)
	return &base{ctx: ctx, cancel: cancel, broken: make(chan struct{}), agentEOF: make(chan struct{})}
}

// Break tears the stream down the way a dropped connection would. A nil err
// becomes ErrBroken. Breaking twice keeps the first error.
func (b *base) Break(err error) {
	if err == nil {
		err = ErrBroken
	}
	b.mu.Lock()
	if b.err == nil {
		b.err = err
		close(b.broken)
	}
	b.mu.Unlock()
	b.cancel()
}

// CloseSend makes the next server-side Recv return io.EOF, which is how gRPC
// reports that the client half-closed.
func (b *base) CloseSend() {
	b.closeOnce.Do(func() { close(b.agentEOF) })
}

func (b *base) failure() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		return ErrBroken
	}
	return b.err
}

// serverStream satisfies the parts of grpc.ServerStream the generated code
// embeds. Only Context is ever called by the server under test; the rest
// return errors rather than panicking so a mistaken call is a test failure
// with a readable message.
type serverStream struct{}

func (s serverStream) SetHeader(metadata.MD) error {
	return errors.New("gatewaytest: SetHeader unsupported")
}
func (s serverStream) SendHeader(metadata.MD) error {
	return errors.New("gatewaytest: SendHeader unsupported")
}
func (s serverStream) SetTrailer(metadata.MD)   {}
func (s serverStream) Context() context.Context { return context.Background() }
func (s serverStream) SendMsg(any) error        { return errors.New("gatewaytest: SendMsg unsupported") }
func (s serverStream) RecvMsg(any) error        { return errors.New("gatewaytest: RecvMsg unsupported") }

var _ grpc.ServerStream = serverStream{}

// -----------------------------------------------------------------------
// Control
// -----------------------------------------------------------------------

// ControlStream is one Control stream. The Send/Recv pair is the server side,
// driven by the handler under test; FromAgent/ToTest is the Agent side, driven
// by the test.
type ControlStream struct {
	serverStream
	*base
	fromAgent chan *tunnelv1.AgentControl
	toAgent   chan *tunnelv1.GatewayControl
}

// NewControlStream returns a Control stream whose peer is authenticated as
// nodeID. Buffering the Gateway-to-Agent direction keeps a test from having to
// read HelloAck, SlotHint and the roster in lockstep with the handshake.
func NewControlStream(ctx context.Context, nodeID string, ca *CA) *ControlStream {
	ctx = ca.PeerContext(ctx, nodeID)
	b := newBase(ctx)
	return &ControlStream{
		base:      b,
		fromAgent: make(chan *tunnelv1.AgentControl),
		toAgent:   make(chan *tunnelv1.GatewayControl, 16),
	}
}

// NewControlStreamWithContext returns a Control stream on a caller-supplied
// context, for tests that need to install their own peer information.
func NewControlStreamWithContext(ctx context.Context) *ControlStream {
	b := newBase(ctx)
	return &ControlStream{
		base:      b,
		fromAgent: make(chan *tunnelv1.AgentControl),
		toAgent:   make(chan *tunnelv1.GatewayControl, 16),
	}
}

// Context reports the stream context, carrying the peer information the
// server authenticates from.
func (s *ControlStream) Context() context.Context { return s.base.ctx }

// Send implements the server side of the stream.
func (s *ControlStream) Send(frame *tunnelv1.GatewayControl) error {
	select {
	case s.toAgent <- frame:
		return nil
	case <-s.broken:
		return s.failure()
	case <-s.base.ctx.Done():
		return s.base.ctx.Err()
	}
}

// Recv implements the server side of the stream.
func (s *ControlStream) Recv() (*tunnelv1.AgentControl, error) {
	select {
	case frame := <-s.fromAgent:
		return frame, nil
	case <-s.agentEOF:
		return nil, io.EOF
	case <-s.broken:
		return nil, s.failure()
	case <-s.base.ctx.Done():
		return nil, s.base.ctx.Err()
	}
}

// FromAgent plays one Agent frame into the stream.
func (s *ControlStream) FromAgent(frame *tunnelv1.AgentControl) error {
	select {
	case s.fromAgent <- frame:
		return nil
	case <-s.broken:
		return s.failure()
	case <-s.base.ctx.Done():
		return s.base.ctx.Err()
	}
}

// ToAgent returns the next frame the server sent, blocking until one arrives
// or ctx is done.
func (s *ControlStream) ToAgent(ctx context.Context) (*tunnelv1.GatewayControl, error) {
	select {
	case frame := <-s.toAgent:
		return frame, nil
	case <-s.broken:
		return nil, s.failure()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var _ tunnelv1.Tunnel_ControlServer = (*ControlStream)(nil)

// -----------------------------------------------------------------------
// Serve
// -----------------------------------------------------------------------

// ServeStream is one data-plane slot stream.
type ServeStream struct {
	serverStream
	*base
	fromAgent chan *tunnelv1.AgentFrame
	toAgent   chan *tunnelv1.GatewayFrame
}

// NewServeStream returns a slot stream whose peer is authenticated as nodeID.
func NewServeStream(ctx context.Context, nodeID string, ca *CA) *ServeStream {
	ctx = ca.PeerContext(ctx, nodeID)
	b := newBase(ctx)
	return &ServeStream{
		base:      b,
		fromAgent: make(chan *tunnelv1.AgentFrame),
		toAgent:   make(chan *tunnelv1.GatewayFrame, 16),
	}
}

// Context reports the stream context, carrying the peer information the
// server authenticates from.
func (s *ServeStream) Context() context.Context { return s.base.ctx }

// Send implements the server side of the stream.
func (s *ServeStream) Send(frame *tunnelv1.GatewayFrame) error {
	select {
	case s.toAgent <- frame:
		return nil
	case <-s.broken:
		return s.failure()
	case <-s.base.ctx.Done():
		return s.base.ctx.Err()
	}
}

// Recv implements the server side of the stream.
func (s *ServeStream) Recv() (*tunnelv1.AgentFrame, error) {
	select {
	case frame := <-s.fromAgent:
		return frame, nil
	case <-s.agentEOF:
		return nil, io.EOF
	case <-s.broken:
		return nil, s.failure()
	case <-s.base.ctx.Done():
		return nil, s.base.ctx.Err()
	}
}

// FromAgent plays one Agent frame into the slot.
func (s *ServeStream) FromAgent(frame *tunnelv1.AgentFrame) error {
	select {
	case s.fromAgent <- frame:
		return nil
	case <-s.broken:
		return s.failure()
	case <-s.base.ctx.Done():
		return s.base.ctx.Err()
	}
}

// ToAgent returns the next frame the server sent on this slot.
func (s *ServeStream) ToAgent(ctx context.Context) (*tunnelv1.GatewayFrame, error) {
	select {
	case frame := <-s.toAgent:
		return frame, nil
	case <-s.broken:
		return nil, s.failure()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var _ tunnelv1.Tunnel_ServeServer = (*ServeStream)(nil)

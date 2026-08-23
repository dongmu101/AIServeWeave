package tunneltest

import (
	"context"
	"errors"
	"io"
	"sync"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// Gateway is an in-memory stand-in for one Gateway replica's TunnelService.
// It implements tunnel.Transport, so a Client under test opens Control
// streams against it without a listener, a port or TLS; the test then plays
// the replica by hand, deciding frame by frame what the Agent sees.
//
// Every stream the Agent opens is queued for AcceptControl, so a test that
// exercises reconnection observes each attempt separately.
type Gateway struct {
	mu         sync.Mutex
	dialErr    error
	dials      int
	sessions   chan *ControlSession
	serveErr   error
	serveDials int
	serves     chan *ServeSession
	closed     bool
}

// NewGateway returns a Gateway that accepts Control and Serve streams.
func NewGateway() *Gateway {
	return &Gateway{
		sessions: make(chan *ControlSession, 32),
		serves:   make(chan *ServeSession, 64),
	}
}

// Control implements tunnel.Transport: it opens one Control stream, or fails
// with whatever SetDialError installed.
func (g *Gateway) Control(ctx context.Context) (tunnel.ControlStream, error) {
	g.mu.Lock()
	g.dials++
	err := g.dialErr
	closed := g.closed
	g.mu.Unlock()

	switch {
	case closed:
		return nil, errors.New("tunneltest: gateway closed")
	case err != nil:
		return nil, err
	}

	s := newControlSession(ctx)
	select {
	case g.sessions <- s:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close implements tunnel.Transport.
func (g *Gateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	return nil
}

// SetDialError makes every subsequent Control call fail with err, simulating
// a replica that is unreachable. Passing nil restores normal service.
func (g *Gateway) SetDialError(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dialErr = err
}

// Dials reports how many times the Agent has tried to open a Control stream,
// successfully or not.
func (g *Gateway) Dials() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dials
}

// AcceptControl returns the next Control stream the Agent opened, blocking
// until one arrives or ctx is done.
func (g *Gateway) AcceptControl(ctx context.Context) (*ControlSession, error) {
	select {
	case s := <-g.sessions:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ControlSession is one Control stream: the Agent side satisfies
// tunnel.ControlStream, and the Send/Recv pair named "…ToAgent" and
// "…FromAgent" is the replica side the test drives.
type ControlSession struct {
	ctx       context.Context
	toAgent   chan *tunnelv1.GatewayControl
	fromAgent chan *tunnelv1.AgentControl

	mu         sync.Mutex
	err        error
	broken     chan struct{}
	sendClosed bool
}

func newControlSession(ctx context.Context) *ControlSession {
	return &ControlSession{
		ctx:       ctx,
		toAgent:   make(chan *tunnelv1.GatewayControl, 32),
		fromAgent: make(chan *tunnelv1.AgentControl, 32),
		broken:    make(chan struct{}),
	}
}

// Send implements tunnel.ControlStream: the Agent writes one frame.
func (s *ControlSession) Send(frame *tunnelv1.AgentControl) error {
	s.mu.Lock()
	closed := s.sendClosed
	s.mu.Unlock()
	if closed {
		return errors.New("tunneltest: send on a half-closed stream")
	}
	select {
	case s.fromAgent <- frame:
		return nil
	case <-s.broken:
		return s.failure()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// Recv implements tunnel.ControlStream: the Agent reads one frame, blocking
// until the replica sends one, breaks the stream, or the stream context ends.
func (s *ControlSession) Recv() (*tunnelv1.GatewayControl, error) {
	select {
	case frame := <-s.toAgent:
		return frame, nil
	case <-s.broken:
		return nil, s.failure()
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

// CloseSend implements tunnel.ControlStream.
func (s *ControlSession) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendClosed = true
	return nil
}

// SendToAgent plays a replica frame into the stream.
func (s *ControlSession) SendToAgent(frame *tunnelv1.GatewayControl) error {
	select {
	case s.toAgent <- frame:
		return nil
	case <-s.broken:
		return s.failure()
	}
}

// RecvFromAgent returns the next frame the Agent sent, blocking until one
// arrives or ctx is done.
func (s *ControlSession) RecvFromAgent(ctx context.Context) (*tunnelv1.AgentControl, error) {
	select {
	case frame := <-s.fromAgent:
		return frame, nil
	case <-s.broken:
		return nil, s.failure()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Break tears the stream down with err, the way a dropped TCP connection or a
// replica restart would. A nil err becomes io.EOF, which is what gRPC reports
// when the server closes a stream cleanly. Breaking twice keeps the first
// error.
func (s *ControlSession) Break(err error) {
	if err == nil {
		err = io.EOF
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	s.err = err
	close(s.broken)
}

// SendClosed reports whether the Agent has half-closed its side, which is how
// it signals that draining has finished.
func (s *ControlSession) SendClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendClosed
}

func (s *ControlSession) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		return io.EOF
	}
	return s.err
}

// -----------------------------------------------------------------------
// Serve streams
// -----------------------------------------------------------------------

// Serve implements tunnel.Transport: it opens one data-plane slot stream, or
// fails with whatever SetServeError installed. Every stream is queued for
// AcceptServe, so a test sees each slot the pool opens separately.
func (g *Gateway) Serve(ctx context.Context) (tunnel.ServeStream, error) {
	g.mu.Lock()
	g.serveDials++
	err := g.serveErr
	closed := g.closed
	g.mu.Unlock()

	switch {
	case closed:
		return nil, errors.New("tunneltest: gateway closed")
	case err != nil:
		return nil, err
	}

	s := newServeSession(ctx)
	select {
	case g.serves <- s:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SetServeError makes every subsequent Serve call fail with err, simulating a
// replica that accepts control traffic but no slots. Passing nil restores
// normal service.
func (g *Gateway) SetServeError(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.serveErr = err
}

// ServeDials reports how many slot streams the Agent has tried to open.
func (g *Gateway) ServeDials() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.serveDials
}

// AcceptServe returns the next slot stream the Agent opened, blocking until
// one arrives or ctx is done.
func (g *Gateway) AcceptServe(ctx context.Context) (*ServeSession, error) {
	select {
	case s := <-g.serves:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ServeSession is one slot stream: the Agent side satisfies
// tunnel.ServeStream, and the "…ToAgent"/"…FromAgent" pair is the replica
// side the test drives.
type ServeSession struct {
	ctx       context.Context
	toAgent   chan *tunnelv1.GatewayFrame
	fromAgent chan *tunnelv1.AgentFrame

	mu         sync.Mutex
	err        error
	broken     chan struct{}
	sendClosed bool
}

func newServeSession(ctx context.Context) *ServeSession {
	return &ServeSession{
		ctx:       ctx,
		toAgent:   make(chan *tunnelv1.GatewayFrame, 32),
		fromAgent: make(chan *tunnelv1.AgentFrame, 256),
		broken:    make(chan struct{}),
	}
}

// Send implements tunnel.ServeStream: the Agent writes one frame.
func (s *ServeSession) Send(frame *tunnelv1.AgentFrame) error {
	s.mu.Lock()
	closed := s.sendClosed
	s.mu.Unlock()
	if closed {
		return errors.New("tunneltest: send on a half-closed stream")
	}
	select {
	case s.fromAgent <- frame:
		return nil
	case <-s.broken:
		return s.failure()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// Recv implements tunnel.ServeStream.
func (s *ServeSession) Recv() (*tunnelv1.GatewayFrame, error) {
	select {
	case frame := <-s.toAgent:
		return frame, nil
	case <-s.broken:
		return nil, s.failure()
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

// CloseSend implements tunnel.ServeStream.
func (s *ServeSession) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendClosed = true
	return nil
}

// SendToAgent plays a replica frame into the slot.
func (s *ServeSession) SendToAgent(frame *tunnelv1.GatewayFrame) error {
	select {
	case s.toAgent <- frame:
		return nil
	case <-s.broken:
		return s.failure()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// RecvFromAgent returns the next frame the Agent sent on this slot.
func (s *ServeSession) RecvFromAgent(ctx context.Context) (*tunnelv1.AgentFrame, error) {
	select {
	case frame := <-s.fromAgent:
		return frame, nil
	case <-s.broken:
		return nil, s.failure()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Break tears the slot down with err, the way a dropped connection would.
func (s *ServeSession) Break(err error) {
	if err == nil {
		err = io.EOF
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	s.err = err
	close(s.broken)
}

// Closed reports whether the slot's stream has ended, which is how a test
// observes that a slot was retired, reaped or aborted.
func (s *ServeSession) Closed() bool {
	select {
	case <-s.ctx.Done():
		return true
	case <-s.broken:
		return true
	default:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.sendClosed
	}
}

func (s *ServeSession) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		return io.EOF
	}
	return s.err
}

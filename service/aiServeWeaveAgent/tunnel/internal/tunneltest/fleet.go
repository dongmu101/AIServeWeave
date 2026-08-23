package tunneltest

import (
	"context"
	"errors"
	"sync"

	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// Fleet is a set of in-memory Gateway replicas addressed by endpoint, which
// is what the connection table needs to be exercised: several replicas that
// can be added, made unreachable and killed independently, with no listener,
// no TLS and no port anywhere.
//
// Any endpoint the Agent dials gets a Gateway, created on demand — a roster
// naming a replica the test never registered must still be dialable, because
// that is exactly what happens when the Gateway scales out.
type Fleet struct {
	mu       sync.Mutex
	gateways map[string]*Gateway
	dials    map[string]int
}

// ErrUnreachable is what a Fleet transport reports for a replica a test has
// taken down. It carries no gRPC status, so the tunnel treats it as an
// ordinary transport failure and backs off, exactly as it would for a
// replica that is simply not answering.
var ErrUnreachable = errors.New("tunneltest: replica unreachable")

// NewFleet returns an empty fleet.
func NewFleet() *Fleet {
	return &Fleet{
		gateways: map[string]*Gateway{},
		dials:    map[string]int{},
	}
}

// Add registers a replica at endpoint and returns its Gateway, so a test can
// accept its streams and play frames into them.
func (f *Fleet) Add(endpoint string) *Gateway {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gatewayLocked(endpoint)
}

// Get returns the Gateway at endpoint, creating it if the Agent has not
// dialled it yet.
func (f *Fleet) Get(endpoint string) *Gateway {
	return f.Add(endpoint)
}

func (f *Fleet) gatewayLocked(endpoint string) *Gateway {
	gw, ok := f.gateways[endpoint]
	if !ok {
		gw = NewGateway()
		f.gateways[endpoint] = gw
	}
	return gw
}

// Unreachable makes every subsequent connection to endpoint fail, standing in
// for a replica that is down, restarting or behind a broken network path.
// Reachable puts it back.
func (f *Fleet) Unreachable(endpoint string) {
	f.Add(endpoint).SetDialError(ErrUnreachable)
	f.Add(endpoint).SetServeError(ErrUnreachable)
}

// Reachable restores normal service at endpoint.
func (f *Fleet) Reachable(endpoint string) {
	f.Add(endpoint).SetDialError(nil)
	f.Add(endpoint).SetServeError(nil)
}

// Transport implements tunnel.TransportFactory. The identity is ignored: the
// fleet authenticates nobody, which is what keeps these tests free of
// certificates.
func (f *Fleet) Transport(endpoint string, _ *tunnel.Identity) (tunnel.Transport, error) {
	f.mu.Lock()
	f.dials[endpoint]++
	gw := f.gatewayLocked(endpoint)
	f.mu.Unlock()
	return &fleetTransport{gateway: gw}, nil
}

// Dials reports how many transports have been opened to endpoint, which is
// how a test sees a tunnel being rebuilt.
func (f *Fleet) Dials(endpoint string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials[endpoint]
}

// Endpoints lists every endpoint the Agent has dialled or a test registered.
func (f *Fleet) Endpoints() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.gateways))
	for endpoint := range f.gateways {
		out = append(out, endpoint)
	}
	return out
}

// fleetTransport is one Agent-to-replica transport. It delegates to the
// Gateway but keeps its own Close, so closing one replica's transport does
// not stop the replica answering the next tunnel opened to it — a real
// gateway outlives any one connection to it.
type fleetTransport struct {
	gateway *Gateway

	mu     sync.Mutex
	closed bool
}

func (t *fleetTransport) Control(ctx context.Context) (tunnel.ControlStream, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	return t.gateway.Control(ctx)
}

func (t *fleetTransport) Serve(ctx context.Context) (tunnel.ServeStream, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	return t.gateway.Serve(ctx)
}

func (t *fleetTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}

func (t *fleetTransport) check() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("tunneltest: transport closed")
	}
	return nil
}

var _ tunnel.TransportFactory = (*Fleet)(nil).Transport

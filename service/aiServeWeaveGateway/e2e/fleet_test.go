package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// waitTimeout bounds every "has it connected yet" wait. Real TLS handshakes
// and real reconnect backoff are involved, so it is measured in seconds.
const waitTimeout = 20 * time.Second

// replica is one Gateway process's worth of tunnel termination: a listener, a
// gRPC server and the tunnelserver that owns the node table.
type replica struct {
	id     string
	addr   string
	server *tunnelserver.Server
	grpc   *grpc.Server
	lis    net.Listener
	done   chan struct{}
}

// stop kills the replica the way a process dying would: the listener closes
// and every stream on it is torn down without a goodbye.
func (r *replica) stop() {
	r.grpc.Stop()
	<-r.done
}

// fleet is three Gateway replicas plus one Agent, wired together over
// loopback TCP with mutual TLS.
type fleet struct {
	t        *testing.T
	pki      *pki
	replicas []*replica

	manager   runtime.Manager
	tunnels   *tunnel.Manager
	tunnelWG  sync.WaitGroup
	tunnelErr chan error

	cancel context.CancelFunc
}

// newFleet starts replicaCount replicas and one Agent connected to all of
// them, with one runtime instance registered and healthy.
func newFleet(t *testing.T, replicaCount int) *fleet {
	t.Helper()
	p := newPKI(t)
	f := &fleet{t: t, pki: p, tunnelErr: make(chan error, 1)}

	for i := range replicaCount {
		f.replicas = append(f.replicas, f.startReplica(fmt.Sprintf("replica-%d", i)))
	}

	seeds := make([]string, 0, len(f.replicas))
	for _, r := range f.replicas {
		seeds = append(seeds, r.addr)
	}
	f.startAgent(seeds)

	t.Cleanup(f.close)
	return f
}

func (f *fleet) startReplica(id string) *replica {
	f.t.Helper()
	return newReplicaOn(f.t, f.pki, id, "", 0)
}

// newReplicaOn starts one Gateway replica's tunnel termination listening on
// addr ("" picks an ephemeral 127.0.0.1 port), signed by p. heartbeatTimeout
// overrides tunnelserver.Config.HeartbeatTimeout (0 keeps its 45s default) —
// fault-injection tests that simulate a severed link need that timeout
// shortened to seconds, or waiting for the real default would make the test
// itself the slow part.
//
// It is a free function rather than a *fleet method so fault-injection tests
// can build replicas without the rest of a fleet's Agent wiring.
func newReplicaOn(t *testing.T, p *pki, id, addr string, heartbeatTimeout time.Duration) *replica {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listening on %s: %v", addr, err)
	}
	srv, err := tunnelserver.New(tunnelserver.Config{
		ReplicaID:        id,
		Logger:           slog.New(slog.DiscardHandler),
		SlotHint:         &tunnelv1.SlotHint{MinSlots: 2, MaxSlots: 4, BulkSlots: 1},
		HeartbeatTimeout: heartbeatTimeout,
	})
	if err != nil {
		t.Fatalf("tunnelserver.New: %v", err)
	}
	gs := grpc.NewServer(grpc.Creds(credentials.NewTLS(p.serverTLS())))
	tunnelv1.RegisterTunnelServer(gs, srv)

	r := &replica{id: srv.ReplicaID(), addr: lis.Addr().String(), server: srv, grpc: gs, lis: lis, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		_ = gs.Serve(lis)
	}()
	return r
}

func (f *fleet) startAgent(seeds []string) {
	f.t.Helper()
	dir := f.t.TempDir()
	certFile, keyFile, caFile := f.pki.writeNodeIdentity(dir, "mac-mini-01")

	deps := runtime.Dependencies{
		HTTPClient: &http.Client{},
		Clock:      runtime.NewSystemClock(),
		Logger:     slog.New(slog.DiscardHandler),
		Metrics:    discardMetrics{},
	}
	registry := runtime.NewRegistry()
	if err := registry.Register(backendKind, newBackend); err != nil {
		f.t.Fatalf("registering the scripted backend: %v", err)
	}
	f.manager = runtime.NewManager(registry, deps)

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel

	if err := f.manager.Add(ctx, runtime.Config{
		ID:            "backend-1",
		Kind:          backendKind,
		BaseURL:       "http://127.0.0.1:1",
		MaxConcurrent: 8,
	}); err != nil {
		f.t.Fatalf("adding the backend: %v", err)
	}

	// The Registry endpoint is never dialed: the certificate on disk is valid
	// and nowhere near renewal, which is exactly the offline-issuance path
	// that lets an Agent run before a Registry exists.
	connector, err := tunnel.NewGRPCRegistryConnector("127.0.0.1:1", f.pki.caPEM)
	if err != nil {
		f.t.Fatalf("registry connector: %v", err)
	}
	identities, err := tunnel.NewIdentityManager(tunnel.IdentityConfig{
		NodeID:           "mac-mini-01",
		RegistryEndpoint: "127.0.0.1:1",
		CertFile:         certFile,
		KeyFile:          keyFile,
		CAFile:           caFile,
		AgentVersion:     "e2e",
		Logger:           slog.New(slog.DiscardHandler),
	}, connector)
	if err != nil {
		f.t.Fatalf("identity manager: %v", err)
	}
	identity, err := identities.Ensure(ctx)
	if err != nil {
		f.t.Fatalf("loading the node identity: %v", err)
	}

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         f.manager,
		NodeID:          identity.NodeID,
		AllowedRuntimes: []string{"backend-1"},
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		f.t.Fatalf("dispatcher: %v", err)
	}

	tunnels, err := tunnel.NewManager(tunnel.ManagerConfig{
		Client: tunnel.ClientConfig{
			NodeID:          identity.NodeID,
			AgentVersion:    "e2e",
			Manager:         f.manager,
			AllowedRuntimes: []string{"backend-1"},
			Handler:         dispatcher,
			Logger:          slog.New(slog.DiscardHandler),
		},
		SeedEndpoints: seeds,
		Identities:    identities,
		Logger:        slog.New(slog.DiscardHandler),
	})
	if err != nil {
		f.t.Fatalf("tunnel manager: %v", err)
	}
	f.tunnels = tunnels

	f.tunnelWG.Add(1)
	go func() {
		defer f.tunnelWG.Done()
		f.tunnelErr <- tunnels.Run(ctx)
	}()
}

func (f *fleet) close() {
	f.cancel()
	f.tunnelWG.Wait()
	for _, r := range f.replicas {
		r.grpc.Stop()
		<-r.done
	}
	shutdown, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	_ = f.manager.Close(shutdown)
}

// awaitReady blocks until every named replica holds a live node with at least
// one parked inference slot, which is the point at which it can serve.
func (f *fleet) awaitReady(replicas ...*replica) {
	f.t.Helper()
	awaitReplicaReady(f.t, replicas...)
}

// awaitReplicaReady blocks until every named replica holds a live
// "mac-mini-01" node with at least one parked inference slot. It is the free
// function behind fleet.awaitReady, usable by tests that build replicas
// without a *fleet.
func awaitReplicaReady(t *testing.T, replicas ...*replica) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for _, r := range replicas {
		for {
			info, ok := r.server.Node("mac-mini-01")
			if ok && info.Live && info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE] > 0 && len(info.Runtimes) > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("replica %s did not become ready: node=%+v present=%t", r.id, info, ok)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// awaitReadySome blocks until at least one of the replicas is ready and
// returns those that are, for the cases where the fleet is deliberately not
// whole.
func (f *fleet) awaitReadyExcept(dead *replica) []*replica {
	f.t.Helper()
	var alive []*replica
	for _, r := range f.replicas {
		if r != dead {
			alive = append(alive, r)
		}
	}
	f.awaitReady(alive...)
	return alive
}

// discardMetrics satisfies runtime.Metrics without recording anything. The
// metrics themselves have their own tests; here they would only add noise.
type discardMetrics struct{}

func (discardMetrics) Counter(string, map[string]string) runtime.Counter { return discardInstrument{} }
func (discardMetrics) Gauge(string, map[string]string) runtime.Gauge     { return discardInstrument{} }
func (discardMetrics) Histogram(string, map[string]string) runtime.Histogram {
	return discardInstrument{}
}

type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}

// restartReplica brings a replacement process up on the same address the dead
// one held, which is what a rolling upgrade looks like from the Agent's side.
func (f *fleet) restartReplica(dead *replica, addr string) *replica {
	f.t.Helper()
	r := newReplicaOn(f.t, f.pki, dead.id+"-restarted", addr, 0)
	f.replicas = append(f.replicas, r)
	return r
}

// chatRequestPayload marshals a chat request with the codec both sides share.
func chatRequestPayload(req runtime.ChatRequest) ([]byte, error) {
	return tunnelwire.MarshalChatRequest(req)
}

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/ollama"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// This file covers the tunnel README's four remaining fault-injection
// scenarios (拔网线、kill 全部副本、证书过期、后端假死). The README notes their
// blocker is "没有部署", not "没有 Gateway": every collaborator these tests
// need — the tunnel server, the Agent's tunnel client, a real inference
// adapter — already exists, so what is missing is a real network link, a
// real certificate clock and a real hung backend, not more fakes.
//
// This machine has no passwordless sudo, so pfctl (the kernel-level way to
// blackhole a loopback port) cannot be driven non-interactively from a test
// process. Where the README's blast radius is a real link, this file
// reproduces the same *symptom* — bytes stop moving, silently, with no RST —
// in userspace instead: see chaosLink. Everything else (real TCP, real TLS,
// real process/goroutine teardown, real certificate expiry against the wall
// clock, a real HTTP client's timeout against a genuinely unresponsive
// server) is exactly as real as the rest of this package.
//
// Every test here uses real wall-clock time, so they are opt-in like
// TestLiveOllama: set AISW_FAULT_INJECTION=1 to run them.

func requireFaultInjection(t *testing.T) {
	t.Helper()
	if os.Getenv("AISW_FAULT_INJECTION") == "" {
		t.Skip("set AISW_FAULT_INJECTION=1 to run real fault-injection scenarios (real time, real sockets)")
	}
}

// -----------------------------------------------------------------------
// Scenario 1: 拔网线 — the link to one replica goes silent.
// -----------------------------------------------------------------------

// chaosLink is a userspace TCP relay that can go silent on command: once cut,
// it holds every connection open but stops copying bytes in either
// direction. That is what a severed cable looks like to both ends — no RST,
// no FIN, just silence until a timeout notices — which is a closer
// simulation of "拔网线" than closing the socket (that looks like the process
// exiting, i.e. scenario 2).
type chaosLink struct {
	lis    net.Listener
	target string

	mu  sync.Mutex
	cut bool

	stop chan struct{}
	wg   sync.WaitGroup
}

func newChaosLink(t *testing.T, target string) *chaosLink {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("chaos link: listening: %v", err)
	}
	c := &chaosLink{lis: lis, target: target, stop: make(chan struct{})}
	c.wg.Add(1)
	go c.acceptLoop()
	t.Cleanup(c.close)
	return c
}

func (c *chaosLink) acceptLoop() {
	defer c.wg.Done()
	for {
		front, err := c.lis.Accept()
		if err != nil {
			return
		}
		c.wg.Add(1)
		go c.serve(front)
	}
}

func (c *chaosLink) serve(front net.Conn) {
	defer c.wg.Done()
	back, err := net.Dial("tcp", c.target)
	if err != nil {
		front.Close()
		return
	}
	var once sync.Once
	closeBoth := func() { once.Do(func() { front.Close(); back.Close() }) }

	c.wg.Add(2)
	go func() { defer c.wg.Done(); defer closeBoth(); c.relay(back, front) }()
	go func() { defer c.wg.Done(); defer closeBoth(); c.relay(front, back) }()
}

// relay copies from src to dst except while cut, when it holds new reads
// back instead of forwarding or discarding them.
func (c *chaosLink) relay(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		c.mu.Lock()
		cut := c.cut
		c.mu.Unlock()
		if cut {
			select {
			case <-time.After(20 * time.Millisecond):
				continue
			case <-c.stop:
				return
			}
		}
		src.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
	}
}

func (c *chaosLink) setCut(cut bool) {
	c.mu.Lock()
	c.cut = cut
	c.mu.Unlock()
}

func (c *chaosLink) addr() string { return c.lis.Addr().String() }

func (c *chaosLink) close() {
	close(c.stop)
	c.lis.Close()
	c.wg.Wait()
}

func TestFaultInjectionNetworkPartition(t *testing.T) {
	requireFaultInjection(t)

	p := newPKI(t)
	// replica-0's heartbeat timeout is shortened to seconds: it is the one
	// behind the chaos link, and the production 45s default would make the
	// test itself the slowest part of proving detection works.
	replicas := []*replica{
		newReplicaOn(t, p, "replica-0", "", 2*time.Second),
		newReplicaOn(t, p, "replica-1", "", 0),
		newReplicaOn(t, p, "replica-2", "", 0),
	}
	t.Cleanup(func() {
		for _, r := range replicas {
			r.grpc.Stop()
			<-r.done
		}
	})

	// Only replica-0 is reached through the chaos link; the other two are
	// dialed directly, so their tunnels are unaffected by what happens to it.
	link := newChaosLink(t, replicas[0].addr)
	seeds := []string{link.addr(), replicas[1].addr, replicas[2].addr}

	agent, _, _, _ := startFIAgent(t, p, fiAgentConfig{
		seeds:             seeds,
		heartbeatInterval: 300 * time.Millisecond,
		keepalive:         300 * time.Millisecond,
	})
	awaitReplicaReady(t, replicas...)

	cutAt := time.Now()
	link.setCut(true)

	deadline := time.Now().Add(waitTimeout)
	for {
		info, ok := replicas[0].server.Node("mac-mini-01")
		if !ok || !info.Live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replica-0 never noticed the severed link")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("network partition: replica-0 marked the node not-live %v after the link went silent", time.Since(cutAt))

	// User-side behaviour: the surviving replicas keep serving the whole
	// time, on their own independent TCP connections.
	for _, r := range replicas[1:] {
		rt := r.server.Runtime("mac-mini-01", "backend-1")
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		resp, err := rt.Chat(ctx, runtime.ChatRequest{
			Model:    "e2e-model",
			Messages: []runtime.ChatMessage{{Role: "user", Content: "during the partition"}},
		})
		cancel()
		if err != nil {
			t.Errorf("chat through surviving replica %s during the partition: %v", r.id, err)
			continue
		}
		if resp.Message.Content != "echo:during the partition" {
			t.Errorf("replica %s returned %q", r.id, resp.Message.Content)
		}
	}

	restoreAt := time.Now()
	link.setCut(false)
	awaitReplicaReady(t, replicas[0])
	t.Logf("network partition: replica-0 recovered %v after the link came back", time.Since(restoreAt))

	_ = agent
}

// -----------------------------------------------------------------------
// Scenario 2: kill 全部副本 — every Gateway replica dies at once.
// -----------------------------------------------------------------------

func TestFaultInjectionAllReplicasKilled(t *testing.T) {
	requireFaultInjection(t)

	p := newPKI(t)
	replicas := []*replica{
		newReplicaOn(t, p, "replica-0", "", 0),
		newReplicaOn(t, p, "replica-1", "", 0),
		newReplicaOn(t, p, "replica-2", "", 0),
	}
	seeds := []string{replicas[0].addr, replicas[1].addr, replicas[2].addr}

	agent, _, _, _ := startFIAgent(t, p, fiAgentConfig{
		seeds:             seeds,
		heartbeatInterval: 300 * time.Millisecond,
	})
	awaitReplicaReady(t, replicas...)

	killedAt := time.Now()
	for _, r := range replicas {
		r.stop()
	}

	// Every replica is unreachable, which the README calls out as distinct
	// from a certificate failure: nothing here is fatal, so Client.Run must
	// not return — it keeps retrying every replica forever.
	select {
	case err := <-agent.errCh:
		t.Fatalf("tunnels.Run returned %v; every replica being unreachable must not be fatal", err)
	case <-time.After(200 * time.Millisecond):
	}

	replacements := make([]*replica, len(replicas))
	for i, r := range replicas {
		replacements[i] = newReplicaOn(t, p, r.id+"-restarted", r.addr, 0)
	}
	t.Cleanup(func() {
		for _, r := range replacements {
			r.grpc.Stop()
			<-r.done
		}
	})

	awaitReplicaReady(t, replacements...)
	t.Logf("all replicas killed: node totally unreachable, then all %d replicas restarted and rejoined in %v",
		len(replicas), time.Since(killedAt))

	for _, r := range replacements {
		rt := r.server.Runtime("mac-mini-01", "backend-1")
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		resp, err := rt.Chat(ctx, runtime.ChatRequest{
			Model:    "e2e-model",
			Messages: []runtime.ChatMessage{{Role: "user", Content: "after total outage"}},
		})
		cancel()
		if err != nil {
			t.Errorf("chat through restarted replica %s: %v", r.id, err)
			continue
		}
		if resp.Message.Content != "echo:after total outage" {
			t.Errorf("replica %s returned %q", r.id, resp.Message.Content)
		}
	}
}

// -----------------------------------------------------------------------
// Scenario 3: 证书过期 — the node identity expires mid-flight.
// -----------------------------------------------------------------------

func TestFaultInjectionCertificateExpiry(t *testing.T) {
	requireFaultInjection(t)

	p := newPKI(t)
	replicas := []*replica{
		newReplicaOn(t, p, "replica-0", "", 0),
		newReplicaOn(t, p, "replica-1", "", 0),
		newReplicaOn(t, p, "replica-2", "", 0),
	}
	t.Cleanup(func() {
		for _, r := range replicas {
			r.grpc.Stop()
			<-r.done
		}
	})
	seeds := []string{replicas[0].addr, replicas[1].addr, replicas[2].addr}

	const ttl = 6 * time.Second
	agent, certFile, keyFile, caFile := startFIAgent(t, p, fiAgentConfig{
		seeds:             seeds,
		identityTTL:       ttl,
		identityInterval:  1 * time.Second,
		heartbeatInterval: 300 * time.Millisecond,
	})
	awaitReplicaReady(t, replicas...)

	issuedAt := time.Now()
	expiresAt := issuedAt.Add(ttl)
	t.Logf("certificate expiry: issued at %s, NotAfter %s", issuedAt.Format(time.RFC3339), expiresAt.Format(time.RFC3339))

	var runErr error
	select {
	case runErr = <-agent.errCh:
	case <-time.After(ttl + waitTimeout):
		t.Fatal("tunnels.Run did not return after the certificate expired")
	}
	if !tunnel.IsFatal(runErr) {
		t.Fatalf("Run() returned %v, want a fatal identity error", runErr)
	}
	t.Logf("certificate expiry: detected %v after NotAfter, as: %v", time.Since(expiresAt), runErr)

	// User-side behaviour: identity is node-wide, so every replica's route to
	// the node must be gone at once — not "the two replicas whose tunnel
	// happened to reconnect first".
	for _, r := range replicas {
		if _, ok := r.server.Node("mac-mini-01"); ok {
			t.Errorf("replica %s still has a route to the node after the certificate expired", r.id)
		}
	}

	// Recovery: an operator (or, once one exists, a Registry) replaces the
	// three identity files with a freshly issued certificate. tunnel.Manager
	// rejects a second Run on the same instance, so recovering here takes the
	// same shape restarting the Agent process would: a new Manager, the same
	// seeds, the fixed identity files.
	fixAt := time.Now()
	dir := filepath.Dir(certFile)
	newCert, newKey, newCA, _ := writeShortLivedIdentity(t, p, dir, "mac-mini-01", 24*time.Hour)
	if newCert != certFile || newKey != keyFile || newCA != caFile {
		t.Fatalf("recovery wrote different paths than the original identity: %s/%s/%s", newCert, newKey, newCA)
	}

	recovered := startFIAgentWithIdentity(t, p, seeds, certFile, keyFile, caFile, fiAgentConfig{
		heartbeatInterval: 300 * time.Millisecond,
	})
	awaitReplicaReady(t, replicas...)
	t.Logf("certificate expiry: recovered %v after a fresh certificate was written and the tunnel manager restarted",
		time.Since(fixAt))

	_ = recovered
}

// -----------------------------------------------------------------------
// Scenario 4: 后端假死 — the inference backend accepts connections but never
// answers.
// -----------------------------------------------------------------------

// hangingOllamaBackend serves Ollama's two identity endpoints normally
// (so Manager.Add's Probe/Discover succeed while the backend is still
// healthy) but can be told to let /v1/chat/completions hang indefinitely,
// simulating a backend process wedged behind a live TCP connection — no RST,
// no timeout of its own, just a request that never gets a response.
type hangingOllamaBackend struct {
	mu      sync.Mutex
	hanging bool
	release chan struct{}
	srv     *httptest.Server
}

func newHangingOllamaBackend(t *testing.T) *hangingOllamaBackend {
	t.Helper()
	b := &hangingOllamaBackend{hanging: true, release: make(chan struct{})}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "0.1.0-fault-injection"})
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{
				"name": "e2e-model", "model": "e2e-model", "digest": "d1",
				"capabilities": []string{"completion"},
			}},
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		release := b.release
		hanging := b.hanging
		b.mu.Unlock()
		if hanging {
			<-release
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "fi-1", "model": "e2e-model", "created": time.Now().Unix(),
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": "back online"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	})
	b.srv = httptest.NewServer(mux)

	t.Cleanup(func() {
		b.mu.Lock()
		if b.hanging {
			close(b.release)
		}
		b.mu.Unlock()
		b.srv.Close()
	})
	return b
}

// recover flips the backend to answering normally and releases the request
// that was blocked mid-hang, the way a wedged process coming back to life
// would finally reply to whatever was queued.
func (b *hangingOllamaBackend) recover() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hanging {
		b.hanging = false
		close(b.release)
	}
}

func (b *hangingOllamaBackend) url() string { return b.srv.URL }

func TestFaultInjectionBackendHang(t *testing.T) {
	requireFaultInjection(t)

	p := newPKI(t)
	rep := newReplicaOn(t, p, "replica-0", "", 0)
	t.Cleanup(func() { rep.grpc.Stop(); <-rep.done })

	backend := newHangingOllamaBackend(t)

	dir := t.TempDir()
	certFile, keyFile, caFile, _ := writeShortLivedIdentity(t, p, dir, "mac-mini-01", 0)

	deps := runtime.Dependencies{
		HTTPClient: &http.Client{},
		Clock:      runtime.NewSystemClock(),
		Logger:     slog.New(slog.DiscardHandler),
		Metrics:    discardMetrics{},
	}
	registry := runtime.NewRegistry()
	if err := registry.Register(runtime.KindOllama, ollama.New); err != nil {
		t.Fatalf("registering the ollama adapter: %v", err)
	}
	manager := runtime.NewManager(registry, deps)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Registered while the backend answers its identity endpoints normally:
	// Probe/Discover must not themselves hang, or nothing gets far enough to
	// exercise the fault this test is about.
	if err := manager.Add(ctx, runtime.Config{
		ID:             "hung-backend",
		Kind:           runtime.KindOllama,
		BaseURL:        backend.url(),
		MaxConcurrent:  4,
		RequestTimeout: 1 * time.Second,
		ProbeTimeout:   2 * time.Second,
	}); err != nil {
		t.Fatalf("adding the backend while it is healthy: %v", err)
	}
	t.Cleanup(func() {
		shutdown, c := context.WithTimeout(context.Background(), waitTimeout)
		defer c()
		_ = manager.Close(shutdown)
	})

	connector, err := tunnel.NewGRPCRegistryConnector("127.0.0.1:1", p.caPEM)
	if err != nil {
		t.Fatalf("registry connector: %v", err)
	}
	identities, err := tunnel.NewIdentityManager(tunnel.IdentityConfig{
		NodeID:           "mac-mini-01",
		RegistryEndpoint: "127.0.0.1:1",
		CertFile:         certFile,
		KeyFile:          keyFile,
		CAFile:           caFile,
		AgentVersion:     "fault-injection",
		Logger:           slog.New(slog.DiscardHandler),
	}, connector)
	if err != nil {
		t.Fatalf("identity manager: %v", err)
	}
	identity, err := identities.Ensure(ctx)
	if err != nil {
		t.Fatalf("loading the node identity: %v", err)
	}

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         manager,
		NodeID:          identity.NodeID,
		AllowedRuntimes: []string{"hung-backend"},
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}

	tunnels, err := tunnel.NewManager(tunnel.ManagerConfig{
		Client: tunnel.ClientConfig{
			NodeID:            identity.NodeID,
			AgentVersion:      "fault-injection",
			Manager:           manager,
			AllowedRuntimes:   []string{"hung-backend"},
			Handler:           dispatcher,
			Logger:            slog.New(slog.DiscardHandler),
			HeartbeatInterval: 300 * time.Millisecond,
		},
		SeedEndpoints: []string{rep.addr},
		Identities:    identities,
		Logger:        slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("tunnel manager: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = tunnels.Run(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	awaitReplicaReady(t, rep)

	rt := rep.server.Runtime("mac-mini-01", "hung-backend")
	start := time.Now()
	_, err = rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    "e2e-model",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "is anyone home"}},
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("chat against a wedged backend returned success")
	}
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("error = %v (%T), want *runtime.RuntimeError", err, err)
	}
	t.Logf("backend hang: request against a wedged backend failed after %v with code=%s retryable=%t: %v",
		elapsed, rtErr.Code, rtErr.Retryable, err)
	if rtErr.Code != runtime.ErrorTimeout {
		t.Errorf("Code = %s, want %s", rtErr.Code, runtime.ErrorTimeout)
	}
	if !rtErr.Retryable {
		t.Error("Retryable = false; a request that never reached a model must be placeable elsewhere")
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v to notice a hung backend; RequestTimeout=1s should have bounded it far tighter", elapsed)
	}

	// Recovery: the backend comes back for a request issued after it does,
	// proving the earlier stuck call did not wedge the slot or the tunnel for
	// everyone after it.
	recoverAt := time.Now()
	backend.recover()
	resp, err := rt.Chat(context.Background(), runtime.ChatRequest{
		Model:    "e2e-model",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("chat after the backend recovered: %v", err)
	}
	if resp.Message.Content != "back online" {
		t.Errorf("recovered backend returned %q", resp.Message.Content)
	}
	t.Logf("backend hang: first request after recovery succeeded in %v", time.Since(recoverAt))
}

// -----------------------------------------------------------------------
// Shared Agent scaffolding for the fault-injection scenarios above. It
// mirrors fleet.startAgent but exposes the knobs (identity TTL, heartbeat
// cadence, identity re-check interval) those scenarios need to turn
// production-scale timers (hours) into test-scale ones (seconds) without
// touching a fake clock — the certificate and heartbeat deadlines below are
// real wall-clock time, which is the entire point of this file.
// -----------------------------------------------------------------------

type fiAgentConfig struct {
	seeds             []string
	identityTTL       time.Duration // 0 = 24h, matching pki.writeNodeIdentity
	identityInterval  time.Duration // 0 = tunnel.Manager's 1h default
	heartbeatInterval time.Duration // 0 = tunnel.Client's 15s default
	keepalive         time.Duration // 0 = tunnel.NewGRPCTransport's default
}

type fiAgent struct {
	manager runtime.Manager
	tunnels *tunnel.Manager
	cancel  context.CancelFunc
	errCh   chan error
	wg      sync.WaitGroup
}

func (a *fiAgent) stop() {
	a.cancel()
	a.wg.Wait()
	shutdown, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	_ = a.manager.Close(shutdown)
}

// startFIAgent issues a fresh node identity in a new temp directory and
// starts an Agent with it. It returns the identity file paths so a scenario
// can overwrite them to simulate a reissued certificate.
func startFIAgent(t *testing.T, p *pki, cfg fiAgentConfig) (agent *fiAgent, certFile, keyFile, caFile string) {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile, caFile, _ = writeShortLivedIdentity(t, p, dir, "mac-mini-01", cfg.identityTTL)
	agent = startFIAgentWithIdentity(t, p, cfg.seeds, certFile, keyFile, caFile, cfg)
	return agent, certFile, keyFile, caFile
}

// startFIAgentWithIdentity starts an Agent against an already-written
// identity, so a "certificate replaced" recovery step can reuse the same
// files a fresh tunnel.Manager reads them from.
func startFIAgentWithIdentity(t *testing.T, p *pki, seeds []string, certFile, keyFile, caFile string, cfg fiAgentConfig) *fiAgent {
	t.Helper()

	deps := runtime.Dependencies{
		HTTPClient: &http.Client{},
		Clock:      runtime.NewSystemClock(),
		Logger:     slog.New(slog.DiscardHandler),
		Metrics:    discardMetrics{},
	}
	registry := runtime.NewRegistry()
	if err := registry.Register(backendKind, newBackend); err != nil {
		t.Fatalf("registering the scripted backend: %v", err)
	}
	manager := runtime.NewManager(registry, deps)

	ctx, cancel := context.WithCancel(context.Background())

	if err := manager.Add(ctx, runtime.Config{
		ID:            "backend-1",
		Kind:          backendKind,
		BaseURL:       "http://127.0.0.1:1",
		MaxConcurrent: 8,
	}); err != nil {
		t.Fatalf("adding the backend: %v", err)
	}

	connector, err := tunnel.NewGRPCRegistryConnector("127.0.0.1:1", p.caPEM)
	if err != nil {
		t.Fatalf("registry connector: %v", err)
	}
	identities, err := tunnel.NewIdentityManager(tunnel.IdentityConfig{
		NodeID:           "mac-mini-01",
		RegistryEndpoint: "127.0.0.1:1",
		CertFile:         certFile,
		KeyFile:          keyFile,
		CAFile:           caFile,
		AgentVersion:     "fault-injection",
		Logger:           slog.New(slog.DiscardHandler),
	}, connector)
	if err != nil {
		t.Fatalf("identity manager: %v", err)
	}
	identity, err := identities.Ensure(ctx)
	if err != nil {
		t.Fatalf("loading the node identity: %v", err)
	}

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         manager,
		NodeID:          identity.NodeID,
		AllowedRuntimes: []string{"backend-1"},
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}

	managerCfg := tunnel.ManagerConfig{
		Client: tunnel.ClientConfig{
			NodeID:                    identity.NodeID,
			AgentVersion:              "fault-injection",
			Manager:                   manager,
			AllowedRuntimes:           []string{"backend-1"},
			Handler:                   dispatcher,
			Logger:                    slog.New(slog.DiscardHandler),
			HeartbeatInterval:         cfg.heartbeatInterval,
			HeartbeatFailureThreshold: 3,
			BackoffInitial:            50 * time.Millisecond,
			BackoffMax:                500 * time.Millisecond,
		},
		SeedEndpoints:    seeds,
		Identities:       identities,
		IdentityInterval: cfg.identityInterval,
		Logger:           slog.New(slog.DiscardHandler),
	}
	if cfg.keepalive > 0 {
		managerCfg.TransportFactory = func(endpoint string, id *tunnel.Identity) (tunnel.Transport, error) {
			return tunnel.NewGRPCTransport(endpoint, id, cfg.keepalive, cfg.keepalive)
		}
	}

	tunnels, err := tunnel.NewManager(managerCfg)
	if err != nil {
		t.Fatalf("tunnel manager: %v", err)
	}

	a := &fiAgent{manager: manager, tunnels: tunnels, cancel: cancel, errCh: make(chan error, 1)}
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.errCh <- tunnels.Run(ctx) }()
	t.Cleanup(a.stop)
	return a
}

// writeShortLivedIdentity issues a node certificate with a caller-chosen
// validity window instead of pki.writeNodeIdentity's fixed 24h, so certificate
// expiry can be observed inside a test instead of waited out over a real day.
func writeShortLivedIdentity(t *testing.T, p *pki, dir, nodeID string, ttl time.Duration) (certFile, keyFile, caFile string, notAfter time.Time) {
	t.Helper()
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	certFile = filepath.Join(dir, "node.crt")
	keyFile = filepath.Join(dir, "node.key")
	caFile = filepath.Join(dir, "ca.crt")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{tunnel.NodeURI(nodeID)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &key.PublicKey, p.key)
	if err != nil {
		t.Fatalf("signing a short-lived leaf certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the short-lived leaf certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling the node key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	writeFile(t, certFile, certPEM, 0o600)
	writeFile(t, keyFile, keyPEM, 0o600)
	writeFile(t, caFile, p.caPEM, 0o600)
	return certFile, keyFile, caFile, leaf.NotAfter
}

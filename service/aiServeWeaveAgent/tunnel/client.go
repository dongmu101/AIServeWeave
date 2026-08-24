package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// This file assembles one tunnel — one Agent to one Gateway replica — as a
// state machine over a single Control stream, plus the exponential-backoff
// reconnect loop around it. Everything here is deliberately per-replica: the
// connection state, the backoff attempt counter and the heartbeat accounting
// are private to this Client, so a replica that dies or restarts cannot
// perturb the tunnels to its siblings. The connection table that owns several
// Clients arrives in manager.go (阶段 6).

// clientOperation is the Operation recorded on errors raised by the tunnel's
// connection machinery, so a failure is attributable without a request.
const clientOperation = "tunnel_client"

// Default timings, all from README.md. Each can be overridden in ClientConfig.
const (
	defaultHeartbeatInterval  = 15 * time.Second
	defaultHeartbeatThreshold = 3
	defaultStatusFullInterval = 60 * time.Second
	defaultStatusPollInterval = 2 * time.Second
	defaultHelloTimeout       = 10 * time.Second
	defaultDrainTimeout       = 30 * time.Second
	defaultKeepaliveInterval  = 20 * time.Second
	defaultKeepaliveTimeout   = 10 * time.Second
)

// State is one tunnel's connection state. Each replica has its own; the
// aggregate view of a node lives in manager.go, and metrics.go (阶段 7) maps
// these to the numeric `tunnel_connection_state` series.
type State int

const (
	// StateDisconnected means no Control stream exists and none is being
	// established.
	StateDisconnected State = iota
	// StateConnecting means a Control stream is being opened and Hello sent.
	StateConnecting
	// StateConnected means HelloAck arrived: this replica may dispatch.
	StateConnected
	// StateServing means the slot pool is warmed and standing by. The pool
	// sets it in 阶段 4; until then a tunnel stays in StateConnected.
	StateServing
	// StateReconnecting means the stream broke and a backoff delay is being
	// waited out.
	StateReconnecting
	// StateDraining means in-flight requests are being allowed to finish and
	// no new ones are accepted.
	StateDraining
	// StateFailed is the terminal error state: an identity or authentication
	// problem that reconnecting cannot fix. It requires operator action.
	StateFailed
	// StateRetired is the terminal normal state: the roster says this replica
	// is gone. It produces no alert.
	StateRetired
)

// String renders the state for logs and test failures.
func (s State) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateServing:
		return "serving"
	case StateReconnecting:
		return "reconnecting"
	case StateDraining:
		return "draining"
	case StateFailed:
		return "failed"
	case StateRetired:
		return "retired"
	default:
		return "state(" + fmt.Sprint(int(s)) + ")"
	}
}

// ErrFatal marks a tunnel failure that reconnecting cannot fix: a rejected or
// expired certificate, a node_id the replica refuses, a malformed handshake.
// The run loop stops and the tunnel enters StateFailed instead of backing off
// forever. Transient failures — an unreachable replica, a dropped stream —
// carry no such marker and are retried.
var ErrFatal = errors.New("tunnel: fatal failure, operator intervention required")

// IsFatal reports whether err is a failure that requires operator
// intervention rather than a reconnect.
func IsFatal(err error) bool { return errors.Is(err, ErrFatal) }

// ErrShutdownRequested is returned by Run when the replica asked the Agent to
// drain and disconnect. Draining has already completed by then. It is a
// normal outcome, not a failure: whether to reconnect is the roster's call
// (阶段 6), never this Client's.
var ErrShutdownRequested = errors.New("tunnel: gateway requested shutdown")

// ControlStream is the Control bidirectional stream, narrowed to what the
// Agent uses. tunnelv1.Tunnel_ControlClient satisfies it, and so does the
// in-memory fake in internal/tunneltest.
type ControlStream interface {
	Send(*tunnelv1.AgentControl) error
	Recv() (*tunnelv1.GatewayControl, error)
	CloseSend() error
}

// ServeStream is one data-plane slot's bidirectional stream, narrowed to what
// the Agent uses. tunnelv1.Tunnel_ServeClient satisfies it, and so does the
// in-memory fake in internal/tunneltest.
type ServeStream interface {
	Send(*tunnelv1.AgentFrame) error
	Recv() (*tunnelv1.GatewayFrame, error)
	CloseSend() error
}

// Transport opens streams to one Gateway replica: one Control stream for the
// connection state machine, and one Serve stream per data-plane slot.
type Transport interface {
	Control(ctx context.Context) (ControlStream, error)
	Serve(ctx context.Context) (ServeStream, error)
	Close() error
}

// SecretResolver turns an api_key_ref delivered by the control plane into the
// credential itself. The control plane only ever names a secret, so a
// compromised Gateway learns no credentials; how a name is resolved (file,
// environment, external manager) is 待决问题 4 and is entirely this
// interface's implementation's business.
type SecretResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// ClientConfig configures one tunnel. Only Endpoint, NodeID, Transport and
// Manager are required; every timing defaults to the README's values.
type ClientConfig struct {
	// NodeID is the node identity, which must match the certificate SAN. The
	// replica drops the stream if Hello disagrees with the certificate.
	NodeID string
	// Endpoint is the replica's host:port, used for logging and as the
	// replica's identity until HelloAck supplies its replica_id.
	Endpoint string
	// AgentVersion is reported in Hello.
	AgentVersion string
	// Resources describes the node's hardware for the scheduler. It may be
	// nil on a node that does not report hardware.
	Resources *tunnelv1.NodeResources
	// Manager is the local runtime set: its Snapshot feeds status reports and
	// its Add/Replace/Remove apply control-plane configuration.
	Manager runtime.Manager
	// AllowedRuntimes is the local allowlist. Empty means "no local
	// narrowing", so the caller must pass the configured runtime ids for the
	// README's default of "every id in the runtimes section" to hold. A
	// control-plane configuration naming an id outside the list is refused.
	AllowedRuntimes []string
	// Secrets resolves api_key_ref values. A nil resolver refuses any
	// configuration that names one, rather than silently installing a runtime
	// with no credential.
	Secrets SecretResolver

	// HeartbeatInterval is the application-level heartbeat period (15s) and
	// HeartbeatFailureThreshold the number of unanswered heartbeats that
	// declares the replica dead (3).
	HeartbeatInterval         time.Duration
	HeartbeatFailureThreshold int
	// StatusFullInterval is the periodic full reconciliation period (60s);
	// StatusPollInterval is how often Manager.Snapshot is polled for changes
	// worth reporting immediately (2s).
	StatusFullInterval time.Duration
	StatusPollInterval time.Duration
	// HelloTimeout bounds the wait for HelloAck; DrainTimeout bounds how long
	// draining waits for in-flight requests.
	HelloTimeout time.Duration
	DrainTimeout time.Duration
	// BackoffInitial and BackoffMax bound the full-jitter reconnect delay.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	// Jitter supplies backoff jitter in [0, 1); nil uses a seeded source.
	Jitter func() float64

	// Handler executes the requests the replica dispatches into this
	// tunnel's slots. A nil Handler opens no slot pool at all, which is how a
	// tunnel that only carries control traffic is expressed.
	Handler Handler
	// Slots configures this replica's slot pool. The zero value takes every
	// default from the README.
	Slots SlotConfig

	// InFlight reports how many requests are still running, so draining can
	// wait for them. It defaults to the slot pool's own count; an explicit
	// function overrides it.
	InFlight func() int

	// OnState, OnRoster and OnSlotHint are notification hooks. They are
	// called from the tunnel's own goroutine and must not block: OnRoster
	// hands the roster to the connection table (阶段 6) and OnSlotHint hands
	// watermark advice to the slot pool (阶段 4).
	OnState    func(State)
	OnRoster   func(*tunnelv1.GatewayRoster)
	OnSlotHint func(*tunnelv1.SlotHint)

	// Metrics receives this tunnel's instruments. Nil discards them, which
	// is what an agent with no metrics backend configured gets.
	Metrics runtime.Metrics

	// Clock defaults to runtime.NewSystemClock; every timer in the tunnel
	// runs on it so tests drive an hour of behaviour without sleeping.
	Clock runtime.Clock
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

func (c *ClientConfig) validate() error {
	var problems []string
	if c.NodeID == "" {
		problems = append(problems, "node_id is required")
	}
	if err := validateEndpoint("endpoint", c.Endpoint); err != nil {
		problems = append(problems, err.Error())
	}
	if c.Manager == nil {
		problems = append(problems, "manager is required")
	}

	setDefaultDuration(&c.HeartbeatInterval, defaultHeartbeatInterval)
	setDefaultDuration(&c.StatusFullInterval, defaultStatusFullInterval)
	setDefaultDuration(&c.StatusPollInterval, defaultStatusPollInterval)
	setDefaultDuration(&c.HelloTimeout, defaultHelloTimeout)
	setDefaultDuration(&c.DrainTimeout, defaultDrainTimeout)
	if c.HeartbeatFailureThreshold <= 0 {
		c.HeartbeatFailureThreshold = defaultHeartbeatThreshold
	}
	if c.Clock == nil {
		c.Clock = runtime.NewSystemClock()
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}

	if len(problems) > 0 {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			Operation: clientOperation,
			Message:   "invalid tunnel client configuration: " + strings.Join(problems, "; "),
			Cause:     ErrFatal,
		}
	}
	return nil
}

// Client is one tunnel: the Control stream to a single Gateway replica, its
// connection state machine and its reconnect loop.
type Client struct {
	cfg       ClientConfig
	transport Transport
	clock     runtime.Clock
	logger    *slog.Logger
	backoff   *Backoff

	mu    sync.Mutex
	state State
	// metrics is rebound to the replica named by HelloAck, so every sample
	// after the handshake carries the link it describes.
	metrics        *recorder
	replicaID      string
	rtt            time.Duration
	pool           *Pool
	activeReplicas int
	draining       bool
}

// NewClient validates cfg and returns a tunnel that is not yet connected.
// Run drives it.
func NewClient(cfg ClientConfig, transport Transport) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			Operation: clientOperation,
			Message:   "transport is required",
			Cause:     ErrFatal,
		}
	}
	return &Client{
		cfg:       cfg,
		transport: transport,
		clock:     cfg.Clock,
		logger: cfg.Logger.With(
			slog.String("node_id", cfg.NodeID),
			slog.String("endpoint", cfg.Endpoint)),
		backoff:        NewBackoff(cfg.BackoffInitial, cfg.BackoffMax, cfg.Jitter),
		metrics:        newRecorder(cfg.Metrics, cfg.NodeID, ""),
		state:          StateDisconnected,
		activeReplicas: 1,
	}, nil
}

// State reports the tunnel's current connection state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// ReplicaID reports the replica identity from HelloAck, empty until the
// handshake has completed once.
func (c *Client) ReplicaID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.replicaID
}

// HeartbeatRTT reports the round trip of the most recent acknowledged
// heartbeat, zero before the first one.
func (c *Client) HeartbeatRTT() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rtt
}

// Run connects to the replica and keeps the tunnel up until ctx is cancelled,
// the replica asks for shutdown, or a fatal failure occurs. Cancelling ctx
// starts a graceful drain rather than cutting the stream, so in-flight
// requests end with a real answer instead of a broken connection.
//
// It returns nil after a clean drain, ErrShutdownRequested when the replica
// asked to be left, and a fatal error (IsFatal) when the tunnel gave up.
// Every other failure is retried with full-jitter backoff and never surfaces.
func (c *Client) Run(ctx context.Context) error {
	// The stream must outlive ctx by the length of the drain, so it hangs off
	// a context this Client cancels itself once draining is done.
	base := context.WithoutCancel(ctx)

	for {
		if ctx.Err() != nil {
			c.setState(StateDisconnected)
			return nil
		}

		c.setState(StateConnecting)
		err := c.connectAndServe(ctx, base)

		switch {
		case err == nil:
			c.setState(StateDisconnected)
			return nil
		case errors.Is(err, ErrShutdownRequested):
			c.setState(StateDisconnected)
			return ErrShutdownRequested
		case IsFatal(err):
			// Counted even though no reconnect follows: a certificate the
			// replica refuses is the single most important reason a tunnel
			// ever ends, and a metric that only counted the retryable half
			// would go quiet exactly when the node goes dark.
			c.recorder().Reconnect(reconnectReasonFor(err))
			c.logger.Error("tunnel failed permanently; operator action required",
				slog.String("error", err.Error()))
			c.setState(StateFailed)
			return err
		case ctx.Err() != nil:
			c.setState(StateDisconnected)
			return nil
		}

		c.recorder().Reconnect(reconnectReasonFor(err))
		c.setState(StateReconnecting)
		delay := c.backoff.Next()
		c.logger.Warn("tunnel disconnected; reconnecting after backoff",
			slog.String("error", err.Error()),
			slog.Duration("delay", delay),
			slog.Duration("window", c.backoff.Window()),
			slog.Int("attempt", c.backoff.Attempt()))
		if !c.sleep(ctx, delay) {
			c.setState(StateDisconnected)
			return nil
		}
	}
}

// connectAndServe runs one connection attempt end to end: dial, handshake,
// then the control loop until something ends it.
func (c *Client) connectAndServe(ctx, base context.Context) error {
	streamCtx, cancel := context.WithCancel(base)
	defer cancel()

	stream, err := c.transport.Control(streamCtx)
	if err != nil {
		return c.streamError("dial", err)
	}

	reader := startControlReader(streamCtx, stream)
	// Ordered so cancel runs first and unblocks the reader, then wait
	// collects it: no goroutine outlives a connection attempt.
	defer reader.wait()
	defer cancel()

	if err := c.handshake(ctx, stream, reader); err != nil {
		return err
	}

	c.backoff.Reset()
	c.setState(StateConnected)

	// The pool hangs off the stream context, not off ctx: when this
	// connection ends, every slot it opened is void, which is exactly the
	// README's rule that in-flight requests end with a definite error instead
	// of silently resuming on a new connection. Cancelling ctx instead starts
	// a drain, and the slots stay up for it.
	if err := c.startPool(streamCtx); err != nil {
		return err
	}
	defer c.stopPool()

	return c.runControl(ctx, stream, reader)
}

// handshake sends Hello and requires HelloAck as the very first frame.
// Anything else — a wrong frame, a rejected certificate, a silent replica —
// is fatal: none of it is fixed by dialling again.
func (c *Client) handshake(ctx context.Context, stream ControlStream, reader *controlReader) error {
	hello := &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Hello{Hello: &tunnelv1.Hello{
		NodeId:       c.cfg.NodeID,
		AgentVersion: c.cfg.AgentVersion,
		Resources:    c.cfg.Resources,
		RuntimeIds:   c.helloRuntimeIDs(),
	}}}
	if err := stream.Send(hello); err != nil {
		return c.streamError("send hello", err)
	}

	timer, stop := c.clock.NewTimer(c.cfg.HelloTimeout)
	defer stop()

	select {
	case frame := <-reader.frames:
		ack := frame.GetAck()
		if ack == nil {
			return fatalClientError(runtime.ErrorProtocol,
				"gateway answered Hello with "+controlFrameName(frame)+", want HelloAck", nil)
		}
		c.mu.Lock()
		c.replicaID = ack.GetReplicaId()
		c.metrics = c.metrics.forReplica(ack.GetReplicaId())
		c.mu.Unlock()
		c.logger.Info("tunnel connected", slog.String("replica_id", ack.GetReplicaId()))
		return nil
	case err := <-reader.errs:
		return c.streamError("await hello ack", err)
	case <-timer:
		return fatalClientError(runtime.ErrorTimeout,
			"gateway did not answer Hello within "+c.cfg.HelloTimeout.String(), nil)
	case <-ctx.Done():
		return nil
	}
}

// helloRuntimeIDs is the allowlist advertised to the replica: the configured
// list, or every id currently in the Manager when no narrowing is configured.
// It is advice for the scheduler — the Agent enforces the allowlist itself on
// every request, whatever it advertised here.
func (c *Client) helloRuntimeIDs() []string {
	if len(c.cfg.AllowedRuntimes) > 0 {
		return append([]string(nil), c.cfg.AllowedRuntimes...)
	}
	snaps := c.cfg.Manager.Snapshot()
	ids := make([]string, 0, len(snaps))
	for _, s := range snaps {
		ids = append(ids, s.Descriptor.ID)
	}
	return ids
}

// startPool opens this connection's slot pool. A tunnel with no Handler has
// no data plane and stays in StateConnected.
func (c *Client) startPool(ctx context.Context) error {
	if c.cfg.Handler == nil {
		return nil
	}

	c.mu.Lock()
	replicaID, active, draining := c.replicaID, c.activeReplicas, c.draining
	c.mu.Unlock()

	pool, err := NewPool(PoolConfig{
		NodeID:         c.cfg.NodeID,
		ReplicaID:      replicaID,
		Slots:          c.cfg.Slots,
		Handler:        c.cfg.Handler,
		ActiveReplicas: active,
		OnServing:      c.onServing,
		Metrics:        c.cfg.Metrics,
		Clock:          c.clock,
		Logger:         c.cfg.Logger,
	}, c.transport)
	if err != nil {
		return err
	}
	// Set before Start so a tunnel that reconnects while its replica is
	// draining never warms a slot the replica will not use.
	pool.SetDraining(draining)
	if err := pool.Start(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	c.pool = pool
	c.mu.Unlock()
	return nil
}

// stopPool closes this connection's slots. It runs after draining, so a
// graceful shutdown has already let the in-flight requests finish.
func (c *Client) stopPool() {
	c.mu.Lock()
	pool := c.pool
	c.pool = nil
	c.mu.Unlock()
	if pool != nil {
		pool.Stop()
	}
}

// currentPool returns the live slot pool, nil when the tunnel is between
// connections or carries no data plane.
func (c *Client) currentPool() *Pool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pool
}

// onServing moves the tunnel between connected and serving as the pool gains
// and loses its slots. It never overrides a terminal or transitional state:
// a pool shutting down during a drain must not report the tunnel back as
// connected.
func (c *Client) onServing(serving bool) {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	switch {
	case serving && state == StateConnected:
		c.setState(StateServing)
	case !serving && state == StateServing:
		c.setState(StateConnected)
	}
}

// SetActiveReplicas records how many replicas the roster marks active, so
// this tunnel takes its share of the node's slot budget. 阶段 6 calls it on
// every roster change; until then a single tunnel owns the whole budget.
func (c *Client) SetActiveReplicas(n int) {
	if n < 1 {
		n = 1
	}
	c.mu.Lock()
	c.activeReplicas = n
	pool := c.pool
	c.mu.Unlock()
	if pool != nil {
		pool.SetActiveReplicas(n)
	}
}

// SetDraining stops or resumes refilling this tunnel's slot pool. The roster
// handling (manager.go) calls it when a replica is marked draining: the
// replica stops dispatching, so the parked slots go back and none replace
// them, while the requests already running finish normally.
func (c *Client) SetDraining(draining bool) {
	c.mu.Lock()
	c.draining = draining
	pool := c.pool
	c.mu.Unlock()
	if pool != nil {
		pool.SetDraining(draining)
	}
}

// SlotStats reports this tunnel's slot occupancy, zero when no pool is up.
func (c *Client) SlotStats() PoolStats {
	if pool := c.currentPool(); pool != nil {
		return pool.Stats()
	}
	return PoolStats{}
}

// sleep waits out d, returning false when ctx ended first.
func (c *Client) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer, stop := c.clock.NewTimer(d)
	defer stop()
	select {
	case <-timer:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Client) setState(s State) {
	c.mu.Lock()
	changed := c.state != s
	c.state = s
	rec := c.metrics
	c.mu.Unlock()
	if !changed {
		return
	}
	rec.ConnectionState(s)
	if c.cfg.OnState != nil {
		c.cfg.OnState(s)
	}
}

// recorder returns the metrics recorder bound to whatever replica this
// tunnel currently knows it reached.
func (c *Client) recorder() *recorder {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.metrics
}

// streamError classifies a Control stream failure. Authentication failures
// mean this node's identity is not accepted and are fatal; everything else is
// a transport problem worth another attempt.
func (c *Client) streamError(what string, err error) error {
	if IsFatal(err) {
		return err
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unauthenticated, codes.PermissionDenied:
			return fatalClientError(runtime.ErrorUnauthorized,
				"gateway rejected this node's identity on "+what+": "+st.Code().String(), err)
		case codes.Unimplemented:
			return fatalClientError(runtime.ErrorProtocol,
				"gateway does not implement the tunnel service", err)
		}
	}
	return &runtime.RuntimeError{
		Code:      runtime.ErrorConnection,
		Operation: clientOperation,
		Message:   "control stream failed on " + what,
		Retryable: true,
		Cause:     err,
	}
}

// fatalClientError builds an error that stops the run loop.
func fatalClientError(code runtime.ErrorCode, msg string, cause error) *runtime.RuntimeError {
	return &runtime.RuntimeError{
		Code:      code,
		Operation: clientOperation,
		Message:   msg,
		Retryable: false,
		Cause:     errors.Join(ErrFatal, cause),
	}
}

func setDefaultDuration(d *time.Duration, def time.Duration) {
	if *d <= 0 {
		*d = def
	}
}

// -----------------------------------------------------------------------
// gRPC transport
// -----------------------------------------------------------------------

// grpcTransport is the production Transport: one gRPC connection to one
// replica, always mTLS, with HTTP/2 keepalive tuned for home routers.
type grpcTransport struct {
	conn   *grpc.ClientConn
	client tunnelv1.TunnelClient
}

// NewGRPCTransport dials one Gateway replica with the node identity. The
// keepalive parameters default to the README's 20s interval and 10s timeout,
// which is short enough to keep a home NAT's session table from dropping an
// idle tunnel. The connection is lazy: failures surface on the first stream.
func NewGRPCTransport(endpoint string, id *Identity, keepaliveInterval, keepaliveTimeout time.Duration) (Transport, error) {
	if err := validateEndpoint("endpoint", endpoint); err != nil {
		return nil, fatalClientError(runtime.ErrorInvalidConfig, err.Error(), nil)
	}
	if id == nil {
		return nil, fatalClientError(runtime.ErrorInvalidConfig,
			"a node identity is required: the tunnel is always mutually authenticated", nil)
	}
	setDefaultDuration(&keepaliveInterval, defaultKeepaliveInterval)
	setDefaultDuration(&keepaliveTimeout, defaultKeepaliveTimeout)

	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(id.TLSConfig())),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                keepaliveInterval,
			Timeout:             keepaliveTimeout,
			PermitWithoutStream: true,
		}))
	if err != nil {
		return nil, &runtime.RuntimeError{
			Code:      runtime.ErrorConnection,
			Operation: clientOperation,
			Message:   "cannot create a tunnel client for " + endpoint,
			Retryable: true,
			Cause:     err,
		}
	}
	return &grpcTransport{conn: conn, client: tunnelv1.NewTunnelClient(conn)}, nil
}

func (t *grpcTransport) Control(ctx context.Context) (ControlStream, error) {
	return t.client.Control(ctx)
}

func (t *grpcTransport) Serve(ctx context.Context) (ServeStream, error) {
	return t.client.Serve(ctx)
}

func (t *grpcTransport) Close() error { return t.conn.Close() }

package tunnel

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// This file is the connection table: one Client per Gateway replica, opened
// and closed to follow the roster. Everything below it — the state machine,
// the slot pool, the dispatcher — is deliberately single-replica, so this is
// the only place that knows a node talks to more than one Gateway.
//
// The design rule is that replicas share nothing. Each tunnel has its own
// connection, its own backoff counter, its own slot pool and its own
// in-flight requests; a replica that restarts, hangs or rejects this node
// cannot reach the others. The two things that are node-wide are the identity
// (one certificate serves every tunnel) and the slot budget (one node's worth
// of concurrency, shared out by PerReplicaMax), and both are handled here
// rather than being pushed down into the per-replica code.

// managerOperation is the Operation recorded on connection-table errors.
const managerOperation = "tunnel_manager"

const (
	// defaultIdentityInterval is how often the certificate is re-checked.
	// Certificates last weeks and rotate at a third of their life remaining,
	// so an hourly check is many times more often than it needs to be and
	// still costs nothing.
	defaultIdentityInterval = time.Hour
	// defaultRotateTimeout bounds how long a certificate rotation waits for
	// one replaced tunnel to come back before moving to the next.
	defaultRotateTimeout = 30 * time.Second
)

// TransportFactory opens the transport for one replica. Production uses
// NewGRPCTransport; tests substitute an in-memory fleet.
type TransportFactory func(endpoint string, id *Identity) (Transport, error)

// IdentityProvider supplies the node certificate and rotates it when it is
// close to expiring. *IdentityManager implements it.
type IdentityProvider interface {
	Ensure(ctx context.Context) (*Identity, error)
}

// ManagerConfig configures the connection table.
type ManagerConfig struct {
	// Client is the template every tunnel is built from: the node identity
	// fields, the runtime wiring, the slot configuration and all the timings.
	// Endpoint, Handler-independent per-replica wiring and the notification
	// hooks are filled in per tunnel and any value set here is overwritten.
	Client ClientConfig

	// SeedEndpoints is the configured starting list. Only one of them has to
	// be reachable: the roster that comes back over it supplies the rest, so
	// scaling the Gateway out never means editing an Agent's configuration.
	SeedEndpoints []string
	// MaxGateways bounds the connection table however large a roster gets.
	// Default 16.
	MaxGateways int

	// Identities supplies and rotates the node certificate. It may be nil,
	// in which case the transports are built with no identity — only useful
	// when the transport does its own authentication, as the tests' in-memory
	// fleet does.
	Identities IdentityProvider
	// IdentityInterval is how often Identities.Ensure is called. Default 1h.
	IdentityInterval time.Duration
	// RotateTimeout bounds the wait for one rotated tunnel to reconnect
	// before the rotation moves on. Default 30s.
	RotateTimeout time.Duration
	// TransportFactory opens each replica's transport. Defaults to
	// NewGRPCTransport with the README's keepalive parameters.
	TransportFactory TransportFactory

	// Clock defaults to runtime.NewSystemClock, Logger to slog.Default.
	Clock  runtime.Clock
	Logger *slog.Logger
}

func (c *ManagerConfig) validate() error {
	var problems []string
	if c.Client.NodeID == "" {
		problems = append(problems, "node_id is required")
	}
	if c.Client.Manager == nil {
		problems = append(problems, "manager is required")
	}
	if len(c.SeedEndpoints) == 0 {
		problems = append(problems, "at least one gateway endpoint is required")
	}
	for _, endpoint := range c.SeedEndpoints {
		if err := validateEndpoint("gateway_endpoint", endpoint); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if c.MaxGateways == 0 {
		c.MaxGateways = defaultMaxGateways
	}
	if c.MaxGateways > 0 && len(c.SeedEndpoints) > c.MaxGateways {
		problems = append(problems, "max_gateways must be at least len(gateway_endpoints)")
	}
	setDefaultDuration(&c.IdentityInterval, defaultIdentityInterval)
	setDefaultDuration(&c.RotateTimeout, defaultRotateTimeout)
	if c.Clock == nil {
		c.Clock = runtime.NewSystemClock()
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.TransportFactory == nil {
		c.TransportFactory = func(endpoint string, id *Identity) (Transport, error) {
			return NewGRPCTransport(endpoint, id, 0, 0)
		}
	}

	if len(problems) > 0 {
		return &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			Operation: managerOperation,
			Message:   "invalid tunnel manager configuration: " + strings.Join(problems, "; "),
			Cause:     ErrFatal,
		}
	}
	return nil
}

// Manager keeps one tunnel open per Gateway replica named by the roster.
type Manager struct {
	cfg    ManagerConfig
	clock  runtime.Clock
	logger *slog.Logger
	roster *Roster
	// metrics carries the two node-scoped gauges. The sink comes from the
	// Client template, so a node has one metrics backend rather than one per
	// configuration site.
	metrics *metrics

	// wake carries "the roster or a tunnel changed, reconsider the table" to
	// the run loop. It holds one token; a second change before the loop wakes
	// is covered by the reconcile the first one causes.
	wake chan struct{}
	wg   sync.WaitGroup

	mu       sync.Mutex
	tunnels  map[string]*tunnelEntry
	stopped  map[string]stoppedRecord
	identity *Identity
	// generation increments on every certificate rotation, so a tunnel built
	// with the previous certificate is identifiable without comparing keys.
	generation int64
	lastFatal  error
	running    bool
}

// tunnelEntry is one supervised tunnel.
type tunnelEntry struct {
	endpoint  string
	replicaID string
	client    *Client
	transport Transport
	cancel    context.CancelFunc
	done      chan struct{}
	// ready is closed the first time this incarnation reaches connected, so
	// a certificate rotation can wait for one tunnel to come back before
	// touching the next.
	ready     chan struct{}
	readyOnce sync.Once
	// generation is the certificate generation this tunnel was built with.
	generation int64

	mu    sync.Mutex
	state State
	// draining is the roster's view of the replica.
	draining bool
	// replacing marks a tunnel the Manager itself is shutting down, so its
	// exit is not mistaken for the replica giving up. It is set from the run
	// loop and read from the tunnel's own goroutine, so it lives under mu
	// with the rest of the mutable state.
	replacing bool
}

// stoppedRecord remembers why a tunnel ended on its own, so the reconcile
// does not immediately rebuild it. A replica that asked the Agent to leave,
// or rejected its certificate, is reconnected only when the roster says
// something new or the certificate is replaced — never in a tight loop.
type stoppedRecord struct {
	version    int64
	generation int64
	err        error
}

// NewManager validates cfg and returns a connection table with no tunnels
// open yet. Run opens them.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	logger := cfg.Logger.With(slog.String("node_id", cfg.Client.NodeID))
	return &Manager{
		cfg:     cfg,
		clock:   cfg.Clock,
		logger:  logger,
		roster:  NewRoster(cfg.MaxGateways, logger),
		metrics: newMetrics(cfg.Client.Metrics, cfg.Client.NodeID, ""),
		wake:    make(chan struct{}, 1),
		tunnels: map[string]*tunnelEntry{},
		stopped: map[string]stoppedRecord{},
	}, nil
}

// Run opens a tunnel to every replica the roster names and keeps the table
// matching it until ctx is cancelled. Cancelling ctx drains every tunnel.
//
// It returns nil after a clean shutdown. It returns an error only when every
// replica has failed for a reason reconnecting cannot fix — an unreachable
// replica is not one of those, because its Client keeps retrying and never
// exits.
func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return &runtime.RuntimeError{
			Code:      runtime.ErrorInvalidConfig,
			Operation: managerOperation,
			Message:   "the tunnel manager has already been started",
			Cause:     ErrFatal,
		}
	}
	m.running = true
	m.mu.Unlock()

	defer m.stopAll()

	if err := m.refreshIdentity(ctx); err != nil {
		return err
	}
	m.roster.Seed(m.cfg.SeedEndpoints)

	identityTick := newRearmingTimer(m.clock, m.cfg.IdentityInterval)
	defer identityTick.stop()

	if err := m.reconcile(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil

		case <-m.wake:
			if err := m.reconcile(ctx); err != nil {
				return err
			}

		case <-identityTick.C():
			if err := m.refreshIdentity(ctx); err != nil {
				return err
			}
			identityTick.arm()
			if err := m.reconcile(ctx); err != nil {
				return err
			}
		}
	}
}

// ConnectedReplicas reports how many tunnels can currently take a request,
// which is the value behind tunnel_connected_replicas. A node is online for
// as long as this is not zero.
func (m *Manager) ConnectedReplicas() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0
	for _, entry := range m.tunnels {
		switch entry.currentState() {
		case StateConnected, StateServing:
			n++
		}
	}
	return n
}

// TunnelStates reports every live tunnel's state by endpoint, for logs,
// metrics and tests.
func (m *Manager) TunnelStates() map[string]State {
	m.mu.Lock()
	defer m.mu.Unlock()

	states := make(map[string]State, len(m.tunnels))
	for endpoint, entry := range m.tunnels {
		states[endpoint] = entry.currentState()
	}
	return states
}

// SlotStats reports one tunnel's slot occupancy, and whether that tunnel is
// live at all. The per-replica ceiling it carries is the visible half of the
// shared node budget.
func (m *Manager) SlotStats(endpoint string) (PoolStats, bool) {
	m.mu.Lock()
	entry, ok := m.tunnels[endpoint]
	m.mu.Unlock()
	if !ok {
		return PoolStats{}, false
	}
	return entry.client.SlotStats(), true
}

// RosterVersion reports the roster version in force and whether one has been
// accepted at all.
func (m *Manager) RosterVersion() (int64, bool) { return m.roster.Version() }

// -----------------------------------------------------------------------
// Reconciliation
// -----------------------------------------------------------------------

// reconcile brings the connection table in line with the roster: open what is
// missing, drain what is winding down, close what is gone, and re-share the
// slot budget across whatever is left.
func (m *Manager) reconcile(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}

	entries := m.roster.Entries()
	version, _ := m.roster.Version()
	active := m.roster.ActiveCount()

	desired := make(map[string]RosterEntry, len(entries))
	for _, entry := range entries {
		if entry.State != tunnelv1.ReplicaState_REPLICA_STATE_REMOVED {
			desired[entry.Endpoint] = entry
		}
	}

	var (
		retire []*tunnelEntry
		open   []RosterEntry
	)

	m.mu.Lock()
	generation := m.generation
	for endpoint, entry := range m.tunnels {
		if _, want := desired[endpoint]; !want {
			retire = append(retire, entry)
		}
	}
	for endpoint, want := range desired {
		if _, live := m.tunnels[endpoint]; live {
			continue
		}
		// A tunnel that ended by itself waits for news before being rebuilt:
		// a newer roster, or a new certificate. Without that, a replica that
		// asked us to leave would be re-dialled the moment it hung up.
		if record, stopped := m.stopped[endpoint]; stopped &&
			record.version >= version && record.generation >= generation {
			continue
		}
		open = append(open, want)
	}
	m.mu.Unlock()

	for _, entry := range retire {
		m.logger.Info("replica left the roster; retiring its tunnel",
			slog.String("endpoint", entry.endpoint),
			slog.String("replica_id", entry.replicaID))
		m.stopTunnel(entry)
	}
	for _, want := range open {
		if err := m.openTunnel(ctx, want); err != nil {
			// One replica that cannot even be dialled must not stop the
			// others from being opened.
			m.logger.Error("cannot open a tunnel to a replica",
				slog.String("endpoint", want.Endpoint),
				slog.String("error", err.Error()))
		}
	}

	// Apply the states and the share after the table has settled, so a
	// replica that has just been added already counts towards the divisor and
	// no tunnel is briefly sized for a table it is no longer part of.
	m.applyStates(desired, active)
	// The table itself changed, which moves the count without any tunnel
	// changing state.
	m.metrics.ConnectedReplicas(m.ConnectedReplicas())

	return m.fatalIfExhausted(desired)
}

// applyStates pushes each replica's roster state and the shared slot budget
// into its tunnel.
func (m *Manager) applyStates(desired map[string]RosterEntry, active int) {
	m.mu.Lock()
	live := make([]*tunnelEntry, 0, len(m.tunnels))
	for _, entry := range m.tunnels {
		live = append(live, entry)
	}
	m.mu.Unlock()

	for _, entry := range live {
		want, ok := desired[entry.endpoint]
		if !ok {
			continue
		}
		if want.ReplicaID != "" {
			entry.replicaID = want.ReplicaID
		}

		draining := want.State == tunnelv1.ReplicaState_REPLICA_STATE_DRAINING
		if entry.setDraining(draining) {
			m.logger.Info("replica state changed",
				slog.String("endpoint", entry.endpoint),
				slog.String("state", want.State.String()))
		}
		// A draining replica stops being refilled but keeps its in-flight
		// requests; an active one starts being refilled again.
		entry.client.SetDraining(draining)
		entry.client.SetActiveReplicas(active)
	}
}

// fatalIfExhausted reports the failure that took the last tunnel down, when
// every replica the roster names has failed fatally and none is left. An
// unreachable replica never lands here: its Client retries forever rather
// than returning.
func (m *Manager) fatalIfExhausted(desired map[string]RosterEntry) error {
	if len(desired) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.tunnels) > 0 {
		return nil
	}
	for endpoint := range desired {
		record, stopped := m.stopped[endpoint]
		if !stopped || record.err == nil || !IsFatal(record.err) {
			return nil
		}
	}
	m.logger.Error("every replica rejected this node; giving up",
		slog.String("error", m.lastFatal.Error()))
	return m.lastFatal
}

// -----------------------------------------------------------------------
// Tunnel lifecycle
// -----------------------------------------------------------------------

// openTunnel builds and starts one replica's tunnel.
func (m *Manager) openTunnel(ctx context.Context, want RosterEntry) error {
	m.mu.Lock()
	identity, generation := m.identity, m.generation
	m.mu.Unlock()

	transport, err := m.cfg.TransportFactory(want.Endpoint, identity)
	if err != nil {
		return err
	}

	entry := &tunnelEntry{
		endpoint:   want.Endpoint,
		replicaID:  want.ReplicaID,
		transport:  transport,
		done:       make(chan struct{}),
		ready:      make(chan struct{}),
		generation: generation,
		state:      StateDisconnected,
	}

	cfg := m.cfg.Client
	cfg.Endpoint = want.Endpoint
	cfg.Clock = m.clock
	cfg.Logger = m.cfg.Logger
	cfg.OnState = func(state State) { m.onState(entry, state) }
	cfg.OnRoster = m.onRoster
	// The slot hint reaches the pool through the Client itself; a caller hook
	// here would only duplicate it.
	cfg.OnSlotHint = nil

	client, err := NewClient(cfg, transport)
	if err != nil {
		_ = transport.Close()
		return err
	}
	entry.client = client
	client.SetActiveReplicas(m.roster.ActiveCount())
	client.SetDraining(want.State == tunnelv1.ReplicaState_REPLICA_STATE_DRAINING)

	tunnelCtx, cancel := context.WithCancel(ctx)
	entry.cancel = cancel

	m.mu.Lock()
	m.tunnels[want.Endpoint] = entry
	delete(m.stopped, want.Endpoint)
	m.mu.Unlock()

	m.logger.Info("opening a tunnel to a replica",
		slog.String("endpoint", want.Endpoint),
		slog.String("replica_id", want.ReplicaID))

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		err := client.Run(tunnelCtx)
		m.tunnelExited(entry, err)
	}()
	return nil
}

// tunnelExited records why one tunnel ended and asks for a reconcile. One
// tunnel ending is never more than one tunnel ending: the others keep their
// connections, their slots and their in-flight requests.
func (m *Manager) tunnelExited(entry *tunnelEntry, err error) {
	defer close(entry.done)
	defer func() { _ = entry.transport.Close() }()

	version, _ := m.roster.Version()
	replacing := entry.isReplacing()

	m.mu.Lock()
	// A tunnel the Manager is replacing has already been removed from the
	// table, and its exit says nothing about the replica.
	if !replacing {
		if live, ok := m.tunnels[entry.endpoint]; ok && live == entry {
			delete(m.tunnels, entry.endpoint)
		}
		m.stopped[entry.endpoint] = stoppedRecord{
			version:    version,
			generation: entry.generation,
			err:        err,
		}
		if IsFatal(err) {
			m.lastFatal = err
		}
	}
	m.mu.Unlock()

	switch {
	case replacing:
		m.logger.Debug("tunnel replaced", slog.String("endpoint", entry.endpoint))
	case err == nil:
		m.logger.Info("tunnel closed", slog.String("endpoint", entry.endpoint))
	case errors.Is(err, ErrShutdownRequested):
		m.logger.Info("replica asked this node to leave; waiting for the roster",
			slog.String("endpoint", entry.endpoint))
	default:
		m.logger.Error("tunnel ended",
			slog.String("endpoint", entry.endpoint),
			slog.String("error", err.Error()))
	}

	m.metrics.ConnectedReplicas(m.ConnectedReplicas())
	m.poke()
}

// stopTunnel closes one tunnel and waits for it to drain.
func (m *Manager) stopTunnel(entry *tunnelEntry) {
	m.mu.Lock()
	if live, ok := m.tunnels[entry.endpoint]; ok && live == entry {
		delete(m.tunnels, entry.endpoint)
	}
	m.mu.Unlock()

	entry.markReplacing()
	entry.cancel()
	<-entry.done
}

// stopAll closes every tunnel and waits for the goroutines to finish. It runs
// on the way out of Run, after each Client has already drained on its own
// context cancellation.
func (m *Manager) stopAll() {
	m.mu.Lock()
	entries := make([]*tunnelEntry, 0, len(m.tunnels))
	for _, entry := range m.tunnels {
		entries = append(entries, entry)
	}
	m.tunnels = map[string]*tunnelEntry{}
	m.mu.Unlock()

	for _, entry := range entries {
		entry.markReplacing()
		entry.cancel()
	}
	m.wg.Wait()
}

// onState records one tunnel's state change and releases anything waiting for
// it to come back.
func (m *Manager) onState(entry *tunnelEntry, state State) {
	entry.mu.Lock()
	entry.state = state
	entry.mu.Unlock()

	if state == StateConnected || state == StateServing {
		entry.readyOnce.Do(func() { close(entry.ready) })
	}
	// Republished on every transition rather than on a timer: the number of
	// usable links is the node's health verdict, and a stale one is worse
	// than none.
	m.metrics.ConnectedReplicas(m.ConnectedReplicas())
}

// onRoster takes a roster broadcast from any tunnel. Every replica broadcasts
// the same Registry roster, so which tunnel it arrived on does not matter —
// only its version does.
func (m *Manager) onRoster(pb *tunnelv1.GatewayRoster) {
	if !m.roster.Apply(pb) {
		return
	}
	version, _ := m.roster.Version()
	m.metrics.RosterVersion(version)
	m.logger.Info("accepted a new gateway roster",
		slog.Int64("version", version),
		slog.Int("replicas", len(m.roster.Entries())),
		slog.Int("active", m.roster.ActiveCount()))
	m.poke()
}

func (m *Manager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// markReplacing records that the Manager, not the replica, is ending this
// tunnel.
func (e *tunnelEntry) markReplacing() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.replacing = true
}

func (e *tunnelEntry) isReplacing() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.replacing
}

func (e *tunnelEntry) currentState() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// setDraining records the roster's view, reporting whether it changed.
func (e *tunnelEntry) setDraining(draining bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := e.draining != draining
	e.draining = draining
	return changed
}

// -----------------------------------------------------------------------
// Certificate rotation
// -----------------------------------------------------------------------

// refreshIdentity obtains the node certificate and, when it has changed,
// replaces the tunnels built with the old one.
func (m *Manager) refreshIdentity(ctx context.Context) error {
	if m.cfg.Identities == nil {
		return nil
	}

	identity, err := m.cfg.Identities.Ensure(ctx)
	if err != nil {
		if IsFatal(err) {
			// A certificate problem is node-wide, so it is reported once here
			// rather than once per tunnel.
			return err
		}
		m.logger.Warn("cannot refresh the node identity; keeping the current one",
			slog.String("error", err.Error()))
		return nil
	}

	m.mu.Lock()
	current := m.identity
	unchanged := current != nil && sameIdentity(current, identity)
	if !unchanged {
		m.identity = identity
		m.generation++
	}
	first := current == nil
	generation := m.generation
	m.mu.Unlock()

	if unchanged || first {
		return nil
	}

	m.logger.Info("the node certificate was rotated; replacing tunnels one at a time",
		slog.Time("not_after", identity.NotAfter))
	m.rotateTunnels(ctx, generation)
	return nil
}

// rotateTunnels replaces the tunnels running on the previous certificate, one
// at a time and waiting for each replacement to connect. Replacing them all
// at once would make the node unreachable to every replica simultaneously,
// which is precisely the outage a rotation is supposed to avoid.
func (m *Manager) rotateTunnels(ctx context.Context, generation int64) {
	m.mu.Lock()
	stale := make([]*tunnelEntry, 0, len(m.tunnels))
	for _, entry := range m.tunnels {
		if entry.generation < generation {
			stale = append(stale, entry)
		}
	}
	m.mu.Unlock()

	for _, entry := range stale {
		if ctx.Err() != nil {
			return
		}
		want := RosterEntry{
			ReplicaID: entry.replicaID,
			Endpoint:  entry.endpoint,
			State:     tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE,
		}
		if entry.isDraining() {
			want.State = tunnelv1.ReplicaState_REPLICA_STATE_DRAINING
		}

		m.stopTunnel(entry)
		if err := m.openTunnel(ctx, want); err != nil {
			m.logger.Error("cannot reopen a tunnel after the certificate rotated",
				slog.String("endpoint", entry.endpoint),
				slog.String("error", err.Error()))
			continue
		}
		m.waitReady(ctx, entry.endpoint)
	}
}

// waitReady blocks until the tunnel at endpoint reports connected, the
// rotation timeout passes, or ctx ends. A tunnel that does not come back in
// time is left to its own reconnect loop rather than stalling the rotation of
// every other replica behind it.
func (m *Manager) waitReady(ctx context.Context, endpoint string) {
	m.mu.Lock()
	entry := m.tunnels[endpoint]
	m.mu.Unlock()
	if entry == nil {
		return
	}

	timer, stop := m.clock.NewTimer(m.cfg.RotateTimeout)
	defer stop()

	select {
	case <-entry.ready:
	case <-entry.done:
	case <-timer:
		m.logger.Warn("a rotated tunnel has not reconnected yet; continuing",
			slog.String("endpoint", endpoint))
	case <-ctx.Done():
	}
}

func (e *tunnelEntry) isDraining() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.draining
}

// sameIdentity reports whether two identities are the same certificate. The
// validity window and the node id together identify one issuance: a rotation
// always produces a new one, because it generates a fresh key as well.
func sameIdentity(a, b *Identity) bool {
	return a.NodeID == b.NodeID &&
		a.NotBefore.Equal(b.NotBefore) &&
		a.NotAfter.Equal(b.NotAfter)
}

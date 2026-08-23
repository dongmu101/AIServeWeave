package tunnel_test

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel/internal/tunneltest"
)

// The metrics tests do two things the other files cannot: they check that
// every instrument README.md's 可观测性 section lists is actually recorded by
// the code that would have to record it, and they check what ends up in the
// labels. The second half is a security assertion as much as a cardinality
// one — a Cancel reason, a prompt or a workflow template reaching a label
// would put user content into a metrics backend that is neither access
// controlled nor bounded in size.

// secretText is planted everywhere a replica can put free-form text — the
// Cancel reason, the model name, the trace map — so any label carrying it is
// visible as a single substring check.
const secretText = "how-do-i-make-mayonnaise"

// documentedMetrics is README.md's 可观测性 list, transcribed. The coverage
// test compares it against what a full workload actually records, in both
// directions: a listed instrument nobody records is a documentation lie, and
// a recorded instrument nobody documented is an unreviewed label set.
var documentedMetrics = []string{
	tunnel.MetricConnectionState,
	tunnel.MetricConnectedReplicas,
	tunnel.MetricRosterVersion,
	tunnel.MetricReconnectsTotal,
	tunnel.MetricHeartbeatRTTSeconds,
	tunnel.MetricSlotsTotal,
	tunnel.MetricSlotAcquireFailuresTotal,
	tunnel.MetricLimiterRejectionsTotal,
	tunnel.MetricRequestsTotal,
	tunnel.MetricRequestDurationSeconds,
	tunnel.MetricStreamFirstEventSeconds,
	tunnel.MetricFrameBytes,
	tunnel.MetricCancelTotal,
}

// metricLabelKeys is the exact label set each instrument may carry.
var metricLabelKeys = map[string][]string{
	tunnel.MetricConnectionState:          {tunnel.LabelNodeID, tunnel.LabelReplicaID},
	tunnel.MetricConnectedReplicas:        {tunnel.LabelNodeID},
	tunnel.MetricRosterVersion:            {tunnel.LabelNodeID},
	tunnel.MetricReconnectsTotal:          {tunnel.LabelNodeID, tunnel.LabelReplicaID, tunnel.LabelReason},
	tunnel.MetricHeartbeatRTTSeconds:      {tunnel.LabelNodeID, tunnel.LabelReplicaID},
	tunnel.MetricSlotsTotal:               {tunnel.LabelNodeID, tunnel.LabelReplicaID, tunnel.LabelClass, tunnel.LabelState},
	tunnel.MetricSlotAcquireFailuresTotal: {tunnel.LabelNodeID, tunnel.LabelReplicaID, tunnel.LabelClass},
	tunnel.MetricLimiterRejectionsTotal:   {tunnel.LabelNodeID, tunnel.LabelRuntimeID},
	tunnel.MetricRequestsTotal:            {tunnel.LabelNodeID, tunnel.LabelReplicaID, tunnel.LabelOperation, tunnel.LabelResult},
	tunnel.MetricRequestDurationSeconds:   {tunnel.LabelNodeID, tunnel.LabelReplicaID, tunnel.LabelOperation},
	tunnel.MetricStreamFirstEventSeconds:  {tunnel.LabelNodeID, tunnel.LabelReplicaID, tunnel.LabelOperation},
	tunnel.MetricFrameBytes:               {tunnel.LabelNodeID, tunnel.LabelReplicaID, tunnel.LabelDirection},
	tunnel.MetricCancelTotal:              {tunnel.LabelNodeID, tunnel.LabelReplicaID, tunnel.LabelReason},
}

// labelValues is the closed vocabulary of every label whose values do not
// come from local configuration. node_id, replica_id and runtime_id are
// excluded: they are bounded by the deployment (one node, at most
// max_gateways replicas, the configured runtime instances) rather than by an
// enumeration in code.
var labelValues = map[string][]string{
	tunnel.LabelClass:     {"inference", "bulk"},
	tunnel.LabelState:     {tunnel.SlotStateIdle, tunnel.SlotStateBusy},
	tunnel.LabelDirection: {tunnel.DirectionInbound, tunnel.DirectionOutbound},
	tunnel.LabelResult: {
		string(tunnel.ResultSuccess), string(tunnel.ResultClientError),
		string(tunnel.ResultUpstreamError), string(tunnel.ResultTimeout),
		string(tunnel.ResultCancelled), string(tunnel.ResultBackpressure),
	},
	tunnel.LabelReason: {
		string(tunnel.ReconnectTransport), string(tunnel.ReconnectProtocol),
		string(tunnel.ReconnectTimeout), string(tunnel.ReconnectUnauthorized),
		string(tunnel.ReconnectOther),
		string(tunnel.CancelByGateway), string(tunnel.CancelSlotClosed),
	},
}

// operationLabels is every value the operation label may take: the protocol's
// enum names, plus the placeholder an operation from a newer Gateway becomes.
func operationLabels() []string {
	out := []string{"unknown"}
	for _, name := range tunnelv1.Operation_name {
		out = append(out, name)
	}
	return out
}

// -----------------------------------------------------------------------
// A workload that touches every instrument
// -----------------------------------------------------------------------

// exerciseMetrics runs one of everything the tunnel measures against a single
// collector: a Control stream that connects, beats and breaks; a slot pool
// that serves, streams, is cancelled and fails to open; a connection table
// that applies a roster; and a dispatcher whose quota is exhausted.
func exerciseMetrics(t *testing.T) *tunneltest.Metrics {
	t.Helper()

	mx := tunneltest.NewMetrics()
	exerciseControlMetrics(t, mx)
	exerciseSlotMetrics(t, mx)
	exerciseSlotOpenFailure(t, mx)
	exerciseTableMetrics(t, mx)
	exerciseLimiterMetrics(t, mx)
	return mx
}

// exerciseControlMetrics drives one tunnel through connect, heartbeat and a
// broken stream.
func exerciseControlMetrics(t *testing.T, mx *tunneltest.Metrics) {
	t.Helper()

	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		isolateHeartbeat(cfg)
		cfg.Metrics = mx
	})
	f.start()
	sess := f.connect()

	f.advance(15*time.Second, 1)
	hb := f.recv(sess).GetHeartbeat()
	if hb == nil {
		t.Fatal("no heartbeat after one interval")
	}
	f.advance(250*time.Millisecond, 0)
	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_HbAck{HbAck: &tunnelv1.HeartbeatAck{
		SentUnixMs: hb.GetSentUnixMs(),
	}}})
	waitMetric(t, mx, tunnel.MetricHeartbeatRTTSeconds, nil, func(s *tunneltest.Series) bool {
		return s.Count() > 0
	})

	// A broken stream is the ordinary reconnect: the tunnel backs off and the
	// reason is the transport, not the protocol.
	sess.Break(errors.New("tunneltest: the replica went away"))
	waitMetric(t, mx, tunnel.MetricReconnectsTotal,
		map[string]string{tunnel.LabelReason: string(tunnel.ReconnectTransport)},
		func(s *tunneltest.Series) bool { return s.Value() >= 1 })
}

// exerciseSlotMetrics runs three requests through a real slot pool: one that
// succeeds, one streaming request whose first event is timed, and one the
// replica cancels with a free-form reason.
func exerciseSlotMetrics(t *testing.T, mx *tunneltest.Metrics) {
	t.Helper()

	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) { cfg.Metrics = mx })
	f.start()
	conns := f.acceptSlots(3)
	inference := ofClass(conns, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE)

	// A plain request, answered at once.
	f.handler.set(func(_ context.Context, _ *tunnel.Request, sink tunnel.ResponseSink) error {
		return sink.Data([]byte("ok"))
	})
	dispatchOp(f, inference[0], "req-ok", tunnelv1.Operation_OPERATION_CHAT)
	f.expectEnd(inference[0], "req-ok")
	f.expectReady(inference[0])

	// A streaming request: two events, so the first one is what the TTFT
	// histogram sees.
	f.handler.set(func(_ context.Context, _ *tunnel.Request, sink tunnel.ResponseSink) error {
		if err := sink.Data([]byte("event-1")); err != nil {
			return err
		}
		return sink.Data([]byte("event-2"))
	})
	dispatchOp(f, inference[0], "req-stream", tunnelv1.Operation_OPERATION_CHAT_STREAM)
	f.expectEnd(inference[0], "req-stream")
	f.expectReady(inference[0])

	// A cancelled request. The replica's reason is free-form text and is
	// exactly what must not become a label.
	entered := make(chan struct{})
	f.handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	dispatchOp(f, inference[1], "req-cancel", tunnelv1.Operation_OPERATION_CHAT)
	<-entered
	f.sendFrame(inference[1], &tunnelv1.GatewayFrame{
		RequestId: "req-cancel",
		Body: &tunnelv1.GatewayFrame_Cancel{Cancel: &tunnelv1.Cancel{
			Reason: "the user closed the tab while asking " + secretText,
		}},
	})
	f.expectEnd(inference[1], "req-cancel")
}

// exerciseSlotOpenFailure drives the path where a replica accepts Control but
// refuses Serve, which is what tunnel_slot_acquire_failures_total counts.
func exerciseSlotOpenFailure(t *testing.T, mx *tunneltest.Metrics) {
	t.Helper()

	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) { cfg.Metrics = mx })
	f.gw.SetServeError(errors.New("tunneltest: no stream quota left"))
	f.start()
	waitMetric(t, mx, tunnel.MetricSlotAcquireFailuresTotal, nil,
		func(s *tunneltest.Series) bool { return s.Value() >= 1 })
}

// exerciseTableMetrics connects a connection table to two replicas and gives
// it a roster, for the two node-scoped gauges.
func exerciseTableMetrics(t *testing.T, mx *tunneltest.Metrics) {
	t.Helper()

	f := newManagerFixture(t, []string{gw1}, func(cfg *tunnel.ManagerConfig) {
		cfg.Client.Metrics = mx
	})
	f.start()
	conn := f.handshake(gw1)
	f.waitConnected(1)

	f.sendRoster(conn, &tunnelv1.GatewayRoster{
		Version: 7,
		Replicas: []*tunnelv1.GatewayReplica{
			{ReplicaId: "gw-1", Endpoint: gw1, State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
			{ReplicaId: "gw-2", Endpoint: gw2, State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
		},
	})
	f.handshake(gw2)
	f.waitConnected(2)
	waitMetric(t, mx, tunnel.MetricRosterVersion, nil,
		func(s *tunneltest.Series) bool { return s.Value() == 7 })
	waitMetric(t, mx, tunnel.MetricConnectedReplicas, nil,
		func(s *tunneltest.Series) bool { return s.Value() == 2 })
}

// exerciseLimiterMetrics fills a runtime instance's single permit and sends a
// second request at it, which is the rejection node_total is calibrated by.
func exerciseLimiterMetrics(t *testing.T, mx *tunneltest.Metrics) {
	t.Helper()

	f := newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) {
		cfg.NodeID = "mac-mini-01"
		cfg.Metrics = mx
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	f.inference(1).ChatFunc = func(context.Context, runtime.ChatRequest) (runtime.ChatResponse, error) {
		entered <- struct{}{}
		<-release
		return runtime.ChatResponse{ID: "cmpl-1"}, nil
	}

	// The model name is user-visible text that a replica chooses; it must not
	// reach a label either.
	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: secretText})
	first := make(chan error, 1)
	go func() {
		_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil)
		first <- err
	}()
	<-entered

	if _, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil); err == nil {
		t.Fatal("the second request was admitted while the only permit was held")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("the first request failed: %v", err)
	}
}

// dispatchOp plays a RequestHeaders frame for one operation into a slot.
func dispatchOp(f *poolFixture, c slotConn, requestID string, op tunnelv1.Operation) {
	f.t.Helper()
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: requestID,
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId: "ollama-local",
			Operation: op,
			Trace:     map[string]string{"tenant_id": secretText},
		}},
	})
}

// waitMetric polls until a series named name and matching match satisfies
// pred. Recording happens on the tunnel's own goroutines, so the value a test
// wants may be a moment behind the frame that caused it.
func waitMetric(t *testing.T, mx *tunneltest.Metrics, name string, match map[string]string, pred func(*tunneltest.Series) bool) *tunneltest.Series {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		if s := mx.Find(name, match); s != nil && pred(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s%v never reached the expected value; recorded metrics: %v", name, match, mx.Names())
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}

// -----------------------------------------------------------------------
// Coverage
// -----------------------------------------------------------------------

func TestMetricsCoverEveryDocumentedInstrument(t *testing.T) {
	mx := exerciseMetrics(t)

	recorded := mx.Names()
	want := slices.Clone(documentedMetrics)
	sort.Strings(want)

	for _, name := range want {
		if !slices.Contains(recorded, name) {
			t.Errorf("%s is documented in README.md but nothing recorded it; recorded = %v", name, recorded)
		}
	}
	for _, name := range recorded {
		if !slices.Contains(want, name) {
			t.Errorf("%s was recorded but is not in README.md's 可观测性 list", name)
		}
	}
}

// -----------------------------------------------------------------------
// Label cardinality
// -----------------------------------------------------------------------

func TestMetricsLabelKeysAreExactlyTheDocumentedSet(t *testing.T) {
	mx := exerciseMetrics(t)

	for _, s := range mx.All() {
		want, ok := metricLabelKeys[s.Name]
		if !ok {
			t.Errorf("%s has no documented label set", s.Name)
			continue
		}
		got := make([]string, 0, len(s.Labels))
		for k := range s.Labels {
			got = append(got, k)
		}
		sort.Strings(got)
		wantSorted := slices.Clone(want)
		sort.Strings(wantSorted)
		if !slices.Equal(got, wantSorted) {
			t.Errorf("%s labels = %v, want %v", s.Name, got, wantSorted)
		}
	}
}

func TestMetricsLabelValuesComeFromClosedVocabularies(t *testing.T) {
	mx := exerciseMetrics(t)

	operations := operationLabels()
	for _, s := range mx.All() {
		for key, value := range s.Labels {
			if key == tunnel.LabelOperation {
				if !slices.Contains(operations, value) {
					t.Errorf("%s operation = %q, which is not an Operation enum name", s.Name, value)
				}
				continue
			}
			allowed, bounded := labelValues[key]
			if !bounded {
				continue
			}
			if !slices.Contains(allowed, value) {
				t.Errorf("%s %s = %q, want one of %v", s.Name, key, value, allowed)
			}
		}
	}
}

func TestMetricsLabelsNeverCarryPayloadContent(t *testing.T) {
	mx := exerciseMetrics(t)

	for _, s := range mx.All() {
		for key, value := range s.Labels {
			if strings.Contains(value, secretText) {
				t.Errorf("%s %s = %q leaked replica-supplied text into a label", s.Name, key, value)
			}
		}
	}
}

func TestMetricsPerLinkSeriesCarryReplicaID(t *testing.T) {
	mx := exerciseMetrics(t)

	for _, s := range mx.All() {
		nodeScoped := slices.Contains(tunnel.NodeScopedMetrics, s.Name)
		replica, ok := s.Labels[tunnel.LabelReplicaID]
		switch {
		case nodeScoped && ok:
			t.Errorf("%s is node scoped but carries replica_id = %q", s.Name, replica)
		case !nodeScoped && !ok:
			t.Errorf("%s carries no replica_id; a symptom on one link would be indistinguishable from a node-wide one", s.Name)
		case !nodeScoped && replica == "":
			t.Errorf("%s carries an empty replica_id", s.Name)
		}
		if s.Labels[tunnel.LabelNodeID] != "mac-mini-01" {
			t.Errorf("%s node_id = %q, want the configured node id", s.Name, s.Labels[tunnel.LabelNodeID])
		}
	}
}

// TestMetricsSeriesCountStaysBounded is the cardinality assertion proper: a
// long-running node must not grow a new series per request. Three requests
// under two operations may add series for those operations and no more.
func TestMetricsSeriesCountStaysBounded(t *testing.T) {
	mx := exerciseMetrics(t)

	// One series per (operation, result) pair actually seen, and one per
	// operation for the duration histogram. Three requests produced two
	// operations, so anything above a handful means a request-scoped label
	// slipped in.
	for _, tc := range []struct {
		name string
		max  int
	}{
		{tunnel.MetricRequestsTotal, 4},
		{tunnel.MetricRequestDurationSeconds, 2},
		{tunnel.MetricStreamFirstEventSeconds, 1},
		{tunnel.MetricFrameBytes, 4},
		{tunnel.MetricCancelTotal, 2},
		{tunnel.MetricConnectedReplicas, 1},
		{tunnel.MetricRosterVersion, 1},
		{tunnel.MetricLimiterRejectionsTotal, 1},
	} {
		if got := mx.SeriesCount(tc.name); got > tc.max {
			t.Errorf("%s has %d distinct label sets, want at most %d", tc.name, got, tc.max)
		}
	}
}

// -----------------------------------------------------------------------
// Values
// -----------------------------------------------------------------------

func TestMetricsRequestResultsUseTheSixValueConvention(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want tunnel.Result
	}{
		{
			name: "success",
			err:  nil,
			want: tunnel.ResultSuccess,
		},
		{
			name: "a rejected request is a client error",
			err:  &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, Message: "no such runtime"},
			want: tunnel.ResultClientError,
		},
		{
			name: "a capability gap is a client error",
			err:  &runtime.RuntimeError{Code: runtime.ErrorCapability, Message: "no streaming here"},
			want: tunnel.ResultClientError,
		},
		{
			name: "a backend failure is an upstream error",
			err:  &runtime.RuntimeError{Code: runtime.ErrorUpstream, Message: "backend said 500"},
			want: tunnel.ResultUpstreamError,
		},
		{
			name: "an exhausted quota is backpressure",
			err:  &runtime.RuntimeError{Code: runtime.ErrorBackpressure, Cause: runtime.ErrConcurrencyLimit},
			want: tunnel.ResultBackpressure,
		},
		{
			name: "a rate limited backend is backpressure",
			err:  &runtime.RuntimeError{Code: runtime.ErrorRateLimited},
			want: tunnel.ResultBackpressure,
		},
		{
			name: "an expired deadline is a timeout",
			err:  &runtime.RuntimeError{Code: runtime.ErrorTimeout},
			want: tunnel.ResultTimeout,
		},
		{
			name: "a cancelled request is cancelled, not a connection error",
			err:  &runtime.RuntimeError{Code: runtime.ErrorConnection, Cause: context.Canceled},
			want: tunnel.ResultCancelled,
		},
		{
			name: "a bare cancellation is cancelled",
			err:  context.Canceled,
			want: tunnel.ResultCancelled,
		},
		{
			name: "an unclassified error is an upstream error",
			err:  errors.New("something went sideways"),
			want: tunnel.ResultUpstreamError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mx := tunneltest.NewMetrics()
			f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) { cfg.Metrics = mx })
			f.start()
			conn := ofClass(f.acceptSlots(3), tunnelv1.SlotClass_SLOT_CLASS_INFERENCE)[0]

			f.handler.set(func(context.Context, *tunnel.Request, tunnel.ResponseSink) error {
				return tc.err
			})
			dispatchOp(f, conn, "req-1", tunnelv1.Operation_OPERATION_CHAT)
			f.expectEnd(conn, "req-1")

			match := map[string]string{
				tunnel.LabelOperation: tunnelv1.Operation_OPERATION_CHAT.String(),
				tunnel.LabelResult:    string(tc.want),
			}
			s := waitMetric(t, mx, tunnel.MetricRequestsTotal, match,
				func(s *tunneltest.Series) bool { return s.Value() >= 1 })
			if got := s.Value(); got != 1 {
				t.Errorf("requests_total{result=%s} = %v, want 1", tc.want, got)
			}
			// Exactly one outcome was recorded, so no request is counted twice
			// under two different results.
			if got := mx.Sum(tunnel.MetricRequestsTotal, nil); got != 1 {
				t.Errorf("requests_total across all results = %v, want 1", got)
			}
		})
	}
}

func TestMetricsSlotGaugesFollowOccupancy(t *testing.T) {
	mx := tunneltest.NewMetrics()
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) { cfg.Metrics = mx })
	f.start()
	conns := f.acceptSlots(3)
	inference := ofClass(conns, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE)

	idle := map[string]string{tunnel.LabelClass: "inference", tunnel.LabelState: tunnel.SlotStateIdle}
	busy := map[string]string{tunnel.LabelClass: "inference", tunnel.LabelState: tunnel.SlotStateBusy}
	bulkIdle := map[string]string{tunnel.LabelClass: "bulk", tunnel.LabelState: tunnel.SlotStateIdle}

	waitMetric(t, mx, tunnel.MetricSlotsTotal, idle,
		func(s *tunneltest.Series) bool { return s.Value() == 2 })
	waitMetric(t, mx, tunnel.MetricSlotsTotal, bulkIdle,
		func(s *tunneltest.Series) bool { return s.Value() == 1 })

	// One request in flight moves exactly one slot from idle to busy; the
	// bulk quota is untouched, which is the physical isolation the gauge has
	// to make visible.
	entered := make(chan struct{})
	release := make(chan struct{})
	f.handler.set(func(context.Context, *tunnel.Request, tunnel.ResponseSink) error {
		close(entered)
		<-release
		return nil
	})
	dispatchOp(f, inference[0], "req-1", tunnelv1.Operation_OPERATION_CHAT)
	<-entered

	waitMetric(t, mx, tunnel.MetricSlotsTotal, busy,
		func(s *tunneltest.Series) bool { return s.Value() == 1 })
	if got := mx.Find(tunnel.MetricSlotsTotal, bulkIdle).Value(); got != 1 {
		t.Errorf("bulk idle slots = %v while an inference request is running, want 1", got)
	}

	close(release)
	f.expectEnd(inference[0], "req-1")
	waitMetric(t, mx, tunnel.MetricSlotsTotal, busy,
		func(s *tunneltest.Series) bool { return s.Value() == 0 })
}

func TestMetricsConnectionStateFollowsTheStateMachine(t *testing.T) {
	mx := tunneltest.NewMetrics()
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) { cfg.Metrics = mx })
	f.start()

	// Before HelloAck the tunnel has state to report but no replica identity,
	// so the label is present and bounded rather than empty.
	waitMetric(t, mx, tunnel.MetricConnectionState,
		map[string]string{tunnel.LabelReplicaID: "unknown"},
		func(s *tunneltest.Series) bool { return s.Value() == tunnel.StateConnecting.Metric() })

	f.connect()
	waitMetric(t, mx, tunnel.MetricConnectionState,
		map[string]string{tunnel.LabelReplicaID: "gw-1"},
		func(s *tunneltest.Series) bool { return s.Value() == tunnel.StateConnected.Metric() })
}

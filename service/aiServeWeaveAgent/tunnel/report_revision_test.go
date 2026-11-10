package tunnel

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// revManager is a Manager whose snapshots and revisions the test sets by hand,
// counting how often the expensive Snapshot is taken.
//
// revManager 是一个由测试手工设置快照与版本号的 Manager，并统计昂贵的 Snapshot
// 被调用了多少次。
type revManager struct {
	mu            sync.Mutex
	snaps         []runtime.Snapshot
	snapshotCalls int
	// unversioned hides RevisionSource, like a Manager that predates it.
	unversioned bool
}

func (m *revManager) set(snaps ...runtime.Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snaps = snaps
}

func (m *revManager) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotCalls
}

func (m *revManager) Add(context.Context, runtime.Config) error     { return nil }
func (m *revManager) Replace(context.Context, runtime.Config) error { return nil }
func (m *revManager) Remove(context.Context, string) error          { return nil }
func (m *revManager) Get(string) (runtime.Runtime, bool)            { return nil, false }
func (m *revManager) Close(context.Context) error                   { return nil }

func (m *revManager) Snapshot() []runtime.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshotCalls++
	return append([]runtime.Snapshot(nil), m.snaps...)
}

// revSource is what a Manager exposes when it supports revisions.
type revSource struct{ *revManager }

func (m revSource) Revisions() map[string]uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	revs := make(map[string]uint64, len(m.snaps))
	for _, s := range m.snaps {
		revs[s.Descriptor.ID] = s.Revision
	}
	return revs
}

var revNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func revSnap(id string, state runtime.State, rev uint64, at time.Time) runtime.Snapshot {
	return runtime.Snapshot{
		Descriptor: runtime.Descriptor{ID: id, Kind: runtime.KindVLLM, BaseURL: "http://127.0.0.1:1", MaxConcurrent: 1},
		State:      state,
		Probe:      runtime.ProbeResult{Kind: runtime.KindVLLM, ProbedAt: at},
		Health:     runtime.HealthReport{State: state, CheckedAt: at, Latency: time.Duration(at.Second()) * time.Millisecond},
		Discovery:  runtime.Discovery{Version: "1", Models: []runtime.Model{{ID: "m"}}, DiscoveredAt: at},
		UpdatedAt:  at,
		Revision:   rev,
	}
}

// recordingStream keeps every status frame.
type recordingStream struct {
	frames []*tunnelv1.RuntimeStatus
}

func (s *recordingStream) Send(f *tunnelv1.AgentControl) error {
	if st := f.GetStatus(); st != nil {
		s.frames = append(s.frames, st)
	}
	return nil
}
func (s *recordingStream) Recv() (*tunnelv1.GatewayControl, error) { return nil, context.Canceled }
func (s *recordingStream) CloseSend() error                        { return nil }

func newRevSession(m *revManager) (*controlSession, *recordingStream) {
	var mgr runtime.Manager = m
	if !m.unversioned {
		mgr = revSource{m}
	}
	c := &Client{cfg: ClientConfig{Manager: mgr}, clock: runtime.NewSystemClock()}
	stream := &recordingStream{}
	return &controlSession{client: c, stream: stream, reported: map[string][]byte{}}, stream
}

func TestReportSkipsSnapshotWhileRevisionsAreUnchanged(t *testing.T) {
	m := &revManager{}
	m.set(revSnap("a", runtime.StateHealthy, 1, revNow), revSnap("b", runtime.StateHealthy, 2, revNow))
	s, stream := newRevSession(m)
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}
	base := m.calls()

	// Timestamps and latency move on every health check but the revision does not.
	for i := range 5 {
		at := revNow.Add(time.Duration(i+1) * 2 * time.Second)
		m.set(revSnap("a", runtime.StateHealthy, 1, at), revSnap("b", runtime.StateHealthy, 2, at))
		if err := s.report(false); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.calls(); got != base {
		t.Errorf("Snapshot was taken %d more times although no revision changed", got-base)
	}
	if got := len(stream.frames); got != 1 {
		t.Errorf("frames = %d, want only the initial full report", got)
	}
}

func TestReportSendsOnlyTheInstanceWhoseRevisionMoved(t *testing.T) {
	m := &revManager{}
	m.set(revSnap("a", runtime.StateHealthy, 1, revNow), revSnap("b", runtime.StateHealthy, 2, revNow))
	s, stream := newRevSession(m)
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}

	m.set(revSnap("a", runtime.StateHealthy, 1, revNow), revSnap("b", runtime.StateUnhealthy, 3, revNow))
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if got := len(stream.frames); got != 2 {
		t.Fatalf("frames = %d, want the initial report plus one change", got)
	}
	st := stream.frames[1]
	if st.GetFull() {
		t.Error("a change-triggered report must not be full")
	}
	if got := st.GetSnapshots(); len(got) != 1 || got[0].GetDescriptor_().GetId() != "b" || got[0].GetState() != string(runtime.StateUnhealthy) {
		t.Fatalf("snapshots = %v, want only b in state unhealthy", got)
	}

	// The change is now the baseline: an unchanged poll sends nothing, cheaply.
	base := m.calls()
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 2 || m.calls() != base {
		t.Errorf("an unchanged poll after a change must be a no-op (frames=%d, extra snapshots=%d)", len(stream.frames), m.calls()-base)
	}
}

func TestReportAbsorbsAnImmaterialRevisionBump(t *testing.T) {
	m := &revManager{}
	m.set(revSnap("a", runtime.StateHealthy, 1, revNow))
	s, stream := newRevSession(m)
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}

	// The revision moved but only timestamps differ: nothing is reported,
	// and the new revision becomes the baseline so the fast path resumes.
	m.set(revSnap("a", runtime.StateHealthy, 2, revNow.Add(time.Minute)))
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	base := m.calls()
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 1 {
		t.Errorf("frames = %d, want only the initial report", len(stream.frames))
	}
	if m.calls() != base {
		t.Error("the fast path did not resume after an immaterial revision bump")
	}
}

func TestReportFullAlwaysSendsFreshSnapshots(t *testing.T) {
	m := &revManager{}
	m.set(revSnap("a", runtime.StateHealthy, 1, revNow))
	s, stream := newRevSession(m)
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}

	later := revNow.Add(time.Minute)
	m.set(revSnap("a", runtime.StateHealthy, 1, later))
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 2 || !stream.frames[1].GetFull() {
		t.Fatalf("the 60s reconciliation must always be sent in full, got %d frames", len(stream.frames))
	}
	if got := stream.frames[1].GetSnapshots()[0].GetUpdatedAt().AsTime(); !got.Equal(later) {
		t.Errorf("full report carries UpdatedAt %s, want the fresh %s", got, later)
	}
}

func TestReportForceReportResendsEverything(t *testing.T) {
	m := &revManager{}
	m.set(revSnap("a", runtime.StateHealthy, 1, revNow), revSnap("b", runtime.StateHealthy, 2, revNow))
	s, stream := newRevSession(m)
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}
	if err := s.forceReport(); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 2 || !stream.frames[1].GetFull() || len(stream.frames[1].GetSnapshots()) != 2 {
		t.Fatalf("forceReport must resend the whole set in full, got %d frames", len(stream.frames))
	}
}

func TestReportInstanceSetChangeIsFullEvenWhenRevisionsMatch(t *testing.T) {
	m := &revManager{}
	m.set(revSnap("a", runtime.StateHealthy, 1, revNow), revSnap("b", runtime.StateHealthy, 2, revNow))
	s, stream := newRevSession(m)
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}

	m.set(revSnap("a", runtime.StateHealthy, 1, revNow))
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 2 || !stream.frames[1].GetFull() || len(stream.frames[1].GetSnapshots()) != 1 {
		t.Fatalf("a removal must be reported as a full report of what remains, got %d frames", len(stream.frames))
	}

	m.set(revSnap("a", runtime.StateHealthy, 1, revNow), revSnap("c", runtime.StateHealthy, 4, revNow))
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 3 || !stream.frames[2].GetFull() || len(stream.frames[2].GetSnapshots()) != 2 {
		t.Fatalf("an addition must be reported in full, got %d frames", len(stream.frames))
	}
}

func TestReportZeroRevisionIsPolledTheSlowWayAndStillDetectsChanges(t *testing.T) {
	m := &revManager{}
	m.set(revSnap("a", runtime.StateHealthy, 0, revNow))
	s, stream := newRevSession(m)
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}
	base := m.calls()
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if m.calls() == base {
		t.Error("an unversioned snapshot must never take the fast path")
	}
	if len(stream.frames) != 1 {
		t.Errorf("frames = %d, want 1: nothing changed", len(stream.frames))
	}

	m.set(revSnap("a", runtime.StateUnhealthy, 0, revNow))
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 2 {
		t.Error("a change on an unversioned Manager was missed")
	}
}

func TestReportWithoutRevisionSourceKeepsWorking(t *testing.T) {
	m := &revManager{unversioned: true}
	m.set(revSnap("a", runtime.StateHealthy, 7, revNow))
	s, stream := newRevSession(m)
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}
	base := m.calls()
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if m.calls() == base {
		t.Error("a Manager without RevisionSource must be polled through Snapshot")
	}
	m.set(revSnap("a", runtime.StateUnhealthy, 8, revNow))
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 2 {
		t.Error("a change was missed")
	}
}

// TestReportWithRealManagerSeesReplace proves the whole chain: a real Manager
// stamps revisions, an unchanged poll is a no-op, and replacing the instance
// with different discovery results is reported.
//
// TestReportWithRealManagerSeesReplace 验证完整链路：真实 Manager 打版本号，
// 无变化的轮询是空操作，用不同发现结果替换实例则会被上报。
func TestReportWithRealManagerSeesReplace(t *testing.T) {
	models := 3
	reg := runtime.NewRegistry()
	if err := reg.Register(runtime.KindVLLM, func(runtime.Config, runtime.Dependencies) (runtime.Runtime, error) {
		d := runtime.Discovery{Version: "1"}
		for range models {
			d.Models = append(d.Models, runtime.Model{ID: "m"})
		}
		return &benchRuntime{id: "r", discovery: d}, nil
	}); err != nil {
		t.Fatal(err)
	}
	mgr := runtime.NewManager(reg, runtime.Dependencies{HTTPClient: &http.Client{}, Logger: slog.Default(), Metrics: benchMetrics{}})
	t.Cleanup(func() { mgr.Close(context.Background()) })
	cfg := runtime.Config{ID: "r", Kind: runtime.KindVLLM, BaseURL: "http://127.0.0.1:1", HealthInterval: time.Hour, DiscoveryInterval: time.Hour}
	if err := mgr.Add(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	c := &Client{cfg: ClientConfig{Manager: mgr}, clock: runtime.NewSystemClock()}
	stream := &recordingStream{}
	s := &controlSession{client: c, stream: stream, reported: map[string][]byte{}}
	if err := s.report(true); err != nil {
		t.Fatal(err)
	}
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 1 {
		t.Fatalf("frames = %d, want only the initial report for an unchanged poll", len(stream.frames))
	}

	models = 5
	if err := mgr.Replace(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.report(false); err != nil {
		t.Fatal(err)
	}
	if len(stream.frames) != 2 {
		t.Fatalf("frames = %d: replacing the instance with a different catalogue was not reported", len(stream.frames))
	}
	got := stream.frames[1].GetSnapshots()
	if len(got) != 1 || len(got[0].GetDiscovery().GetModels()) != 5 {
		t.Fatalf("reported snapshots = %v, want one with the 5 new models", got)
	}
}

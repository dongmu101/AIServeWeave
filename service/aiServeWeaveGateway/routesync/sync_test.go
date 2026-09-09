package routesync

import (
	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publication(rev int64, model string) modelroute.Snapshot {
	r := []modelroute.Route{{Model: "alias", Targets: []modelroute.Target{{RuntimeModel: model}}}}
	d, _ := modelroute.Digest(r)
	return modelroute.Snapshot{RevisionInfo: modelroute.RevisionInfo{Revision: rev, Digest: d, CreatedAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), ActorID: "operator"}, Routes: r}
}
func TestSyncRetainsLastGoodAndRestarts(t *testing.T) {
	current := publication(2, "old")
	status := 200
	oversized := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/routes/current" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(status)
		if oversized {
			_, _ = w.Write([]byte(strings.Repeat("x", modelroute.MaxDocumentBytes+1)))
			return
		}
		_ = json.NewEncoder(w).Encode(current)
	}))
	defer srv.Close()
	var active *routing.Table
	cfg := Config{Endpoint: srv.URL, Token: "secret", StateFile: filepath.Join(t.TempDir(), "routes.json"), Apply: func(v *routing.Table) { active = v }}
	syncer, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = syncer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		snapshot modelroute.Snapshot
		code     int
		large    bool
	}{
		{"regression", publication(1, "bad"), 200, false}, {"same revision changed", publication(2, "bad"), 200, false},
		{"bad digest", modelroute.Snapshot{RevisionInfo: modelroute.RevisionInfo{Revision: 3, Digest: "bad"}}, 200, false},
		{"unavailable", publication(3, "bad"), 503, false}, {"oversized", publication(3, "bad"), 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, status, oversized = tc.snapshot, tc.code, tc.large
			if err := syncer.Sync(context.Background()); err == nil {
				t.Fatal("want rejection, got nil")
			}
			got, _ := active.Resolve("alias")
			if got[0].RuntimeModel != "old" || syncer.Status().Revision != 2 {
				t.Fatalf("want old revision 2, got %+v", syncer.Status())
			}
		})
	}
	current, status, oversized = publication(3, "new"), 200, false
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = 503
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarted.Status().Revision != 3 {
		t.Fatalf("want cached revision 3, got %+v", restarted.Status())
	}
	if err = os.WriteFile(cfg.StateFile, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	broken, _ := New(cfg)
	if err = broken.Start(context.Background()); err == nil {
		t.Fatal("want cold startup failure, got nil")
	}
}
func TestPersistBeforeActivate(t *testing.T) {
	current := publication(1, "one")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(current) }))
	defer srv.Close()
	calls := 0
	syncer, err := New(Config{Endpoint: srv.URL, Token: "token", StateFile: filepath.Join(t.TempDir(), "missing", "cache"), Apply: func(*routing.Table) { calls++ }})
	if err != nil {
		t.Fatal(err)
	}
	if err = syncer.Start(context.Background()); err == nil || calls != 0 {
		t.Fatalf("want failure without activation, got err=%v calls=%d", err, calls)
	}
}

func TestRejectMalformedEnvelopeOnNetworkAndDisk(t *testing.T) {
	emptyDigest, _ := modelroute.Digest(nil)
	for _, tc := range []struct {
		name   string
		mutate func(*modelroute.Snapshot)
	}{
		{"null routes", func(s *modelroute.Snapshot) { s.Routes = nil; s.Digest = emptyDigest }},
		{"missing timestamp", func(s *modelroute.Snapshot) { s.CreatedAt = time.Time{} }},
		{"missing actor", func(s *modelroute.Snapshot) { s.ActorID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := publication(1, "real")
			tc.mutate(&snap)
			body, _ := json.Marshal(snap)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "state")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			activated := false
			s, err := New(Config{Endpoint: srv.URL, Token: "token", StateFile: path, Apply: func(*routing.Table) { activated = true }})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Start(context.Background()); err == nil || activated {
				t.Fatalf("want rejected malformed cache and response, got error=%v activated=%v", err, activated)
			}
		})
	}
}

func TestStatusIsUnconfirmedDuringActivation(t *testing.T) {
	var s *Syncer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(publication(1, "real")) }))
	defer srv.Close()
	var err error
	s, err = New(Config{Endpoint: srv.URL, Token: "token", StateFile: filepath.Join(t.TempDir(), "state"), Apply: func(*routing.Table) {
		if got := s.Status(); got.Error != "activating" {
			t.Errorf("want activating state, got %+v", got)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(); got.Error != "" || got.Revision != 1 {
		t.Fatalf("want healthy revision 1, got %+v", got)
	}
}

type observedClock struct {
	*gatewaytest.Clock
	registered chan struct{}
}

func (c *observedClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	ch, stop := c.Clock.NewTimer(d)
	c.registered <- struct{}{}
	return ch, stop
}

func TestRunUsesClockAndDoesNotOverlapPulls(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_ = json.NewEncoder(w).Encode(publication(1, "real"))
	}))
	defer srv.Close()
	clock := &observedClock{Clock: gatewaytest.NewClock(), registered: make(chan struct{}, 2)}
	s, err := New(Config{Endpoint: srv.URL, Token: "token", StateFile: filepath.Join(t.TempDir(), "state"), Interval: time.Minute, Clock: clock, Apply: func(*routing.Table) {}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	<-clock.registered
	clock.Advance(time.Minute)
	<-entered
	clock.Advance(10 * time.Minute)
	if got := clock.PendingTimers(); got != 0 {
		t.Errorf("want no polling timer while pull is in flight, got %d", got)
	}
	close(release)
	<-clock.registered
	if got := s.Status(); got.Revision != 1 || !got.CheckedAt.Equal(clock.Now()) {
		t.Errorf("want revision 1 and injected timestamp, got %+v", got)
	}
	cancel()
	<-done
	if got := clock.PendingTimers(); got != 0 {
		t.Fatalf("want no pending timer after cancellation, got %d", got)
	}
}

package workflowsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

const bundleGraph = `{"6":{"class_type":"CLIPTextEncode","inputs":{"text":"a cat"}}}`

func bundle(rev int64, description string) []workflowtemplate.Snapshot {
	content := workflowtemplate.Content{Description: description, Graph: []byte(bundleGraph)}
	d, _ := workflowtemplate.Digest("alpha", content, nil)
	return []workflowtemplate.Snapshot{{
		RevisionInfo: workflowtemplate.RevisionInfo{TemplateID: "alpha", Revision: rev, Digest: d, CreatedAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), ActorID: "operator"},
		Content:      content,
	}}
}

func TestSyncRetainsLastGoodAndRestarts(t *testing.T) {
	current := bundle(2, "old")
	status := 200
	oversized := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/workflow-templates/current" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(status)
		if oversized {
			_, _ = w.Write([]byte(strings.Repeat("x", MaxBundleBytes+1)))
			return
		}
		_ = json.NewEncoder(w).Encode(current)
	}))
	defer srv.Close()
	var active *workflow.Registry
	cfg := Config{Endpoint: srv.URL, Token: "secret", StateFile: filepath.Join(t.TempDir(), "workflow-templates.json"), Apply: func(v *workflow.Registry) { active = v }}
	syncer, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = syncer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		snaps []workflowtemplate.Snapshot
		code  int
		large bool
	}{
		{"regression", bundle(1, "bad"), 200, false},
		{"same revision changed", bundle(2, "bad"), 200, false},
		{"bad digest", []workflowtemplate.Snapshot{{RevisionInfo: workflowtemplate.RevisionInfo{TemplateID: "alpha", Revision: 3, Digest: "bad", CreatedAt: time.Now(), ActorID: "operator"}}}, 200, false},
		{"unavailable", bundle(3, "bad"), 503, false},
		{"oversized", bundle(3, "bad"), 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, status, oversized = tc.snaps, tc.code, tc.large
			if err := syncer.Sync(context.Background()); err == nil {
				t.Fatal("want rejection, got nil")
			}
			tpl, _ := active.Lookup("alpha")
			if tpl == nil || tpl.Description != "old" || syncer.Status().TemplateCount != 1 {
				t.Fatalf("want old bundle retained, got %+v status=%+v", tpl, syncer.Status())
			}
		})
	}
	current, status, oversized = bundle(3, "new"), 200, false
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
	if restarted.Status().TemplateCount != 1 || restarted.Status().BundleDigest != syncer.Status().BundleDigest {
		t.Fatalf("want cached bundle restored, got %+v", restarted.Status())
	}
	if err = os.WriteFile(cfg.StateFile, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	broken, _ := New(cfg)
	if err = broken.Start(context.Background()); err == nil {
		t.Fatal("want cold startup failure, got nil")
	}
}

func TestSyncAcceptsAnEmptyBundle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]workflowtemplate.Snapshot{})
	}))
	defer srv.Close()
	syncer, err := New(Config{Endpoint: srv.URL, Token: "token", StateFile: filepath.Join(t.TempDir(), "state"), Apply: func(*workflow.Registry) {}})
	if err != nil {
		t.Fatal(err)
	}
	if err = syncer.Start(context.Background()); err != nil {
		t.Fatalf("Start() with an empty published bundle = %v, want nil", err)
	}
	if got := syncer.Status(); got.TemplateCount != 0 || got.AppliedAt.IsZero() {
		t.Fatalf("want an activated empty bundle, got %+v", got)
	}
}

func TestPersistBeforeActivate(t *testing.T) {
	current := bundle(1, "one")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(current) }))
	defer srv.Close()
	calls := 0
	syncer, err := New(Config{Endpoint: srv.URL, Token: "token", StateFile: filepath.Join(t.TempDir(), "missing", "cache"), Apply: func(*workflow.Registry) { calls++ }})
	if err != nil {
		t.Fatal(err)
	}
	if err = syncer.Start(context.Background()); err == nil || calls != 0 {
		t.Fatalf("want failure without activation, got err=%v calls=%d", err, calls)
	}
}

func TestRejectMalformedEnvelopeOnNetworkAndDisk(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]workflowtemplate.Snapshot) []workflowtemplate.Snapshot
	}{
		{"missing timestamp", func(s []workflowtemplate.Snapshot) []workflowtemplate.Snapshot {
			s[0].CreatedAt = time.Time{}
			return s
		}},
		{"missing actor", func(s []workflowtemplate.Snapshot) []workflowtemplate.Snapshot { s[0].ActorID = ""; return s }},
		{"invalid template graph", func(s []workflowtemplate.Snapshot) []workflowtemplate.Snapshot {
			s[0].Graph = []byte(`"nope"`)
			return s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snaps := tc.mutate(bundle(1, "real"))
			body, _ := json.Marshal(snaps)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "state")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			activated := false
			s, err := New(Config{Endpoint: srv.URL, Token: "token", StateFile: path, Apply: func(*workflow.Registry) { activated = true }})
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(bundle(1, "real")) }))
	defer srv.Close()
	var err error
	s, err = New(Config{Endpoint: srv.URL, Token: "token", StateFile: filepath.Join(t.TempDir(), "state"), Apply: func(*workflow.Registry) {
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
	if got := s.Status(); got.Error != "" || got.TemplateCount != 1 {
		t.Fatalf("want healthy bundle, got %+v", got)
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
		_ = json.NewEncoder(w).Encode(bundle(1, "real"))
	}))
	defer srv.Close()
	clock := &observedClock{Clock: gatewaytest.NewClock(), registered: make(chan struct{}, 2)}
	s, err := New(Config{Endpoint: srv.URL, Token: "token", StateFile: filepath.Join(t.TempDir(), "state"), Interval: time.Minute, Clock: clock, Apply: func(*workflow.Registry) {}})
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
	if got := s.Status(); got.TemplateCount != 1 || !got.CheckedAt.Equal(clock.Now()) {
		t.Errorf("want a healthy bundle and injected timestamp, got %+v", got)
	}
	cancel()
	<-done
	if got := clock.PendingTimers(); got != 0 {
		t.Fatalf("want no pending timer after cancellation, got %d", got)
	}
}

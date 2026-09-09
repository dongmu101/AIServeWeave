package fleet_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/modelroute"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
)

type routingTransport func(*http.Request) (*http.Response, error)

func (f routingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRoutingStatusRequiresEveryConfiguredReplica(t *testing.T) {
	digest, _ := modelroute.Digest([]modelroute.Route{})
	desired := modelroute.Snapshot{RevisionInfo: modelroute.RevisionInfo{Revision: 2, Digest: digest}, Routes: []modelroute.Route{}}
	for _, tt := range []struct {
		name, mode, errcode         string
		revision                    int64
		badDigest, duplicate, stale bool
		status                      int
		complete                    bool
	}{
		{name: "all applied", mode: "controlplane", revision: 2, status: 200, complete: true},
		{name: "file mode", mode: "file", revision: 2, status: 200},
		{name: "old revision", mode: "controlplane", revision: 1, status: 200},
		{name: "digest mismatch", mode: "controlplane", revision: 2, status: 200, badDigest: true},
		{name: "failed replica", status: 503},
		{name: "unsupported replica", status: 404},
		{name: "poll failure", mode: "controlplane", revision: 2, status: 200, errcode: "unreachable"},
		{name: "duplicate replica IDs", mode: "controlplane", revision: 2, status: 200, duplicate: true},
		{name: "stale observation", mode: "controlplane", revision: 2, status: 200, stale: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: routingTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/internal/v1/routes" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Errorf("request = %s %s, want route path with token", r.URL.Path, r.Header.Get("Authorization"))
				}
				id := r.URL.Host
				if tt.duplicate {
					id = "same"
				}
				d := digest
				if tt.badDigest {
					d = strings.Repeat("a", 64)
				}
				generated := collectedAt
				if tt.stale {
					generated = generated.Add(-time.Hour)
				}
				doc := modelroute.ReplicaStatus{Applied: modelroute.Applied{Mode: tt.mode, Revision: tt.revision, Digest: d, AppliedAt: collectedAt, CheckedAt: collectedAt, Error: tt.errcode}, ReplicaID: id, GeneratedAt: generated}
				body, _ := json.Marshal(doc)
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			a := fleet.New(fleet.Config{Gateways: []string{"http://one", "http://two"}, Token: token, Client: client, Clock: fixedClock{collectedAt}})
			got := a.RoutingStatus(context.Background(), desired)
			if got.Complete != tt.complete || len(got.Replicas) != 2 {
				t.Fatalf("complete/count = %v/%d, want %v/2", got.Complete, len(got.Replicas), tt.complete)
			}
		})
	}
	var disabled *fleet.Aggregator
	if got := disabled.RoutingStatus(context.Background(), desired); got.Complete || len(got.Replicas) != 0 {
		t.Fatalf("no config = %+v, want incomplete empty", got)
	}
}

func TestRoutingStatusRejectsUnusableDocuments(t *testing.T) {
	digest, _ := modelroute.Digest([]modelroute.Route{})
	base := modelroute.ReplicaStatus{Applied: modelroute.Applied{Mode: "controlplane", Revision: 1, Digest: digest, AppliedAt: collectedAt, CheckedAt: collectedAt}, ReplicaID: "one", GeneratedAt: collectedAt}
	for _, tc := range []struct {
		name   string
		mutate func(*modelroute.ReplicaStatus)
		raw    string
	}{
		{name: "null", raw: "null"}, {name: "oversized", raw: strings.Repeat(" ", 16*1024+1)},
		{name: "invalid digest", mutate: func(d *modelroute.ReplicaStatus) { d.Digest = "not-a-digest" }},
		{name: "missing applied time", mutate: func(d *modelroute.ReplicaStatus) { d.AppliedAt = time.Time{} }},
		{name: "missing check time", mutate: func(d *modelroute.ReplicaStatus) { d.CheckedAt = time.Time{} }},
		{name: "future generation", mutate: func(d *modelroute.ReplicaStatus) { d.GeneratedAt = collectedAt.Add(time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := base
			if tc.mutate != nil {
				tc.mutate(&doc)
			}
			body, _ := json.Marshal(doc)
			if tc.raw != "" {
				body = []byte(tc.raw)
			}
			a := fleet.New(fleet.Config{Gateways: []string{"http://one"}, Token: token, Clock: fixedClock{collectedAt}, Client: &http.Client{Transport: routingTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}})
			got := a.RoutingStatus(context.Background(), modelroute.Snapshot{RevisionInfo: modelroute.RevisionInfo{Revision: 1, Digest: digest}})
			if got.Complete || len(got.Replicas) != 1 || got.Replicas[0].Error == "" {
				t.Fatalf("report=%+v, want failed observation", got)
			}
		})
	}
}

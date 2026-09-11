package metricshistory_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/metricshistory"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// fakeDoer returns a fixed Prometheus text body for every request, keyed by URL.
type fakeDoer struct{ bodies map[string]string }

func (f fakeDoer) Do(req *http.Request) (*http.Response, error) {
	body, ok := f.bodies[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// fakeStore records every UpsertRollup call for assertions.
type fakeStore struct{ upserts [][]model.MetricsHistoryPoint }

func (f *fakeStore) UpsertRollup(ctx context.Context, points []model.MetricsHistoryPoint) error {
	f.upserts = append(f.upserts, points)
	return nil
}

func TestCollectOnceSumsAcrossReplicasAndDropsNodeID(t *testing.T) {
	doer := fakeDoer{bodies: map[string]string{
		"http://gw-1:9090/metrics": "gateway_http_requests_total{endpoint=\"chat\",status=\"200\"} 10\n" +
			"tunnel_server_slots_total{node_id=\"n1\",class=\"chat\",state=\"idle\"} 3\n",
		"http://gw-2:9090/metrics": "gateway_http_requests_total{endpoint=\"chat\",status=\"200\"} 7\n" +
			"tunnel_server_slots_total{node_id=\"n2\",class=\"chat\",state=\"idle\"} 5\n",
	}}
	store := &fakeStore{}
	c := metricshistory.New(metricshistory.Config{
		GatewayAddrs: []string{"http://gw-1:9090", "http://gw-2:9090"},
		Interval:     5 * time.Minute,
		Store:        store,
		Client:       doer,
	})

	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	if err := c.CollectOnce(context.Background(), at); err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}

	if len(store.upserts) != 1 {
		t.Fatalf("UpsertRollup called %d times, want 1", len(store.upserts))
	}
	points := store.upserts[0]
	var gotRequests, gotSlots bool
	for _, p := range points {
		if p.Metric == "gateway_http_requests_total" && p.Value == 17 {
			gotRequests = true
		}
		if p.Metric == "tunnel_server_slots_total" && p.Value == 8 {
			gotSlots = true
			if strings.Contains(p.Labels, "node_id") {
				t.Errorf("tunnel_server_slots_total labels = %q, want node_id dropped", p.Labels)
			}
		}
		if !p.BucketAt.Equal(at) {
			t.Errorf("point %+v BucketAt = %v, want %v", p, p.BucketAt, at)
		}
	}
	if !gotRequests {
		t.Error("did not find gateway_http_requests_total summed to 17 across both replicas")
	}
	if !gotSlots {
		t.Error("did not find tunnel_server_slots_total summed to 8 with node_id rolled up away")
	}
}

func TestCollectOnceUnreachableSourceStillWritesTheRest(t *testing.T) {
	doer := fakeDoer{bodies: map[string]string{
		"http://gw-1:9090/metrics": "gateway_http_requests_total{endpoint=\"chat\",status=\"200\"} 4\n",
	}}
	store := &fakeStore{}
	c := metricshistory.New(metricshistory.Config{
		GatewayAddrs: []string{"http://gw-1:9090", "http://gw-down:9090"},
		Interval:     5 * time.Minute,
		Store:        store,
		Client:       doer,
	})

	if err := c.CollectOnce(context.Background(), time.Now()); err != nil {
		t.Fatalf("CollectOnce() error = %v, want a partial scrape failure to not fail the whole collection", err)
	}
	if len(store.upserts) != 1 || len(store.upserts[0]) == 0 {
		t.Fatal("expected the reachable replica's samples to still be written")
	}
}

func TestCollectOnceIgnoresMetricsOutsideTheAllowlist(t *testing.T) {
	doer := fakeDoer{bodies: map[string]string{
		"http://gw-1:9090/metrics": "some_unrelated_metric{label=\"x\"} 1\n",
	}}
	store := &fakeStore{}
	c := metricshistory.New(metricshistory.Config{
		GatewayAddrs: []string{"http://gw-1:9090"},
		Interval:     5 * time.Minute,
		Store:        store,
		Client:       doer,
	})

	if err := c.CollectOnce(context.Background(), time.Now()); err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("UpsertRollup called %d times, want 0 for a metric outside the allowlist", len(store.upserts))
	}
}

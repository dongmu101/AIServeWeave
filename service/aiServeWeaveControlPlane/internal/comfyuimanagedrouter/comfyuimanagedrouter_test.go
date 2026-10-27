package comfyuimanagedrouter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	goruntime "runtime"
	"testing"
	"time"

	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/comfyuimanagedrouter"
)

// TestMain asserts no test in this package leaks a goroutine, the same
// assertion modelpullrouter's TestMain makes for its own fan-out.
func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !settles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func settles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if goruntime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const token = "comfyui-managed-token-long-enough-for-a-test"

// managed is one replica's fake ComfyUI Managed listener, mirroring
// comfyuimanagedapi's contract: POST answers 202 or 404 by connected, GET
// answers the given instances or 404.
func managed(t *testing.T, connected bool, instances []map[string]any) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !connected {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"generated_at": time.Now().UTC().Format(time.RFC3339),
				"instances":    instances,
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func router(t *testing.T, endpoints ...string) *comfyuimanagedrouter.Router {
	t.Helper()
	return comfyuimanagedrouter.New(comfyuimanagedrouter.Config{Gateways: endpoints, Token: token, Timeout: time.Second})
}

func TestTriggerReportsConnectedWhenAnyReplicaHasTheNode(t *testing.T) {
	elsewhere := managed(t, false, nil)
	here := managed(t, true, nil)

	result, err := router(t, elsewhere, here).Trigger(context.Background(), "node-a", comfyuimanagedstatus.ActionStart)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !result.Connected {
		t.Fatalf("Connected = false, want true when one replica has the node")
	}
	if len(result.Replicas) != 2 {
		t.Fatalf("got %d replica statuses, want 2", len(result.Replicas))
	}
	if result.Replicas[0].Endpoint != elsewhere || result.Replicas[0].Connected {
		t.Errorf("replica[0] = %+v, want the unconnected endpoint reported not connected", result.Replicas[0])
	}
	if result.Replicas[1].Endpoint != here || !result.Replicas[1].Connected {
		t.Errorf("replica[1] = %+v, want the connected endpoint reported connected", result.Replicas[1])
	}
}

func TestTriggerReportsNotConnectedWhenNoReplicaHasTheNode(t *testing.T) {
	a := managed(t, false, nil)
	b := managed(t, false, nil)

	result, err := router(t, a, b).Trigger(context.Background(), "node-a", comfyuimanagedstatus.ActionRestart)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if result.Connected {
		t.Fatalf("Connected = true, want false when no replica has the node")
	}
}

func TestInstallCustomNodeReportsConnectedWhenAnyReplicaHasTheNode(t *testing.T) {
	elsewhere := managed(t, false, nil)
	here := managed(t, true, nil)

	result, err := router(t, elsewhere, here).InstallCustomNode(context.Background(), "node-a", "my-node")
	if err != nil {
		t.Fatalf("InstallCustomNode: %v", err)
	}
	if !result.Connected {
		t.Fatalf("Connected = false, want true when one replica has the node")
	}
}

func TestInstallCustomNodeReportsNotConnectedWhenNoReplicaHasTheNode(t *testing.T) {
	a := managed(t, false, nil)
	b := managed(t, false, nil)

	result, err := router(t, a, b).InstallCustomNode(context.Background(), "node-a", "my-node")
	if err != nil {
		t.Fatalf("InstallCustomNode: %v", err)
	}
	if result.Connected {
		t.Fatalf("Connected = true, want false when no replica has the node")
	}
}

func TestStatusReturnsTheInstanceFromTheConnectedReplicaWithCustomNodes(t *testing.T) {
	here := managed(t, true, []map[string]any{
		{
			"container_name": "aisw-comfyui-managed",
			"state":          "running",
			"updated_at":     time.Now().UTC().Format(time.RFC3339),
			"custom_nodes":   []map[string]any{{"name": "my-node", "version": "v1"}},
		},
	})

	result, err := router(t, here).Status(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(result.Instances) != 1 || len(result.Instances[0].CustomNodes) != 1 {
		t.Fatalf("Instances = %+v, want one entry with one custom node", result.Instances)
	}
	if result.Instances[0].CustomNodes[0].Name != "my-node" || result.Instances[0].CustomNodes[0].Version != "v1" {
		t.Errorf("CustomNodes[0] = %+v, want name=my-node version=v1", result.Instances[0].CustomNodes[0])
	}
}

func TestStatusReturnsTheInstanceFromTheConnectedReplica(t *testing.T) {
	here := managed(t, true, []map[string]any{
		{"container_name": "aisw-comfyui-managed", "state": "running", "updated_at": time.Now().UTC().Format(time.RFC3339)},
	})
	elsewhere := managed(t, false, nil)

	result, err := router(t, elsewhere, here).Status(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !result.Connected {
		t.Fatalf("Connected = false, want true")
	}
	if len(result.Instances) != 1 || result.Instances[0].ContainerName != "aisw-comfyui-managed" || result.Instances[0].State != "running" {
		t.Fatalf("Instances = %+v, want the one entry the connected replica reported", result.Instances)
	}
}

// TestStatusPrefersTheFreshestReplica mirrors
// modelpullrouter's TestStatusPrefersTheFreshestReplica: when the node is
// connected to more than one configured replica, the reply with the more
// recently updated entry wins.
func TestStatusPrefersTheFreshestReplica(t *testing.T) {
	older := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	newer := time.Now().UTC().Format(time.RFC3339)

	stale := managed(t, true, []map[string]any{
		{"container_name": "aisw-comfyui-managed", "state": "starting", "updated_at": older},
	})
	fresh := managed(t, true, []map[string]any{
		{"container_name": "aisw-comfyui-managed", "state": "running", "updated_at": newer},
	})

	result, err := router(t, stale, fresh).Status(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(result.Instances) != 1 || result.Instances[0].State != "running" {
		t.Fatalf("Instances = %+v, want the fresher replica's running state", result.Instances)
	}
}

func TestFailuresAreClassifiedAndNeverCarryTransportText(t *testing.T) {
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(unauthorized.Close)

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{this is not json"))
	}))
	t.Cleanup(broken.Close)

	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()

	result, err := router(t, unauthorized.URL, broken.URL, goneURL).Status(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result.Connected {
		t.Fatalf("Connected = true, want false: every configured replica failed")
	}

	wantErrors := map[string]string{
		unauthorized.URL: "unauthorized",
		broken.URL:       "malformed",
		goneURL:          "unreachable",
	}
	if len(result.Replicas) != len(wantErrors) {
		t.Fatalf("got %d replica statuses, want %d", len(result.Replicas), len(wantErrors))
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshalling the result: %v", err)
	}
	for _, status := range result.Replicas {
		want, known := wantErrors[status.Endpoint]
		if !known {
			t.Errorf("unexpected endpoint %q", status.Endpoint)
			continue
		}
		if status.Error != want {
			t.Errorf("%s: Error = %q, want %q", status.Endpoint, status.Error, want)
		}
	}
	for _, leak := range []string{"connection refused", "dial tcp", "EOF", "invalid character"} {
		if containsText(string(body), leak) {
			t.Errorf("the result carried transport text %q:\n%s", leak, body)
		}
	}
}

// TestASlowReplicaDoesNotHoldTheAnswer mirrors modelpullrouter's test of the
// same name: the timeout bounds one replica rather than the whole call.
func TestASlowReplicaDoesNotHoldTheAnswer(t *testing.T) {
	fast := managed(t, true, nil)

	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(block)
		slow.Close()
	})

	r := comfyuimanagedrouter.New(comfyuimanagedrouter.Config{
		Gateways: []string{fast, slow.URL},
		Token:    token,
		Timeout:  100 * time.Millisecond,
	})

	started := time.Now()
	result, err := r.Trigger(context.Background(), "node-a", comfyuimanagedstatus.ActionStop)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Trigger took %s, want it bounded by the 100ms per-replica timeout", elapsed)
	}
	if !result.Connected {
		t.Fatalf("Connected = false, want true: the fast replica answered")
	}
	for _, status := range result.Replicas {
		if status.Endpoint == slow.URL && status.Error != "timeout" {
			t.Errorf("slow replica Error = %q, want %q", status.Error, "timeout")
		}
	}
}

func TestUnconfiguredRouterIsDisabled(t *testing.T) {
	r := comfyuimanagedrouter.New(comfyuimanagedrouter.Config{})
	if r != nil {
		t.Fatal("New() returned a router for an empty configuration")
	}
	if _, err := r.Trigger(context.Background(), "node-a", comfyuimanagedstatus.ActionStart); err != comfyuimanagedrouter.ErrDisabled {
		t.Errorf("Trigger() error = %v, want ErrDisabled", err)
	}
	if _, err := r.Status(context.Background(), "node-a"); err != comfyuimanagedrouter.ErrDisabled {
		t.Errorf("Status() error = %v, want ErrDisabled", err)
	}
	if _, err := r.InstallCustomNode(context.Background(), "node-a", "my-node"); err != comfyuimanagedrouter.ErrDisabled {
		t.Errorf("InstallCustomNode() error = %v, want ErrDisabled", err)
	}
}

func containsText(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

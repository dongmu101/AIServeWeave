package comfyuimanagedapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	goruntime "runtime"
	"testing"
	"time"

	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/service/aiServeWeaveGateway/comfyuimanagedapi"
)

// TestMain asserts no test in this package leaks a goroutine, per the
// repository quality gate.
//
// TestMain 断言本包没有测试泄漏协程，对应仓库的质量门禁。
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

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
func (c fixedClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

const token = "comfyui-managed-token-long-enough-for-a-test"

var generatedAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// fakeServer stands in for tunnelserver.Server, so this package's tests
// never need a real tunnel.
type fakeServer struct {
	triggerErr             error
	triggered              map[string]comfyuimanagedstatus.Action
	statusByID             map[string][]comfyuimanagedstatus.Status
	knownByID              map[string]bool
	activeJobByID          map[string]bool
	customNodeInstallErr   error
	customNodeInstallCalls map[string]string
}

func (f *fakeServer) trigger(nodeID string, action comfyuimanagedstatus.Action) error {
	if f.triggerErr != nil {
		return f.triggerErr
	}
	if f.triggered == nil {
		f.triggered = map[string]comfyuimanagedstatus.Action{}
	}
	f.triggered[nodeID] = action
	return nil
}

func (f *fakeServer) status(nodeID string) ([]comfyuimanagedstatus.Status, bool) {
	if !f.knownByID[nodeID] {
		return nil, false
	}
	return f.statusByID[nodeID], true
}

func (f *fakeServer) hasActiveJob(nodeID string) bool {
	return f.activeJobByID[nodeID]
}

func (f *fakeServer) triggerCustomNodeInstall(nodeID, name string) error {
	if f.customNodeInstallErr != nil {
		return f.customNodeInstallErr
	}
	if f.customNodeInstallCalls == nil {
		f.customNodeInstallCalls = map[string]string{}
	}
	f.customNodeInstallCalls[nodeID] = name
	return nil
}

func serve(t *testing.T, fs *fakeServer) http.Handler {
	t.Helper()
	handler, err := comfyuimanagedapi.New(comfyuimanagedapi.Config{
		Token:                    token,
		Clock:                    fixedClock{now: generatedAt},
		Trigger:                  fs.trigger,
		Status:                   fs.status,
		HasActiveJob:             fs.hasActiveJob,
		TriggerCustomNodeInstall: fs.triggerCustomNodeInstall,
	})
	if err != nil {
		t.Fatalf("comfyuimanagedapi.New: %v", err)
	}
	return handler
}

func TestTheListenerRefusesToStartUnconfigured(t *testing.T) {
	fs := &fakeServer{}
	tests := []struct {
		name string
		cfg  comfyuimanagedapi.Config
	}{
		{"no token", comfyuimanagedapi.Config{Trigger: fs.trigger, Status: fs.status, HasActiveJob: fs.hasActiveJob, TriggerCustomNodeInstall: fs.triggerCustomNodeInstall}},
		{"no trigger", comfyuimanagedapi.Config{Token: token, Status: fs.status, HasActiveJob: fs.hasActiveJob, TriggerCustomNodeInstall: fs.triggerCustomNodeInstall}},
		{"no status", comfyuimanagedapi.Config{Token: token, Trigger: fs.trigger, HasActiveJob: fs.hasActiveJob, TriggerCustomNodeInstall: fs.triggerCustomNodeInstall}},
		{"no active job checker", comfyuimanagedapi.Config{Token: token, Trigger: fs.trigger, Status: fs.status, TriggerCustomNodeInstall: fs.triggerCustomNodeInstall}},
		{"no custom node installer", comfyuimanagedapi.Config{Token: token, Trigger: fs.trigger, Status: fs.status, HasActiveJob: fs.hasActiveJob}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := comfyuimanagedapi.New(tt.cfg); err == nil {
				t.Fatal("New did not refuse an incomplete configuration")
			}
		})
	}
}

func TestComfyUIManagedRequiresTheToken(t *testing.T) {
	fs := &fakeServer{knownByID: map[string]bool{"node-a": true}}
	handler := serve(t, fs)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"the comfyui managed token", "Bearer " + token, http.StatusOK},
		{"no header at all", "", http.StatusUnauthorized},
		{"the wrong token", "Bearer nope", http.StatusUnauthorized},
		{"the right token, wrong scheme", "Basic " + token, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes/node-a/comfyui-managed", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestTriggerDispatchesAndAnswersAccepted(t *testing.T) {
	fs := &fakeServer{}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string]string{"action": "restart"})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if got := fs.triggered["node-a"]; got != comfyuimanagedstatus.ActionRestart {
		t.Fatalf("triggered[node-a] = %v, want ActionRestart", got)
	}
}

func TestRestartIsRefusedWhileAJobIsActive(t *testing.T) {
	fs := &fakeServer{activeJobByID: map[string]bool{"node-a": true}}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string]string{"action": "restart"})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if _, dispatched := fs.triggered["node-a"]; dispatched {
		t.Fatal("Trigger was called despite the active-job refusal")
	}
}

func TestStartAndStopAreNotBlockedByAnActiveJob(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		t.Run(action, func(t *testing.T) {
			fs := &fakeServer{activeJobByID: map[string]bool{"node-a": true}}
			handler := serve(t, fs)

			body, _ := json.Marshal(map[string]string{"action": action})
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
			}
		})
	}
}

func TestRestartProceedsOnceNoJobIsActive(t *testing.T) {
	fs := &fakeServer{activeJobByID: map[string]bool{"node-a": false}}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string]string{"action": "restart"})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if got := fs.triggered["node-a"]; got != comfyuimanagedstatus.ActionRestart {
		t.Fatalf("triggered[node-a] = %v, want ActionRestart", got)
	}
}

func TestTriggerOnADisconnectedNodeIs404(t *testing.T) {
	fs := &fakeServer{triggerErr: errors.New("tunnelserver: node is not connected to this replica")}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string]string{"action": "start"})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/ghost/comfyui-managed", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestTriggerRejectsAnEmptyOrMalformedBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty action", `{"action": ""}`},
		{"unknown action", `{"action": "delete"}`},
		{"not json", `not json`},
		{"wrong shape", `{"action": 1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &fakeServer{}
			handler := serve(t, fs)
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed", bytes.NewReader([]byte(tt.body)))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestStatusRendersStatesAndTimestamps(t *testing.T) {
	fs := &fakeServer{
		knownByID: map[string]bool{"node-a": true},
		statusByID: map[string][]comfyuimanagedstatus.Status{
			"node-a": {
				{ContainerName: "aiserveweave-comfyui", State: comfyuimanagedstatus.StateRunning, UpdatedAt: generatedAt},
			},
		},
	}
	handler := serve(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes/node-a/comfyui-managed", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got struct {
		GeneratedAt string `json:"generated_at"`
		Instances   []struct {
			ContainerName string `json:"container_name"`
			State         string `json:"state"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.GeneratedAt == "" {
		t.Error("generated_at is empty")
	}
	if len(got.Instances) != 1 {
		t.Fatalf("instances = %v, want 1 entry", got.Instances)
	}
	if got.Instances[0].ContainerName != "aiserveweave-comfyui" || got.Instances[0].State != "running" {
		t.Errorf("instance = %+v, want container_name=aiserveweave-comfyui state=running", got.Instances[0])
	}
}

func TestStatusRendersCustomNodes(t *testing.T) {
	fs := &fakeServer{
		knownByID: map[string]bool{"node-a": true},
		statusByID: map[string][]comfyuimanagedstatus.Status{
			"node-a": {
				{
					ContainerName: "aiserveweave-comfyui",
					State:         comfyuimanagedstatus.StateRunning,
					UpdatedAt:     generatedAt,
					CustomNodes:   []comfyuimanagedstatus.CustomNodeStatus{{Name: "my-node", Version: "v1"}},
				},
			},
		},
	}
	handler := serve(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes/node-a/comfyui-managed", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got struct {
		Instances []struct {
			CustomNodes []struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"custom_nodes"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Instances) != 1 || len(got.Instances[0].CustomNodes) != 1 {
		t.Fatalf("instances = %+v, want one instance with one custom node", got.Instances)
	}
	if got.Instances[0].CustomNodes[0].Name != "my-node" || got.Instances[0].CustomNodes[0].Version != "v1" {
		t.Errorf("custom node = %+v, want name=my-node version=v1", got.Instances[0].CustomNodes[0])
	}
}

func TestCustomNodeInstallDispatchesAndAnswersAccepted(t *testing.T) {
	fs := &fakeServer{knownByID: map[string]bool{"node-a": true}}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string]string{"name": "my-node"})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed/custom-nodes", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if got := fs.customNodeInstallCalls["node-a"]; got != "my-node" {
		t.Errorf("TriggerCustomNodeInstall called with %q, want %q", got, "my-node")
	}
}

func TestCustomNodeInstallRejectsEmptyName(t *testing.T) {
	fs := &fakeServer{knownByID: map[string]bool{"node-a": true}}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string]string{"name": ""})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed/custom-nodes", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestCustomNodeInstallOnADisconnectedNodeIs404(t *testing.T) {
	fs := &fakeServer{customNodeInstallErr: errors.New("tunnelserver: node is not connected to this replica")}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string]string{"name": "my-node"})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed/custom-nodes", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestCustomNodeInstallRequiresTheToken(t *testing.T) {
	fs := &fakeServer{knownByID: map[string]bool{"node-a": true}}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string]string{"name": "my-node"})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/comfyui-managed/custom-nodes", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestStatusOnAnUnknownNodeIs404(t *testing.T) {
	fs := &fakeServer{}
	handler := serve(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes/ghost/comfyui-managed", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

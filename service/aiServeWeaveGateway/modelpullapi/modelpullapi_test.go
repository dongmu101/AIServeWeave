package modelpullapi_test

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

	"AIServeWeave/common/modelpullstatus"
	"AIServeWeave/service/aiServeWeaveGateway/modelpullapi"
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

const token = "model-pull-token-long-enough-for-a-test"

var generatedAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// fakeServer stands in for tunnelserver.Server, so this package's tests
// never need a real tunnel.
type fakeServer struct {
	triggerErr error
	triggered  map[string][]string
	statusByID map[string][]modelpullstatus.Status
	knownByID  map[string]bool
}

func (f *fakeServer) trigger(nodeID string, names []string) error {
	if f.triggerErr != nil {
		return f.triggerErr
	}
	if f.triggered == nil {
		f.triggered = map[string][]string{}
	}
	f.triggered[nodeID] = names
	return nil
}

func (f *fakeServer) status(nodeID string) ([]modelpullstatus.Status, bool) {
	if !f.knownByID[nodeID] {
		return nil, false
	}
	return f.statusByID[nodeID], true
}

func serve(t *testing.T, fs *fakeServer) http.Handler {
	t.Helper()
	handler, err := modelpullapi.New(modelpullapi.Config{
		Token:   token,
		Clock:   fixedClock{now: generatedAt},
		Trigger: fs.trigger,
		Status:  fs.status,
	})
	if err != nil {
		t.Fatalf("modelpullapi.New: %v", err)
	}
	return handler
}

func TestTheListenerRefusesToStartUnconfigured(t *testing.T) {
	fs := &fakeServer{}
	tests := []struct {
		name string
		cfg  modelpullapi.Config
	}{
		{"no token", modelpullapi.Config{Trigger: fs.trigger, Status: fs.status}},
		{"no trigger", modelpullapi.Config{Token: token, Status: fs.status}},
		{"no status", modelpullapi.Config{Token: token, Trigger: fs.trigger}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := modelpullapi.New(tt.cfg); err == nil {
				t.Fatal("New did not refuse an incomplete configuration")
			}
		})
	}
}

func TestModelPullRequiresTheToken(t *testing.T) {
	fs := &fakeServer{knownByID: map[string]bool{"node-a": true}}
	handler := serve(t, fs)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"the model pull token", "Bearer " + token, http.StatusOK},
		{"no header at all", "", http.StatusUnauthorized},
		{"the wrong token", "Bearer nope", http.StatusUnauthorized},
		{"the right token, wrong scheme", "Basic " + token, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes/node-a/model-pulls", nil)
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

	body, _ := json.Marshal(map[string][]string{"names": {"m1", "m2"}})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/model-pulls", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if got := fs.triggered["node-a"]; len(got) != 2 || got[0] != "m1" || got[1] != "m2" {
		t.Fatalf("triggered[node-a] = %v, want [m1 m2]", got)
	}
}

func TestTriggerOnADisconnectedNodeIs404(t *testing.T) {
	fs := &fakeServer{triggerErr: errors.New("tunnelserver: node is not connected to this replica")}
	handler := serve(t, fs)

	body, _ := json.Marshal(map[string][]string{"names": {"m1"}})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/ghost/model-pulls", bytes.NewReader(body))
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
		{"empty names", `{"names": []}`},
		{"not json", `not json`},
		{"wrong shape", `{"names": "m1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &fakeServer{}
			handler := serve(t, fs)
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/nodes/node-a/model-pulls", bytes.NewReader([]byte(tt.body)))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestStatusRendersKnownReasonsAndTimestamps(t *testing.T) {
	fs := &fakeServer{
		knownByID: map[string]bool{"node-a": true},
		statusByID: map[string][]modelpullstatus.Status{
			"node-a": {
				{Name: "m1", State: modelpullstatus.StateDownloading, BytesDownloaded: 10, BytesTotal: 100, UpdatedAt: generatedAt},
				{Name: "m2", State: modelpullstatus.StateFailed, Reason: modelpullstatus.ReasonChecksumMismatch, UpdatedAt: generatedAt},
			},
		},
	}
	handler := serve(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes/node-a/model-pulls", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got struct {
		GeneratedAt string `json:"generated_at"`
		Pulls       []struct {
			Name            string `json:"name"`
			State           string `json:"state"`
			BytesDownloaded int64  `json:"bytes_downloaded"`
			Reason          string `json:"reason"`
		} `json:"pulls"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.GeneratedAt == "" {
		t.Error("generated_at is empty")
	}
	if len(got.Pulls) != 2 {
		t.Fatalf("pulls = %v, want 2 entries", got.Pulls)
	}
	if got.Pulls[0].State != "downloading" || got.Pulls[0].BytesDownloaded != 10 {
		t.Errorf("m1 = %+v, want state=downloading bytes_downloaded=10", got.Pulls[0])
	}
	if got.Pulls[1].State != "failed" || got.Pulls[1].Reason != "checksum_mismatch" {
		t.Errorf("m2 = %+v, want state=failed reason=checksum_mismatch", got.Pulls[1])
	}
}

func TestStatusOnAnUnknownNodeIs404(t *testing.T) {
	fs := &fakeServer{}
	handler := serve(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/nodes/ghost/model-pulls", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

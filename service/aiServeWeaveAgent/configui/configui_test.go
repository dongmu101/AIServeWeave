package configui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestValidateLoopback(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "127.0.0.1 with port", addr: "127.0.0.1:8080", wantErr: false},
		{name: "IPv6 loopback", addr: "[::1]:8080", wantErr: false},
		{name: "localhost by name", addr: "localhost:8080", wantErr: false},
		{name: "bare port binds every interface", addr: ":8080", wantErr: true},
		{name: "a public IP is refused", addr: "0.0.0.0:8080", wantErr: true},
		{name: "an arbitrary hostname is refused", addr: "example.com:8080", wantErr: true},
		{name: "a routable IP is refused", addr: "10.0.0.5:8080", wantErr: true},
		{name: "missing port is a parse error", addr: "127.0.0.1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLoopback(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateLoopback(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

func TestServeRejectsNonLoopback(t *testing.T) {
	if err := Serve(t.Context(), discardLogger(), "0.0.0.0:0", filepath.Join(t.TempDir(), "agent.yaml")); err == nil {
		t.Fatal("Serve on a non-loopback address returned no error")
	}
}

func TestServeRejectsEmptyConfigPath(t *testing.T) {
	if err := Serve(t.Context(), discardLogger(), "127.0.0.1:0", ""); err == nil {
		t.Fatal("Serve with an empty configPath returned no error")
	}
}

func TestParseRuntimesDropsMalformedLines(t *testing.T) {
	got := parseRuntimes("ollama-local,ollama,http://127.0.0.1:11434\nmissing-fields,ollama\n\nvllm-local,vllm,http://127.0.0.1:8000")
	want := []agentconfig.RuntimeConfig{
		{ID: "ollama-local", Kind: "ollama", BaseURL: "http://127.0.0.1:11434"},
		{ID: "vllm-local", Kind: "vllm", BaseURL: "http://127.0.0.1:8000"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseRuntimes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseRuntimes()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseLabelsDropsMalformedLines(t *testing.T) {
	got := parseLabels("region=local\njustakey\ngpu=4090\n")
	if len(got) != 2 || got["region"] != "local" || got["gpu"] != "4090" {
		t.Errorf("parseLabels() = %v, want region=local,gpu=4090", got)
	}
}

// TestIndexHandlerSaveAndReload exercises the whole HTTP round trip a
// browser would drive: GET the empty form, POST a filled-in form, then GET
// again and check the saved values come back pre-filled — the same
// round-trip TestSaveLoadRoundTrip pins at the agentconfig layer, but here
// through the HTTP handler an operator actually uses.
//
// TestIndexHandlerSaveAndReload 走一遍浏览器会走的完整 HTTP 往返：GET 空表
// 单、POST 一份填好的表单、再 GET 一次检查保存的值被正确回显——与
// agentconfig 层的 TestSaveLoadRoundTrip 是同一种往返，只是这里走的是操作
// 者实际使用的 HTTP handler。
func TestIndexHandlerSaveAndReload(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	handler := newIndexHandler(discardLogger(), configPath)

	getRec := httptest.NewRecorder()
	handler(getRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("initial GET status = %d, want 200", getRec.Code)
	}

	form := url.Values{
		"endpoints":     {"gw-1.example.com:8443, gw-2.example.com:8443"},
		"registry":      {"registry.example.com:9443"},
		"node_id":       {"node-a"},
		"labels":        {"region=local\ngpu=4090"},
		"runtimes":      {"ollama-local,ollama,http://127.0.0.1:11434"},
		"auto_discover": {"1"},
		"metrics_addr":  {"127.0.0.1:9091"},
		"log_level":     {"debug"},
	}
	postReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRec := httptest.NewRecorder()
	handler(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200, body: %s", postRec.Code, postRec.Body.String())
	}
	if !strings.Contains(postRec.Body.String(), "已保存") {
		t.Errorf("POST response does not confirm the save: %s", postRec.Body.String())
	}

	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	saved, err := agentconfig.Load(configPath)
	if err != nil {
		t.Fatalf("Load after save: %v", err)
	}
	if saved.Gateway.NodeID != "node-a" {
		t.Errorf("saved NodeID = %q, want %q", saved.Gateway.NodeID, "node-a")
	}
	if len(saved.Runtimes) != 1 || saved.Runtimes[0].ID != "ollama-local" {
		t.Errorf("saved Runtimes = %v, want one ollama-local entry", saved.Runtimes)
	}

	reGetRec := httptest.NewRecorder()
	handler(reGetRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(reGetRec.Body.String(), "node-a") {
		t.Errorf("reloaded form does not show the saved node_id: %s", reGetRec.Body.String())
	}
}

func TestIndexHandlerRejectsOtherMethods(t *testing.T) {
	handler := newIndexHandler(discardLogger(), filepath.Join(t.TempDir(), "agent.yaml"))
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodDelete, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

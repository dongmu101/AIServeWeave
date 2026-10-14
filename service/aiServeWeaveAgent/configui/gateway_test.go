package configui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentGateway(t *testing.T) {
	for _, tc := range []struct {
		name      string
		source    func() GatewaySnapshot
		want      string
		connected int
	}{
		{name: "setup only", want: "仅配置模式"},
		{name: "disabled", source: func() GatewaySnapshot { return GatewaySnapshot{} }, want: "隧道未启用"},
		{name: "starting", source: func() GatewaySnapshot { return GatewaySnapshot{Enabled: true} }, want: "正在初始化"},
		{name: "no replicas", source: func() GatewaySnapshot { return GatewaySnapshot{Enabled: true, Started: true} }, want: "未连接"},
		{name: "partial connectivity", source: func() GatewaySnapshot {
			return GatewaySnapshot{Enabled: true, Started: true, States: map[string]string{"b:8443": "reconnecting", "a:8443": "serving"}}
		}, want: "部分连接", connected: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := currentGateway(tc.source)
			if got.Summary != tc.want || got.Connected != tc.connected {
				t.Fatalf("summary/count = %q/%d, want %q/%d", got.Summary, got.Connected, tc.want, tc.connected)
			}
			if len(got.Connections) == 2 && (got.Connections[0].Endpoint != "a:8443" || got.Connections[1].Endpoint != "b:8443") {
				t.Errorf("connections = %+v, want endpoint order a:8443, b:8443", got.Connections)
			}
		})
	}
	for _, tc := range []struct {
		name      string
		label     string
		connected int
	}{
		{name: "connected", label: "已连接", connected: 1},
		{name: "serving", label: "已连接", connected: 1},
		{name: "connecting", label: "连接中"},
		{name: "reconnecting", label: "重连中"},
		{name: "draining", label: "正在下线"},
		{name: "failed", label: "连接失败"},
		{name: "retired", label: "已移除"},
		{name: "disconnected", label: "未连接"},
		{name: "unrecognized", label: "未知状态"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := currentGateway(func() GatewaySnapshot {
				return GatewaySnapshot{Enabled: true, Started: true, States: map[string]string{"gateway:8443": tc.name}}
			})
			if got.Total != 1 || got.Connected != tc.connected || got.Connections[0].Label != tc.label {
				t.Errorf("view = %+v, want total 1, connected %d, label %q", got, tc.connected, tc.label)
			}
		})
	}
}

func TestGatewayRefreshUsesLiveSnapshotWithoutSavingConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	before := []byte("gateway:\n  endpoints: [saved-gateway:8443]\n")
	if err := os.WriteFile(configPath, before, 0o600); err != nil {
		t.Fatalf("write configuration error = %v, want nil", err)
	}
	snapshot := GatewaySnapshot{Enabled: true, Started: true, States: map[string]string{"live-gateway:8443": "connected"}}
	sources := Sources{Gateway: func() GatewaySnapshot { return snapshot }}
	page := newIndexHandler(discardLogger(), configPath, sources)
	refresh := newGatewayHandler(sources.Gateway)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		state   string
		want    string
	}{
		{name: "page shows actual connection instead of saved address", handler: page, state: "connected", want: "已连接 1 / 1 个 Gateway"},
		{name: "refresh reflects disconnect", handler: refresh, state: "reconnecting", want: "重连中"},
		{name: "refresh reflects reconnection", handler: refresh, state: "serving", want: "已连接 1 / 1 个 Gateway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot.States["live-gateway:8443"] = tc.state
			rec := httptest.NewRecorder()
			tc.handler(rec, httptest.NewRequest(http.MethodGet, "/status/gateway", nil))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) || !strings.Contains(rec.Body.String(), "live-gateway:8443") {
				t.Errorf("response = %d/%s, want 200 with %q and live-gateway:8443", rec.Code, rec.Body.String(), tc.want)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			after, err := os.ReadFile(configPath)
			if err != nil || string(after) != string(before) {
				t.Errorf("configuration/error = %q/%v, want unchanged %q and nil", after, err, before)
			}
		})
	}
}

func TestGatewayFragmentEscapesEndpointsAndOmitsSettings(t *testing.T) {
	handler := newGatewayHandler(func() GatewaySnapshot {
		return GatewaySnapshot{Enabled: true, Started: true, States: map[string]string{"<script>endpoint</script>": "failed"}}
	})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/status/gateway", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "&lt;script&gt;endpoint&lt;/script&gt;") || !strings.Contains(body, "连接失败") {
		t.Errorf("fragment = %q, want escaped endpoint and failure state", body)
	}
	for _, forbidden := range []string{"<script>", "<form", "bootstrap_token_file", "key_file"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("fragment contains %q, want absent", forbidden)
		}
	}
}

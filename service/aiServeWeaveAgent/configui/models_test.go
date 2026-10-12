package configui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestAvailableModels(t *testing.T) {
	tests := []struct {
		name      string
		snapshots func() []runtime.Snapshot
		want      modelCatalogue
	}{
		{name: "setup-only mode", want: modelCatalogue{}},
		{name: "discovery pending", snapshots: func() []runtime.Snapshot { return nil }, want: modelCatalogue{Live: true}},
		{
			name: "multiple backends deduplicate IDs and omit unhealthy cached models",
			snapshots: func() []runtime.Snapshot {
				return []runtime.Snapshot{
					{Descriptor: runtime.Descriptor{ID: "claude-local", Kind: runtime.KindClaude}, State: runtime.StateHealthy, Discovery: runtime.Discovery{Models: []runtime.Model{{ID: "claude-sonnet-5-5", DisplayName: "Sonnet"}, {ID: "claude-sonnet-5-5"}, {ID: ""}}}},
					{Descriptor: runtime.Descriptor{ID: "codex-local", Kind: runtime.KindCodex}, State: runtime.StateHealthy, Discovery: runtime.Discovery{Models: []runtime.Model{{ID: "gpt-6.1-sol"}}}},
					{Descriptor: runtime.Descriptor{ID: "ollama-local", Kind: runtime.KindOllama}, State: runtime.StateHealthy, Discovery: runtime.Discovery{Models: []runtime.Model{{ID: "gpt-6.1-sol"}}}},
					{Descriptor: runtime.Descriptor{ID: "unhealthy", Kind: runtime.KindVLLM}, State: runtime.StateUnhealthy, Discovery: runtime.Discovery{Models: []runtime.Model{{ID: "stale-model"}}}},
				}
			},
			want: modelCatalogue{Live: true, Count: 2, Runtimes: []runtimeModels{
				{ID: "claude-local", Kind: runtime.KindClaude, State: runtime.StateHealthy, IDs: []string{"claude-sonnet-5-5"}},
				{ID: "codex-local", Kind: runtime.KindCodex, State: runtime.StateHealthy, IDs: []string{"gpt-6.1-sol"}},
				{ID: "ollama-local", Kind: runtime.KindOllama, State: runtime.StateHealthy, IDs: []string{"gpt-6.1-sol"}},
				{ID: "unhealthy", Kind: runtime.KindVLLM, State: runtime.StateUnhealthy},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := availableModels(tt.snapshots); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("availableModels() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestModelCatalogueUsesCurrentSnapshotAndEscapesIDs(t *testing.T) {
	snapshot := runtime.Snapshot{
		Descriptor: runtime.Descriptor{ID: "claude-local", Kind: runtime.KindClaude, BaseURL: "http://secret-backend.invalid"},
		State:      runtime.StateHealthy,
		Discovery:  runtime.Discovery{Models: []runtime.Model{{ID: "<script>model</script>", DisplayName: "secret-display-name"}}},
	}
	handler := newIndexHandler(discardLogger(), filepath.Join(t.TempDir(), "agent.yaml"), func() []runtime.Snapshot {
		return []runtime.Snapshot{snapshot}
	})
	for _, tt := range []struct {
		name  string
		state runtime.State
		want  string
	}{
		{name: "healthy catalogue", state: runtime.StateHealthy, want: "&lt;script&gt;model&lt;/script&gt;"},
		{name: "refresh removes unavailable model", state: runtime.StateUnhealthy, want: "共 0 个可用模型"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot.State = tt.state
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("GET status/body = %d/%s, want 200 containing %q", rec.Code, rec.Body.String(), tt.want)
			}
			for _, secret := range []string{"secret-backend.invalid", "secret-display-name", "<script>model</script>"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Errorf("GET body contains %q, want omitted or escaped", secret)
				}
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

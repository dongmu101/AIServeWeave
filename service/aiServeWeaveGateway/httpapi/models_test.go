package httpapi_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// TestModelsFromMultipleLocalBackends checks the client catalogue for one Agent
// with HTTP, Claude and Codex runtimes, including independent readiness filtering.
//
// TestModelsFromMultipleLocalBackends 验证同一 Agent 的 HTTP 与 CLI 多后端模型目录，
// 包括 Claude/Codex 同时存在以及彼此独立的未就绪过滤。
func TestModelsFromMultipleLocalBackends(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		codexState, claudeState runtime.State
		want                    []string
	}{
		{name: "all ready", codexState: runtime.StateHealthy, claudeState: runtime.StateHealthy, want: []string{"claude-sonnet-4-6", "codex-model-a", "codex-model-b", "ollama-model"}},
		{name: "Codex not ready", codexState: runtime.StateUnhealthy, claudeState: runtime.StateHealthy, want: []string{"claude-sonnet-4-6", "ollama-model"}},
		{name: "Claude not ready", codexState: runtime.StateHealthy, claudeState: runtime.StateUnhealthy, want: []string{"codex-model-a", "codex-model-b", "ollama-model"}},
		{name: "both CLIs not ready", codexState: runtime.StateUnhealthy, claudeState: runtime.StateUnhealthy, want: []string{"ollama-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, harness := newServer(t, httpapi.Config{})
			ollama := chatCapableSnapshot("ollama-local", "ollama-model")
			cli := chatCapableSnapshot("codex-local", "codex-model-a")
			cli.Descriptor.Kind, cli.Descriptor.BaseURL, cli.State = runtime.KindCodex, "", tc.codexState
			cli.Discovery.Models = append(cli.Discovery.Models,
				runtime.Model{ID: "codex-model-b", Capabilities: cli.Discovery.Models[0].Capabilities},
				runtime.Model{ID: "ollama-model", Capabilities: cli.Discovery.Models[0].Capabilities})
			claude := runtime.Snapshot{
				Descriptor: runtime.Descriptor{ID: "claude-local", Kind: runtime.KindClaude}, State: tc.claudeState,
				Discovery: runtime.Discovery{
					Capabilities: runtime.CapabilitySet{runtime.CapabilityMessages: {Level: runtime.SupportSupported}},
					Models:       []runtime.Model{{ID: "claude-sonnet-4-6", DisplayName: "claude-sonnet-4-6", DisplayOrder: 1}},
				},
			}
			control := harness.Connect("multi-backend-node", "ollama-local", "codex-local", "claude-local")
			control.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
				Full: true, ReportedAt: timestamppb.New(harness.Clock.Now()),
				Snapshots: tunnelwire.SnapshotsToProto([]runtime.Snapshot{ollama, cli, claude}),
			}}})
			gatewaytest.WaitFor(t, "all local backends to be reported", func() bool {
				node, _ := harness.Srv.Node("multi-backend-node")
				return len(node.Runtimes) == 3
			})
			response, err := server.Client().Get(server.URL + "/v1/models")
			if err != nil {
				t.Fatalf("GET models error = %v, want nil", err)
			}
			defer response.Body.Close()
			var body struct {
				Data []struct{ ID string } `json:"data"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode models error = %v, want nil", err)
			}
			var ids []string
			for _, model := range body.Data {
				ids = append(ids, model.ID)
			}
			if response.StatusCode != http.StatusOK || !slices.Equal(ids, tc.want) {
				t.Fatalf("models status = %d, IDs = %v; want status %d, IDs %v", response.StatusCode, ids, http.StatusOK, tc.want)
			}
		})
	}
}

// TestModelsDisplayMetadata preserves CLI labels and order alongside ordinary models.
// TestModelsDisplayMetadata 验证 CLI 名称和顺序，并兼容普通模型及重复 ID。
func TestModelsDisplayMetadata(t *testing.T) {
	for _, tc := range []struct {
		name               string
		state              runtime.State
		wantIDs, wantNames []string
	}{
		{"ready", runtime.StateHealthy, []string{"sonnet", "opus", "ollama-model"}, []string{"Sonnet 4.6", "Opus fixture", "ollama-model"}},
		{"not ready", runtime.StateUnhealthy, []string{"ollama-model", "sonnet"}, []string{"ollama-model", "sonnet"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, harness := newServer(t, httpapi.Config{})
			ordinary := chatCapableSnapshot("ollama-local", "ollama-model")
			ordinary.Discovery.Models = append(ordinary.Discovery.Models, runtime.Model{ID: "sonnet", Capabilities: ordinary.Discovery.Models[0].Capabilities})
			cli := chatCapableSnapshot("claude-local", "unused")
			cli.Descriptor.Kind, cli.State = runtime.KindClaude, tc.state
			cli.Discovery.Capabilities = runtime.CapabilitySet{runtime.CapabilityMessages: {Level: runtime.SupportSupported}}
			cli.Discovery.Models = []runtime.Model{
				{ID: "sonnet", DisplayName: "Sonnet 4.6", DisplayOrder: 1},
				{ID: "opus", DisplayName: "Opus fixture", DisplayOrder: 2},
			}
			control := harness.Connect("display-node", "ollama-local", "claude-local")
			control.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
				Full: true, Snapshots: tunnelwire.SnapshotsToProto([]runtime.Snapshot{ordinary, cli}),
			}}})
			gatewaytest.WaitFor(t, "model metadata status", func() bool {
				node, _ := harness.Srv.Node("display-node")
				return len(node.Runtimes) == 2
			})
			response, err := server.Client().Get(server.URL + "/v1/models")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var body struct {
				Data []struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					DisplayName string `json:"display_name"`
				}
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var ids, names []string
			for _, model := range body.Data {
				ids, names = append(ids, model.ID), append(names, model.Name)
				if model.Name != model.DisplayName {
					t.Fatalf("display_name=%q, want name=%q", model.DisplayName, model.Name)
				}
			}
			if response.StatusCode != http.StatusOK || !reflect.DeepEqual(ids, tc.wantIDs) || !reflect.DeepEqual(names, tc.wantNames) {
				t.Fatalf("status=%d IDs=%v names=%v, want 200 IDs=%v names=%v", response.StatusCode, ids, names, tc.wantIDs, tc.wantNames)
			}
		})
	}
}

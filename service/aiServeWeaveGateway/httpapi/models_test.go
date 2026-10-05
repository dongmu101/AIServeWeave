package httpapi_test

import (
	"encoding/json"
	"net/http"
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
// with HTTP and CLI runtimes, including multiple CLI models and readiness filtering.
//
// TestModelsFromMultipleLocalBackends 验证同一 Agent 的 HTTP 与 CLI 多后端模型目录，
// 包括 CLI 的多个模型和未就绪运行时过滤。
func TestModelsFromMultipleLocalBackends(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state runtime.State
		want  []string
	}{
		{name: "all ready", state: runtime.StateHealthy, want: []string{"codex-model-a", "codex-model-b", "ollama-model"}},
		{name: "CLI not ready", state: runtime.StateUnhealthy, want: []string{"ollama-model"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, harness := newServer(t, httpapi.Config{})
			ollama := chatCapableSnapshot("ollama-local", "ollama-model")
			cli := chatCapableSnapshot("codex-local", "codex-model-a")
			cli.Descriptor.Kind, cli.Descriptor.BaseURL, cli.State = runtime.KindCodex, "", tc.state
			cli.Discovery.Models = append(cli.Discovery.Models,
				runtime.Model{ID: "codex-model-b", Capabilities: cli.Discovery.Models[0].Capabilities},
				runtime.Model{ID: "ollama-model", Capabilities: cli.Discovery.Models[0].Capabilities})
			control := harness.Connect("multi-backend-node", "ollama-local", "codex-local")
			control.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
				Full: true, ReportedAt: timestamppb.New(harness.Clock.Now()),
				Snapshots: tunnelwire.SnapshotsToProto([]runtime.Snapshot{ollama, cli}),
			}}})
			gatewaytest.WaitFor(t, "both local backends to be reported", func() bool {
				node, _ := harness.Srv.Node("multi-backend-node")
				return len(node.Runtimes) == 2
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

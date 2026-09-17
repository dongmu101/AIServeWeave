package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// rerankHandler answers OPERATION_RERANK by echoing the request's document
// count back as one result per document, tagged with source so a test can
// tell which node served the request.
func rerankHandler(source string) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if req.GetOperation() != tunnelv1.Operation_OPERATION_RERANK {
			return errors.New("unsupported operation")
		}
		in, err := tunnelwire.UnmarshalRerankRequest(req.GetPayload())
		if err != nil {
			return err
		}
		results := make([]runtime.RerankResult, len(in.Documents))
		for i := range in.Documents {
			results[i] = runtime.RerankResult{Index: i, Score: 1.0 / float64(i+1)}
		}
		payload, err := tunnelwire.MarshalRerankResponse(runtime.RerankResponse{Model: source, Results: results})
		if err != nil {
			return err
		}
		return reply(gatewaytest.DataFrame(payload))
	}
}

// connectRerankNode wires up nodeID with an inference slot advertising
// CapabilityRerank for model.
func connectRerankNode(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID, model string) {
	t.Helper()
	snap := chatCapableSnapshot(runtimeID, model)
	snap.Discovery.Models[0].Capabilities[runtime.CapabilityRerank] = runtime.CapabilityEvidence{Level: runtime.SupportSupported}

	c := h.Connect(nodeID, runtimeID)
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{snap}),
	}}})
	h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, nodeID+"-slot-1", rerankHandler(nodeID))
	gatewaytest.WaitFor(t, "the slot to park on "+nodeID, func() bool { return gatewaytest.IdleCount(h, nodeID) == 1 })
	gatewaytest.WaitFor(t, "the inventory to arrive on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return len(info.Runtimes) == 1
	})
}

func TestRerankDispatchesAndReturnsResults(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectRerankNode(t, h, "node-a", "backend-1", "rerank-1")

	body, _ := json.Marshal(map[string]any{
		"model":     "rerank-1",
		"query":     "what is a cat",
		"documents": []string{"a dog is a mammal", "a cat is a mammal"},
	})
	resp, err := http.Post(srv.URL+"/v1/rerank", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got struct {
		Model   string `json:"model"`
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "node-a" {
		t.Errorf("Model = %q, want node-a", got.Model)
	}
	if len(got.Results) != 2 || got.Results[0].Index != 0 || got.Results[0].RelevanceScore != 1 {
		t.Errorf("Results = %+v, want two results starting with index 0 score 1", got.Results)
	}
}

func TestRerankRequiresModelQueryAndDocuments(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectRerankNode(t, h, "node-a", "backend-1", "rerank-1")

	tests := []map[string]any{
		{"query": "q", "documents": []string{"d"}},
		{"model": "rerank-1", "documents": []string{"d"}},
		{"model": "rerank-1", "query": "q", "documents": []string{}},
	}
	for _, body := range tests {
		b, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+"/v1/rerank", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %v: status = %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestRerankRejectsInvalidJSON(t *testing.T) {
	srv, _ := newServer(t, httpapi.Config{})

	resp, err := http.Post(srv.URL+"/v1/rerank", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestRerankNoCapableNode(t *testing.T) {
	srv, _ := newServer(t, httpapi.Config{})

	body, _ := json.Marshal(map[string]any{"model": "rerank-1", "query": "q", "documents": []string{"d"}})
	resp, err := http.Post(srv.URL+"/v1/rerank", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

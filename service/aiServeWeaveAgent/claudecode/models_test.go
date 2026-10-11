package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

func modelProcessFixture() {
	if os.Args[1] == "auth" {
		fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai"}`)
		return
	}
	for _, flag := range []string{"--restricted", "--strict-mcp-config", "--disable-slash-commands", "--no-chrome", "--no-session-persistence"} {
		if !slices.Contains(os.Args, flag) {
			os.Exit(3)
		}
	}
	var request struct {
		Type      string                   `json:"type"`
		RequestID string                   `json:"request_id"`
		Request   struct{ Subtype string } `json:"request"`
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil || request.Type != "control_request" || request.Request.Subtype != "initialize" {
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": request.RequestID, "response": map[string]any{"models": []any{
			map[string]string{"value": "sonnet", "resolvedModel": "claude-sonnet-4-6", "displayName": "Sonnet 4.6"},
			map[string]string{"value": "opus", "resolvedModel": "fixture-opus", "displayName": "Opus fixture"},
			map[string]string{"value": "default", "resolvedModel": "fixture-opus", "displayName": "Default"},
		}},
	}})
}

// TestParseModels rejects malformed catalogues and correlates initialize responses.
// TestParseModels 拒绝畸形目录，并匹配 initialize 响应的请求 ID。
func TestParseModels(t *testing.T) {
	wrap := func(models string) string {
		return `{"type":"control_response","response":{"subtype":"success","request_id":"aisw_model_discovery","response":{"models":` + models + `}}}`
	}
	for _, tc := range []struct {
		name, data string
		want       []string
	}{
		{"resolved IDs and duplicate aliases", wrap(`[{"value":"default","resolvedModel":" claude-opus-5-5 ","displayName":"Default"},{"value":"opus","resolvedModel":"claude-opus-5-5"},{"value":"sonnet","resolvedModel":"claude-sonnet-5-5"},{"value":"claude-opus-5-5","resolvedModel":"claude-opus-5-5"},{"id":"unresolved"}]`), []string{"claude-opus-5-5", "claude-sonnet-5-5"}},
		{"unrelated event", "{bad-json}\n{\"type\":\"system\"}\n" + wrap(`[{"value":"opus","resolvedModel":"claude-opus-5-5"}]`), []string{"claude-opus-5-5"}},
		{"wrong request", strings.ReplaceAll(wrap(`[{"resolvedModel":"claude-sonnet-4-6"}]`), modelDiscoveryID, "other"), nil},
		{"failed response", strings.ReplaceAll(wrap(`[{"resolvedModel":"claude-sonnet-4-6"}]`), "success", "error"), nil},
		{"empty", wrap(`[]`), nil},
		{"unresolved aliases", wrap(`[{"value":"sonnet"},{"id":"haiku"},{"resolvedModel":" "}]`), nil},
		{"oversized ID", wrap(`[{"resolvedModel":"` + strings.Repeat("m", 257) + `"}]`), nil},
		{"control character", wrap(`[{"value":"sonnet","resolvedModel":"bad\nmodel"}]`), nil},
		{"too many models", wrap(`[` + strings.Repeat(`{"value":"sonnet"},`, 256) + `{"value":"sonnet"}]`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseModels([]byte(tc.data))
			if (err != nil) != (tc.want == nil) || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("models=%v, error=%v; want models=%v, error present=%t", got, err, tc.want, tc.want == nil)
			}
		})
	}
}

// TestModelDiscovery publishes CLI models and restricts requests to that catalogue.
// TestModelDiscovery 发布 CLI 模型，并将请求限制在该目录内。
func TestModelDiscovery(t *testing.T) {
	rt, err := NewFactory(os.Args[0])(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	t.Cleanup(func() { _ = r.Close() })
	discovery, err := r.Discover(t.Context())
	if err != nil {
		t.Fatalf("Discover error=%v, want nil", err)
	}
	var ids []string
	for _, model := range discovery.Models {
		ids = append(ids, model.ID)
	}
	if !slices.Equal(ids, []string{"claude-sonnet-4-6", "fixture-opus"}) {
		t.Fatalf("models=%v, want [claude-sonnet-4-6 fixture-opus]", ids)
	}
	for i, want := range ids {
		if got := discovery.Models[i]; got.DisplayName != want || got.DisplayOrder != uint32(i+1) {
			t.Fatalf("model=%+v, want name=%q, order=%d", got, want, i+1)
		}
	}
	r.bridge.start = fixtureStart
	for _, tc := range []struct{ name, model, upstream string }{
		{"discovered real ID", "fixture-opus", "fixture-opus"},
		{"CLI alias rejected", "opus", ""},
		{"default alias rejected", "default", ""},
		{"unknown model", "unlisted", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testMessagesRequest()
			req.Model = tc.model
			data, _ := json.Marshal(req)
			stream, err := r.Messages(t.Context(), runtime.MessagesRequest{Model: tc.model, TenantID: "tenant", KeyID: "key", JSON: data})
			if tc.upstream == "" {
				if err == nil {
					_ = stream.Close()
					t.Fatal("error=nil, want unsupported model")
				}
				return
			}
			if err != nil {
				t.Fatalf("Messages error=%v, want nil", err)
			}
			defer stream.Close()
			if got := stream.(*nativeStream).s.request.cliModel; got != tc.upstream {
				t.Fatalf("upstream=%q, want %q", got, tc.upstream)
			}
		})
	}
}

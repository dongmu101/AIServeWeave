package comfyui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeTopLevelKeys(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		limit         int
		want          []string
		wantTruncated bool
		wantErr       bool
	}{
		{
			name: "flat object",
			body: `{"KSampler":1,"CLIPTextEncode":2}`,
			want: []string{"KSampler", "CLIPTextEncode"},
		},
		{
			// The real /object_info nests each node's full input schema;
			// the decoder must walk past it without materializing it.
			name: "nested values are skipped",
			body: `{"KSampler":{"input":{"required":{"seed":["INT",{"default":0}]}},"output":["LATENT"]},"SaveImage":{"input":{}}}`,
			want: []string{"KSampler", "SaveImage"},
		},
		{
			name: "arrays of objects are skipped",
			body: `{"A":[{"x":[1,2,{"y":3}]}],"B":null}`,
			want: []string{"A", "B"},
		},
		{
			name:          "limit truncates",
			body:          `{"A":1,"B":2,"C":3}`,
			limit:         2,
			want:          []string{"A", "B"},
			wantTruncated: true,
		},
		{
			name: "empty object",
			body: `{}`,
			want: nil,
		},
		{
			name:    "not an object",
			body:    `["A","B"]`,
			wantErr: true,
		},
		{
			name:    "truncated json",
			body:    `{"A":{"b":`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, truncated, err := decodeTopLevelKeys(strings.NewReader(tt.body), tt.limit)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("decodeTopLevelKeys(%q) = %v, want an error", tt.body, keys)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeTopLevelKeys(%q): %v", tt.body, err)
			}
			if truncated != tt.wantTruncated {
				t.Errorf("truncated = %v, want %v", truncated, tt.wantTruncated)
			}
			if strings.Join(keys, ",") != strings.Join(tt.want, ",") {
				t.Errorf("keys = %v, want %v", keys, tt.want)
			}
		})
	}
}

func TestParseErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "comfy validation envelope",
			body: `{"error":{"type":"prompt_outputs_failed_validation","message":"Prompt outputs failed validation","details":"CheckpointLoaderSimple: value not in list"},"node_errors":{"4":{}}}`,
			want: "Prompt outputs failed validation: CheckpointLoaderSimple: value not in list",
		},
		{
			name: "message without details",
			body: `{"error":{"message":"invalid prompt"}}`,
			want: "invalid prompt",
		},
		{
			name: "error as a bare string",
			body: `{"error":"no such file"}`,
			want: "no such file",
		},
		{
			name: "unrecognized shape falls back to the body",
			body: `{"detail":"Not Found"}`,
			want: `{"detail":"Not Found"}`,
		},
		{
			name: "empty body",
			body: "",
			want: "empty error response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseErrorMessage([]byte(tt.body)); got != tt.want {
				t.Errorf("parseErrorMessage = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPromptIDsFromQueue(t *testing.T) {
	entries := [][]json.RawMessage{
		{json.RawMessage(`1`), json.RawMessage(`"first"`), json.RawMessage(`{}`)},
		{json.RawMessage(`2`)},
		{json.RawMessage(`3`), json.RawMessage(`{"not":"a string"}`)},
		{json.RawMessage(`4`), json.RawMessage(`"second"`)},
	}

	// One malformed entry must not hide the rest of the queue: the adapter
	// decides whether a run is pending or running from this list.
	got := promptIDsFromQueue(entries)
	if strings.Join(got, ",") != "first,second" {
		t.Errorf("promptIDsFromQueue = %v, want [first second]", got)
	}
}

func TestWebSocketURLPreservesSchemeAndPrefix(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "http becomes ws", baseURL: "http://127.0.0.1:8188", want: "ws://127.0.0.1:8188/ws?clientId=abc"},
		{name: "https becomes wss", baseURL: "https://gpu.internal:8188", want: "wss://gpu.internal:8188/ws?clientId=abc"},
		{name: "path prefix is preserved", baseURL: "http://gateway/comfy", want: "ws://gateway/comfy/ws?clientId=abc"},
		{name: "trailing slash is not doubled", baseURL: "http://gateway/comfy/", want: "ws://gateway/comfy/ws?clientId=abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(ClientConfig{BaseURL: tt.baseURL})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if got := client.WebSocketURL("abc"); got != tt.want {
				t.Errorf("WebSocketURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewClientRejectsUnusableBaseURLs(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "empty", baseURL: ""},
		{name: "wrong scheme", baseURL: "ftp://host"},
		{name: "userinfo", baseURL: "http://user:pass@host"},
		{name: "query string", baseURL: "http://host?a=b"},
		{name: "fragment", baseURL: "http://host#frag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient(ClientConfig{BaseURL: tt.baseURL}); err == nil {
				t.Errorf("NewClient(%q) = nil error, want a rejection", tt.baseURL)
			}
		})
	}
}

func TestNewClientRejectsHopByHopHeaders(t *testing.T) {
	_, err := NewClient(ClientConfig{
		BaseURL: "http://127.0.0.1:8188",
		Headers: map[string]string{"connection": "keep-alive"},
	})
	if err == nil {
		t.Fatal("NewClient accepted a connection-scoped header override")
	}
}

func TestNormalizeEvent(t *testing.T) {
	tests := []struct {
		name     string
		event    string
		node     string
		wantType string
		wantNode string
	}{
		{name: "status", event: "status", wantType: "queue_changed"},
		{name: "execution_start", event: "execution_start", wantType: "started"},
		{name: "execution_cached", event: "execution_cached", wantType: "cached"},
		{name: "executing a node", event: "executing", node: `"7"`, wantType: "node_started", wantNode: "7"},
		{name: "executing null means finished", event: "executing", node: `null`, wantType: "completed"},
		{name: "numeric node id", event: "executing", node: `7`, wantType: "node_started", wantNode: "7"},
		{name: "progress", event: "progress", node: `"3"`, wantType: "progress", wantNode: "3"},
		{name: "executed", event: "executed", node: `"9"`, wantType: "node_output", wantNode: "9"},
		{name: "execution_success", event: "execution_success", wantType: "succeeded"},
		{name: "execution_error", event: "execution_error", wantType: "failed"},
		{name: "execution_interrupted", event: "execution_interrupted", wantType: "cancelled"},
		{name: "anything else", event: "b_preview_meta", wantType: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := wsFrameData{}
			if tt.node != "" {
				data.Node = json.RawMessage(tt.node)
			}
			gotType, gotNode := normalizeEvent(tt.event, data)
			if string(gotType) != tt.wantType {
				t.Errorf("type = %q, want %q", gotType, tt.wantType)
			}
			if gotNode != tt.wantNode {
				t.Errorf("node = %q, want %q", gotNode, tt.wantNode)
			}
		})
	}
}

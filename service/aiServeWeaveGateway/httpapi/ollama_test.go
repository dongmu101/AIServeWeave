package httpapi_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func postOllama(t *testing.T, url, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func TestOllamaChatNonStreaming(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postOllama(t, srv.URL, "/api/chat",
		`{"model":"qwen3:8b","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Model   string `json:"model"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		Done            bool   `json:"done"`
		DoneReason      string `json:"done_reason"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Message.Role != "assistant" || body.Message.Content != "answer to: hello" {
		t.Errorf("message = %+v, want role=assistant content=%q", body.Message, "answer to: hello")
	}
	if !body.Done || body.DoneReason != "stop" {
		t.Errorf("done/done_reason = %v/%q, want true/stop", body.Done, body.DoneReason)
	}
	if body.PromptEvalCount != 3 || body.EvalCount != 5 {
		t.Errorf("prompt_eval_count/eval_count = %d/%d, want 3/5", body.PromptEvalCount, body.EvalCount)
	}
}

// TestOllamaChatStreamsByDefault covers Ollama's own default: a "stream"
// field that is entirely absent means streaming, the opposite of the
// OpenAI front door's default.
func TestOllamaChatStreamsByDefault(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postOllama(t, srv.URL, "/api/chat",
		`{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}

	var content strings.Builder
	sawDone := false
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var chunk struct {
			Message struct{ Content string } `json:"message"`
			Done    bool                     `json:"done"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("decoding ndjson line %q: %v", line, err)
		}
		content.WriteString(chunk.Message.Content)
		if chunk.Done {
			sawDone = true
		}
	}
	if !sawDone {
		t.Error("stream ended without a done:true line")
	}
	if content.String() != "Hello" {
		t.Errorf("streamed content = %q, want %q", content.String(), "Hello")
	}
}

func TestOllamaChatRejectsUnsupportedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "tools is present",
			body: `{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}],
				"tools":[{"type":"function","function":{"name":"get_weather"}}]}`,
		},
		{
			name: "format is present",
			body: `{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}],"format":"json"}`,
		},
		{
			name: "a message has images",
			body: `{"model":"qwen3:8b","messages":[{"role":"user","content":"hi","images":["base64..."]}]}`,
		},
		{
			name: "empty messages",
			body: `{"model":"qwen3:8b","messages":[]}`,
		},
	}
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := postOllama(t, srv.URL, "/api/chat", tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decoding error body: %v", err)
			}
			if body.Error == "" {
				t.Error("error is empty, want a description of the rejected input")
			}
		})
	}
}

func TestOllamaChatUnknownModelReturns404(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postOllama(t, srv.URL, "/api/chat",
		`{"model":"does-not-exist","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestOllamaGenerateNonStreaming(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postOllama(t, srv.URL, "/api/generate",
		`{"model":"qwen3:8b","stream":false,"prompt":"hello"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Response   string `json:"response"`
		Done       bool   `json:"done"`
		DoneReason string `json:"done_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Response != "answer to: hello" {
		t.Errorf("response = %q, want %q", body.Response, "answer to: hello")
	}
	if !body.Done || body.DoneReason != "stop" {
		t.Errorf("done/done_reason = %v/%q, want true/stop", body.Done, body.DoneReason)
	}
}

func TestOllamaGenerateRejectsUnsupportedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "format is present", body: `{"model":"qwen3:8b","prompt":"hi","format":"json"}`},
		{name: "images is present", body: `{"model":"qwen3:8b","prompt":"hi","images":["base64..."]}`},
		{name: "context is present", body: `{"model":"qwen3:8b","prompt":"hi","context":[1,2,3]}`},
		{name: "raw is true", body: `{"model":"qwen3:8b","prompt":"hi","raw":true}`},
		{name: "template is present", body: `{"model":"qwen3:8b","prompt":"hi","template":"{{ .Prompt }}"}`},
		{name: "suffix is present", body: `{"model":"qwen3:8b","prompt":"hi","suffix":"tail"}`},
		{name: "missing prompt", body: `{"model":"qwen3:8b"}`},
	}
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := postOllama(t, srv.URL, "/api/generate", tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decoding error body: %v", err)
			}
			if body.Error == "" {
				t.Error("error is empty, want a description of the rejected input")
			}
		})
	}
}

func TestOllamaEmbeddings(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postOllama(t, srv.URL, "/api/embeddings", `{"model":"qwen3:8b","prompt":"hello"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	want := []float32{0.5, -0.25}
	if len(body.Embedding) != len(want) || body.Embedding[0] != want[0] || body.Embedding[1] != want[1] {
		t.Errorf("embedding = %v, want %v", body.Embedding, want)
	}
}

func TestOllamaEmbeddingsMissingFields(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp := postOllama(t, srv.URL, "/api/embeddings", `{"model":"qwen3:8b"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestOllamaModelManagementEndpointsAreExplicitlyUnsupported covers the
// design doc's requirement (§五.3) that a model-management call gets a
// clear "not supported" answer instead of a bare 404 or a misleading
// success — the Gateway never manages any node's local model files.
func TestOllamaModelManagementEndpointsAreExplicitlyUnsupported(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{"POST", "/api/create"},
		{"POST", "/api/pull"},
		{"POST", "/api/push"},
		{"DELETE", "/api/delete"},
		{"POST", "/api/copy"},
		{"POST", "/api/show"},
		{"GET", "/api/tags"},
		{"GET", "/api/ps"},
	}
	srv, _ := newServer(t, httpapi.Config{})

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, srv.URL+tt.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tt.method, tt.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501", resp.StatusCode)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decoding error body: %v", err)
			}
			if body.Error == "" {
				t.Error("error is empty, want an explanation that model management is not supported")
			}
		})
	}
}

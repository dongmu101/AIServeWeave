package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

func newServer(t *testing.T, cfg httpapi.Config) (*httptest.Server, *gatewaytest.Harness) {
	t.Helper()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock})
	srv := httptest.NewServer(httpapi.New(sched, cfg))
	t.Cleanup(srv.Close)
	return srv, h
}

func TestChatCompletionsNonStreaming(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"qwen3:8b","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != "answer to: hello" {
		t.Errorf("choices = %+v, want one choice with content %q", body.Choices, "answer to: hello")
	}
	if body.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", body.Choices[0].FinishReason)
	}
	if body.Usage.TotalTokens != 8 {
		t.Errorf("usage.total_tokens = %d, want 8", body.Usage.TotalTokens)
	}
}

func TestChatCompletionsStreamingEndsWithDone(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"qwen3:8b","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	var content strings.Builder
	sawDone := false
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			sawDone = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("decoding chunk %q: %v", data, err)
		}
		if len(chunk.Choices) == 1 {
			content.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if !sawDone {
		t.Error("stream ended without a [DONE] event")
	}
	if content.String() != "Hello" {
		t.Errorf("streamed content = %q, want %q", content.String(), "Hello")
	}
}

// TestChatCompletionsStreamingStopsOnClientDisconnect checks that a client
// disconnect unblocks the handler goroutine promptly rather than leaking it
// forever. It does not (and structurally cannot) observe the scripted fake
// Agent's own goroutine exit: gatewaytest's AgentSlot runs its slotHandler
// synchronously, with nothing reading a Cancel frame while the handler is
// mid-call, so that side only unwinds once the harness tears every stream
// down at test cleanup. What is real, and what this test verifies, is that
// tunnelserver.Response.Recv selects on the request's own context (call.go)
// and returns as soon as it is cancelled — independent of whether the Agent
// ever reacts — so the front door's handler goroutine for this request
// exits on its own. httptest.Server.Close blocking on exactly that
// goroutine is the proof.
func TestChatCompletionsStreamingStopsOnClientDisconnect(t *testing.T) {
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	srv := httptest.NewServer(httpapi.New(scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock}), httpapi.Config{Logger: slog.New(slog.DiscardHandler)}))

	started := make(chan struct{})
	connectNodeWithHandler(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), blockingStreamHandler(started))

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"qwen3:8b","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("reading the first SSE line: %v", err)
	}

	<-started
	cancel()
	resp.Body.Close()

	closed := make(chan struct{})
	go func() { srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(gatewaytest.Timeout):
		t.Fatal("the httptest server did not shut down; the streaming handler likely did not exit after the client disconnected")
	}
}

func TestChatCompletionsUnknownModelReturns404(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"does-not-exist","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	var body struct {
		Error struct{ Type string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Error.Type != "invalid_request_error" {
		t.Errorf("error.type = %q, want invalid_request_error", body.Error.Type)
	}
}

func TestMissingAPIKeyReturns401(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{APIKeys: []string{"secret-key"}})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(
		`{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status without a key = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(
		`{"model":"qwen3:8b","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-key")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status with the correct key = %d, want 200", resp2.StatusCode)
	}
}

func TestModelsAggregates(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler)
	connectNode(t, h, "node-b", "backend-1", chatCapableSnapshot("backend-1", "gemma3:27b"), chatHandler)

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("data = %v, want 2 models", body.Data)
	}
}

func TestEmbeddings(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "nomic-embed"), chatHandler)

	resp, err := http.Post(srv.URL+"/v1/embeddings", "application/json", bytes.NewReader(
		[]byte(`{"model":"nomic-embed","input":"hello"}`)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Data) != 1 || len(body.Data[0].Embedding) != 2 || body.Data[0].Embedding[1] != -0.25 {
		t.Errorf("data = %v, want one 2-dimensional embedding ending in -0.25", body.Data)
	}
}

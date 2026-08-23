package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

func writeSSEChunk(w http.ResponseWriter, flusher http.Flusher, jsonPayload string) {
	fmt.Fprintf(w, "data: %s\n\n", jsonPayload)
	flusher.Flush()
}

func TestChatStreamDecodesDeltasAndStopsAtDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		writeSSEChunk(w, flusher, `{"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi"}}]}`)
		writeSSEChunk(w, flusher, `{"id":"c1","model":"m1","choices":[{"delta":{"content":" there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := ChatStream(context.Background(), c, runtime.ChatRequest{Model: "m1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	ev1, err := stream.Recv()
	if err != nil {
		t.Fatalf("unexpected error on first event: %v", err)
	}
	if ev1.Delta.Role != "assistant" || ev1.Delta.Content != "Hi" {
		t.Fatalf("unexpected first event: %+v", ev1)
	}

	ev2, err := stream.Recv()
	if err != nil {
		t.Fatalf("unexpected error on second event: %v", err)
	}
	if ev2.Delta.Content != " there" || ev2.FinishReason != "stop" {
		t.Fatalf("unexpected second event: %+v", ev2)
	}
	if ev2.Usage == nil || ev2.Usage.TotalTokens != 3 {
		t.Fatalf("unexpected usage: %+v", ev2.Usage)
	}

	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF after [DONE], got %v", err)
	}
}

func TestChatStreamSendsStreamOptionsIncludeUsage(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := ChatStream(context.Background(), c, runtime.ChatRequest{Model: "m1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	stream.Recv()

	if gotBody["stream"] != true {
		t.Errorf("stream = %v, want true", gotBody["stream"])
	}
	opts, _ := gotBody["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Errorf("stream_options.include_usage = %v, want true", gotBody["stream_options"])
	}
}

func TestChatStreamCommittedTracksFirstDelivery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		writeSSEChunk(w, flusher, `{"id":"c1","model":"m1","choices":[{"delta":{"content":"a"}}]}`)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := ChatStream(context.Background(), c, runtime.ChatRequest{Model: "m1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if stream.Committed() {
		t.Fatal("Committed() = true before the first event was received")
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if !stream.Committed() {
		t.Fatal("Committed() = false after the first event was received")
	}
}

func TestChatStreamExtraFieldCollisionIsRejected(t *testing.T) {
	c, err := NewClient(ClientConfig{BaseURL: "http://example.com", Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.ChatRequest{
		Model: "m1",
		Extra: map[string]json.RawMessage{"stream": json.RawMessage(`false`)},
	}
	_, err = ChatStream(context.Background(), c, req, 0)
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorInvalidConfig {
		t.Fatalf("error = %v, want a RuntimeError with Code %s", err, runtime.ErrorInvalidConfig)
	}
}

func TestChatStreamMalformedJSONIsProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {not json}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := ChatStream(context.Background(), c, runtime.ChatRequest{Model: "m1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	_, err = stream.Recv()
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected *runtime.RuntimeError, got %v (%T)", err, err)
	}
	if rtErr.Code != runtime.ErrorProtocol {
		t.Fatalf("Code = %s, want %s", rtErr.Code, runtime.ErrorProtocol)
	}
}

func TestChatStreamCloseCancelsUnderlyingRequest(t *testing.T) {
	serverCanceled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		writeSSEChunk(w, flusher, `{"id":"c1","model":"m1","choices":[{"delta":{"content":"a"}}]}`)
		<-r.Context().Done()
		close(serverCanceled)
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := ChatStream(context.Background(), c, runtime.ChatRequest{Model: "m1"}, 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stream.Recv(); err != nil {
		t.Fatalf("unexpected error on first event: %v", err)
	}

	if err := stream.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	select {
	case <-serverCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe request cancellation after Close")
	}
}

func TestChatStreamIdleTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		writeSSEChunk(w, flusher, `{"id":"c1","model":"m1","choices":[{"delta":{"content":"a"}}]}`)
		<-block // hang without sending more data or closing
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	stream, err := ChatStream(context.Background(), c, runtime.ChatRequest{Model: "m1"}, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if _, err := stream.Recv(); err != nil {
		t.Fatalf("unexpected error on first event: %v", err)
	}

	_, err = stream.Recv()
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected *runtime.RuntimeError after idle timeout, got %v (%T)", err, err)
	}
	if rtErr.Code != runtime.ErrorTimeout {
		t.Fatalf("Code = %s, want %s", rtErr.Code, runtime.ErrorTimeout)
	}
}

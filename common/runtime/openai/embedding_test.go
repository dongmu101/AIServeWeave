package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

func TestEmbedSendsRequestAndDecodesResponse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{
			"model": "embed-1",
			"data": [
				{"index": 0, "embedding": [0.1, 0.2, 0.3]},
				{"index": 1, "embedding": [0.4, 0.5, 0.6]}
			],
			"usage": {"prompt_tokens": 4, "total_tokens": 4}
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	dims := 3
	req := runtime.EmbeddingRequest{Model: "embed-1", Input: []string{"a", "b"}, Dimensions: &dims}
	resp, err := Embed(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}

	if gotBody["model"] != "embed-1" {
		t.Errorf("request model = %v, want embed-1", gotBody["model"])
	}
	if gotBody["dimensions"] != float64(3) {
		t.Errorf("request dimensions = %v, want 3", gotBody["dimensions"])
	}
	if resp.Model != "embed-1" {
		t.Errorf("response model = %q, want embed-1", resp.Model)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Data))
	}
	if resp.Data[1].Index != 1 || len(resp.Data[1].Vector) != 3 {
		t.Errorf("unexpected embedding[1]: %+v", resp.Data[1])
	}
	if resp.Usage.TotalTokens != 4 {
		t.Errorf("Usage.TotalTokens = %d, want 4", resp.Usage.TotalTokens)
	}
}

func TestEmbedOmitsDimensionsWhenNil(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"model":"embed-1","data":[{"index":0,"embedding":[0.1]}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Embed(context.Background(), c, runtime.EmbeddingRequest{Model: "embed-1", Input: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["dimensions"]; ok {
		t.Errorf("dimensions should be omitted when nil, got %v", gotBody["dimensions"])
	}
}

func TestEmbedUnauthorizedIsRuntimeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = Embed(context.Background(), c, runtime.EmbeddingRequest{Model: "embed-1", Input: []string{"a"}})
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected *runtime.RuntimeError, got %v", err)
	}
	if rtErr.Code != runtime.ErrorUnauthorized {
		t.Fatalf("Code = %s, want %s", rtErr.Code, runtime.ErrorUnauthorized)
	}
}

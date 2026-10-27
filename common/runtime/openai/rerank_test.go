package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestRerankSendsRequestAndDecodesResponse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{
			"model": "rerank-1",
			"results": [
				{"index": 1, "relevance_score": 0.9},
				{"index": 0, "relevance_score": 0.2}
			]
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	topN := 2
	req := runtime.RerankRequest{
		Model:     "rerank-1",
		Query:     "what is a cat",
		Documents: []string{"a dog is a mammal", "a cat is a mammal"},
		TopN:      &topN,
	}
	resp, err := Rerank(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}

	if gotBody["query"] != "what is a cat" {
		t.Errorf("request query = %v, want %q", gotBody["query"], "what is a cat")
	}
	if gotBody["top_n"] != float64(2) {
		t.Errorf("request top_n = %v, want 2", gotBody["top_n"])
	}
	if resp.Model != "rerank-1" {
		t.Errorf("response model = %q, want rerank-1", resp.Model)
	}
	if len(resp.Results) != 2 || resp.Results[0].Index != 1 || resp.Results[0].Score != 0.9 {
		t.Errorf("unexpected results: %+v", resp.Results)
	}
}

func TestRerankOmitsTopNWhenNil(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"model":"rerank-1","results":[{"index":0,"relevance_score":0.5}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Rerank(context.Background(), c, runtime.RerankRequest{Model: "rerank-1", Query: "q", Documents: []string{"d"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["top_n"]; ok {
		t.Errorf("top_n should be omitted when nil, got %v", gotBody["top_n"])
	}
}

func TestRerankUnauthorizedIsRuntimeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = Rerank(context.Background(), c, runtime.RerankRequest{Model: "rerank-1", Query: "q", Documents: []string{"d"}})
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected *runtime.RuntimeError, got %v", err)
	}
	if rtErr.Code != runtime.ErrorUnauthorized {
		t.Fatalf("Code = %s, want %s", rtErr.Code, runtime.ErrorUnauthorized)
	}
}

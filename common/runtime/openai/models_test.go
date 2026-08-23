package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %q", r.Method)
		}
		w.Write([]byte(`{"object":"list","data":[{"id":"llama-3","object":"model"},{"id":"qwen2","object":"model"}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	ids, err := ListModels(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "llama-3" || ids[1] != "qwen2" {
		t.Fatalf("unexpected model ids: %v", ids)
	}
}

func TestListModelsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	ids, err := ListModels(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected no models, got %v", ids)
	}
}

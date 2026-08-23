package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

func TestClientURLPrefixPreserved(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL + "/vendor", Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Do(context.Background(), "list_models", http.MethodGet, "/v1/models", nil, &map[string]any{}); err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	if gotPath != "/vendor/v1/models" {
		t.Fatalf("got path %q, want /vendor/v1/models", gotPath)
	}
}

func TestClientAuthAndCustomHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Trace-Id")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{
		BaseURL: srv.URL,
		APIKey:  "sk-secret",
		Headers: map[string]string{"X-Trace-Id": "abc"},
		Kind:    runtime.KindVLLM,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), "op", http.MethodGet, "/x", nil, &map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Fatalf("Authorization = %q, want Bearer sk-secret", gotAuth)
	}
	if gotCustom != "abc" {
		t.Fatalf("X-Trace-Id = %q, want abc", gotCustom)
	}
}

func TestClientRejectsHopByHopHeaders(t *testing.T) {
	for _, name := range []string{"Host", "Content-Length", "Connection", "Transfer-Encoding"} {
		if _, err := NewClient(ClientConfig{BaseURL: "http://example.com", Headers: map[string]string{name: "x"}}); err == nil {
			t.Errorf("expected error for header %q", name)
		}
	}
}

func TestClientRejectsInvalidBaseURL(t *testing.T) {
	cases := []string{
		"ftp://example.com",
		"http://user:pass@example.com",
		"http://example.com?x=1",
		"http://example.com#frag",
		"",
	}
	for _, base := range cases {
		if _, err := NewClient(ClientConfig{BaseURL: base}); err == nil {
			t.Errorf("expected error for base URL %q", base)
		}
	}
}

func TestClientContextDeadlineExceeded(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err = c.Do(ctx, "op", http.MethodGet, "/slow", nil, nil)
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected *runtime.RuntimeError, got %v (%T)", err, err)
	}
	if rtErr.Code != runtime.ErrorTimeout {
		t.Fatalf("Code = %s, want %s", rtErr.Code, runtime.ErrorTimeout)
	}
}

func TestClientResponseTooLarge(t *testing.T) {
	big := strings.Repeat("a", 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":"` + big + `"}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, MaxResponseBytes: 10, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	err = c.Do(context.Background(), "op", http.MethodGet, "/big", nil, &map[string]any{})
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected *runtime.RuntimeError, got %v", err)
	}
	if rtErr.Code != runtime.ErrorResponseTooLarge {
		t.Fatalf("Code = %s, want %s", rtErr.Code, runtime.ErrorResponseTooLarge)
	}
}

func TestClientErrorResponseRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		body, _ := json.Marshal(map[string]any{
			"error": map[string]any{"message": "invalid api key: sk-secret123"},
		})
		w.Write(body)
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, APIKey: "sk-secret123", Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	err = c.Do(context.Background(), "op", http.MethodGet, "/x", nil, nil)
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected *runtime.RuntimeError, got %v", err)
	}
	if rtErr.Code != runtime.ErrorUnauthorized {
		t.Fatalf("Code = %s, want %s", rtErr.Code, runtime.ErrorUnauthorized)
	}
	if strings.Contains(rtErr.Message, "sk-secret123") {
		t.Fatalf("Message leaked API key: %q", rtErr.Message)
	}
	if !strings.Contains(rtErr.Message, "[REDACTED]") {
		t.Fatalf("Message not redacted: %q", rtErr.Message)
	}
}

func TestClientDecodesSuccessResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"abc","object":"model"}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	var out struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	if err := c.Do(context.Background(), "op", http.MethodGet, "/x", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "abc" || out.Object != "model" {
		t.Fatalf("unexpected decode result: %+v", out)
	}
}

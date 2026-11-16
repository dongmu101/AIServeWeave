package pricecatalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPSourceFetch(t *testing.T) {
	var accept, agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept, agent = r.Header.Get("Accept"), r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"m":{}}`))
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", MaxFetchBytes+1)))
		default:
			http.Error(w, "nope", http.StatusForbidden)
		}
	}))
	defer srv.Close()

	src := HTTPSource{URL: srv.URL + "/ok", Client: srv.Client()}
	body, err := src.Fetch(context.Background())
	if err != nil || string(body) != `{"m":{}}` {
		t.Fatalf("Fetch = %q, %v", body, err)
	}
	if accept != "application/vnd.github.raw" || agent != userAgent {
		t.Errorf("headers: Accept=%q User-Agent=%q", accept, agent)
	}
	if src.Label() != srv.URL+"/ok" {
		t.Errorf("Label = %q", src.Label())
	}
	if _, err := (HTTPSource{URL: srv.URL + "/denied", Client: srv.Client()}).Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("non-200: %v", err)
	}
	if _, err := (HTTPSource{URL: srv.URL + "/big", Client: srv.Client()}).Fetch(context.Background()); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.Fetch(ctx); err == nil {
		t.Error("a cancelled context must fail the fetch")
	}
}

func TestBytesSource(t *testing.T) {
	src := BytesSource{Data: []byte("x"), Name: EmbeddedLabel}
	if body, err := src.Fetch(context.Background()); err != nil || string(body) != "x" || src.Label() != EmbeddedLabel {
		t.Fatalf("BytesSource = %q, %v, %q", body, err, src.Label())
	}
}

package modelpull

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func writeNDJSON(t *testing.T, w http.ResponseWriter, lines []map[string]any) {
	t.Helper()
	for _, line := range lines {
		if err := json.NewEncoder(w).Encode(line); err != nil {
			t.Fatalf("encode ndjson line: %v", err)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func TestPullOllama_Success(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/pull" {
			t.Fatalf("request = %s %s, want POST /api/pull", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		writeNDJSON(t, w, []map[string]any{
			{"status": "pulling manifest"},
			{"status": "downloading digest1", "digest": "sha256:abc", "total": int64(100), "completed": int64(40)},
			{"status": "downloading digest1", "digest": "sha256:abc", "total": int64(100), "completed": int64(100)},
			{"status": "success"},
		})
	}))
	defer srv.Close()

	var progress [][2]int64
	err := pullOllama(context.Background(), http.DefaultClient, srv.URL, Spec{Name: "qwen3-coder:30b"}, func(downloaded, total int64) {
		progress = append(progress, [2]int64{downloaded, total})
	})
	if err != nil {
		t.Fatalf("pullOllama() error = %v, want nil", err)
	}
	if gotBody["model"] != "qwen3-coder:30b" {
		t.Fatalf("request body model = %v, want qwen3-coder:30b", gotBody["model"])
	}
	if gotBody["stream"] != true {
		t.Fatalf("request body stream = %v, want true", gotBody["stream"])
	}
	want := [][2]int64{{40, 100}, {100, 100}}
	if len(progress) != len(want) || progress[0] != want[0] || progress[1] != want[1] {
		t.Fatalf("progress = %v, want %v", progress, want)
	}
}

func TestPullOllama_NoProgressCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(t, w, []map[string]any{{"status": "success"}})
	}))
	defer srv.Close()

	if err := pullOllama(context.Background(), http.DefaultClient, srv.URL, Spec{Name: "m1"}, nil); err != nil {
		t.Fatalf("pullOllama() error = %v, want nil", err)
	}
}

func TestPullOllama_ServerReportedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(t, w, []map[string]any{
			{"status": "pulling manifest"},
			{"error": "model \"nonexistent:latest\" not found"},
		})
	}))
	defer srv.Close()

	err := pullOllama(context.Background(), http.DefaultClient, srv.URL, Spec{Name: "nonexistent:latest"}, nil)
	if !errors.Is(err, errOllamaPullFailed) {
		t.Fatalf("pullOllama() error = %v, want errOllamaPullFailed", err)
	}
}

func TestPullOllama_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := pullOllama(context.Background(), http.DefaultClient, srv.URL, Spec{Name: "m1"}, nil)
	if !errors.Is(err, errUnexpectedStatus) {
		t.Fatalf("pullOllama() error = %v, want errUnexpectedStatus", err)
	}
}

func TestPullOllama_StreamEndsWithoutSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNDJSON(t, w, []map[string]any{{"status": "pulling manifest"}})
	}))
	defer srv.Close()

	err := pullOllama(context.Background(), http.DefaultClient, srv.URL, Spec{Name: "m1"}, nil)
	if !errors.Is(err, errFetchFailed) {
		t.Fatalf("pullOllama() error = %v, want errFetchFailed", err)
	}
}

func TestPullOllama_Unconfigured(t *testing.T) {
	err := pullOllama(context.Background(), http.DefaultClient, "", Spec{Name: "m1"}, nil)
	if !errors.Is(err, errOllamaUnconfigured) {
		t.Fatalf("pullOllama() error = %v, want errOllamaUnconfigured", err)
	}
}

func TestPullOllama_TransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before use: connection refused

	err := pullOllama(context.Background(), http.DefaultClient, srv.URL, Spec{Name: "m1"}, nil)
	if !errors.Is(err, errFetchFailed) {
		t.Fatalf("pullOllama() error = %v, want errFetchFailed", err)
	}
}

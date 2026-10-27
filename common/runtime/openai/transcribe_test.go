package openai

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

func float64Ptr(f float64) *float64 { return &f }
func stringPtr(s string) *string    { return &s }

func TestTranscribeSendsMultipartAndDecodesJSON(t *testing.T) {
	var gotPath string
	var gotFields map[string]string
	var gotFileBytes []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("unexpected content type: %v, %v", mediaType, err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		gotFields = map[string]string{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			if part.FormName() == "file" {
				gotFileBytes = b
				continue
			}
			gotFields[part.FormName()] = string(b)
		}
		w.Write([]byte(`{"text":"hello world","language":"en","duration":1.5}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.AudioTranscriptionRequest{
		Model:       "whisper-1",
		Filename:    "clip.mp3",
		Task:        runtime.AudioTaskTranscribe,
		Language:    stringPtr("en"),
		Prompt:      stringPtr("a greeting"),
		Temperature: float64Ptr(0.2),
	}
	resp, err := Transcribe(context.Background(), c, req, strings.NewReader("fake audio bytes"))
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/v1/audio/transcriptions" {
		t.Errorf("path = %q, want /v1/audio/transcriptions", gotPath)
	}
	if string(gotFileBytes) != "fake audio bytes" {
		t.Errorf("file bytes = %q", gotFileBytes)
	}
	if gotFields["model"] != "whisper-1" {
		t.Errorf("model field = %q", gotFields["model"])
	}
	if gotFields["language"] != "en" {
		t.Errorf("language field = %q", gotFields["language"])
	}
	if gotFields["prompt"] != "a greeting" {
		t.Errorf("prompt field = %q", gotFields["prompt"])
	}
	if gotFields["temperature"] != "0.2" {
		t.Errorf("temperature field = %q", gotFields["temperature"])
	}
	if gotFields["response_format"] != "json" {
		t.Errorf("response_format field = %q, want json (the default)", gotFields["response_format"])
	}
	if resp.Text != "hello world" || resp.Language != "en" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.Duration == nil || *resp.Duration != 1.5 {
		t.Errorf("Duration = %v, want 1.5", resp.Duration)
	}
}

func TestTranscribeTranslateUsesTranslationsPathAndOmitsLanguage(t *testing.T) {
	var gotPath string
	var sawLanguage bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaType != "multipart/form-data" {
			t.Fatal("expected multipart/form-data")
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if part.FormName() == "language" {
				sawLanguage = true
			}
			io.Copy(io.Discard, part)
		}
		w.Write([]byte(`{"text":"bonjour translated"}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.AudioTranscriptionRequest{
		Model:    "whisper-1",
		Filename: "clip.mp3",
		Task:     runtime.AudioTaskTranslate,
		// A caller-supplied Language is meaningless for translation; the
		// httpapi layer rejects it before it reaches this type, but this
		// client-level test confirms Transcribe itself never forwards it
		// even if one somehow arrived here.
		Language: stringPtr("fr"),
	}
	if _, err := Transcribe(context.Background(), c, req, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}

	if gotPath != "/v1/audio/translations" {
		t.Errorf("path = %q, want /v1/audio/translations", gotPath)
	}
	if sawLanguage {
		t.Error("translation request should not carry a language field")
	}
}

func TestTranscribeTextResponseFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		if got := r.FormValue("response_format"); got != "text" {
			t.Errorf("response_format = %q, want text", got)
		}
		w.Write([]byte("plain text transcript"))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.AudioTranscriptionRequest{
		Model:          "whisper-1",
		Filename:       "clip.wav",
		Task:           runtime.AudioTaskTranscribe,
		ResponseFormat: "text",
	}
	resp, err := Transcribe(context.Background(), c, req, strings.NewReader("audio"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "plain text transcript" {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestTranscribeUpstreamErrorIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": {"message": "unsupported audio format"}}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = Transcribe(context.Background(), c, runtime.AudioTranscriptionRequest{
		Model: "whisper-1", Filename: "clip.mp3", Task: runtime.AudioTaskTranscribe,
	}, strings.NewReader("x"))
	var rtErr *runtime.RuntimeError
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected a *runtime.RuntimeError, got %T: %v", err, err)
	}
	if rtErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", rtErr.StatusCode)
	}
}

package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
)

// fakeWAV is a minimal byte sequence net/http.DetectContentType recognizes
// as audio (the "RIFF....WAVE" signature), followed by arbitrary payload —
// enough to pass validateUploadContent's sniff without a real encoder.
func fakeWAV(payload string) []byte {
	header := []byte("RIFF\x00\x00\x00\x00WAVEfmt ")
	return append(header, []byte(payload)...)
}

// transcribeHandler answers OPERATION_AUDIO_TRANSCRIBE with the request's
// model, filename and the audio bytes it received, tagged with source, so a
// test can confirm both which node served the request and that the bytes
// survived DataChunk framing.
func transcribeHandler(source string) gatewaytest.SlotHandler {
	return func(req *tunnelv1.RequestHeaders, body [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
		if req.GetOperation() != tunnelv1.Operation_OPERATION_AUDIO_TRANSCRIBE {
			return errors.New("unsupported operation")
		}
		in, err := tunnelwire.UnmarshalAudioTranscriptionRequest(req.GetPayload())
		if err != nil {
			return err
		}
		var joined []byte
		for _, chunk := range body {
			joined = append(joined, chunk...)
		}
		payload, err := tunnelwire.MarshalAudioTranscriptionResponse(runtime.AudioTranscriptionResponse{
			Text: source + ":" + in.Model + ":" + in.Filename + ":" + string(joined),
		})
		if err != nil {
			return err
		}
		return reply(gatewaytest.DataFrame(payload))
	}
}

// connectTranscriptionNode wires up nodeID with a bulk slot advertising
// CapabilityAudioTranscription for model — AUDIO_TRANSCRIBE travels on
// SLOT_CLASS_BULK (tunnelserver's classFor), unlike connectNode's
// inference-only slot.
func connectTranscriptionNode(t *testing.T, h *gatewaytest.Harness, nodeID, runtimeID, model string) {
	t.Helper()
	snap := chatCapableSnapshot(runtimeID, model)
	snap.Discovery.Models[0].Capabilities[runtime.CapabilityAudioTranscription] = runtime.CapabilityEvidence{Level: runtime.SupportSupported}

	c := h.Connect(nodeID, runtimeID)
	c.Send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
		Full:       true,
		ReportedAt: timestamppb.New(h.Clock.Now()),
		Snapshots:  tunnelwire.SnapshotsToProto([]runtime.Snapshot{snap}),
	}}})
	h.OpenSlot(nodeID, tunnelv1.SlotClass_SLOT_CLASS_BULK, nodeID+"-bulk-0", transcribeHandler(nodeID))
	gatewaytest.WaitFor(t, "bulk slot to park on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_BULK] == 1
	})
	gatewaytest.WaitFor(t, "inventory to arrive on "+nodeID, func() bool {
		info, _ := h.Srv.Node(nodeID)
		return len(info.Runtimes) == 1
	})
}

// postAudio builds a multipart/form-data request against path with fields
// and a "file" part named filename holding content.
func postAudio(t *testing.T, url, path string, fields map[string]string, filename string, content []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		part, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url+path, mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func TestAudioTranscriptionsDefaultJSONFormat(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	resp := postAudio(t, srv.URL, "/v1/audio/transcriptions",
		map[string]string{"model": "whisper-1"}, "clip.wav", fakeWAV("hello world"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := "node-a:whisper-1:clip.wav:" + string(fakeWAV("hello world"))
	if got.Text != want {
		t.Errorf("Text = %q, want %q", got.Text, want)
	}
}

func TestAudioTranscriptionsTextFormat(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	resp := postAudio(t, srv.URL, "/v1/audio/transcriptions",
		map[string]string{"model": "whisper-1", "response_format": "text"}, "clip.wav", fakeWAV("hi"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want := "node-a:whisper-1:clip.wav:" + string(fakeWAV("hi"))
	if string(body) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestAudioTranslationsRejectsLanguage(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	resp := postAudio(t, srv.URL, "/v1/audio/translations",
		map[string]string{"model": "whisper-1", "language": "fr"}, "clip.wav", fakeWAV("bonjour"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAudioTranslationsDispatchesToTranslationsTask(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	resp := postAudio(t, srv.URL, "/v1/audio/translations",
		map[string]string{"model": "whisper-1"}, "clip.wav", fakeWAV("bonjour"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

func TestAudioTranscriptionsRejectsUnsupportedExtension(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	resp := postAudio(t, srv.URL, "/v1/audio/transcriptions",
		map[string]string{"model": "whisper-1"}, "clip.png", []byte("not audio"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAudioTranscriptionsRejectsMismatchedContent(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	// A .wav extension whose bytes are not actually audio must be caught by
	// the content sniff, the same defense uploadformat.go already applies to
	// workflow InputFile parts.
	resp := postAudio(t, srv.URL, "/v1/audio/transcriptions",
		map[string]string{"model": "whisper-1"}, "clip.wav", []byte("plain text, not a wav file"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAudioTranscriptionsRequiresModel(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	resp := postAudio(t, srv.URL, "/v1/audio/transcriptions", map[string]string{}, "clip.wav", fakeWAV("x"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAudioTranscriptionsRejectsUnsupportedResponseFormat(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	resp := postAudio(t, srv.URL, "/v1/audio/transcriptions",
		map[string]string{"model": "whisper-1", "response_format": "verbose_json"}, "clip.wav", fakeWAV("x"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAudioTranscriptionsNoCapableNode(t *testing.T) {
	srv, _ := newServer(t, httpapi.Config{})

	resp := postAudio(t, srv.URL, "/v1/audio/transcriptions",
		map[string]string{"model": "whisper-1"}, "clip.wav", fakeWAV("x"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAudioTranscriptionsRejectsNonMultipart(t *testing.T) {
	srv, h := newServer(t, httpapi.Config{})
	connectTranscriptionNode(t, h, "node-a", "backend-1", "whisper-1")

	resp, err := http.Post(srv.URL+"/v1/audio/transcriptions", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

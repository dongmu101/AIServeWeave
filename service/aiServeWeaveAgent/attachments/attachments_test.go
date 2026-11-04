package attachments

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		converterFixture()
		return
	}
	os.Exit(m.Run())
}

func converterFixture() {
	args := os.Args[1:]
	last := args[len(args)-1]
	value := func(flag string) string {
		for i, arg := range args {
			if arg == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	switch {
	case args[0] == "fixture-output":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
	case args[0] == "fixture-disk":
		file, err := os.Create("oversized")
		if err != nil {
			os.Exit(3)
		}
		_ = file.Truncate(129 << 20)
		_ = file.Close()
	case args[0] == "-isodates":
		data, _ := os.ReadFile(last)
		if bytes.Contains(data, []byte("MANY")) {
			fmt.Println("Pages: 99")
		} else {
			fmt.Println("Pages: 2")
		}
	case args[0] == "fixture-wait":
		_ = os.WriteFile("started", nil, 0600)
		time.Sleep(time.Minute)
	case strings.HasPrefix(args[0], "-env:"):
		data, _ := os.ReadFile(last)
		if bytes.Contains(data, []byte("FAIL")) {
			os.Exit(3)
		}
		_ = os.WriteFile(filepath.Join(filepath.Dir(last), "input.pdf"), append([]byte("%PDF-1.4\n"), data...), 0600)
	case args[0] == "-v":
		data, _ := os.ReadFile(last)
		duration := "12.5"
		if bytes.Contains(data, []byte("LONG")) {
			duration = "301"
		}
		if bytes.Contains(data, []byte("NAN")) {
			duration = "NaN"
		}
		streams := []map[string]string{{"codec_type": "video"}}
		if bytes.Contains(data, []byte("AUDIO")) {
			streams = append(streams, map[string]string{"codec_type": "audio"})
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"format": map[string]string{"duration": duration}, "streams": streams})
	case args[0] == "-nostdin":
		if strings.HasSuffix(last, ".wav") {
			data, _ := os.ReadFile(value("-i"))
			_ = os.WriteFile(last, data, 0600)
		} else {
			writeJPEG(last)
		}
	case args[0] == "-m":
		data, _ := os.ReadFile(value("-f"))
		if bytes.Contains(data, []byte("FAIL")) {
			os.Exit(3)
		}
		_ = os.WriteFile(value("-of")+".srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nspoken marker\n"), 0600)
	case args[0] == "-f":
		data, _ := os.ReadFile(args[len(args)-2])
		pages := 2
		if bytes.Contains(data, []byte("MANY")) {
			pages = 99
		}
		limit, _ := strconv.Atoi(value("-l"))
		pages = min(pages, limit)
		for i := 1; i <= pages; i++ {
			writeJPEG(fmt.Sprintf("%s-%d.jpg", last, i))
		}
	case args[0] == "-enc":
		_ = os.WriteFile(last, []byte("extracted table marker"), 0600)
	default:
		os.Exit(3)
	}
}

func writeJPEG(path string) {
	file, err := os.Create(path)
	if err != nil {
		os.Exit(3)
	}
	_ = jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	_ = file.Close()
}

func testProcessor(t *testing.T) *Processor {
	t.Helper()
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("fixture-model"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{LibreOfficePath: os.Args[0], PDFInfoPath: os.Args[0], PDFToPPMPath: os.Args[0], PDFToTextPath: os.Args[0], FFmpegPath: os.Args[0], FFprobePath: os.Args[0], WhisperPath: os.Args[0], WhisperModelPath: model, Timeout: "10s"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func document(mime, data string) Part {
	return Part{Type: "document", Title: "fixture", Source: &Source{Type: "base64", MediaType: mime, Data: base64.StdEncoding.EncodeToString([]byte(data))}}
}

func TestDocumentsPreserveContent(t *testing.T) {
	p := testProcessor(t)
	for _, mime := range []string{"text/plain", "text/markdown", "text/x-go", "text/csv", "application/json"} {
		parts, err := p.Convert(t.Context(), document(mime, "source marker"), false)
		if err != nil || len(parts) != 1 || !strings.Contains(parts[0].Text, "source marker") {
			t.Fatalf("%s lost text: %v", mime, err)
		}
	}
	pdf := document("application/pdf", "%PDF-1.4\nmarker")
	parts, err := p.Convert(t.Context(), pdf, false)
	if err != nil || len(parts) != 1 || parts[0].Source.Data != pdf.Source.Data {
		t.Fatalf("native PDF not preserved: %v", err)
	}
	parts, err = p.Convert(t.Context(), pdf, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 5 || parts[0].Type != "text" || !strings.Contains(parts[0].Text, "extracted table marker") || parts[2].Type != "image" || parts[4].Type != "image" {
		t.Fatal("MCP PDF result lost text or pages")
	}
	for _, mime := range []string{"application/msword", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.ms-excel", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.ms-powerpoint", "application/vnd.openxmlformats-officedocument.presentationml.presentation"} {
		parts, err := p.Convert(t.Context(), document(mime, "office marker"), false)
		if err != nil || len(parts) != 1 || parts[0].Source.MediaType != "application/pdf" {
			t.Fatalf("%s conversion failed: %v", mime, err)
		}
	}
}

func TestVideoFramesAndSpeech(t *testing.T) {
	p := testProcessor(t)
	for _, mime := range []string{"video/mp4", "video/quicktime", "video/webm"} {
		for _, audio := range []bool{false, true} {
			input := "VIDEO"
			if audio {
				input += " AUDIO"
			}
			part := document(mime, input)
			part.Type = "video"
			parts, err := p.Convert(t.Context(), part, false)
			if err != nil {
				t.Fatal(err)
			}
			frames := 0
			for _, part := range parts {
				if part.Type == "image" {
					frames++
				}
			}
			transcript := parts[len(parts)-1].Text
			if frames != 3 || (audio && !strings.Contains(transcript, "00:00:01,000")) || (!audio && !strings.Contains(transcript, "No audio track")) {
				t.Fatal("video lost frames, time alignment or speech")
			}
		}
	}
}

func TestConversionFailures(t *testing.T) {
	for _, tc := range []struct {
		name, mime, data string
		tool             bool
		want             error
	}{
		{"bad PDF", "application/pdf", "not a pdf", false, ErrInvalid},
		{"too many pages", "application/pdf", "%PDF-1.4 MANY", true, ErrLimit},
		{"Office failure", "application/msword", "FAIL", false, ErrConversion},
		{"Office page limit", "application/msword", "MANY", false, ErrLimit},
		{"long video", "video/mp4", "LONG", false, ErrLimit},
		{"bad duration", "video/mp4", "NAN", false, ErrInvalid},
		{"ASR failure", "video/mp4", "AUDIO FAIL", false, ErrConversion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProcessor(t)
			part := document(tc.mime, tc.data)
			if strings.HasPrefix(tc.mime, "video/") {
				part.Type = "video"
			}
			_, err := p.Convert(t.Context(), part, tc.tool)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
	p, _ := New(Config{})
	_, err := p.Convert(t.Context(), document("application/msword", "office"), false)
	if !errors.Is(err, ErrDependency) {
		t.Fatal("missing converter must fail explicitly")
	}
	p, _ = New(Config{MaxFileBytes: 3})
	_, err = p.Convert(t.Context(), document("text/plain", "too long"), false)
	if !errors.Is(err, ErrLimit) {
		t.Fatal("decoded limit was ignored")
	}
}

func TestCancellationAndCapacity(t *testing.T) {
	p := testProcessor(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := p.run(ctx, dir, os.Args[0], "fixture-wait"); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("child did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("converter process was not reaped")
	}
	p.slots <- struct{}{}
	_, err := p.Convert(t.Context(), document("application/msword", "office"), false)
	<-p.slots
	if !errors.Is(err, ErrBusy) {
		t.Fatal("conversion queue must be bounded")
	}
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "aisw-attachment-*"))
	_, _ = p.Convert(t.Context(), document("application/msword", "FAIL"), false)
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "aisw-attachment-*"))
	if len(before) != len(after) {
		t.Fatal("failed conversion left temporary files")
	}
}

func TestRejectsUnsafeShapesAndConfiguration(t *testing.T) {
	for _, part := range []Part{
		{Type: "document", Source: &Source{Type: "url", Data: "http://localhost/private"}},
		{Type: "video", Source: &Source{Type: "base64", MediaType: "video/mp4", Data: "***"}},
		{Type: "document", Title: "bad\nfilename", Source: &Source{Type: "text", Data: "x"}},
	} {
		if ValidateShape(part) == nil {
			t.Fatal("unsafe shape accepted")
		}
	}
	for _, cfg := range []Config{{FFmpegPath: "ffmpeg"}, {MaxFileBytes: -1}, {MaxConcurrent: -1}, {Timeout: "invalid"}} {
		if _, err := New(cfg); err == nil {
			t.Fatal("invalid local configuration accepted")
		}
	}
}

func TestConverterOutputBounds(t *testing.T) {
	p := testProcessor(t)
	for _, mode := range []string{"fixture-output", "fixture-disk"} {
		_, err := p.run(t.Context(), t.TempDir(), os.Args[0], mode)
		if !errors.Is(err, ErrLimit) {
			t.Fatalf("%s: error=%v, want output limit", mode, err)
		}
	}
	t.Run("symlink output", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skip("symlinks unavailable in this execution context")
		}
		if _, err := readBounded(link, 100); !errors.Is(err, ErrConversion) {
			t.Fatal("converter output followed a symlink")
		}
	})
	p, _ = New(Config{LibreOfficePath: os.Args[0], Timeout: "1ns"})
	_, err := p.Convert(t.Context(), document("application/msword", "office"), false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not enforced: %v", err)
	}
}

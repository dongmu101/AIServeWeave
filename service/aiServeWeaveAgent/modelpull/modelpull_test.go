package modelpull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestRunManifest_FullDownload(t *testing.T) {
	content := []byte("model bytes go here, pretend this is a checkpoint")
	digest := sha256Hex(content)

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "model.bin")
	cfg := Config{Allowlist: []string{srv.URL}}
	specs := []Spec{{Name: "m1", SourceURL: srv.URL, SHA256: digest, SizeBytes: int64(len(content)), TargetPath: target}}

	result := RunManifest(context.Background(), cfg, specs)

	if len(result.Failed) != 0 {
		t.Fatalf("Failed = %v, want none", result.Failed)
	}
	if len(result.Pulled) != 1 || result.Pulled[0] != "m1" {
		t.Fatalf("Pulled = %v, want [m1]", result.Pulled)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("target content = %q, want %q", got, content)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if _, err := os.Stat(target + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part file should be gone after finalize, stat err = %v", err)
	}
}

func TestRunManifest_ResumesPartialDownload(t *testing.T) {
	content := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	digest := sha256Hex(content)
	const prefixLen = 10

	gotRange := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		if gotRange == "" {
			w.Write(content)
			return
		}
		var n int
		fmt.Sscanf(gotRange, "bytes=%d-", &n)
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[n:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(target+".part", content[:prefixLen], 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	cfg := Config{Allowlist: []string{srv.URL}}
	specs := []Spec{{Name: "m1", SourceURL: srv.URL, SHA256: digest, SizeBytes: int64(len(content)), TargetPath: target}}

	result := RunManifest(context.Background(), cfg, specs)

	if len(result.Failed) != 0 {
		t.Fatalf("Failed = %v, want none", result.Failed)
	}
	if want := fmt.Sprintf("bytes=%d-", prefixLen); gotRange != want {
		t.Fatalf("Range header = %q, want %q", gotRange, want)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("target content = %q, want %q", got, content)
	}
}

func TestRunManifest_ResumeFallsBackToFreshDownloadWhenServerIgnoresRange(t *testing.T) {
	content := []byte("full content the server always resends from scratch")
	digest := sha256Hex(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server ignores Range and always answers 200 with the whole body.
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(target+".part", []byte("stale partial bytes"), 0o644); err != nil {
		t.Fatalf("seed partial file: %v", err)
	}

	cfg := Config{Allowlist: []string{srv.URL}}
	specs := []Spec{{Name: "m1", SourceURL: srv.URL, SHA256: digest, TargetPath: target}}

	result := RunManifest(context.Background(), cfg, specs)

	if len(result.Failed) != 0 {
		t.Fatalf("Failed = %v, want none", result.Failed)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("target content = %q, want %q", got, content)
	}
}

func TestRunManifest_ChecksumMismatchIsRejectedAndCleanedUp(t *testing.T) {
	content := []byte("actual content")
	wrongDigest := sha256Hex([]byte("a completely different payload"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "model.bin")
	cfg := Config{Allowlist: []string{srv.URL}}
	specs := []Spec{{Name: "m1", SourceURL: srv.URL, SHA256: wrongDigest, TargetPath: target}}

	result := RunManifest(context.Background(), cfg, specs)

	if _, ok := result.Failed["m1"]; !ok {
		t.Fatalf("expected m1 to fail, got Pulled=%v Failed=%v", result.Pulled, result.Failed)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target should not exist after a checksum mismatch")
	}
	if _, err := os.Stat(target + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part file should be removed after a checksum mismatch")
	}
}

func TestRunManifest_AllowlistRejectsUnlistedSource(t *testing.T) {
	tests := []struct {
		name      string
		allowlist []string
	}{
		{name: "empty allowlist rejects everything", allowlist: nil},
		{name: "non-matching prefix rejects", allowlist: []string{"https://example.invalid/models/"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Write([]byte("x"))
			}))
			defer srv.Close()

			dir := t.TempDir()
			target := filepath.Join(dir, "model.bin")
			specs := []Spec{{Name: "m1", SourceURL: srv.URL, SHA256: sha256Hex([]byte("x")), TargetPath: target}}

			result := RunManifest(context.Background(), Config{Allowlist: tt.allowlist}, specs)

			if _, ok := result.Failed["m1"]; !ok {
				t.Fatalf("expected m1 to fail, got Pulled=%v Failed=%v", result.Pulled, result.Failed)
			}
			if requests != 0 {
				t.Fatalf("requests = %d, want 0: allowlist rejection must not touch the network", requests)
			}
		})
	}
}

func TestRunManifest_QuotaRejectsBeforeRequestWhenSizeIsKnown(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write(make([]byte, 1000))
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "model.bin")
	cfg := Config{Allowlist: []string{srv.URL}, QuotaBytes: 100}
	specs := []Spec{{Name: "m1", SourceURL: srv.URL, SHA256: sha256Hex(make([]byte, 1000)), SizeBytes: 1000, TargetPath: target}}

	result := RunManifest(context.Background(), cfg, specs)

	if _, ok := result.Failed["m1"]; !ok {
		t.Fatalf("expected m1 to fail on quota, got Pulled=%v Failed=%v", result.Pulled, result.Failed)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0: a quota precheck must not touch the network", requests)
	}
}

func TestRunManifest_QuotaAbortsMidStreamThenResumesWithMoreBudget(t *testing.T) {
	content := make([]byte, 5000)
	for i := range content {
		content[i] = byte(i)
	}
	digest := sha256Hex(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Write(content)
			return
		}
		var n int
		fmt.Sscanf(rangeHeader, "bytes=%d-", &n)
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[n:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "model.bin")
	spec := Spec{Name: "m1", SourceURL: srv.URL, SHA256: digest, TargetPath: target} // SizeBytes deliberately unknown

	tight := Config{Allowlist: []string{srv.URL}, QuotaBytes: 10}
	result := RunManifest(context.Background(), tight, []Spec{spec})
	if _, ok := result.Failed["m1"]; !ok {
		t.Fatalf("expected m1 to fail under a tight quota, got Pulled=%v Failed=%v", result.Pulled, result.Failed)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target should not exist after a quota abort")
	}

	unlimited := Config{Allowlist: []string{srv.URL}}
	result = RunManifest(context.Background(), unlimited, []Spec{spec})
	if len(result.Failed) != 0 {
		t.Fatalf("Failed = %v, want none once the quota is lifted", result.Failed)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("target content mismatch after the resumed run")
	}
}

func TestRunManifest_SkipsAlreadyVerifiedTarget(t *testing.T) {
	content := []byte("already have this")
	digest := sha256Hex(content)

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	cfg := Config{Allowlist: []string{srv.URL}}
	specs := []Spec{{Name: "m1", SourceURL: srv.URL, SHA256: digest, TargetPath: target}}

	result := RunManifest(context.Background(), cfg, specs)

	if len(result.Skipped) != 1 || result.Skipped[0] != "m1" {
		t.Fatalf("Skipped = %v, want [m1]", result.Skipped)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0: an already-verified target must not touch the network", requests)
	}
}

func TestRunManifest_OneFailureDoesNotStopTheRest(t *testing.T) {
	content := []byte("good content")
	digest := sha256Hex(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := Config{Allowlist: []string{srv.URL}}
	specs := []Spec{
		{Name: "missing-source", SourceURL: "", SHA256: digest, TargetPath: filepath.Join(dir, "a.bin")},
		{Name: "good", SourceURL: srv.URL, SHA256: digest, TargetPath: filepath.Join(dir, "b.bin")},
	}

	result := RunManifest(context.Background(), cfg, specs)

	if _, ok := result.Failed["missing-source"]; !ok {
		t.Fatalf("expected missing-source to fail")
	}
	if len(result.Pulled) != 1 || result.Pulled[0] != "good" {
		t.Fatalf("Pulled = %v, want [good]", result.Pulled)
	}
}

func TestValidateSpec(t *testing.T) {
	validDigest := sha256Hex([]byte("x"))

	tests := []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{name: "valid", spec: Spec{Name: "m1", SourceURL: "https://example.invalid/m1", SHA256: validDigest, TargetPath: "/abs/m1.bin"}, wantErr: false},
		{name: "missing name", spec: Spec{SourceURL: "https://example.invalid/m1", SHA256: validDigest, TargetPath: "/abs/m1.bin"}, wantErr: true},
		{name: "missing source_url", spec: Spec{Name: "m1", SHA256: validDigest, TargetPath: "/abs/m1.bin"}, wantErr: true},
		{name: "invalid sha256 length", spec: Spec{Name: "m1", SourceURL: "https://example.invalid/m1", SHA256: "abc", TargetPath: "/abs/m1.bin"}, wantErr: true},
		{name: "non-hex sha256", spec: Spec{Name: "m1", SourceURL: "https://example.invalid/m1", SHA256: strings.Repeat("z", 64), TargetPath: "/abs/m1.bin"}, wantErr: true},
		{name: "missing target_path", spec: Spec{Name: "m1", SourceURL: "https://example.invalid/m1", SHA256: validDigest}, wantErr: true},
		{name: "relative target_path", spec: Spec{Name: "m1", SourceURL: "https://example.invalid/m1", SHA256: validDigest, TargetPath: "relative/m1.bin"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSpec(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSpec(%+v) error = %v, wantErr %v", tt.spec, err, tt.wantErr)
			}
		})
	}
}

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	data := `[{"name":"m1","source_url":"https://example.invalid/m1","sha256":"` + sha256Hex([]byte("x")) + `","target_path":"/tmp/m1.bin"}]`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	specs, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "m1" {
		t.Fatalf("specs = %+v, want one entry named m1", specs)
	}
}

func TestLoadManifest_MissingFile(t *testing.T) {
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Fatalf("LoadManifest on a missing file: want an error, got nil")
	}
}

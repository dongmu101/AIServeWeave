package httpapi

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNormalizeAllowedExtensionsFallsBackToTheDefaultWhenEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string // one extension expected to be present
	}{
		{name: "nil falls back to the default set", in: nil, want: ".png"},
		{name: "empty slice falls back to the default set", in: []string{}, want: ".png"},
		{name: "a configured set is used as-is, not merged with the default", in: []string{".zip"}, want: ".zip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeAllowedExtensions(tt.in)
			if _, ok := got[tt.want]; !ok {
				t.Errorf("normalizeAllowedExtensions(%v) = %v, want it to contain %q", tt.in, got, tt.want)
			}
		})
	}

	custom := normalizeAllowedExtensions([]string{".zip"})
	if _, ok := custom[".png"]; ok {
		t.Errorf("a configured allowlist must not silently include the default set's entries, got %v", custom)
	}
}

func TestNormalizeAllowedExtensionsLowercases(t *testing.T) {
	got := normalizeAllowedExtensions([]string{".PNG", ".Jpg"})
	for _, want := range []string{".png", ".jpg"} {
		if _, ok := got[want]; !ok {
			t.Errorf("normalizeAllowedExtensions([.PNG, .Jpg]) = %v, want it to contain %q", got, want)
		}
	}
}

func TestValidateUploadFilename(t *testing.T) {
	allowed := normalizeAllowedExtensions([]string{".png", ".mp4"})
	tests := []struct {
		name     string
		filename string
		wantErr  bool
	}{
		{name: "an allowed extension passes", filename: "photo.png", wantErr: false},
		{name: "matching is case-insensitive", filename: "PHOTO.PNG", wantErr: false},
		{name: "a disallowed extension is rejected", filename: "payload.exe", wantErr: true},
		{name: "no extension at all is rejected", filename: "noext", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUploadFilename(tt.filename, allowed)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateUploadFilename(%q) error = %v, wantErr %v", tt.filename, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, errUnsupportedUploadFormat) {
				t.Errorf("validateUploadFilename(%q) error = %v, want it to wrap errUnsupportedUploadFormat", tt.filename, err)
			}
		})
	}
}

// realPNGBytes is a real (if minimal) PNG file's signature — enough for
// net/http.DetectContentType to identify it as image/png without needing a
// fully valid image.
//
// realPNGBytes 是一个真实（哪怕只是最简）PNG 文件的签名——足以让
// net/http.DetectContentType 把它识别为 image/png，不需要一张完整合法的图片。
const realPNGBytes = "\x89PNG\r\n\x1a\n" + "rest of the file does not matter for sniffing"

func TestValidateUploadContent(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
		wantErr  bool
	}{
		{name: "real PNG bytes pass for a .png filename", filename: "photo.png", content: realPNGBytes, wantErr: false},
		{name: "plain text fails for a .png filename", filename: "photo.png", content: "not a png at all", wantErr: true},
		{name: "an extension outside the sniff table skips the check", filename: "model.safetensors", content: "anything at all", wantErr: false},
		{name: "empty content fails a sniffed extension", filename: "photo.png", content: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := validateUploadContent(strings.NewReader(tt.content), tt.filename)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateUploadContent(%q) error = %v, wantErr %v", tt.filename, err, tt.wantErr)
			}
			if err != nil {
				if !errors.Is(err, errUnsupportedUploadFormat) {
					t.Errorf("validateUploadContent(%q) error = %v, want it to wrap errUnsupportedUploadFormat", tt.filename, err)
				}
				return
			}
			got, readErr := io.ReadAll(out)
			if readErr != nil {
				t.Fatalf("reading the returned reader: %v", readErr)
			}
			if string(got) != tt.content {
				t.Errorf("validateUploadContent(%q) returned reader yielded %q, want the original content %q unchanged", tt.filename, got, tt.content)
			}
		})
	}
}

func TestValidateUploadContentReadsAtMostThePeekWindowForALargeFile(t *testing.T) {
	large := bytes.Repeat([]byte("a"), uploadSniffPeekBytes*4)
	copy(large, []byte("\x89PNG\r\n\x1a\n"))

	out, err := validateUploadContent(bytes.NewReader(large), "photo.png")
	if err != nil {
		t.Fatalf("validateUploadContent: %v", err)
	}
	got, err := io.ReadAll(out)
	if err != nil {
		t.Fatalf("reading the returned reader: %v", err)
	}
	if !bytes.Equal(got, large) {
		t.Errorf("returned reader yielded %d bytes not identical to the %d-byte input; a sniff must not drop or alter any of the file's bytes", len(got), len(large))
	}
}

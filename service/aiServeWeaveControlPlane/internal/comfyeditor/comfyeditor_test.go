package comfyeditor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewRejectsUnmanifestedAssets prevents mixed localized editor deployments.
// TestNewRejectsUnmanifestedAssets 防止部署混用未携带本地化清单的编辑器资源。
func TestNewRejectsUnmanifestedAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "extensions"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("New succeeded without a localization manifest, want deployment error")
	}
}

// TestBuiltLocalizedEditor validates an opt-in real build without a network dependency.
// TestBuiltLocalizedEditor 按需校验真实构建，默认测试不依赖网络或下载产物。
func TestBuiltLocalizedEditor(t *testing.T) {
	dir := os.Getenv("AISW_COMFY_TEST_DIST")
	if dir == "" {
		t.Skip("AISW_COMFY_TEST_DIST is unset")
	}
	server, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", Prefix+"index.html", nil))
	if got, want := w.Code, 200; got != want {
		t.Fatalf("index status = %d, want %d", got, want)
	}
}

func localizedFixture(t *testing.T) (string, localizationManifest) {
	t.Helper()
	dir := t.TempDir()
	index := []byte("<html>editor</html>")
	hash := sha256.Sum256(index)
	if err := os.WriteFile(filepath.Join(dir, "index.html"), index, 0600); err != nil {
		t.Fatal(err)
	}
	_, catalog := Catalog()
	return dir, localizationManifest{
		SchemaVersion: 1, FrontendVersion: catalog.FrontendVersion,
		SourceCommit: localizedSourceCommit, SourceArchiveSHA256: localizedSourceArchiveSHA256,
		ConfigurationSHA256: localizedConfigurationSHA256, Locales: localizedLocales,
		Files: map[string]string{"index.html": hex.EncodeToString(hash[:])},
	}
}

// TestLocalizedArtifactIntegrity rejects mixed, changed or mismatched editor assets.
// TestLocalizedArtifactIntegrity 拒绝混用、被改动或版本不匹配的编辑器资源。
func TestLocalizedArtifactIntegrity(t *testing.T) {
	for _, name := range []string{"valid", "version", "configuration", "missing language", "changed file", "extra file", "source map", "unsafe path", "oversized manifest"} {
		t.Run(name, func(t *testing.T) {
			dir, manifest := localizedFixture(t)
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "version":
				manifest.FrontendVersion = "99.0.0"
			case "configuration":
				manifest.ConfigurationSHA256 = strings.Repeat("0", 64)
			case "missing language":
				manifest.Locales = map[string]string{"en-US": "en"}
			case "changed file":
				write("index.html", []byte("changed"))
			case "extra file":
				write("extra.js", []byte("extra"))
			case "source map":
				manifest.Files["index.js.map"] = strings.Repeat("0", 64)
			case "unsafe path":
				manifest.Files["../index.html"] = strings.Repeat("0", 64)
			}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if name == "oversized manifest" {
				data = append(data, []byte(strings.Repeat(" ", 2<<20))...)
			}
			write("aisw-i18n-manifest.json", data)
			err = validateLocalization(os.DirFS(dir))
			if got, want := err == nil, name == "valid"; got != want {
				t.Fatalf("validation success = %v, want %v (error %v)", got, want, err)
			}
		})
	}
}

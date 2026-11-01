package agentdist

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "control-plane-secret"

// writeDist builds <dir>/<version>/ with two artifacts, support files and a
// checksum file that covers them all.
//
// writeDist 构建 <dir>/<version>/，含两个制品、支持文件和覆盖它们的校验和文件。
func writeDist(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"aiserveweave-agent_" + version + "_linux_amd64":       "linux-binary",
		"aiserveweave-agent_" + version + "_windows_amd64.exe": "windows-binary",
		"agent.sh":    "#!/bin/sh\n",
		"agent.ps1":   "# agent\n",
		"install.sh":  "PLATFORM=" + PlatformPlaceholder + "\n",
		"install.ps1": "$p = '" + PlatformPlaceholder + "'\n",
	}
	var sums strings.Builder
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		sums.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.txt"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestHandler(t *testing.T, version, controlPlane string, mutate func(*Config)) *http.ServeMux {
	t.Helper()
	cfg := Config{DistDir: writeDist(t, version), Version: version, PublicURL: "https://open7x.com/", ControlPlaneURL: controlPlane, Token: testToken,
		GitHubRepo: "dongmu101/AIServeWeave", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if mutate != nil {
		mutate(&cfg)
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)
	return mux
}

func get(mux http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestNewRejectsBadConfig checks half-configured and unsafe settings fail at
// startup rather than serving.
//
// TestNewRejectsBadConfig 检查配置不全或不安全时在启动时失败，而不是照常提供服务。
func TestNewRejectsBadConfig(t *testing.T) {
	good := Config{DistDir: "/d", Version: "v1.2.3", PublicURL: "https://open7x.com", ControlPlaneURL: "http://127.0.0.1:8090", Token: "t"}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no dist dir", func(c *Config) { c.DistDir = "" }, "together"},
		{"no public url", func(c *Config) { c.PublicURL = "" }, "together"},
		{"http public url", func(c *Config) { c.PublicURL = "http://open7x.com" }, "https origin"},
		{"public url with path", func(c *Config) { c.PublicURL = "https://open7x.com/agent" }, "https origin"},
		{"bad repo", func(c *Config) { c.GitHubRepo = "not a repo" }, "owner/name"},
		{"traversal version", func(c *Config) { c.Version = "../etc" }, "directory name"},
		{"no control plane", func(c *Config) { c.ControlPlaneURL = "" }, "control-plane-addr"},
		{"control plane without scheme", func(c *Config) { c.ControlPlaneURL = "127.0.0.1:8090" }, "control-plane-addr"},
		{"no token", func(c *Config) { c.Token = "" }, "token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.mutate(&cfg)
			if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New() error = %v, want one containing %q", err, tc.want)
			}
		})
	}
	if _, err := New(good); err != nil {
		t.Fatalf("New(good) error = %v, want nil even with a missing version directory", err)
	}
}

// TestManifest checks the manifest content for a release and a dev build, and
// the 503 for a missing directory.
//
// TestManifest 检查正式版与开发版的清单内容，以及目录缺失时的 503。
func TestManifest(t *testing.T) {
	var m manifest
	w := get(newTestHandler(t, "v1.2.3", "http://cp", nil), "/agent/v1/manifest")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil {
		t.Fatalf("manifest status=%d body=%s", w.Code, w.Body)
	}
	if m.Version != "v1.2.3" || m.PlatformURL != "https://open7x.com" || len(m.Assets) != 2 || m.GitHub == nil ||
		m.GitHub.BaseURL != "https://github.com/dongmu101/AIServeWeave/releases/download/v1.2.3" {
		t.Fatalf("manifest = %+v, want release v1.2.3 with 2 assets and a GitHub base URL", m)
	}
	want := sha256.Sum256([]byte("linux-binary"))
	if m.Assets[0].OS != "linux" || m.Assets[0].Arch != "amd64" || m.Assets[0].SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("first asset = %+v, want linux/amd64 with the file's SHA-256", m.Assets[0])
	}

	m = manifest{}
	w = get(newTestHandler(t, "a1b2c3d4e5f6", "http://cp", nil), "/agent/v1/manifest")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil || m.GitHub != nil {
		t.Fatalf("commit-version manifest status=%d github=%+v, want 200 without github", w.Code, m.GitHub)
	}

	mux := newTestHandler(t, "v1.2.3", "http://cp", func(c *Config) { c.DistDir = t.TempDir() })
	if w := get(mux, "/agent/v1/manifest"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("manifest with no directory status=%d, want 503", w.Code)
	}
}

// TestScriptsReplacePlatformURL checks the placeholder substitution.
//
// TestScriptsReplacePlatformURL 检查占位符替换。
func TestScriptsReplacePlatformURL(t *testing.T) {
	mux := newTestHandler(t, "v1.2.3", "http://cp", nil)
	for path, want := range map[string]string{"/agent/install.sh": "PLATFORM=https://open7x.com\n", "/agent/install.ps1": "$p = 'https://open7x.com'\n"} {
		w := get(mux, path)
		if w.Code != 200 || w.Body.String() != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s status=%d body=%q, want 200 %q", path, w.Code, w.Body, want)
		}
	}
}

// TestDownloadServesOnlyCurrentVersionFiles checks the allow-list, traversal
// and cross-version refusals.
//
// TestDownloadServesOnlyCurrentVersionFiles 检查允许列表、路径穿越与跨版本的拒绝。
func TestDownloadServesOnlyCurrentVersionFiles(t *testing.T) {
	mux := newTestHandler(t, "v1.2.3", "http://cp", nil)
	for _, tc := range []struct {
		name, path string
		status     int
	}{
		{"artifact", "/agent/download/v1.2.3/aiserveweave-agent_v1.2.3_linux_amd64", 200},
		{"windows artifact", "/agent/download/v1.2.3/aiserveweave-agent_v1.2.3_windows_amd64.exe", 200},
		{"agent.sh", "/agent/download/v1.2.3/agent.sh", 200},
		{"checksums", "/agent/download/v1.2.3/SHA256SUMS.txt", 200},
		{"other version", "/agent/download/v9.9.9/agent.sh", 404},
		{"install.sh is not a download", "/agent/download/v1.2.3/install.sh", 404},
		{"unlisted file", "/agent/download/v1.2.3/secret.txt", 404},
		{"encoded traversal", "/agent/download/v1.2.3/..%2F..%2Fetc%2Fpasswd", 404},
		// ServeMux cleans the path and redirects; no file content is served.
		// ServeMux 会清理路径并重定向；不会提供任何文件内容。
		{"dotdot", "/agent/download/v1.2.3/..", 307},
		{"version traversal", "/agent/download/..%2Fv1.2.3/agent.sh", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := get(mux, tc.path); w.Code != tc.status {
				t.Fatalf("GET %s status=%d, want %d", tc.path, w.Code, tc.status)
			}
		})
	}
	if w := get(mux, "/agent/download/v1.2.3/agent.sh"); w.Body.String() != "#!/bin/sh\n" {
		t.Errorf("agent.sh body = %q", w.Body)
	}
}

// controlPlaneStub records what the forward sent and answers canned replies.
//
// controlPlaneStub 记录转发发出的内容并返回预设应答。
type controlPlaneStub struct {
	status     int
	body       string
	retryAfter string
	gotAuth    string
	gotBody    string
	gotPath    string
}

func (c *controlPlaneStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.gotAuth, c.gotPath = r.Header.Get("Authorization"), r.URL.Path
	raw, _ := io.ReadAll(r.Body)
	c.gotBody = string(raw)
	if c.retryAfter != "" {
		w.Header().Set("Retry-After", c.retryAfter)
	}
	w.WriteHeader(c.status)
	_, _ = io.WriteString(w, c.body)
}

// TestRedeemForwarding checks status mapping, auth, body relay and that
// nothing sensitive reaches the log.
//
// TestRedeemForwarding 检查状态映射、鉴权、body 转述，以及敏感内容不会进入日志。
func TestRedeemForwarding(t *testing.T) {
	for _, tc := range []struct {
		name       string
		upstream   int
		body       string
		retryAfter string
		want       int
		wantBody   string
	}{
		{"success", 200, `{"node_id":"n1","bootstrap_token":"tok"}`, "", 200, `{"node_id":"n1","bootstrap_token":"tok"}`},
		{"invalid code", 404, `{"error":"invalid_code"}`, "", 404, "invalid_code"},
		{"throttled", 429, `{"error":"x"}`, "60", 429, "too many requests"},
		{"upstream error", 500, `internal secret detail`, "", 503, "service unavailable"},
		{"wrong shared secret", 401, `{"error":"unauthorized"}`, "", 503, "service unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &controlPlaneStub{status: tc.upstream, body: tc.body, retryAfter: tc.retryAfter}
			cp := httptest.NewServer(stub)
			defer cp.Close()
			var logs bytes.Buffer
			mux := newTestHandler(t, "v1.2.3", cp.URL, func(c *Config) { c.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/agent/v1/redeem", strings.NewReader(`{"code":"K7QF-9M2D-X4TB-H8RW-3NPC"}`)))
			if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.wantBody) || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d body=%s, want %d containing %q and no-store", w.Code, w.Body, tc.want, tc.wantBody)
			}
			if tc.retryAfter != "" && w.Header().Get("Retry-After") != tc.retryAfter {
				t.Errorf("Retry-After = %q, want %q", w.Header().Get("Retry-After"), tc.retryAfter)
			}
			if tc.upstream != 200 && tc.upstream != 404 && tc.upstream != 429 && strings.Contains(w.Body.String(), "secret detail") {
				t.Errorf("upstream detail leaked to the caller: %s", w.Body)
			}
			if stub.gotAuth != "Bearer "+testToken || stub.gotPath != "/internal/v1/agent-install/redeem" || stub.gotBody != `{"code":"K7QF-9M2D-X4TB-H8RW-3NPC"}` {
				t.Errorf("upstream saw auth=%q path=%q body=%q", stub.gotAuth, stub.gotPath, stub.gotBody)
			}
			for _, secret := range []string{"K7QF", "tok", testToken} {
				if strings.Contains(logs.String(), secret) {
					t.Errorf("log contains %q: %s", secret, logs.String())
				}
			}
		})
	}
}

// TestRedeemBodyLimitAndUnreachableControlPlane checks the 1 KiB bound and
// that an unreachable control plane is a plain 503 without error text.
//
// TestRedeemBodyLimitAndUnreachableControlPlane 检查 1 KiB 上限，以及控制面不可达时只回
// 一个不含错误文本的 503。
func TestRedeemBodyLimitAndUnreachableControlPlane(t *testing.T) {
	stub := &controlPlaneStub{status: 200, body: "{}"}
	cp := httptest.NewServer(stub)
	mux := newTestHandler(t, "v1.2.3", cp.URL, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/agent/v1/redeem", strings.NewReader(strings.Repeat("a", 2048))))
	if w.Code != http.StatusRequestEntityTooLarge || stub.gotPath != "" {
		t.Fatalf("oversized redeem status=%d upstream path=%q, want 413 and no upstream call", w.Code, stub.gotPath)
	}
	cp.Close()
	w = get(mux, "/agent/v1/ca")
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "connect") || strings.Contains(w.Body.String(), "127.0.0.1") {
		t.Fatalf("unreachable control plane status=%d body=%s, want an opaque 503", w.Code, w.Body)
	}
}

// TestRoutesRejectWrongMethods checks the routes accept only their methods.
//
// TestRoutesRejectWrongMethods 检查路由只接受各自的方法。
func TestRoutesRejectWrongMethods(t *testing.T) {
	mux := newTestHandler(t, "v1.2.3", "http://cp", nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/agent/v1/redeem"},
		{http.MethodPost, "/agent/v1/ca"},
		{http.MethodPost, "/agent/v1/manifest"},
		{http.MethodPut, "/agent/install.sh"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status=%d, want 405", tc.method, tc.path, w.Code)
		}
	}
}

// Package agentdist serves the public, key-less /agent/* entry that lets a
// node provider install an Agent with one command: the bootstrap scripts, a
// manifest of the Agent artifacts built from this Gateway's own commit, those
// artifacts, and a pair of forwards to the control plane's install-code
// redeem and CA endpoints.
//
// Everything served comes from one directory, <DistDir>/<Version>/, and only
// for the Gateway's own version, so a platform and the Agents it hands out
// never disagree about the tunnel protocol. No request or response body of
// the forwarded calls is ever logged: they carry install codes and tokens.
//
// agentdist 包提供公开、无需 Key 的 /agent/* 入口，让节点提供者用一行命令安装 Agent：
// 引导脚本、由本 Gateway 同一提交构建的 Agent 制品清单、制品本身，以及对控制面安装码兑换
// 与 CA 端点的两条转发。
//
// 所有内容都来自同一个目录 <DistDir>/<Version>/，且只服务 Gateway 自己的版本，使平台与它
// 发出的 Agent 对隧道协议不会有分歧。转发调用的请求体与响应体从不记录：它们携带安装码与令牌。
package agentdist

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

// PlatformPlaceholder is replaced with Config.PublicURL in the bootstrap
// scripts before they are served.
//
// PlatformPlaceholder 在引导脚本下发前被替换为 Config.PublicURL。
const PlatformPlaceholder = "__AISW_PLATFORM_URL__"

const (
	// maxRedeemBody bounds a redeem request body; a code is a few dozen bytes.
	//
	// maxRedeemBody 限制兑换请求体；一个码只有几十字节。
	maxRedeemBody = 1 << 10
	// maxForwardResponse bounds a control plane response relayed to a caller.
	//
	// maxForwardResponse 限制转发给调用方的控制面响应大小。
	maxForwardResponse = 64 << 10
	// forwardTimeout bounds one call to the control plane.
	//
	// forwardTimeout 限制一次对控制面的调用。
	forwardTimeout = 10 * time.Second
	// checksumsName lists the SHA-256 of every file in the version directory.
	//
	// checksumsName 列出版本目录中每个文件的 SHA-256。
	checksumsName = "SHA256SUMS.txt"
)

var (
	// releaseVersion is a published release; only these have GitHub assets.
	//
	// releaseVersion 是正式发布的版本；只有它们在 GitHub 上有制品。
	releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	// repoName is a GitHub owner/name pair.
	//
	// repoName 是 GitHub 的 owner/name。
	repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	// assetName parses aiserveweave-agent_<version>_<os>_<arch>[.exe].
	//
	// assetName 解析 aiserveweave-agent_<version>_<os>_<arch>[.exe]。
	assetName = regexp.MustCompile(`^aiserveweave-agent_(.+)_(linux|darwin|windows)_(amd64|arm64)(\.exe)?$`)
	// safeVersion keeps a version usable as a single path element.
	//
	// safeVersion 保证版本可作为单个路径元素。
	safeVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// staticFiles are served from the version directory besides the artifacts.
//
// staticFiles 是版本目录中除制品外还会提供的文件。
var staticFiles = map[string]bool{"agent.sh": true, "agent.ps1": true, checksumsName: true}

// Config configures a Handler.
//
// Config 配置一个 Handler。
type Config struct {
	// DistDir is the directory holding one subdirectory per version.
	//
	// DistDir 是按版本分子目录的制品目录。
	DistDir string
	// Version is this Gateway's version, the only one served.
	//
	// Version 是本 Gateway 的版本，也是唯一被服务的版本。
	Version string
	// PublicURL is the platform's HTTPS root, substituted into the scripts.
	//
	// PublicURL 是平台的 HTTPS 根地址，会被替换进脚本。
	PublicURL string
	// GitHubRepo is owner/name; empty means artifacts are platform-only.
	//
	// GitHubRepo 为 owner/name；为空表示制品只来自平台。
	GitHubRepo string
	// ControlPlaneURL and Token reach the control plane's internal API.
	//
	// ControlPlaneURL 与 Token 用于访问控制面的内部 API。
	ControlPlaneURL string
	Token           string
	// HTTPClient performs the forwards; nil builds one with a bounded timeout.
	//
	// HTTPClient 执行转发；为 nil 时构造一个带超时的客户端。
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Handler serves the /agent/* routes.
//
// Handler 提供 /agent/* 路由。
type Handler struct {
	cfg    Config
	client *http.Client
	logger *slog.Logger
	dir    string
}

// New validates cfg and returns a Handler. A missing version directory is not
// an error here: the manifest then answers 503, so a Gateway built without
// Agent artifacts still starts.
//
// New 校验 cfg 并返回 Handler。版本目录缺失在这里不是错误：此时 manifest 回答 503，因此
// 没有内置 Agent 制品的 Gateway 仍可启动。
func New(cfg Config) (*Handler, error) {
	if cfg.DistDir == "" || cfg.PublicURL == "" {
		return nil, errors.New("agentdist: -agent-dist-dir and -agent-public-url must be set together")
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("agentdist: -agent-public-url must be an https origin such as https://open7x.com")
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	if cfg.GitHubRepo != "" && !repoName.MatchString(cfg.GitHubRepo) {
		return nil, errors.New("agentdist: -agent-github-repo must look like owner/name")
	}
	if !safeVersion.MatchString(cfg.Version) {
		return nil, errors.New("agentdist: the Gateway version is not usable as a directory name")
	}
	cp, err := url.Parse(cfg.ControlPlaneURL)
	if cfg.ControlPlaneURL == "" || err != nil || (cp.Scheme != "http" && cp.Scheme != "https") || cp.Host == "" {
		return nil, errors.New("agentdist: the install endpoints need -control-plane-addr with a scheme, e.g. http://127.0.0.1:8090")
	}
	if cfg.Token == "" {
		return nil, errors.New("agentdist: the install endpoints need the control plane token")
	}
	cfg.ControlPlaneURL = strings.TrimRight(cfg.ControlPlaneURL, "/")
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: forwardTimeout}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &Handler{cfg: cfg, client: client, logger: logger, dir: filepath.Join(cfg.DistDir, cfg.Version)}
	if _, err := h.checksums(); err != nil {
		logger.Warn("agent artifacts are unavailable; /agent/v1/manifest will answer 503",
			slog.String("dir", h.dir), slog.String("error", runtimeRedact(err.Error(), cfg.Token)))
	}
	return h, nil
}

// Mount registers the routes on mux, outside any API key check.
//
// Mount 把路由注册到 mux 上，位于任何 API Key 校验之外。
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /agent/install.sh", h.script("install.sh"))
	mux.HandleFunc("GET /agent/install.ps1", h.script("install.ps1"))
	mux.HandleFunc("GET /agent/v1/manifest", h.manifest)
	mux.HandleFunc("GET /agent/download/{version}/{file}", h.download)
	mux.HandleFunc("POST /agent/v1/redeem", h.redeem)
	mux.HandleFunc("GET /agent/v1/ca", h.ca)
}

// checksums parses SHA256SUMS.txt into file name → hex digest.
//
// checksums 把 SHA256SUMS.txt 解析为 文件名 → 十六进制摘要。
func (h *Handler) checksums() (map[string]string, error) {
	f, err := os.Open(filepath.Join(h.dir, checksumsName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sums := map[string]string{}
	scanner := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		sums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}
	if len(sums) == 0 {
		return nil, errors.New("agentdist: " + checksumsName + " lists no files")
	}
	return sums, scanner.Err()
}

type asset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type github struct {
	Repo    string `json:"repo"`
	BaseURL string `json:"base_url"`
}

type manifest struct {
	Version     string  `json:"version"`
	PlatformURL string  `json:"platform_url"`
	GitHub      *github `json:"github,omitempty"`
	Assets      []asset `json:"assets"`
}

// assets lists the artifacts of this version that exist on disk and are
// covered by the checksum file.
//
// assets 列出本版本中确实存在于磁盘且被校验和文件覆盖的制品。
func (h *Handler) assets() ([]asset, error) {
	sums, err := h.checksums()
	if err != nil {
		return nil, err
	}
	var out []asset
	for name, sum := range sums {
		m := assetName.FindStringSubmatch(name)
		if m == nil || m[1] != h.cfg.Version || (m[2] == "windows") != (m[4] == ".exe") {
			continue
		}
		if info, err := os.Stat(filepath.Join(h.dir, name)); err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, asset{OS: m[2], Arch: m[3], Name: name, SHA256: sum})
	}
	if len(out) == 0 {
		return nil, errors.New("agentdist: no Agent artifacts for this version")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (h *Handler) manifest(w http.ResponseWriter, r *http.Request) {
	assets, err := h.assets()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "agent artifacts unavailable")
		return
	}
	m := manifest{Version: h.cfg.Version, PlatformURL: h.cfg.PublicURL, Assets: assets}
	if h.cfg.GitHubRepo != "" && releaseVersion.MatchString(h.cfg.Version) {
		m.GitHub = &github{Repo: h.cfg.GitHubRepo, BaseURL: "https://github.com/" + h.cfg.GitHubRepo + "/releases/download/" + h.cfg.Version}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, m)
}

// script serves a bootstrap script with the platform address filled in.
//
// script 提供填入平台地址的引导脚本。
func (h *Handler) script(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile(filepath.Join(h.dir, name))
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "agent artifacts unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, strings.ReplaceAll(string(raw), PlatformPlaceholder, h.cfg.PublicURL))
	}
}

// download serves one file of the current version's directory. The name must
// be an artifact of this version or one of the fixed support files; anything
// else, another version included, is 404.
//
// download 提供当前版本目录中的一个文件。文件名必须是本版本的制品或固定的支持文件；
// 其他一切（包括其他版本）都是 404。
func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	version, file := r.PathValue("version"), r.PathValue("file")
	if version != h.cfg.Version || file == "" || file != filepath.Base(file) || strings.ContainsAny(file, `/\`) {
		http.NotFound(w, r)
		return
	}
	if !staticFiles[file] {
		assets, err := h.assets()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		found := false
		for _, a := range assets {
			found = found || a.Name == file
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}
	f, err := os.Open(filepath.Join(h.dir, file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// redeem forwards a redeem request to the control plane.
//
// redeem 把兑换请求转发给控制面。
func (h *Handler) redeem(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRedeemBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	h.forward(w, r.Context(), http.MethodPost, "/internal/v1/agent-install/redeem", body)
}

// ca forwards a CA read to the control plane.
//
// ca 把 CA 读取转发给控制面。
func (h *Handler) ca(w http.ResponseWriter, r *http.Request) {
	h.forward(w, r.Context(), http.MethodGet, "/internal/v1/agent-install/ca", nil)
}

// forward calls the control plane and relays only a known set of outcomes:
// success verbatim, an unknown or used code as 404 invalid_code, throttling
// as 429, and every other failure as one opaque 503. Neither body is logged.
//
// forward 调用控制面并只转述已知的几种结果：成功时原样返回，未知或已用的码为 404
// invalid_code，限流为 429，其余一切失败统一为不透明的 503。两边的 body 都不记录。
func (h *Handler) forward(w http.ResponseWriter, ctx context.Context, method, path string, body []byte) {
	w.Header().Set("Cache-Control", "no-store")
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, h.cfg.ControlPlaneURL+path, reader)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.logger.Warn("control plane install call failed", slog.String("path", path), slog.String("error", runtimeRedact(err.Error(), h.cfg.Token)))
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		payload, err := io.ReadAll(io.LimitReader(resp.Body, maxForwardResponse+1))
		if err != nil || len(payload) > maxForwardResponse {
			writeError(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(payload)
	case http.StatusNotFound:
		writeError(w, http.StatusNotFound, "invalid_code")
	case http.StatusTooManyRequests:
		if v := resp.Header.Get("Retry-After"); v != "" && len(v) <= 6 {
			w.Header().Set("Retry-After", v)
		}
		writeError(w, http.StatusTooManyRequests, "too many requests; try again later")
	default:
		h.logger.Warn("control plane install call refused", slog.String("path", path), slog.Int("status", resp.StatusCode))
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// runtimeRedact strips the token from an error string before it is logged.
//
// runtimeRedact 在错误文本写入日志之前去掉令牌。
func runtimeRedact(s, token string) string {
	return runtime.Redact(s, token)
}

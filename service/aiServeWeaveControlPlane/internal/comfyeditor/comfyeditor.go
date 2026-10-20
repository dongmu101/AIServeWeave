// Package comfyeditor serves the official ComfyUI_frontend as an editor-only
// application, together with the stub backend it needs to boot, under one
// path prefix the Console embeds with wujie.
//
// The stub is deliberately not a ComfyUI server. It answers the twenty-odd
// endpoints the frontend calls at start-up with fixed, harmless values, serves
// node definitions only from the catalogue compiled into this binary, and
// refuses everything that would execute, upload or free anything: the editor
// can draw and export a graph, never run one. Settings and user data writes
// are accepted and discarded, so nothing an operator does inside the editor
// is persisted here — the template itself is saved through the ordinary
// publish endpoints, by the Console, after the editor exports it.
//
// /extensions lists the frontend's own core extensions plus this package's
// bridge and nothing else. That is the single most important restriction: the
// Console embeds the editor same-origin (see the Console AGENTS file), so any
// third-party extension script listed here would run with the operator's
// session. Custom-node JavaScript is therefore never served.
//
// comfyeditor 包把官方 ComfyUI_frontend 作为「只编辑」的应用提供出来，连同它启动
// 所需的桩后端，挂在 Console 用 wujie 嵌入的同一个路径前缀之下。
//
// 这个桩刻意不是一个 ComfyUI 服务器。它用固定且无害的值回答前端启动时调用的二十来个
// 端点，节点定义只来自编译进本二进制的目录，一切会执行、上传或释放东西的请求都拒绝：
// 编辑器能画图、能导出，绝不能运行。设置与用户数据的写入被接受后直接丢弃，所以运维在
// 编辑器里做的任何事都不在这里持久化——模板本身由 Console 在编辑器导出之后，经普通的
// 发布端点保存。
//
// /extensions 只列出前端自带的核心扩展加上本包的桥接扩展，别无其他。这是最重要的一条
// 限制：Console 以同源方式嵌入编辑器（见 Console 的 AGENTS 文件），这里列出的任何第三方
// 扩展脚本都会带着运维会话运行。因此自定义节点的 JavaScript 永远不会被提供。
package comfyeditor

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strings"
)

// Prefix is the path every editor request arrives under. / Prefix 是所有编辑器请求所在的路径前缀。
const Prefix = "/operator/v1/comfy-editor/"

// MaxWriteBytes bounds the settings and user-data writes the stub accepts and discards.
// MaxWriteBytes 限制桩后端接受并丢弃的设置与用户数据写入大小。
const MaxWriteBytes = 1 << 20

// bridgePath is where the bridge extension is served, as listed by /api/extensions.
// bridgePath 是桥接扩展的提供路径，与 /api/extensions 列出的一致。
const bridgePath = "extensions/aiserveweave/bridge.js"

//go:embed bridge.js
var bridgeJS []byte

// editorSettings is the fixed settings document every editor session starts from.
// The locale is pinned so node titles exported into _meta.title do not depend
// on which operator's browser published the template — otherwise the same
// graph would digest differently per browser language. Workflow persistence
// is off so one operator's last graph is never restored into another's
// session from browser storage.
//
// editorSettings 是每个编辑器会话起步时固定的设置文档。语言被锁定，使导出到
// _meta.title 的节点标题不取决于是哪位运维的浏览器发布了模板——否则同一张图会因
// 浏览器语言不同而得到不同摘要。工作流持久化被关闭，使一名运维最后编辑的图永远不会
// 经浏览器存储恢复到另一名运维的会话里。
var editorSettings = map[string]any{
	"Comfy.Locale":                           "zh",
	"Comfy.TutorialCompleted":                true,
	"Comfy.Workflow.Persist":                 false,
	"Comfy.Workflow.ShowMissingNodesWarning": true,
}

// Server answers every request under Prefix. Build one with New.
//
// Server 回答 Prefix 之下的每个请求。用 New 构造。
type Server struct {
	dist       fs.FS
	catalog    []byte
	meta       CatalogMeta
	extensions []byte
	settings   []byte
	stats      []byte
}

// New validates distDir as an unpacked ComfyUI_frontend release and indexes
// its core extensions once. It fails rather than serving a half-present
// editor: a configured but empty directory is a deployment error worth
// stopping on.
//
// New 校验 distDir 是一份已解压的 ComfyUI_frontend 发布包，并一次性索引其核心扩展。
// 它宁可失败也不提供一个残缺的编辑器：配置了却为空的目录是值得停下来的部署错误。
func New(distDir string) (_ *Server, err error) {
	if distDir == "" {
		return nil, errors.New("comfyeditor: dist directory is empty")
	}
	// os.Root, not os.DirFS: a symlink inside the release can never resolve
	// to a file outside it. The root stays open for the server's lifetime.
	// 用 os.Root 而不是 os.DirFS：发布包里的符号链接永远无法解析到包外的文件。
	// root 在服务器的整个生命周期内保持打开。
	root, err := os.OpenRoot(distDir)
	if err != nil {
		return nil, fmt.Errorf("comfyeditor: %s is not an unpacked ComfyUI_frontend release (no index.html)", distDir)
	}
	defer func() {
		if err != nil {
			_ = root.Close()
		}
	}()
	dist := root.FS()
	if _, err = fs.Stat(dist, "index.html"); err != nil {
		return nil, fmt.Errorf("comfyeditor: %s is not an unpacked ComfyUI_frontend release (no index.html)", distDir)
	}
	if err = validateLocalization(dist); err != nil {
		return nil, err
	}
	// Every .js under extensions/, as ComfyUI's own server lists them; the
	// release ships only core ones, so nothing third-party can appear here.
	// 与 ComfyUI 自己的服务端一样列出 extensions/ 下的每个 .js；发布包只带核心扩展，
	// 因此这里不可能出现第三方脚本。
	var exts []string
	err = fs.WalkDir(dist, "extensions", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".js") {
			exts = append(exts, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("comfyeditor: reading core extensions: %w", err)
	}
	sort.Strings(exts)
	list := make([]string, 0, len(exts)+1)
	for _, e := range exts {
		list = append(list, "/"+e)
	}
	list = append(list, "/"+bridgePath)

	catalog, meta := Catalog()
	s := &Server{dist: dist, catalog: catalog, meta: meta}
	if s.extensions, err = json.Marshal(list); err != nil {
		return nil, err
	}
	if _, err = fs.Stat(dist, bridgePath); err == nil {
		// The release must not shadow the embedded bridge with a file of its own.
		// 发布包不得用自己的文件遮蔽内嵌的桥接扩展。
		return nil, fmt.Errorf("comfyeditor: %s unexpectedly exists in the dist directory", bridgePath)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if s.settings, err = json.Marshal(editorSettings); err != nil {
		return nil, err
	}
	stats := map[string]any{
		"system": map[string]any{
			"os": "aiserveweave-editor", "ram_total": 0, "ram_free": 0,
			"comfyui_version": meta.ComfyUIVersion, "required_frontend_version": meta.FrontendVersion,
			"python_version": "", "pytorch_version": "", "embedded_python": false, "argv": []string{},
		},
		"devices": []any{},
	}
	if s.stats, err = json.Marshal(stats); err != nil {
		return nil, err
	}
	return s, nil
}

// ServeHTTP routes one editor request. The caller has already authorized it
// as a platform operator; this method only decides what the stub answers.
//
// ServeHTTP 分派一次编辑器请求。调用方已经把它作为平台运维授权过；本方法只决定桩
// 后端回答什么。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel, ok := strings.CutPrefix(r.URL.Path, Prefix)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	// Drain a bounded body first: an unread body desynchronises the next
	// request on a kept-alive connection.
	// 先读掉有界的请求体：没读完的请求体会让保持连接上的下一个请求错位。
	if r.Body != nil {
		if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, MaxWriteBytes)); err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
	}
	switch {
	case strings.HasPrefix(rel, "api/"):
		s.api(w, r, strings.TrimPrefix(rel, "api"))
	case strings.HasPrefix(rel, "internal/"):
		writeJSON(w, http.StatusOK, map[string]any{})
	case rel == "user.css":
		writeBody(w, http.StatusOK, "text/css; charset=utf-8", nil, "no-store")
	case strings.HasPrefix(rel, "templates/"):
		writeJSON(w, http.StatusOK, []any{})
	case rel == bridgePath:
		if !isRead(r.Method) {
			methodNotAllowed(w)
			return
		}
		writeBody(w, http.StatusOK, "text/javascript; charset=utf-8", bridgeJS, "no-store")
	default:
		if !isRead(r.Method) {
			methodNotAllowed(w)
			return
		}
		// Under wujie a few relative fetches resolve against the entry file
		// itself (.../index.html/assets/...); they mean the release root.
		// 在 wujie 下，少数相对请求会以入口文件本身为基准解析
		// （.../index.html/assets/...）；它们指的是发布包根目录。
		s.static(w, r, strings.TrimPrefix(rel, "index.html/"))
	}
}

func (s *Server) api(w http.ResponseWriter, r *http.Request, route string) {
	read := isRead(r.Method)
	switch {
	case route == "/object_info" && read:
		writeBody(w, http.StatusOK, "application/json", s.catalog, "no-store")
	case route == "/extensions" && read:
		writeBody(w, http.StatusOK, "application/json", s.extensions, "no-store")
	case route == "/settings" && read:
		writeBody(w, http.StatusOK, "application/json", s.settings, "no-store")
	case strings.HasPrefix(route, "/settings/") && read:
		writeJSON(w, http.StatusOK, editorSettings[strings.TrimPrefix(route, "/settings/")])
	case route == "/settings" || strings.HasPrefix(route, "/settings/"):
		writeJSON(w, http.StatusOK, map[string]any{}) // accepted, not persisted / 接受但不持久化
	case route == "/users" && read:
		writeJSON(w, http.StatusOK, map[string]any{"storage": "server", "migrated": true})
	case route == "/userdata" && read:
		writeJSON(w, http.StatusOK, []any{})
	case route == "/userdata/user.css" && read:
		writeBody(w, http.StatusOK, "text/css; charset=utf-8", nil, "no-store")
	case strings.HasPrefix(route, "/userdata/") && read:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case strings.HasPrefix(route, "/userdata/") && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		writeJSON(w, http.StatusOK, map[string]any{}) // accepted, not persisted / 接受但不持久化
	case route == "/system_stats" && read:
		writeBody(w, http.StatusOK, "application/json", s.stats, "no-store")
	case (route == "/embeddings" || route == "/experiment/models" || strings.HasPrefix(route, "/experiment/models/")) && read:
		writeJSON(w, http.StatusOK, []any{})
	case (route == "/global_subgraphs" || route == "/workflow_templates" || route == "/i18n" || route == "/features" || strings.HasPrefix(route, "/history")) && read:
		writeJSON(w, http.StatusOK, map[string]any{})
	case route == "/queue" && read:
		writeJSON(w, http.StatusOK, map[string]any{"queue_running": []any{}, "queue_pending": []any{}})
	case strings.HasPrefix(route, "/jobs") && read:
		writeJSON(w, http.StatusOK, map[string]any{"jobs": []any{}})
	case route == "/prompt" && read:
		writeJSON(w, http.StatusOK, map[string]any{"exec_info": map[string]any{"queue_remaining": 0}})
	case read:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	default:
		// /prompt, /queue, /interrupt, /free, /upload/*, /jobs/cancel, /users:
		// the editor never executes, uploads or changes anything server-side.
		// 编辑器从不执行、上传或在服务端改变任何东西。
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "the workflow editor does not execute or upload"})
	}
}

func (s *Server) static(w http.ResponseWriter, r *http.Request, rel string) {
	if rel == "" {
		rel = "index.html"
	}
	if !fs.ValidPath(rel) || strings.HasSuffix(rel, ".map") || hasDotSegment(rel) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	f, err := s.dist.Open(rel)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	content, seekable := f.(io.ReadSeeker)
	if err != nil || info.IsDir() || !seekable {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	// Hashed bundles never change under one name; everything else is
	// revalidated so a frontend upgrade takes effect on the next load.
	// 带哈希的打包文件同名永不变化；其余文件每次重新校验，前端升级后下次加载即生效。
	if strings.HasPrefix(rel, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// ServeContent, not ServeFileFS: the latter redirects .../index.html to
	// ./, which would move the editor off the URL the frontend derives its
	// API base from.
	// 用 ServeContent 而不用 ServeFileFS：后者会把 .../index.html 重定向到 ./，
	// 使编辑器离开前端据以推导 API 基址的那个 URL。
	http.ServeContent(w, r, info.Name(), info.ModTime(), content)
}

// hasDotSegment reports whether any segment of rel is hidden (.git, .env, ...).
// hasDotSegment 报告 rel 是否有任何一段是隐藏的（.git、.env 等）。
func hasDotSegment(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

func isRead(method string) bool { return method == http.MethodGet || method == http.MethodHead }

func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"error":"encode"}`)
	}
	writeBody(w, status, "application/json", body, "no-store")
}

func writeBody(w http.ResponseWriter, status int, contentType string, body []byte, cache string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

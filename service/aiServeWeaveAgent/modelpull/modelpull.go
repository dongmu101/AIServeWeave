// Package modelpull fetches model artifacts from operator-configured
// external sources onto local disk, verifying their checksum and resuming
// interrupted downloads. It is Agent-local and config-driven: a manifest and
// an allowlist come from local flags, not from the Gateway or control plane,
// so the Agent never trusts a remote party about where to fetch bytes from.
// It uses only the standard library (net/http, crypto/sha256), so it does
// not touch the Agent's dependency line (AGENTS.md: "Agent 与 Registry 的
// 直接依赖只有 gRPC、protobuf 与 coder/websocket").
//
// This is subtask 1 of STATUS.md's P2 "模型分发" item (see
// docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md): a
// generic checksum-verified downloader, not an Ollama-native puller (Ollama
// has its own manifest/blob store format this package does not understand;
// pulling into Ollama itself is left to a future subtask that shells out to
// `ollama pull`, the same way hostresources shells out to nvidia-smi).
//
// Puller (puller.go) is subtask 2 (see
// docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md):
// it lets a Gateway trigger a pull on demand, by name only — never a URL,
// which the manifest loaded here still supplies exclusively. Every download
// still runs through pullOne in this file; Puller only adds on-demand
// triggering, sequential queuing, and per-name progress on top of what
// RunManifest already does at startup.
//
// modelpull 把运维配置的外部来源的模型制品下载到本地磁盘，校验其校验和，并
// 支持续传中断的下载。它是纯 Agent 本地、配置驱动的：清单和白名单都来自本地
// flag，不来自 Gateway 或控制面，因此 Agent 永远不会信任远端指定"该从哪里取
// 字节"。它只用标准库（net/http、crypto/sha256），不触碰 Agent 的依赖线
// （AGENTS.md：「Agent 与 Registry 的直接依赖只有 gRPC、protobuf 与
// coder/websocket」）。
//
// 这是 STATUS.md P2「模型分发」条目的子任务一（见
// docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md）：一个
// 通用的、校验和驱动的下载器，不理解 Ollama 自己的 manifest/blob 存储格式
// （真正把文件交给 Ollama 使用留给未来子任务，经由 shell out 到
// `ollama pull`，与 hostresources shell out 到 nvidia-smi 同一先例）。
//
// Puller（puller.go）是子任务二（见
// docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md）：
// 让 Gateway 可以按需触发一次拉取，只能按名字——从不是 URL，这里加载的清单
// 仍然是 URL 的唯一来源。每一次下载仍然经由本文件的 pullOne 执行；Puller
// 只是在 RunManifest 启动时已有的行为之上，加了按需触发、顺序排队与逐名字
// 进度。
package modelpull

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sha256HexLen is the length of a hex-encoded SHA-256 digest.
//
// sha256HexLen 是十六进制编码 SHA-256 摘要的长度。
const sha256HexLen = 64

// errQuotaExceeded is returned by quotaWriter once the shared byte budget
// for this RunManifest call has been used up.
//
// errQuotaExceeded 在本次 RunManifest 调用共享的字节预算耗尽时由 quotaWriter
// 返回。
var errQuotaExceeded = errors.New("modelpull: quota exceeded")

// The sentinel errors below classify a pullOne failure without leaking its
// detail. Puller.runOne (puller.go) maps each one to a
// common/modelpullstatus.FailureReason via errors.Is before a Status ever
// crosses the tunnel; RunManifest's own Result.Failed keeps the full wrapped
// error, detail and all, for a caller that only ever logs it locally.
//
// pullOne 失败时用下面这些哨兵错误分类，不泄漏细节。Puller.runOne
// （puller.go）在一个 Status 跨隧道之前，用 errors.Is 把它们逐一映射到
// common/modelpullstatus.FailureReason；RunManifest 自己的 Result.Failed
// 仍然保留完整的被包裹错误（含细节），供只在本地打日志的调用方使用。
var (
	// errFetchFailed classifies a transport-level failure: DNS, dial, TLS,
	// a canceled context, or the HTTP response body failing mid-read. Go's
	// http.Client embeds the full request URL in a *url.Error's text, which
	// is exactly the detail that must never cross the tunnel (see the
	// package doc and modelpullstatus's doc for why).
	//
	// errFetchFailed 归类一次传输层失败：DNS、拨号、TLS、被取消的
	// context，或 HTTP 响应体读到一半失败。Go 的 http.Client 会把完整请求
	// URL 编进 *url.Error 的文本里，而这正是绝不能跨隧道的细节（原因见本
	// 包文档与 modelpullstatus 的文档）。
	errFetchFailed = errors.New("modelpull: fetch failed")
	// errUnexpectedStatus classifies a response whose status code is
	// neither 200 nor 206.
	//
	// errUnexpectedStatus 归类一个既不是 200 也不是 206 的响应状态码。
	errUnexpectedStatus = errors.New("modelpull: unexpected http status")
	// errChecksumMismatch classifies a completed download whose SHA256
	// does not match Spec.SHA256.
	//
	// errChecksumMismatch 归类一次已完成下载的 SHA256 与 Spec.SHA256 不符。
	errChecksumMismatch = errors.New("modelpull: checksum mismatch")
	// errStorageFailed classifies a local filesystem failure: stat,
	// mkdir, open, or rename.
	//
	// errStorageFailed 归类一次本地文件系统失败：stat、mkdir、open 或
	// rename。
	errStorageFailed = errors.New("modelpull: storage error")
)

// Spec describes one model artifact to fetch and verify.
//
// Spec 描述一个要获取并校验的模型制品。
type Spec struct {
	// Name identifies this entry in logs and in Result.
	//
	// Name 是这一条在日志和 Result 里的标识。
	Name string `json:"name"`

	// SourceURL is fetched with a plain HTTP(S) GET. It must match a prefix
	// in Config.Allowlist or the entry is rejected before any request.
	//
	// SourceURL 用一次普通的 HTTP(S) GET 获取。它必须命中 Config.Allowlist
	// 里的某个前缀，否则这一条在发起任何请求前就会被拒绝。
	SourceURL string `json:"source_url"`

	// SHA256 is the required, hex-encoded expected digest of the fetched
	// file. There is no way to skip verification.
	//
	// SHA256 是获取到的文件的期望摘要，十六进制编码，必填。没有跳过校验的
	// 途径。
	SHA256 string `json:"sha256"`

	// SizeBytes is the artifact's known size, used to pre-check the quota
	// before any request is made. Zero means unknown; the quota is still
	// enforced, but only once bytes start streaming.
	//
	// SizeBytes 是制品的已知大小，用于在发起请求前预检查配额。零值表示未知；
	// 配额仍会生效，但只能在字节开始流式写入之后才能强制。
	SizeBytes int64 `json:"size_bytes,omitempty"`

	// TargetPath is the absolute path the verified file is atomically
	// renamed to. An existing file at this path with a matching digest
	// causes the entry to be skipped without any network access.
	//
	// TargetPath 是校验通过后原子改名到的绝对路径。如果该路径已经有文件且
	// 摘要匹配，这一条会被直接跳过，不发起任何网络访问。
	TargetPath string `json:"target_path"`
}

// Config controls how RunManifest fetches and verifies every Spec in one
// call.
//
// Config 控制 RunManifest 在一次调用里如何获取并校验每一个 Spec。
type Config struct {
	// Allowlist holds URL prefixes a Spec.SourceURL may match. An empty
	// list rejects every pull — see
	// docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md 第
	// 三节 for why this defaults to deny rather than mirroring
	// tunnel.AllowedRuntimes's "empty allows everything".
	//
	// Allowlist 存放 Spec.SourceURL 可以匹配的 URL 前缀。空列表拒绝全部拉取
	// ——为什么这里的默认方向是拒绝而不是照搬 tunnel.AllowedRuntimes 的
	// 「空=放行」，见
	// docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md
	// 第三节。
	Allowlist []string

	// QuotaBytes bounds the total bytes this RunManifest call may write
	// across every Spec combined. A value <= 0 means unlimited. This is a
	// per-call budget, not a cumulative ledger across Agent restarts.
	//
	// QuotaBytes 限定这一次 RunManifest 调用在所有 Spec 上总共能写入的字节
	// 数。<=0 表示不限。这是单次调用的预算，不是跨 Agent 重启的累计账本。
	QuotaBytes int64

	// HTTPClient issues the GET requests. nil uses http.DefaultClient.
	//
	// HTTPClient 用于发起 GET 请求。为 nil 时使用 http.DefaultClient。
	HTTPClient *http.Client
}

// Result reports the outcome of one RunManifest call.
//
// Result 汇报一次 RunManifest 调用的结果。
type Result struct {
	// Skipped lists specs whose TargetPath already held a verified file.
	//
	// Skipped 是 TargetPath 已经有校验通过的文件、因而被跳过的 spec 列表。
	Skipped []string

	// Pulled lists specs fetched and verified during this call.
	//
	// Pulled 是本次调用里成功获取并校验通过的 spec 列表。
	Pulled []string

	// Failed maps a spec's Name to the error that stopped it. A failure
	// here never stops the remaining specs from being processed.
	//
	// Failed 把 spec 的 Name 映射到阻止它完成的错误。这里的失败不会影响其余
	// spec 继续被处理。
	Failed map[string]error
}

// LoadManifest reads a JSON-encoded []Spec from path.
//
// LoadManifest 从 path 读取 JSON 编码的 []Spec。
func LoadManifest(path string) ([]Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("modelpull: read manifest: %w", err)
	}
	var specs []Spec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("modelpull: parse manifest %s: %w", path, err)
	}
	return specs, nil
}

// RunManifest processes every Spec in order. An entry whose TargetPath
// already holds a file matching its SHA256 is skipped without touching the
// quota or the network. Otherwise the source must match cfg.Allowlist, and
// the download resumes into "<TargetPath>.part" via an HTTP Range request
// when a partial file already exists; on completion the whole file is
// checksummed and, only on a match, atomically renamed to TargetPath. A
// failing entry is recorded in Result.Failed and does not stop the rest.
//
// RunManifest 按顺序处理每个 Spec。TargetPath 已经存在且校验和匹配的条目直接
// 跳过，不占用配额、不发起网络请求。否则来源必须命中 cfg.Allowlist；如果
// "<TargetPath>.part" 已存在部分文件，用 HTTP Range 请求续传；完成后对整个
// 文件计算校验和，只有匹配才原子改名为 TargetPath。失败的条目记入
// Result.Failed，不影响其余条目继续处理。
func RunManifest(ctx context.Context, cfg Config, specs []Spec) Result {
	result := Result{Failed: make(map[string]error)}

	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	var budget *int64
	if cfg.QuotaBytes > 0 {
		b := cfg.QuotaBytes
		budget = &b
	}

	for _, spec := range specs {
		if err := validateSpec(spec); err != nil {
			result.Failed[specKey(spec)] = err
			continue
		}
		if alreadySatisfied(spec) {
			result.Skipped = append(result.Skipped, spec.Name)
			continue
		}
		if !sourceAllowed(spec.SourceURL, cfg.Allowlist) {
			result.Failed[spec.Name] = fmt.Errorf("modelpull: source not allowlisted for %q: %s", spec.Name, spec.SourceURL)
			continue
		}
		if err := pullOne(ctx, client, spec, budget, nil); err != nil {
			result.Failed[spec.Name] = err
			continue
		}
		result.Pulled = append(result.Pulled, spec.Name)
	}

	return result
}

// specKey returns a key to report a validation failure under even when
// Spec.Name itself is what's missing or empty.
//
// specKey 在 Spec.Name 本身缺失或为空时，仍能给校验失败提供一个可用的上报键。
func specKey(spec Spec) string {
	if spec.Name != "" {
		return spec.Name
	}
	return spec.SourceURL
}

// validateSpec rejects a Spec that is missing required fields before any
// network access or filesystem check is attempted.
//
// validateSpec 在发起任何网络访问或文件系统检查之前，拒绝缺少必填字段的
// Spec。
func validateSpec(spec Spec) error {
	if spec.Name == "" {
		return errors.New("modelpull: spec missing name")
	}
	if spec.SourceURL == "" {
		return fmt.Errorf("modelpull: spec %q missing source_url", spec.Name)
	}
	if len(spec.SHA256) != sha256HexLen || !isHex(spec.SHA256) {
		return fmt.Errorf("modelpull: spec %q has an invalid sha256 %q", spec.Name, spec.SHA256)
	}
	if spec.TargetPath == "" {
		return fmt.Errorf("modelpull: spec %q missing target_path", spec.Name)
	}
	if !filepath.IsAbs(spec.TargetPath) {
		return fmt.Errorf("modelpull: spec %q target_path must be absolute: %q", spec.Name, spec.TargetPath)
	}
	return nil
}

// isHex reports whether s contains only hex digits.
//
// isHex 判断 s 是否只包含十六进制数字。
func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// sourceAllowed reports whether url matches a non-empty prefix in
// allowlist. An empty allowlist rejects every url.
//
// sourceAllowed 判断 url 是否命中 allowlist 里的某个非空前缀。空 allowlist
// 拒绝所有 url。
func sourceAllowed(url string, allowlist []string) bool {
	for _, prefix := range allowlist {
		if prefix != "" && strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// alreadySatisfied reports whether spec.TargetPath already holds a file
// whose SHA256 matches spec.SHA256.
//
// alreadySatisfied 判断 spec.TargetPath 是否已经有一个 SHA256 与 spec.SHA256
// 匹配的文件。
func alreadySatisfied(spec Spec) bool {
	sum, err := sha256File(spec.TargetPath)
	if err != nil {
		return false
	}
	return strings.EqualFold(sum, spec.SHA256)
}

// sha256File returns the hex-encoded SHA-256 digest of the file at path.
//
// sha256File 返回 path 处文件的十六进制编码 SHA-256 摘要。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pullOne resumes or starts spec's download into "<TargetPath>.part",
// verifies the checksum on completion, and atomically renames it into place
// on success. budget, when non-nil, is the shared byte counter for this
// RunManifest call (or, from Puller, this worker session); pullOne
// decrements it as bytes are written and aborts once it would go negative,
// leaving the partial file for a later run. onProgress, when non-nil, is
// called after every chunk written with the file's total size so far
// (resumed bytes plus this call's own); RunManifest passes nil since it has
// no per-name status to update.
//
// Every error returned wraps one of the package's sentinel errors so a
// caller that needs to classify the failure (Puller.runOne) can use
// errors.Is instead of matching on message text — the message text itself
// may embed spec.SourceURL (a *url.Error does), which must never leave this
// package once a caller starts forwarding it across the tunnel.
//
// pullOne 续传或开始把 spec 下载到 "<TargetPath>.part"，完成后校验校验和，
// 成功则原子改名到位。budget 非 nil 时是本次 RunManifest 调用（或者，从
// Puller 调用时，是这次 worker session）共享的字节计数器；pullOne 随写入
// 递减它，一旦会变为负数就中止，把部分文件留给以后的运行。onProgress 非
// nil 时，每写入一个分块后都会被调用一次，参数是文件当前的总大小（续传
// 部分加上本次调用自己写入的部分）；RunManifest 传 nil，因为它没有需要更新
// 的逐名字状态。
//
// 返回的每一个错误都包裹了本包的某个哨兵错误，这样需要对失败分类的调用方
// （Puller.runOne）可以用 errors.Is 而不是匹配消息文本——消息文本本身可能
// 带有 spec.SourceURL（*url.Error 就会），一旦调用方开始把它转发过隧道，这
// 个细节绝不能离开本包。
func pullOne(ctx context.Context, client *http.Client, spec Spec, budget *int64, onProgress func(downloaded int64)) error {
	partPath := spec.TargetPath + ".part"

	var resumeFrom int64
	if fi, err := os.Stat(partPath); err == nil {
		resumeFrom = fi.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: stat partial file for %q: %v", errStorageFailed, spec.Name, err)
	}

	if budget != nil && spec.SizeBytes > 0 {
		remainingNeeded := spec.SizeBytes - resumeFrom
		if remainingNeeded > 0 && remainingNeeded > *budget {
			return fmt.Errorf("%w: before starting %q: need %d bytes, %d remaining", errQuotaExceeded, spec.Name, remainingNeeded, *budget)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.SourceURL, nil)
	if err != nil {
		return fmt.Errorf("%w: build request for %q: %v", errFetchFailed, spec.Name, err)
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(resumeFrom, 10)+"-")
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: fetch %q: %v", errFetchFailed, spec.Name, err)
	}
	defer resp.Body.Close()

	resumed := resumeFrom > 0 && resp.StatusCode == http.StatusPartialContent
	switch {
	case resumed:
	case resp.StatusCode == http.StatusOK:
		resumeFrom = 0
	default:
		return fmt.Errorf("%w: status %d fetching %q", errUnexpectedStatus, resp.StatusCode, spec.Name)
	}

	if err := os.MkdirAll(filepath.Dir(partPath), 0o755); err != nil {
		return fmt.Errorf("%w: create target directory for %q: %v", errStorageFailed, spec.Name, err)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resumed {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("%w: open partial file for %q: %v", errStorageFailed, spec.Name, err)
	}

	var writer io.Writer = f
	if budget != nil {
		writer = &quotaWriter{w: writer, remaining: budget}
	}
	if onProgress != nil {
		writer = &progressWriter{w: writer, base: resumeFrom, onProgress: onProgress}
	}

	_, copyErr := io.Copy(writer, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		if errors.Is(copyErr, errQuotaExceeded) {
			return copyErr
		}
		return fmt.Errorf("%w: download %q: %v", errFetchFailed, spec.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: flush partial file for %q: %v", errStorageFailed, spec.Name, closeErr)
	}

	sum, err := sha256File(partPath)
	if err != nil {
		return fmt.Errorf("%w: checksum %q: %v", errStorageFailed, spec.Name, err)
	}
	if !strings.EqualFold(sum, spec.SHA256) {
		os.Remove(partPath)
		return fmt.Errorf("%w: for %q: want %s, got %s", errChecksumMismatch, spec.Name, spec.SHA256, sum)
	}
	if err := os.Rename(partPath, spec.TargetPath); err != nil {
		return fmt.Errorf("%w: finalize %q: %v", errStorageFailed, spec.Name, err)
	}
	return nil
}

// progressWriter reports the file's cumulative size — base (bytes resumed
// from a previous run) plus everything written through it this call — after
// every successful Write. It wraps whatever writer sits beneath it (a
// quotaWriter when a budget applies, otherwise the file directly), so
// progress only ever reflects bytes actually accepted, never bytes a
// quotaWriter rejected.
//
// progressWriter 在每次成功 Write 之后上报文件的累计大小——base（从上一次
// 运行续传的字节数）加上本次调用通过它写入的一切。它包裹在下层 writer 之
// 外（有预算时是 quotaWriter，否则直接是文件），因此进度只反映实际被接受
// 的字节，从不包括被 quotaWriter 拒绝的字节。
type progressWriter struct {
	w          io.Writer
	base       int64
	written    int64
	onProgress func(downloaded int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if n > 0 {
		p.onProgress(p.base + p.written)
	}
	return n, err
}

// quotaWriter enforces a shared byte budget across every Spec processed by
// one RunManifest call. A Write that would exceed the remaining budget
// writes nothing and returns errQuotaExceeded, leaving whatever was already
// written on disk for a later, better-funded run.
//
// quotaWriter 在一次 RunManifest 调用处理的所有 Spec 之间强制一个共享字节
// 预算。一次会超出剩余预算的 Write 什么都不写，返回 errQuotaExceeded，把
// 已经写到磁盘上的部分留给以后预算更充足的一次运行。
type quotaWriter struct {
	w         io.Writer
	remaining *int64
}

func (q *quotaWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > *q.remaining {
		return 0, errQuotaExceeded
	}
	n, err := q.w.Write(p)
	*q.remaining -= int64(n)
	return n, err
}

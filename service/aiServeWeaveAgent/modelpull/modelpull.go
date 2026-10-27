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
// has its own manifest/blob store format this package does not understand).
//
// Subtask 3 (ollamapull.go, see
// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md)
// adds a second Spec.Kind, KindOllama, for exactly that case: it calls the
// target Ollama server's own POST /api/pull instead of reimplementing its
// storage format, the same restraint that made subtask 1's design doc
// originally point at shelling out to `ollama pull` (hostresources' os/exec
// precedent) — the design doc for subtask 3 reconsiders that and uses
// Ollama's HTTP API instead, still stdlib net/http, for structured progress
// without depending on the `ollama` CLI binary being on the Agent's PATH.
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
// 通用的、校验和驱动的下载器，不理解 Ollama 自己的 manifest/blob 存储格式。
//
// 子任务三（ollamapull.go，见
// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md）
// 为这种情况新增第二种 Spec.Kind——KindOllama：它调用目标 Ollama 服务器自己
// 的 POST /api/pull，而不是重新实现它的存储格式，与子任务一设计文档当初设
// 想的 shell out 到 `ollama pull`（hostresources 的 os/exec 先例）是同一种
// 克制——子任务三的设计文档重新权衡后改用 Ollama 的 HTTP API，同样只用标准
// 库 net/http，换来结构化进度，且不依赖 Agent 的 PATH 上要有 `ollama` 这个
// CLI 二进制。
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
	"sync"
	"sync/atomic"

	"AIServeWeave/service/aiServeWeaveAgent/hostresources"
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

// Kind selects how a Spec is fetched. See Spec.Kind.
//
// Kind 选择一个 Spec 用什么方式获取，见 Spec.Kind。
type Kind string

const (
	// KindHTTP is the zero value: the generic checksum-verified downloader
	// (subtask 1). SourceURL, SHA256 and TargetPath are required.
	//
	// KindHTTP 是零值：通用的校验和驱动下载器（子任务一）。SourceURL、
	// SHA256 与 TargetPath 均为必填。
	KindHTTP Kind = ""
	// KindOllama pulls spec.Name as a model tag from the Ollama server at
	// Config.OllamaBaseURL via its own POST /api/pull (subtask 3, see
	// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md).
	// SourceURL, SHA256 and TargetPath do not apply and must be left empty
	// — Ollama's own manifest/blob store decides where the bytes land.
	//
	// KindOllama 经由 Ollama 服务器（地址在 Config.OllamaBaseURL）自己的
	// POST /api/pull，把 spec.Name 当作模型 tag 拉取（子任务三，见
	// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md）。
	// SourceURL、SHA256、TargetPath 均不适用，必须留空——字节最终落在哪
	// 由 Ollama 自己的 manifest/blob 存储决定。
	KindOllama Kind = "ollama"
)

// Spec describes one model artifact to fetch and verify.
//
// Spec 描述一个要获取并校验的模型制品。
type Spec struct {
	// Kind selects the fetch mechanism. The zero value, KindHTTP, is
	// subtask 1's generic downloader.
	//
	// Kind 选择获取方式。零值 KindHTTP 是子任务一的通用下载器。
	Kind Kind `json:"kind,omitempty"`

	// Name identifies this entry in logs and in Result.
	//
	// Name 是这一条在日志和 Result 里的标识。
	Name string `json:"name"`

	// SourceURL is fetched with a plain HTTP(S) GET. It must match a prefix
	// in Config.Allowlist or the entry is rejected before any request.
	// KindHTTP only — a KindOllama spec must leave this empty.
	//
	// SourceURL 用一次普通的 HTTP(S) GET 获取。它必须命中 Config.Allowlist
	// 里的某个前缀，否则这一条在发起任何请求前就会被拒绝。仅用于
	// KindHTTP——KindOllama 的 spec 必须留空。
	SourceURL string `json:"source_url,omitempty"`

	// SHA256 is the required, hex-encoded expected digest of the fetched
	// file. There is no way to skip verification. KindHTTP only — a
	// KindOllama spec must leave this empty; Ollama verifies its own blobs.
	//
	// SHA256 是获取到的文件的期望摘要，十六进制编码，必填。没有跳过校验的
	// 途径。仅用于 KindHTTP——KindOllama 的 spec 必须留空，Ollama 自己校验
	// 它的 blob。
	SHA256 string `json:"sha256,omitempty"`

	// SizeBytes is the artifact's known size, used to pre-check the quota
	// before any request is made. Zero means unknown; the quota is still
	// enforced, but only once bytes start streaming.
	//
	// SizeBytes 是制品的已知大小，用于在发起请求前预检查配额。零值表示未知；
	// 配额仍会生效，但只能在字节开始流式写入之后才能强制。
	SizeBytes int64 `json:"size_bytes,omitempty"`

	// TargetPath is the absolute path the verified file is atomically
	// renamed to. An existing file at this path with a matching digest
	// causes the entry to be skipped without any network access. KindHTTP
	// only — a KindOllama spec must leave this empty; Ollama decides where
	// its own blobs live.
	//
	// TargetPath 是校验通过后原子改名到的绝对路径。如果该路径已经有文件且
	// 摘要匹配，这一条会被直接跳过，不发起任何网络访问。仅用于
	// KindHTTP——KindOllama 的 spec 必须留空，它的 blob 存在哪由 Ollama 自
	// 己决定。
	TargetPath string `json:"target_path,omitempty"`
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
	// across every KindHTTP Spec combined. A value <= 0 means unlimited.
	// This is a per-call budget, not a cumulative ledger across Agent
	// restarts. It does not apply to KindOllama specs — see OllamaBaseURL
	// and docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md
	// 第五节 for why.
	//
	// QuotaBytes 限定这一次 RunManifest 调用在所有 KindHTTP Spec 上总共能写
	// 入的字节数。<=0 表示不限。这是单次调用的预算，不是跨 Agent 重启的累
	// 计账本。它不适用于 KindOllama 的 spec——原因见 OllamaBaseURL 与
	// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md
	// 第五节。
	QuotaBytes int64

	// HTTPClient issues every request, KindHTTP's GET and KindOllama's
	// POST /api/pull alike. nil uses http.DefaultClient.
	//
	// HTTPClient 用于发起每一次请求，KindHTTP 的 GET 与 KindOllama 的
	// POST /api/pull 皆是。为 nil 时使用 http.DefaultClient。
	HTTPClient *http.Client

	// OllamaBaseURL is the target for KindOllama specs' POST /api/pull
	// calls, e.g. http://127.0.0.1:11434. Empty rejects every KindOllama
	// spec with ReasonOllamaUnconfigured before any request — the manifest
	// may list KindOllama entries even when this Agent has no Ollama
	// runtime configured; they simply never succeed until it does.
	//
	// OllamaBaseURL 是 KindOllama spec 的 POST /api/pull 请求目标，例如
	// http://127.0.0.1:11434。为空时，任何 KindOllama 的 spec 在发起请求前
	// 就会被拒绝为 ReasonOllamaUnconfigured——清单里即使列了 KindOllama 条
	// 目，只要这个 Agent 没配置 Ollama 运行时，它们就永远不会成功，直到配
	// 置上为止。
	OllamaBaseURL string

	// MaxConcurrency bounds how many Specs RunManifest (or one Puller worker
	// session) fetches at once. <= 1 (including the zero value) keeps the
	// original sequential behavior subtask 1/2/3's tests already depend on.
	// A single Spec's own download is never split further — concurrency
	// only happens across different Specs. See
	// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md
	// 第 2.1 节.
	//
	// MaxConcurrency 限定 RunManifest（或一次 Puller worker session）同一时
	// 间获取多少个 Spec。<= 1（含零值）保持子任务一/二/三测试已经依赖的原
	// 始顺序行为。单个 Spec 自己的下载从不被进一步拆分——并发只发生在不同
	// Spec 之间。见
	// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md
	// 第 2.1 节。
	MaxConcurrency int

	// Ledger, when non-nil, enforces LedgerQuotaBytes as a total that
	// persists across Agent restarts, unlike QuotaBytes which resets every
	// call. See Ledger's doc and
	// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md
	// 第 2.2 节.
	//
	// Ledger 非 nil 时，把 LedgerQuotaBytes 作为一个跨 Agent 重启持久化的总
	// 量强制执行，这与每次调用都重新计满的 QuotaBytes 不同。见 Ledger 的文
	// 档与
	// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md
	// 第 2.2 节。
	Ledger *Ledger

	// LedgerQuotaBytes bounds Ledger's cumulative total. <= 0 means the
	// ledger itself imposes no limit (only meaningful together with a
	// non-nil Ledger).
	//
	// LedgerQuotaBytes 限定 Ledger 的累计总量。<= 0 表示账本本身不设限（只
	// 在 Ledger 非 nil 时才有意义）。
	LedgerQuotaBytes int64

	// DiskFreeMarginBytes, when > 0, aborts a KindHTTP download once the
	// target filesystem's free space (hostresources.DiskFreeBytes) falls
	// below it — a secondary defense beyond QuotaBytes/Ledger, since both
	// are policy ceilings, not a measurement of real remaining disk space.
	// Does not apply to KindOllama.
	//
	// DiskFreeMarginBytes > 0 时，一旦目标文件系统的剩余空间
	// （hostresources.DiskFreeBytes）低于它，就中止一次 KindHTTP 下载——这
	// 是 QuotaBytes/Ledger 之外的二次防线，因为两者都只是策略上限，不是对
	// 真实剩余磁盘空间的度量。不适用于 KindOllama。
	DiskFreeMarginBytes int64
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

	var budget *atomic.Int64
	if cfg.QuotaBytes > 0 {
		budget = new(atomic.Int64)
		budget.Store(cfg.QuotaBytes)
	}

	maxConcurrency := cfg.MaxConcurrency
	if maxConcurrency < 1 {
		maxConcurrency = 1
	}

	var mu sync.Mutex
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for _, spec := range specs {
		if err := validateSpec(spec); err != nil {
			mu.Lock()
			result.Failed[specKey(spec)] = err
			mu.Unlock()
			continue
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(spec Spec) {
			defer wg.Done()
			defer func() { <-sem }()

			var pullErr error
			skipped := false
			switch {
			case spec.Kind == KindOllama:
				// Ollama's own pull is already idempotent (an already-present
				// model answers quickly with "success"), so there is no
				// separate "already satisfied" skip here the way KindHTTP has
				// one, and no allowlist or quota check — neither concept
				// applies to a spec with no SourceURL (see Config's doc and
				// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md).
				pullErr = pullOllama(ctx, client, cfg.OllamaBaseURL, spec, nil)
			case alreadySatisfied(spec):
				skipped = true
			case !sourceAllowed(spec.SourceURL, cfg.Allowlist):
				pullErr = fmt.Errorf("modelpull: source not allowlisted for %q: %s", spec.Name, spec.SourceURL)
			default:
				pullErr = pullOne(ctx, client, spec, pullOpts{
					Budget:              budget,
					Ledger:              cfg.Ledger,
					LedgerQuotaBytes:    cfg.LedgerQuotaBytes,
					DiskFreeMarginBytes: cfg.DiskFreeMarginBytes,
				})
			}

			mu.Lock()
			defer mu.Unlock()
			switch {
			case skipped:
				result.Skipped = append(result.Skipped, spec.Name)
			case pullErr != nil:
				result.Failed[spec.Name] = pullErr
			default:
				result.Pulled = append(result.Pulled, spec.Name)
			}
		}(spec)
	}
	wg.Wait()

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

// validateSpec rejects a Spec that is missing required fields, or that sets
// a field its Kind does not use, before any network access or filesystem
// check is attempted.
//
// validateSpec 在发起任何网络访问或文件系统检查之前，拒绝缺少必填字段、或
// 设置了其 Kind 不适用字段的 Spec。
func validateSpec(spec Spec) error {
	if spec.Name == "" {
		return errors.New("modelpull: spec missing name")
	}
	if spec.Kind == KindOllama {
		if spec.SourceURL != "" || spec.SHA256 != "" || spec.TargetPath != "" {
			return fmt.Errorf("modelpull: spec %q is kind ollama and must not set source_url/sha256/target_path", spec.Name)
		}
		return nil
	}
	if spec.Kind != KindHTTP {
		return fmt.Errorf("modelpull: spec %q has unknown kind %q", spec.Name, spec.Kind)
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

// pullOpts bundles pullOne's cross-cutting, optional concerns: the
// session/call-scoped byte budget, the cross-restart ledger (subtask 4), the
// disk-space secondary defense (subtask 4), and a per-chunk progress
// callback. Grouping them keeps pullOne's signature from growing a parameter
// per subtask.
//
// pullOpts 打包 pullOne 的横切、可选关注点：session/调用范围的字节预算、
// 跨重启账本（子任务四）、磁盘空间二次防线（子任务四），以及逐块进度回
// 调。打包在一起是为了不让 pullOne 的签名随每个子任务多长一个参数。
type pullOpts struct {
	// Budget, when non-nil, is the shared byte counter for this RunManifest
	// call (or, from Puller, this worker session); pullOne decrements it as
	// bytes are written and aborts once it would go negative, leaving the
	// partial file for a later run. atomic.Int64 rather than a plain int64
	// because MaxConcurrency > 1 means multiple Specs share it concurrently.
	//
	// Budget 非 nil 时是本次 RunManifest 调用（或者，从 Puller 调用时，是
	// 这次 worker session）共享的字节计数器；pullOne 随写入递减它，一旦会
	// 变为负数就中止，把部分文件留给以后的运行。用 atomic.Int64 而不是普
	// 通 int64，因为 MaxConcurrency > 1 时多个 Spec 会并发共享它。
	Budget *atomic.Int64
	// Ledger, when non-nil, is consulted (and updated) before every chunk
	// write, on top of Budget — see Config.Ledger's doc.
	//
	// Ledger 非 nil 时，在每次分块写入之前都会被查询（并更新），叠加在
	// Budget 之上——见 Config.Ledger 的文档。
	Ledger              *Ledger
	LedgerQuotaBytes    int64
	DiskFreeMarginBytes int64
	// OnProgress, when non-nil, is called after every chunk written with the
	// file's total size so far (resumed bytes plus this call's own).
	// RunManifest leaves it nil since it has no per-name status to update.
	//
	// OnProgress 非 nil 时，每写入一个分块后都会被调用一次，参数是文件当
	// 前的总大小（续传部分加上本次调用自己写入的部分）。RunManifest 留空，
	// 因为它没有需要更新的逐名字状态。
	OnProgress func(downloaded int64)
}

// pullOne resumes or starts spec's download into "<TargetPath>.part",
// verifies the checksum on completion, and atomically renames it into place
// on success.
//
// Every error returned wraps one of the package's sentinel errors so a
// caller that needs to classify the failure (Puller.runOne) can use
// errors.Is instead of matching on message text — the message text itself
// may embed spec.SourceURL (a *url.Error does), which must never leave this
// package once a caller starts forwarding it across the tunnel.
//
// pullOne 续传或开始把 spec 下载到 "<TargetPath>.part"，完成后校验校验和，
// 成功则原子改名到位。
//
// 返回的每一个错误都包裹了本包的某个哨兵错误，这样需要对失败分类的调用方
// （Puller.runOne）可以用 errors.Is 而不是匹配消息文本——消息文本本身可能
// 带有 spec.SourceURL（*url.Error 就会），一旦调用方开始把它转发过隧道，这
// 个细节绝不能离开本包。
func pullOne(ctx context.Context, client *http.Client, spec Spec, opts pullOpts) error {
	partPath := spec.TargetPath + ".part"
	budget := opts.Budget

	var resumeFrom int64
	if fi, err := os.Stat(partPath); err == nil {
		resumeFrom = fi.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: stat partial file for %q: %v", errStorageFailed, spec.Name, err)
	}

	if budget != nil && spec.SizeBytes > 0 {
		remainingNeeded := spec.SizeBytes - resumeFrom
		if remainingNeeded > 0 && remainingNeeded > budget.Load() {
			return fmt.Errorf("%w: before starting %q: need %d bytes, %d remaining", errQuotaExceeded, spec.Name, remainingNeeded, budget.Load())
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
	if opts.Ledger != nil {
		writer = &ledgerWriter{w: writer, ledger: opts.Ledger, quotaBytes: opts.LedgerQuotaBytes}
	}
	if opts.DiskFreeMarginBytes > 0 {
		writer = &diskCheckWriter{w: writer, dir: filepath.Dir(partPath), marginBytes: opts.DiskFreeMarginBytes}
	}
	if opts.OnProgress != nil {
		writer = &progressWriter{w: writer, base: resumeFrom, onProgress: opts.OnProgress}
	}

	_, copyErr := io.Copy(writer, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		if errors.Is(copyErr, errQuotaExceeded) || errors.Is(copyErr, errLedgerQuotaExceeded) || errors.Is(copyErr, errDiskSpaceLow) {
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
// one RunManifest call (or Puller worker session). A Write that would exceed
// the remaining budget writes nothing and returns errQuotaExceeded, leaving
// whatever was already written on disk for a later, better-funded run.
// remaining is an atomic.Int64, not a plain int64, because
// Config.MaxConcurrency > 1 (subtask 4) lets multiple Specs share it from
// different goroutines at once; the check-then-decrement below is a CAS loop
// so two concurrent downloads can never both pass the check and jointly
// overspend the remaining budget.
//
// quotaWriter 在一次 RunManifest 调用（或 Puller worker session）处理的所
// 有 Spec 之间强制一个共享字节预算。一次会超出剩余预算的 Write 什么都不
// 写，返回 errQuotaExceeded，把已经写到磁盘上的部分留给以后预算更充足的
// 一次运行。remaining 是 atomic.Int64 而不是普通 int64，因为
// Config.MaxConcurrency > 1（子任务四）允许多个 Spec 从不同 goroutine 同时
// 共享它；下面的"先检查再扣减"用 CAS 循环实现，这样两次并发下载不会都通
// 过检查、合计透支剩余预算。
type quotaWriter struct {
	w         io.Writer
	remaining *atomic.Int64
}

func (q *quotaWriter) Write(p []byte) (int, error) {
	n := int64(len(p))
	for {
		cur := q.remaining.Load()
		if n > cur {
			return 0, errQuotaExceeded
		}
		if q.remaining.CompareAndSwap(cur, cur-n) {
			break
		}
	}
	written, err := q.w.Write(p)
	if int64(written) != n {
		q.remaining.Add(n - int64(written))
	}
	return written, err
}

// ledgerWriter reserves every chunk's bytes against a cross-restart Ledger
// (subtask 4) before passing the write through, and returns the unused
// portion of a short write back to the ledger — see Ledger.Reserve's doc.
//
// ledgerWriter 在每个分块的写入通过之前，先向跨重启的 Ledger（子任务四）预
// 留其字节数，并把一次没写完的差额还回账本——见 Ledger.Reserve 的文档。
type ledgerWriter struct {
	w          io.Writer
	ledger     *Ledger
	quotaBytes int64
}

func (l *ledgerWriter) Write(p []byte) (int, error) {
	rollback, err := l.ledger.Reserve(int64(len(p)), l.quotaBytes)
	if err != nil {
		return 0, err
	}
	n, werr := l.w.Write(p)
	if int64(n) < int64(len(p)) {
		rollback(int64(len(p) - n))
	}
	return n, werr
}

// diskCheckWriter is subtask 4's secondary defense beyond any byte quota: it
// re-checks the target filesystem's free space before every chunk write and
// aborts once it drops below marginBytes. A platform where
// hostresources.DiskFreeBytes is unsupported never blocks a write — the
// check is best-effort, not load-bearing (see hostresources' doc).
//
// diskCheckWriter 是子任务四在任何字节配额之外的二次防线：在每次分块写入
// 之前重新检查目标文件系统的剩余空间，一旦低于 marginBytes 就中止。在
// hostresources.DiskFreeBytes 不支持的平台上，这项检查从不阻塞写入——它是
// 尽力而为的，不是强依赖（见 hostresources 的文档）。
type diskCheckWriter struct {
	w           io.Writer
	dir         string
	marginBytes int64
}

func (d *diskCheckWriter) Write(p []byte) (int, error) {
	if free, err := hostresources.DiskFreeBytes(d.dir); err == nil && free < d.marginBytes {
		return 0, errDiskSpaceLow
	}
	return d.w.Write(p)
}

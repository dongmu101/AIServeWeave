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
// generic checksum-verified downloader, not a Gateway-triggered distribution
// system and not an Ollama-native puller (Ollama has its own manifest/blob
// store format this package does not understand; pulling into Ollama itself
// is left to a future subtask that shells out to `ollama pull`, the same way
// hostresources shells out to nvidia-smi).
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
// 通用的、校验和驱动的下载器，不是 Gateway 触发的分发系统，也不理解 Ollama
// 自己的 manifest/blob 存储格式（真正把文件交给 Ollama 使用留给未来子任务，
// 经由 shell out 到 `ollama pull`，与 hostresources shell out 到 nvidia-smi
// 同一先例）。
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
		if err := pullOne(ctx, client, spec, budget); err != nil {
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
// RunManifest call; pullOne decrements it as bytes are written and aborts
// once it would go negative, leaving the partial file for a later run.
//
// pullOne 续传或开始把 spec 下载到 "<TargetPath>.part"，完成后校验校验和，
// 成功则原子改名到位。budget 非 nil 时是本次 RunManifest 调用共享的字节计数
// 器；pullOne 随写入递减它，一旦会变为负数就中止，把部分文件留给以后的运行。
func pullOne(ctx context.Context, client *http.Client, spec Spec, budget *int64) error {
	partPath := spec.TargetPath + ".part"

	var resumeFrom int64
	if fi, err := os.Stat(partPath); err == nil {
		resumeFrom = fi.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("modelpull: stat partial file for %q: %w", spec.Name, err)
	}

	if budget != nil && spec.SizeBytes > 0 {
		remainingNeeded := spec.SizeBytes - resumeFrom
		if remainingNeeded > 0 && remainingNeeded > *budget {
			return fmt.Errorf("modelpull: quota exceeded before starting %q: need %d bytes, %d remaining", spec.Name, remainingNeeded, *budget)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.SourceURL, nil)
	if err != nil {
		return fmt.Errorf("modelpull: build request for %q: %w", spec.Name, err)
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(resumeFrom, 10)+"-")
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("modelpull: fetch %q: %w", spec.Name, err)
	}
	defer resp.Body.Close()

	resumed := resumeFrom > 0 && resp.StatusCode == http.StatusPartialContent
	switch {
	case resumed:
	case resp.StatusCode == http.StatusOK:
		resumeFrom = 0
	default:
		return fmt.Errorf("modelpull: unexpected status %d fetching %q", resp.StatusCode, spec.Name)
	}

	if err := os.MkdirAll(filepath.Dir(partPath), 0o755); err != nil {
		return fmt.Errorf("modelpull: create target directory for %q: %w", spec.Name, err)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if resumed {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(partPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("modelpull: open partial file for %q: %w", spec.Name, err)
	}

	var writer io.Writer = f
	if budget != nil {
		writer = &quotaWriter{w: f, remaining: budget}
	}

	_, copyErr := io.Copy(writer, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("modelpull: download %q: %w", spec.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("modelpull: flush partial file for %q: %w", spec.Name, closeErr)
	}

	sum, err := sha256File(partPath)
	if err != nil {
		return fmt.Errorf("modelpull: checksum %q: %w", spec.Name, err)
	}
	if !strings.EqualFold(sum, spec.SHA256) {
		os.Remove(partPath)
		return fmt.Errorf("modelpull: checksum mismatch for %q: want %s, got %s", spec.Name, spec.SHA256, sum)
	}
	if err := os.Rename(partPath, spec.TargetPath); err != nil {
		return fmt.Errorf("modelpull: finalize %q: %w", spec.Name, err)
	}
	return nil
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

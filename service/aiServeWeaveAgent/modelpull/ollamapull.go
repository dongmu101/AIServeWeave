package modelpull

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// errOllamaUnconfigured classifies a KindOllama spec attempted with no
// Config.OllamaBaseURL set.
//
// errOllamaUnconfigured 归类一次 Config.OllamaBaseURL 未设置时触发的
// KindOllama spec。
var errOllamaUnconfigured = errors.New("modelpull: ollama base url not configured")

// errOllamaPullFailed classifies a POST /api/pull whose Ollama server
// itself reported an error partway through the stream (e.g. an unknown
// model tag) — distinct from errFetchFailed, which means the request never
// reached the server or the connection broke before a final status arrived.
//
// errOllamaPullFailed 归类一次 POST /api/pull 里 Ollama 服务器自己在流中途
// 报告了错误（例如未知的模型 tag）——与 errFetchFailed 不同，后者表示请求根
// 本没有到达服务器，或连接在最终状态之前中断。
var errOllamaPullFailed = errors.New("modelpull: ollama reported a pull failure")

// ollamaPullChunk is one line of Ollama's POST /api/pull NDJSON response
// stream. Total/Completed describe whichever layer (blob) is currently
// being fetched, not a cumulative total across the whole model — the API
// reports no such sum, so pullOllama's progress is only ever an
// approximation of overall completion.
//
// ollamaPullChunk 是 Ollama POST /api/pull NDJSON 响应流里的一行。
// Total/Completed 描述的是当前正在获取的那一个 layer（blob），不是整个模型
// 的累计总量——这个 API 不上报这样的总和，因此 pullOllama 的进度始终只是对
// 整体完成度的近似。
type ollamaPullChunk struct {
	Status    string `json:"status"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// pullOllama asks the Ollama server at baseURL to pull spec.Name via its
// own POST /api/pull, which understands Ollama's manifest/blob store format
// (see the package doc for why this package does not reimplement it
// instead). Ollama's own pull is already idempotent — a model it already
// has answers quickly with a "success" status — so there is no separate
// "already satisfied" check before calling this, unlike pullOne.
//
// There is no quota parameter, unlike pullOne: the Ollama server, not this
// process, receives and writes the bytes, so canceling the request here
// does not stop it from continuing to write to its own blob store (see
// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md
// 第五节). onProgress, when non-nil, is called after every stream line that
// carries a completed/total pair, with values scoped to whichever layer
// Ollama is currently fetching.
//
// Every error returned wraps one of this package's sentinel errors, the
// same errors.Is-classifiable contract pullOne's errors follow, so
// Puller.runOllama can map a failure to a modelpullstatus.FailureReason
// without inspecting message text.
//
// pullOllama 请求 baseURL 处的 Ollama 服务器经由它自己的 POST /api/pull 拉取
// spec.Name，该接口理解 Ollama 自己的 manifest/blob 存储格式（本包为什么不
// 重新实现它，见包文档）。Ollama 自己的拉取本就是幂等的——已经有的模型会很
// 快答复一个 "success" 状态——因此这里不像 pullOne 那样需要单独的"已满足"
// 检查。
//
// 与 pullOne 不同，这里没有配额参数：真正接收并写入字节的是 Ollama 服务
// 器，不是这个进程，取消这里的请求并不能阻止它继续写入自己的 blob 存储
// （见
// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md
// 第五节）。onProgress 非 nil 时，每读到一行带 completed/total 的流式响应
// 都会被调用一次，数值的范围是 Ollama 当前正在获取的那一个 layer。
//
// 返回的每一个错误都包裹了本包的某个哨兵错误，与 pullOne 的错误遵循同一种
// 可用 errors.Is 分类的约定，这样 Puller.runOllama 不需要检查消息文本就能
// 把一次失败映射到 modelpullstatus.FailureReason。
func pullOllama(ctx context.Context, client *http.Client, baseURL string, spec Spec, onProgress func(downloaded, total int64)) error {
	if baseURL == "" {
		return fmt.Errorf("%w: %q", errOllamaUnconfigured, spec.Name)
	}

	body, err := json.Marshal(struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}{Model: spec.Name, Stream: true})
	if err != nil {
		return fmt.Errorf("%w: encode request for %q: %v", errFetchFailed, spec.Name, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request for %q: %v", errFetchFailed, spec.Name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: fetch %q: %v", errFetchFailed, spec.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d pulling %q", errUnexpectedStatus, resp.StatusCode, spec.Name)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var chunk ollamaPullChunk
		if err := dec.Decode(&chunk); err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%w: stream for %q ended without a success status", errFetchFailed, spec.Name)
			}
			return fmt.Errorf("%w: read stream for %q: %v", errFetchFailed, spec.Name, err)
		}
		if chunk.Error != "" {
			return fmt.Errorf("%w: %q: %s", errOllamaPullFailed, spec.Name, chunk.Error)
		}
		if onProgress != nil && (chunk.Completed > 0 || chunk.Total > 0) {
			onProgress(chunk.Completed, chunk.Total)
		}
		if chunk.Status == "success" {
			return nil
		}
	}
}

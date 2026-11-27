package claudecode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// startupCache reuses, across the rounds of one runtime, the answers to the
// start-up requests every CLI process waits on before it sends its model
// request: GET /api/claude_code/policy_limits and the feature-flag
// evaluation POST /api/eval/…. Each round's CLI is a new process, so without
// it every round pays those upstream round trips in series.
//
// A cached answer is served at once and the same request is then sent
// upstream in the background, at most one at a time per key, to refresh the
// entry; an entry nobody refreshed for startupCacheMaxAge is not served, and
// the request goes upstream as before. A refresh answered with a status
// that is not cached removes the entry; a refresh that fails in transport
// keeps it until it ages out. The key covers method, path, query, every
// end-to-end request header — including the credential, so accounts and
// tokens never share an entry — and the body, with the evaluation's
// per-process attributes.sessionId removed. Anything the cache cannot
// handle takes the original path, so the cache adds no failure of its own.
//
// startupCache 在同一运行时的各回合之间复用 CLI 进程发模型请求前必须等待的启动请求答复：
// GET /api/claude_code/policy_limits 与功能开关求值 POST /api/eval/…。每个回合的 CLI
// 都是新进程，没有缓存时每个回合都要串行付这几次上游往返。
//
// 命中时立即返回缓存的答复，随后在后台把同一请求发往上游刷新条目，每个键同时最多一个；
// 超过 startupCacheMaxAge 无人刷新的条目不再使用，请求照原样发往上游。刷新得到不缓存的
// 状态码时删除条目；刷新在传输层失败时保留条目，直到它过期。缓存键覆盖方法、路径、查询串、
// 全部端到端请求头（包括凭据，因此不同账户与 token 从不共用条目）以及请求体，其中去掉求值
// 请求里每个进程不同的 attributes.sessionId。缓存处理不了的请求一律走原路径，缓存因此
// 不引入自己的失败。
type startupCache struct {
	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time
	mu     sync.Mutex
	items  map[[sha256.Size]byte]*startupEntry
	// refreshing holds the keys with a background refresh in flight.
	// refreshing 保存后台刷新正在进行中的键。
	refreshing map[[sha256.Size]byte]bool
	wg         sync.WaitGroup
}

type startupEntry struct {
	status  int
	header  http.Header
	body    []byte
	fetched time.Time
}

const (
	// startupCacheMaxAge bounds how stale a served answer can be when
	// background refreshes keep failing or no round has run for a while.
	// startupCacheMaxAge 限定后台刷新持续失败或一段时间没有回合时所用答复的最大陈旧程度。
	startupCacheMaxAge = 10 * time.Minute
	// startupRefreshTimeout bounds one background refresh.
	// startupRefreshTimeout 限定一次后台刷新的时长。
	startupRefreshTimeout = 30 * time.Second
	// maxStartupAnswerBytes bounds the body of an answer kept in the cache;
	// a larger one is relayed as before and not cached.
	// maxStartupAnswerBytes 限定缓存中单个答复的响应体大小；更大的答复照常转发且不缓存。
	maxStartupAnswerBytes = 1 << 20
)

func newStartupCache() *startupCache {
	ctx, cancel := context.WithCancel(context.Background())
	return &startupCache{ctx: ctx, cancel: cancel, now: time.Now,
		items: make(map[[sha256.Size]byte]*startupEntry), refreshing: make(map[[sha256.Size]byte]bool)}
}

// close cancels background refreshes, waits for them and drops every entry.
// close 取消后台刷新、等待其返回并丢弃全部条目。
func (c *startupCache) close() {
	if c == nil {
		return
	}
	c.cancel()
	c.wg.Wait()
	c.mu.Lock()
	c.items = make(map[[sha256.Size]byte]*startupEntry)
	c.mu.Unlock()
}

// startupKey returns the cache key of a decrypted upstreamAPIHost request
// whose whole body is body, and false for a request the cache does not
// handle.
// startupKey 返回完整请求体为 body 的解密后 upstreamAPIHost 请求的缓存键；缓存不处理的
// 请求返回 false。
func startupKey(r *http.Request, body []byte) ([sha256.Size]byte, bool) {
	var key [sha256.Size]byte
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/claude_code/policy_limits":
		if len(body) != 0 {
			return key, false
		}
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/api/eval/") && len(path) > len("/api/eval/"):
		if r.Header.Get("Content-Encoding") != "" {
			return key, false
		}
		normalized, ok := normalizeEvalBody(body)
		if !ok {
			return key, false
		}
		body = normalized
	default:
		return key, false
	}
	header := r.Header.Clone()
	stripHop(header)
	header.Del("Content-Length")
	header.Del("Accept-Encoding")
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, http.CanonicalHeaderKey(name))
	}
	sort.Strings(names)
	h := sha256.New()
	write := func(s string) {
		_, _ = io.WriteString(h, s)
		_, _ = h.Write([]byte{0})
	}
	write(r.Method)
	write(path)
	write(r.URL.RawQuery)
	for _, name := range names {
		write(name)
		for _, value := range header.Values(name) {
			write(value)
		}
	}
	write("")
	_, _ = h.Write(body)
	copy(key[:], h.Sum(nil))
	return key, true
}

// normalizeEvalBody removes attributes.sessionId, which differs in every
// CLI process, and re-encodes the document with sorted keys.
// normalizeEvalBody 去掉每个 CLI 进程都不同的 attributes.sessionId，并按键排序重新编码文档。
func normalizeEvalBody(body []byte) ([]byte, bool) {
	var doc map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&doc) != nil || doc == nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	if attributes, ok := doc["attributes"].(map[string]any); ok {
		delete(attributes, "sessionId")
	}
	out, err := json.Marshal(doc)
	return out, err == nil
}

// cacheableStartupStatus reports whether an answer with status is kept:
// success, not modified, and policy_limits' "no policy" 404.
// cacheableStartupStatus 判断该状态码的答复是否缓存：成功、未修改，以及 policy_limits
// 表示“没有策略”的 404。
func cacheableStartupStatus(status int) bool {
	return status == http.StatusOK || status == http.StatusNotModified || status == http.StatusNotFound
}

// lookup returns the entry for key when it is fresh enough to serve.
// lookup 在条目足够新、可以使用时返回它。
func (c *startupCache) lookup(key [sha256.Size]byte) (*startupEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if c.now().Sub(entry.fetched) > startupCacheMaxAge {
		delete(c.items, key)
		return nil, false
	}
	return entry, true
}

// store keeps an upstream answer, or removes the entry when status is not
// cached.
// store 保存上游答复；状态码不缓存时删除条目。
func (c *startupCache) store(key [sha256.Size]byte, status int, header http.Header, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return
	}
	if !cacheableStartupStatus(status) || len(body) > maxStartupAnswerBytes {
		delete(c.items, key)
		return
	}
	kept := header.Clone()
	stripHop(kept)
	kept.Del("Content-Length")
	kept.Del("Set-Cookie")
	c.items[key] = &startupEntry{status: status, header: kept, body: bytes.Clone(body), fetched: c.now()}
}

// serve writes a cached answer.
// serve 写出缓存的答复。
func (e *startupEntry) serve(w http.ResponseWriter) {
	for name, values := range e.header {
		w.Header()[name] = append([]string(nil), values...)
	}
	w.WriteHeader(e.status)
	_, _ = w.Write(e.body)
}

// refresh sends the request upstream once in the background over rt and
// stores the answer, unless a refresh of key is already in flight or the
// cache is closed. header is the request's end-to-end header and body its
// whole body; neither is changed.
// refresh 在后台经 rt 把请求发往上游一次并保存答复；同一键已有刷新在进行或缓存已关闭时
// 不做任何事。header 是请求的端到端请求头，body 是完整请求体；两者都不会被修改。
func (c *startupCache) refresh(key [sha256.Size]byte, rt http.RoundTripper, method string, target *url.URL, host string, header http.Header, body []byte) {
	c.mu.Lock()
	if c.refreshing[key] || c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.refreshing[key] = true
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.refreshing, key)
			c.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(c.ctx, startupRefreshTimeout)
		defer cancel()
		var reader io.Reader = http.NoBody
		if len(body) > 0 {
			reader = bytes.NewReader(body)
		}
		out, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
		if err != nil {
			return
		}
		out.Header = header
		out.Host = host
		out.ContentLength = int64(len(body))
		if len(body) > 0 {
			out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		}
		response, err := rt.RoundTrip(out)
		if err != nil {
			return
		}
		defer response.Body.Close()
		answer, err := io.ReadAll(io.LimitReader(response.Body, maxStartupAnswerBytes+1))
		if err != nil {
			return
		}
		c.store(key, response.StatusCode, response.Header, answer)
	}()
}

// startupRecorder keeps the bytes of an answer relayed on a cache miss, up to
// maxStartupAnswerBytes.
// startupRecorder 保存未命中时转发的答复字节，最多 maxStartupAnswerBytes。
type startupRecorder struct {
	buffer bytes.Buffer
	over   bool
}

func (r *startupRecorder) Write(p []byte) (int, error) {
	if !r.over {
		if r.buffer.Len()+len(p) > maxStartupAnswerBytes {
			r.over = true
			r.buffer = bytes.Buffer{}
		} else {
			r.buffer.Write(p)
		}
	}
	return len(p), nil
}

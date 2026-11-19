package claudecode

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliphase"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/proxy"
)

// upstreamAPIHost is the only host the capture proxy decrypts: the Messages
// API the logged-in CLI calls. Every other CONNECT is tunnelled unopened.
//
// upstreamAPIHost 是截获代理唯一解密的主机：已登录 CLI 调用的 Messages API。其他 CONNECT
// 一律不解密、原样隧道转发。
const upstreamAPIHost = "api.anthropic.com"

// maxCapturedRequestBytes bounds the request body the proxy reads to tell
// the round's streamed Messages call from the CLI's side calls (quota probe,
// token counting). A larger body is forwarded unread and never captured.
//
// maxCapturedRequestBytes 限制代理为区分本回合的流式 Messages 调用与 CLI 的旁路调用
// （额度探测、token 计数）而读取的请求体大小。更大的请求体不读取直接转发，也不截获。
const maxCapturedRequestBytes = 64 << 20

// upstreamRecord is one upstream answer to the round's streamed Messages
// call. status and body change once, if an error event arrives inside a
// successful stream.
//
// upstreamRecord 是上游对本回合流式 Messages 调用的一次应答。若成功的流中出现错误事件，
// status 与 body 会被更新一次。
type upstreamRecord struct {
	status int
	header http.Header
	body   json.RawMessage
	// inStream marks an error event inside a successful stream; the CLI
	// then ends its own stream malformed, so a protocol failure is upstream-caused.
	// inStream 表示成功的流中出现了错误事件；CLI 随后会以不完整的流结束，因此此时的协议
	// 失败由上游导致。
	inStream bool
}

// upstreamCapture keeps the latest upstream answer of one native session.
// The CLI retries internally, so only the final attempt describes the round.
//
// upstreamCapture 保存一个原生会话的最新上游应答。CLI 内部会重试，因此只有最后一次尝试
// 描述了本回合。
type upstreamCapture struct {
	mu   sync.Mutex
	last *upstreamRecord
	// phases receives when the model request reached the proxy, its wait for
	// an upstream connection and its response headers; nil records nothing.
	// phases 接收模型请求到达代理的时刻、它等待上游连接的时间以及响应头到达的时刻；为 nil 时不记录。
	phases *cliphase.Timeline
}

// timeConnect marks the model request as sent and returns it traced, so
// the time it waits for an upstream connection, and whether it reused one,
// reach the round's phases. Only the first request of a round counts: a
// CLI retry finds the connection its first attempt opened.
// timeConnect 把模型请求标记为已发出，并返回带跟踪的请求，使其等待上游连接的时间及是否复用
// 连接进入回合的阶段记录。只计回合的第一个请求：CLI 重试时会用到首次尝试打开的连接。
func (c *upstreamCapture) timeConnect(out *http.Request) *http.Request {
	if c.phases == nil {
		return out
	}
	c.phases.Mark(cliphase.RequestSent)
	var asked atomic.Int64
	return out.WithContext(httptrace.WithClientTrace(out.Context(), &httptrace.ClientTrace{
		GetConn: func(string) { asked.Store(time.Now().UnixNano()) },
		GotConn: func(info httptrace.GotConnInfo) {
			if start := asked.Load(); start != 0 {
				c.phases.Connected(time.Duration(time.Now().UnixNano()-start), info.Reused)
			}
		},
	}))
}

func (c *upstreamCapture) record(r *upstreamRecord) {
	c.mu.Lock()
	c.last = r
	c.mu.Unlock()
}

// fail marks r failed with the upstream's error document.
// fail 用上游错误文档把 r 标记为失败。
func (c *upstreamCapture) fail(r *upstreamRecord, status int, body []byte, inStream bool) {
	c.mu.Lock()
	r.status, r.body, r.inStream = status, append(json.RawMessage(nil), body...), inStream
	c.mu.Unlock()
}

// successHeader returns the headers of the final attempt when it opened a
// stream, even one that later reports an error event: those headers
// belong to the response the stream's events arrive in.
// successHeader 在最后一次尝试打开了流时返回其响应头，即使该流随后报告错误事件：这些
// 响应头属于承载流事件的那个响应。
func (c *upstreamCapture) successHeader() http.Header {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil || (c.last.status >= 400 && !c.last.inStream) {
		return nil
	}
	return c.last.header.Clone()
}

// failure returns a copy of the final attempt when it failed.
// failure 在最后一次尝试失败时返回该次尝试的副本。
func (c *upstreamCapture) failure() *upstreamRecord {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil || (c.last.status < 400 && c.last.body == nil) {
		return nil
	}
	copied := *c.last
	copied.header = c.last.header.Clone()
	return &copied
}

// withUpstream attaches the captured upstream answer to a round failure the
// upstream caused, so the Gateway can answer with the upstream's own status,
// headers and error document. Failures the Agent or CLI caused locally, and
// upstream authentication failures — the node's subscription credentials,
// which a client would misread as its own Gateway key being rejected — keep
// their existing form. The error code changes only for a 429, which becomes
// the retryable rate_limited a quota rejection already is.
//
// withUpstream 把截获到的上游应答附加到由上游导致的回合失败上，使 Gateway 能以上游自己的
// 状态码、响应头与错误文档应答。Agent 或 CLI 本地导致的失败，以及上游认证失败（属于节点的
// 订阅凭据，客户端会误认为是自己的 Gateway Key 被拒绝）保持原有形式。只有 429 会改变错误码，
// 与额度拒绝一样成为可重试的 rate_limited。
func withUpstream(failure, cause error, capture *upstreamCapture) error {
	record := capture.failure()
	var quota *quotaError
	upstreamCaused := errors.Is(cause, errCLIResult) || errors.Is(cause, errCLIExit) || errors.Is(cause, errMissingResult) ||
		errors.As(cause, &quota) || runtime.MessagesRejectionMessage(cause) != "" ||
		(record != nil && record.inStream && errors.Is(cause, errCLIProtocol))
	if record == nil || !upstreamCaused || record.status == 401 || record.status == 403 || record.status == 407 {
		return failure
	}
	var base *runtime.RuntimeError
	if !errors.As(failure, &base) {
		return failure
	}
	out := *base
	out.Upstream = &runtime.UpstreamResponse{StatusCode: record.status, Header: record.header, Body: record.body}
	if runtime.MessagesRejectionMessage(base) == "" {
		out.StatusCode = record.status
	}
	if record.status == 429 {
		if out.Code != runtime.ErrorRateLimited {
			out.Code, out.Retryable = runtime.ErrorRateLimited, true
			out.Message = "Claude Messages operation failed: " + string(runtime.ErrorRateLimited)
		}
		if out.RetryAfter == 0 {
			out.RetryAfter = runtime.ParseRetryAfter(record.header.Get("Retry-After"))
		}
	}
	return &out
}

// upstreamProxy is a per-process HTTP proxy on loopback that the CLI reaches
// through HTTPS_PROXY. It decrypts only CONNECTs to upstreamAPIHost, with a
// leaf certificate from a CA created for this process alone and trusted by
// the CLI through NODE_EXTRA_CA_CERTS, then forwards each request unchanged
// to the real host — through the Agent's own proxy settings when it has any —
// and records the round's streamed Messages response. The CLI still sees the
// first-party API address, so none of its first-party behaviour changes.
// Only a client presenting the per-process token may use the proxy.
//
// upstreamProxy 是 CLI 经 HTTPS_PROXY 访问的回环 HTTP 代理，每个进程一个。它只解密到
// upstreamAPIHost 的 CONNECT，使用仅为本进程创建的 CA 签发的叶子证书（CLI 通过
// NODE_EXTRA_CA_CERTS 信任该 CA），再把每个请求原样转发到真实主机（Agent 自身配置了代理时
// 经由该代理），并记录本回合流式 Messages 调用的响应。CLI 看到的仍是官方 API 地址，其官方
// 行为不受影响。只有出示本进程令牌的客户端才能使用该代理。
type upstreamProxy struct {
	// request is an immutable native document overlaid on the primary streamed call.
	// request 只覆盖本回合主流式调用，是不可变的原生文档；旁路调用保持原样。
	request   []byte
	listener  net.Listener
	server    *http.Server
	inner     *http.Server
	accepted  *connQueue
	capture   *upstreamCapture
	transport *http.Transport
	// shared is the runtime's connection pool for decrypted upstreamAPIHost
	// requests; nil sends them over transport.
	// shared 是运行时用于解密后 upstreamAPIHost 请求的连接池；为 nil 时经 transport 发送。
	shared    *upstreamPool
	route     func(*url.URL) (*url.URL, error)
	roots     *x509.CertPool
	proxyURL  string
	authority string
	caPEM     []byte
	extraCA   string
	leaf      tls.Certificate
	mu        sync.Mutex
	tunnels   map[net.Conn]bool
	closed    bool
	wg        sync.WaitGroup
	// handlers counts forwarded requests, so close can release their
	// upstream connections only after they return.
	// handlers 统计正在转发的请求，使 close 能在它们返回之后再释放其上游连接。
	handlers sync.WaitGroup
	// ctx ends with the proxy, cancelling dials still in progress.
	// ctx 随代理结束，取消仍在进行的拨号。
	ctx  context.Context
	stop context.CancelFunc
}

// upstreamRouting reads the proxy and CA settings in env, the environment the
// CLI would otherwise have used: route picks the proxy for a URL, roots
// verifies upstream certificates, and extraCA names the CA bundle env
// already trusted.
//
// upstreamRouting 读取 env（CLI 原本会使用的环境）中的代理与 CA 设置：route 为 URL 选择
// 代理，roots 用于校验上游证书，extraCA 是 env 原本已信任的 CA 证书包路径。
func upstreamRouting(env []string) (route func(*url.URL) (*url.URL, error), roots *x509.CertPool, extraCA string) {
	values := envValues(env)
	config := httpproxy.Config{
		HTTPProxy:  firstValue(values, "HTTP_PROXY", "http_proxy"),
		HTTPSProxy: firstValue(values, "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"),
		NoProxy:    firstValue(values, "NO_PROXY", "no_proxy"),
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	extraCA = values["NODE_EXTRA_CA_CERTS"]
	if extraCA != "" {
		if data, err := os.ReadFile(extraCA); err == nil {
			roots.AppendCertsFromPEM(data)
		}
	}
	return config.ProxyFunc(), roots, extraCA
}

// upstreamPool holds the connections to upstreamAPIHost that every round of
// one runtime — one account directory — shares, HTTP/2 multiplexed when the
// upstream offers it, so the CLI's start-up calls and model request reuse an
// established connection instead of each process paying a new handshake.
// Runtimes never share a pool, so accounts are never linked at the
// connection level. Routing follows the runtime's environment, the same as a
// per-process transport: every round of a runtime starts from the same
// environment, so the first round's routing is every round's. Only close
// ends its connections: a round ends its own requests through their contexts.
//
// upstreamPool 持有一个运行时（一个账户目录）所有回合共用的到 upstreamAPIHost 的连接，
// 上游支持时以 HTTP/2 多路复用，使 CLI 的启动请求与模型请求复用已建好的连接，而不是每个
// 进程各付一次握手。运行时之间从不共用连接池，账户因此不会在连接层面被关联。路由遵循
// 运行时的环境，与每进程的 transport 相同：同一运行时的每个回合都从同一份环境开始，
// 第一个回合的路由即所有回合的路由。只有 close 会结束其连接：回合通过请求上下文结束
// 自己的请求。
type upstreamPool struct {
	once      sync.Once
	transport *http.Transport
	mu        sync.Mutex
	conns     map[net.Conn]bool
	closed    bool
}

// upstreamPoolIdle is how long an idle shared connection is kept.
// upstreamPoolIdle 是共用连接空闲时保留的时长。
const upstreamPoolIdle = 90 * time.Second

func newUpstreamPool() *upstreamPool {
	return &upstreamPool{conns: make(map[net.Conn]bool)}
}

// roundTripper returns the shared transport, built on first use with route
// and roots from the runtime's environment, or nil once the pool is closed
// or for a nil pool; the caller then uses its own connections.
// roundTripper 返回共用的 transport（首次使用时以运行时环境得出的 route 与 roots 构建）；
// 连接池已关闭或为 nil 时返回 nil，调用方随即使用自己的连接。
func (p *upstreamPool) roundTripper(route func(*url.URL) (*url.URL, error), roots *x509.CertPool) http.RoundTripper {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		p.transport = &http.Transport{
			Proxy:               func(r *http.Request) (*url.URL, error) { return route(r.URL) },
			TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 30 * time.Second,
			IdleConnTimeout:     upstreamPoolIdle,
			MaxIdleConnsPerHost: maxSessions,
		}
		p.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			var dialer net.Dialer
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.closed {
				_ = conn.Close()
				return nil, net.ErrClosed
			}
			p.conns[conn] = true
			return &poolConn{Conn: conn, pool: p}, nil
		}
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	return p.transport
}

// close releases every shared connection. Rounds still running keep working
// on their own connections from then on.
// close 释放全部共用连接。此后仍在运行的回合改用自己的连接。
func (p *upstreamPool) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	conns := p.conns
	p.conns = make(map[net.Conn]bool)
	p.mu.Unlock()
	for conn := range conns {
		_ = conn.Close()
	}
	if p.transport != nil {
		p.transport.CloseIdleConnections()
	}
}

// poolConn removes itself from the pool's set when closed.
// poolConn 关闭时把自己从连接池的集合中移除。
type poolConn struct {
	net.Conn
	pool *upstreamPool
	once sync.Once
}

func (c *poolConn) Close() error {
	c.once.Do(func() {
		c.pool.mu.Lock()
		delete(c.pool.conns, c.Conn)
		c.pool.mu.Unlock()
	})
	return c.Conn.Close()
}

// startUpstreamProxy starts a capture proxy whose outbound routing follows
// the proxy and CA settings in env, the environment the CLI would otherwise
// have used. shared, when not nil, carries the decrypted upstreamAPIHost
// requests; tunnels to other hosts and a failure of the shared connection
// use the proxy's own connections, as without it.
//
// startUpstreamProxy 启动截获代理，出站路由遵循 env 中的代理与 CA 设置，即 CLI 原本会使用的环境。
// shared 不为 nil 时承载解密后的 upstreamAPIHost 请求；到其他主机的隧道以及共用连接出错时，
// 照旧使用代理自己的连接。
func startUpstreamProxy(capture *upstreamCapture, env []string, shared *upstreamPool, request ...[]byte) (*upstreamProxy, error) {
	caPEM, leaf, err := upstreamCertificates()
	if err != nil {
		return nil, err
	}
	route, roots, extraCA := upstreamRouting(env)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := randomID()
	ctx, stop := context.WithCancel(context.Background())
	p := &upstreamProxy{
		ctx: ctx, stop: stop, shared: shared, roots: roots,
		listener: listener, accepted: newConnQueue(listener.Addr()), capture: capture, route: route,
		proxyURL:  "http://aisw:" + token + "@" + listener.Addr().String(),
		authority: "Basic " + base64.StdEncoding.EncodeToString([]byte("aisw:"+token)),
		caPEM:     caPEM, extraCA: extraCA, leaf: leaf, tunnels: make(map[net.Conn]bool),
		transport: &http.Transport{
			Proxy:               func(r *http.Request) (*url.URL, error) { return route(r.URL) },
			TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 30 * time.Second,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	if len(request) > 0 {
		p.request = request[0]
	}
	// Every upstream connection is tracked, including one a cancelled
	// request's dial completes in the background, so close leaves none behind.
	// 每条上游连接都被跟踪，包括被取消的请求在后台完成拨号得到的连接，close 因此不留残余。
	p.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if !p.track(conn) {
			_ = conn.Close()
			return nil, net.ErrClosed
		}
		return &trackedConn{Conn: conn, proxy: p}, nil
	}
	quiet := log.New(io.Discard, "", 0)
	p.server = &http.Server{Handler: http.HandlerFunc(p.serveProxy), ReadHeaderTimeout: 30 * time.Second, ErrorLog: quiet}
	p.inner = &http.Server{Handler: http.HandlerFunc(p.serveUpstream), ReadHeaderTimeout: 30 * time.Second, ErrorLog: quiet}
	p.wg.Add(2)
	go func() { defer p.wg.Done(); _ = p.server.Serve(listener) }()
	go func() { defer p.wg.Done(); _ = p.inner.Serve(p.accepted) }()
	return p, nil
}

// environ returns base with the CLI routed through the proxy: every proxy
// variable points at it, loopback stays direct, and NODE_EXTRA_CA_CERTS
// names a 0600 bundle in dir holding the process CA plus any CA bundle base
// already trusted, so tunnelled hosts behind a corporate proxy still verify.
//
// environ 返回经代理改写的 base：所有代理变量指向本代理，回环地址保持直连，
// NODE_EXTRA_CA_CERTS 指向 dir 中权限 0600 的证书包，包含本进程 CA 与 base 原本信任的
// CA，因此企业代理之后被隧道转发的主机仍能通过校验。
func (p *upstreamProxy) environ(dir string, base []string) ([]string, error) {
	bundle := append([]byte(nil), p.caPEM...)
	if p.extraCA != "" {
		if data, err := os.ReadFile(p.extraCA); err == nil {
			bundle = append(append(bundle, '\n'), data...)
		}
	}
	path := filepath.Join(dir, "aisw-upstream-ca.pem")
	if err := os.WriteFile(path, bundle, 0600); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(base)+9)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "NODE_EXTRA_CA_CERTS":
			continue
		}
		out = append(out, entry)
	}
	return append(out, "HTTPS_PROXY="+p.proxyURL, "https_proxy="+p.proxyURL, "HTTP_PROXY="+p.proxyURL, "http_proxy="+p.proxyURL,
		"NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1", "NODE_EXTRA_CA_CERTS="+path), nil
}

// close stops accepting, ends every tunnel and in-flight upstream call, and waits for the servers.
// close 停止接受连接，结束所有隧道与进行中的上游调用，并等待服务退出。
func (p *upstreamProxy) close() {
	p.stop()
	p.mu.Lock()
	p.closed = true
	for conn := range p.tunnels {
		_ = conn.Close()
	}
	p.mu.Unlock()
	_ = p.server.Close()
	_ = p.inner.Close()
	p.wg.Wait()
	// Closed connections cancel every forwarded request; once they return
	// their upstream connections are idle and can be released.
	// 连接关闭会取消每个转发中的请求；它们返回后其上游连接变为空闲，可以释放。
	p.handlers.Wait()
	p.transport.CloseIdleConnections()
	p.mu.Lock()
	for conn := range p.tunnels {
		_ = conn.Close()
	}
	p.mu.Unlock()
}

// trackedConn removes itself from the proxy's set when closed.
// trackedConn 关闭时把自己从代理的集合中移除。
type trackedConn struct {
	net.Conn
	proxy *upstreamProxy
	once  sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.proxy.untrack(c.Conn) })
	return c.Conn.Close()
}

// begin admits one forwarded request unless the proxy is closing.
// begin 在代理未关闭时接纳一个转发请求。
func (p *upstreamProxy) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.handlers.Add(1)
	return true
}

func (p *upstreamProxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.tunnels[conn] = true
	return true
}

func (p *upstreamProxy) untrack(conn net.Conn) {
	p.mu.Lock()
	delete(p.tunnels, conn)
	p.mu.Unlock()
}

// serveProxy authenticates the CLI and handles CONNECT or an absolute-form HTTP request.
// serveProxy 校验 CLI 身份，并处理 CONNECT 或绝对形式的 HTTP 请求。
func (p *upstreamProxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(p.authority)) != 1 {
		w.Header().Set("Proxy-Authenticate", `Basic realm="aisw"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	if r.Method != http.MethodConnect {
		if !r.URL.IsAbs() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.forward(w, r, r.URL, false, false)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	client := net.Conn(conn)
	if n := buffered.Reader.Buffered(); n > 0 {
		head, _ := buffered.Reader.Peek(n)
		client = &prefixConn{Conn: conn, prefix: bytes.NewReader(append([]byte(nil), head...))}
	}
	if !p.track(conn) {
		_ = conn.Close()
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		p.untrack(conn)
		_ = conn.Close()
		return
	}
	if strings.EqualFold(host, upstreamAPIHost) && port == "443" {
		// The inner server owns the decrypted connection from here on.
		// 从这里开始由内部服务持有解密后的连接。
		p.untrack(conn)
		p.accepted.push(tls.Server(client, &tls.Config{Certificates: []tls.Certificate{p.leaf}, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}))
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.untrack(conn)
		defer conn.Close()
		p.tunnel(client, r.Host)
	}()
}

// tunnel relays bytes to target, through the Agent's proxy when its settings route target there.
// tunnel 向 target 原样中继字节；Agent 的代理设置把 target 路由到代理时经由该代理。
func (p *upstreamProxy) tunnel(client net.Conn, target string) {
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	upstream, err := p.dialThrough(ctx, target)
	cancel()
	if err != nil {
		return
	}
	if !p.track(upstream) {
		_ = upstream.Close()
		return
	}
	defer p.untrack(upstream)
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); closeWrite(upstream); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); closeWrite(client); done <- struct{}{} }()
	<-done
	<-done
}

func closeWrite(conn net.Conn) {
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
		return
	}
	if prefixed, ok := conn.(*prefixConn); ok {
		closeWrite(prefixed.Conn)
	}
}

// dialThrough opens a TCP stream to target, directly or through the http,
// https or socks5 proxy the Agent's environment selects for it.
//
// dialThrough 打开到 target 的 TCP 流：直连，或经 Agent 环境为其选择的 http、https 或 socks5 代理。
func (p *upstreamProxy) dialThrough(ctx context.Context, target string) (net.Conn, error) {
	var dialer net.Dialer
	via, err := p.route(&url.URL{Scheme: "https", Host: target})
	if err != nil {
		return nil, err
	}
	if via == nil {
		return dialer.DialContext(ctx, "tcp", target)
	}
	switch via.Scheme {
	case "socks5", "socks5h":
		socks, err := proxy.FromURL(via, &dialer)
		if err != nil {
			return nil, err
		}
		if contextual, ok := socks.(proxy.ContextDialer); ok {
			return contextual.DialContext(ctx, "tcp", target)
		}
		return socks.Dial("tcp", target)
	case "http", "https":
	default:
		return nil, errors.New("unsupported proxy scheme")
	}
	address := via.Host
	if via.Port() == "" {
		address = net.JoinHostPort(via.Hostname(), map[string]string{"http": "80", "https": "443"}[via.Scheme])
	}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	// The socket is not tracked by tunnel until CONNECT succeeds. Cancellation
	// must also interrupt this handshake, rather than wait for its deadline.
	// CONNECT 成功前 socket 尚未由 tunnel 跟踪；取消也必须中断握手，不能只等固定期限。
	dialed := conn
	stop := context.AfterFunc(ctx, func() { _ = dialed.Close() })
	defer stop()
	if via.Scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: via.Hostname(), RootCAs: p.transport.TLSClientConfig.RootCAs, MinVersion: tls.VersionTLS12})
		if err := secure.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = secure
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	if via.User != nil {
		password, _ := via.User.Password()
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(via.User.Username()+":"+password)))
	}
	reader := bufio.NewReader(conn)
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	response, err := http.ReadResponse(reader, request)
	if err != nil || response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, errors.New("proxy CONNECT refused")
	}
	if !stop() {
		_ = conn.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	if n := reader.Buffered(); n > 0 {
		head, _ := reader.Peek(n)
		return &prefixConn{Conn: conn, prefix: bytes.NewReader(append([]byte(nil), head...))}, nil
	}
	return conn, nil
}

// serveUpstream forwards a decrypted request to the real API host.
// serveUpstream 把解密后的请求转发到真实 API 主机。
func (p *upstreamProxy) serveUpstream(w http.ResponseWriter, r *http.Request) {
	target := &url.URL{Scheme: "https", Host: upstreamAPIHost, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	p.forward(w, r, target, r.Method == http.MethodPost && r.URL.Path == "/v1/messages", true)
}

// maxReplayBytes bounds the body of a start-up or side call the proxy keeps
// so it can resend the call on its own connection when the shared one
// fails; a larger body is streamed and not resent.
//
// maxReplayBytes 限制代理为在共用连接出错时改用自己的连接重发而保留的启动请求或旁路调用
// 请求体大小；更大的请求体按流转发，不重发。
const maxReplayBytes = 1 << 20

// hopHeaders are request and response headers that belong to one connection.
// hopHeaders 是只属于单个连接的请求头与响应头。
var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHop(h http.Header) {
	for _, value := range h.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

// forward relays a request and its response, flushing as bytes arrive. A
// Messages call is captured when its body asks for a stream. Only that call
// receives a native document override when settings could not hold it; the
// CLI's side calls and all response bytes are relayed unchanged.
//
// A sharable request — a decrypted upstreamAPIHost call — goes over the
// runtime's shared pool when there is one. If it fails there on a reused
// connection before any response, it is sent once more over the proxy's own
// connections, the path it would have taken without the pool, provided its
// whole body was kept; the CLI would otherwise retry the same call itself.
//
// forward 中继请求和响应，字节到达即刷新。仅要求流式输出的主 Messages 调用在设置文件
// 容纳不下原生文档时接收覆盖；CLI 旁路调用及响应字节保持原样。
//
// 可共用的请求（解密后的 upstreamAPIHost 调用）在运行时有连接池时经连接池发送。它在
// 复用的连接上于收到任何响应之前出错时，只要完整请求体已保留，就改经代理自己的连接
// （没有连接池时本应走的路径）再发一次；否则 CLI 也会自己重试同一个调用。
func (p *upstreamProxy) forward(w http.ResponseWriter, r *http.Request, target *url.URL, messages, sharable bool) {
	if !p.begin() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer p.handlers.Done()
	// The proxy's end cancels the request itself, since a shared connection
	// outlives the proxy and is not closed with it.
	// 代理结束时直接取消请求，因为共用连接比代理存活更久，不会随代理关闭。
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(p.ctx, cancel)()
	var shared http.RoundTripper
	if sharable {
		shared = p.shared.roundTripper(p.route, p.roots)
	}
	body := io.Reader(r.Body)
	// kept is the whole request body when it was read, so the request can
	// be sent again; replayable also covers a request without a body.
	// kept 是已完整读取的请求体，请求因此可以重发；replayable 还涵盖没有请求体的请求。
	var kept []byte
	replayable := r.ContentLength == 0
	primary := false
	var replacement []byte
	if !messages && shared != nil && r.ContentLength != 0 {
		head, err := io.ReadAll(io.LimitReader(r.Body, maxReplayBytes+1))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if len(head) <= maxReplayBytes {
			kept, replayable = head, true
		}
		body = io.MultiReader(bytes.NewReader(head), r.Body)
	}
	if messages {
		head, err := io.ReadAll(io.LimitReader(r.Body, maxCapturedRequestBytes+1))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		probeBody := head
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") && len(head) <= maxCapturedRequestBytes {
			reader, err := gzip.NewReader(bytes.NewReader(head))
			if err == nil {
				probeBody, err = io.ReadAll(io.LimitReader(reader, maxCapturedRequestBytes+1))
				_ = reader.Close()
				if err != nil {
					probeBody = nil
				}
			} else {
				probeBody = nil
			}
		}
		valid := false
		if len(head) <= maxCapturedRequestBytes && len(probeBody) <= maxCapturedRequestBytes {
			var probe struct {
				Stream bool `json:"stream"`
			}
			valid = json.Unmarshal(probeBody, &probe) == nil
			primary = valid && probe.Stream
		}
		if p.request != nil && !valid {
			// Refuse an unreadable seed rather than silently sending a different prompt.
			// 种子请求无法识别时明确失败，不能静默发送不含原始历史的另一份请求。
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if primary && p.request != nil {
			var seed, override map[string]json.RawMessage
			if json.Unmarshal(probeBody, &seed) != nil || json.Unmarshal(p.request, &override) != nil || seed == nil || override == nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			mergeNativeRequest(seed, override)
			var err error
			replacement, err = encodeMessagesJSON(seed)
			if err != nil || len(replacement) > maxCapturedRequestBytes {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
		}
		if len(head) <= maxCapturedRequestBytes {
			kept, replayable = head, true
		}
		body = io.MultiReader(bytes.NewReader(head), r.Body)
	}
	if r.ContentLength == 0 {
		body = http.NoBody
	}
	if replacement != nil {
		body = bytes.NewReader(replacement)
		kept, replayable = replacement, true
	}
	out, err := http.NewRequestWithContext(ctx, r.Method, target.String(), body)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	out.ContentLength = r.ContentLength
	out.Header = r.Header.Clone()
	stripHop(out.Header)
	if replacement != nil {
		out.ContentLength = int64(len(replacement))
		out.Header.Del("Content-Length")
		out.Header.Del("Content-Encoding")
		out.Header.Set("Content-Type", "application/json")
	}
	// The transport negotiates and removes compression, so a captured error
	// document is always readable JSON.
	// 由 transport 协商并解除压缩，截获的错误文档因此总是可读的 JSON。
	out.Header.Del("Accept-Encoding")
	if r.URL.IsAbs() {
		out.Host = r.URL.Host
	} else {
		out.Host = upstreamAPIHost
	}
	if primary {
		out = p.capture.timeConnect(out)
	}
	resend := func() io.ReadCloser {
		if r.ContentLength == 0 {
			return http.NoBody
		}
		return io.NopCloser(bytes.NewReader(kept))
	}
	var response *http.Response
	if shared == nil {
		response, err = p.transport.RoundTrip(out)
	} else {
		var reused atomic.Bool
		out = out.WithContext(httptrace.WithClientTrace(out.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused.Store(reused.Load() || info.Reused) },
		}))
		if replayable {
			// The transport may then retry by itself on a connection the upstream closed while idle.
			// transport 因此可以在上游关闭了空闲连接时自行重试。
			out.GetBody = func() (io.ReadCloser, error) { return resend(), nil }
		}
		response, err = shared.RoundTrip(out)
		if err != nil && reused.Load() && replayable && ctx.Err() == nil {
			retry := out.Clone(out.Context())
			retry.Body = resend()
			response, err = p.transport.RoundTrip(retry)
		}
	}
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if primary {
		p.capture.phases.Mark(cliphase.UpstreamHeaders)
	}
	defer response.Body.Close()
	header := response.Header.Clone()
	stripHop(header)
	for name, values := range header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.StatusCode)
	reader := io.Reader(response.Body)
	if primary {
		record := &upstreamRecord{status: response.StatusCode, header: runtime.SanitizeUpstreamHeader(response.Header)}
		p.capture.record(record)
		switch {
		case response.StatusCode >= 400:
			// Record the error document before the CLI can read it, so the
			// round never ends ahead of its capture.
			// 在 CLI 读到错误文档之前先记录它，回合因此不会先于截获结束。
			head, _ := io.ReadAll(io.LimitReader(response.Body, runtime.MaxUpstreamErrorBytes+1))
			if runtime.ValidUpstreamErrorBody(head) {
				p.capture.fail(record, response.StatusCode, head, false)
			}
			reader = io.MultiReader(bytes.NewReader(head), response.Body)
		case strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream"):
			reader = &sseErrorReader{Reader: reader, found: func(data []byte) {
				if runtime.ValidUpstreamErrorBody(data) {
					errType, _ := runtime.UpstreamErrorDetail(data)
					p.capture.fail(record, runtime.UpstreamErrorStatus(errType), data, true)
				}
			}}
		}
	}
	controller := http.NewResponseController(w)
	buffer := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if _, werr := w.Write(buffer[:n]); werr != nil {
				break
			}
			_ = controller.Flush()
		}
		if err != nil {
			break
		}
	}
}

// sseErrorReader passes a stream through unchanged and reports the data of
// its first "error" event before returning the bytes that complete it, so
// the capture is in place before the CLI sees the error. SSE data lines are
// joined at the event boundary; incomplete or oversized events are skipped.
//
// sseErrorReader 原样传递流，并在返回构成第一个 "error" 事件的字节之前报告其数据，使截获
// 先于 CLI 看到该错误完成。在事件边界合并 data 行；不完整或超限的事件被跳过。
type sseErrorReader struct {
	io.Reader
	found         func([]byte)
	line, data    []byte
	lineOverflow  bool
	eventOverflow bool
	isError       bool
	afterCR       bool
	reported      bool
}

func (s *sseErrorReader) Read(p []byte) (int, error) {
	n, err := s.Reader.Read(p)
	if !s.reported {
		for _, c := range p[:n] {
			if s.afterCR {
				s.afterCR = false
				if c == '\n' {
					continue
				}
			}
			switch c {
			case '\r', '\n':
				s.endLine()
				s.afterCR = c == '\r'
			default:
				if len(s.line) < runtime.MaxUpstreamErrorBytes+16 {
					s.line = append(s.line, c)
				} else {
					s.lineOverflow = true
				}
			}
			if s.reported {
				break
			}
		}
	}
	return n, err
}

func (s *sseErrorReader) endLine() {
	switch {
	case s.lineOverflow:
		s.eventOverflow = true
	case len(s.line) == 0:
		if s.isError && !s.eventOverflow && len(s.data) > 0 {
			s.reported = true
			s.found(s.data[:len(s.data)-1])
		}
		s.data, s.isError, s.eventOverflow = s.data[:0], false, false
	default:
		field, value, _ := bytes.Cut(s.line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			s.isError = bytes.Equal(value, []byte("error"))
		case "data":
			if len(s.data)+len(value) > runtime.MaxUpstreamErrorBytes {
				s.eventOverflow = true
			}
			if !s.eventOverflow {
				s.data = append(s.data, value...)
				s.data = append(s.data, '\n')
			}
		}
	}
	s.line, s.lineOverflow = s.line[:0], false
}

// prefixConn replays bytes already buffered before the connection was handed over.
// prefixConn 先重放连接移交之前已缓冲的字节。
type prefixConn struct {
	net.Conn
	prefix *bytes.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if c.prefix.Len() > 0 {
		return c.prefix.Read(p)
	}
	return c.Conn.Read(p)
}

// connQueue is a net.Listener fed with already-accepted connections.
// connQueue 是由已接受的连接喂入的 net.Listener。
type connQueue struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newConnQueue(addr net.Addr) *connQueue {
	return &connQueue{addr: addr, conns: make(chan net.Conn), done: make(chan struct{})}
}

func (q *connQueue) push(conn net.Conn) {
	select {
	case q.conns <- conn:
	case <-q.done:
		_ = conn.Close()
	}
}

func (q *connQueue) Accept() (net.Conn, error) {
	select {
	case conn := <-q.conns:
		return conn, nil
	case <-q.done:
		return nil, net.ErrClosed
	}
}

func (q *connQueue) Close() error {
	q.once.Do(func() { close(q.done) })
	return nil
}

func (q *connQueue) Addr() net.Addr { return q.addr }

// upstreamCertificates creates a CA for one process and a leaf for upstreamAPIHost signed by it.
// upstreamCertificates 为单个进程创建一个 CA，并用它为 upstreamAPIHost 签发叶子证书。
func upstreamCertificates() ([]byte, tls.Certificate, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	now := time.Now()
	serial := func() *big.Int {
		n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
		return n
	}
	caTemplate := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "AIServeWeave Agent upstream capture"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, PermittedDNSDomainsCritical: true, PermittedDNSDomains: []string{upstreamAPIHost},
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: upstreamAPIHost}, DNSNames: []string{upstreamAPIHost},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(47 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return caPEM, tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}, nil
}

func envValues(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	return values
}

func firstValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := values[key]; value != "" {
			return value
		}
	}
	return ""
}

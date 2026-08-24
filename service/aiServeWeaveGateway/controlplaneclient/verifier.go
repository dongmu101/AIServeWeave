// Package controlplaneclient is the Gateway's side of the control plane's API
// key verification: it resolves a presented key to the tenant it
// authenticates, and caches the answer in process.
//
// Two properties shape it, and both are about the request path it sits on.
//
// The caller's key never leaves this process. The Gateway hashes it here and
// sends only the hash, so a user's credential never appears in the control
// plane's memory, its request logs, or a packet capture between the two. The
// hash is enough to look a key up and is useless for authenticating as one
// anywhere else.
//
// Verification must not cost a round trip per request. Every inference call
// carries a key, and going to the control plane for each one would put an HTTP
// hop in front of every token. The in-process cache makes the common case free;
// the cost is a bounded window in which a revoked key still works, which is
// what CacheTTL bounds and what the service README records.
//
// controlplaneclient 是 Gateway 一侧的控制面 API Key 校验：它把出示的 key 解析为它所
// 认证的租户，并在进程内缓存结果。
//
// 有两条性质塑造了它，且都与它所处的请求路径有关。
//
// 调用方的 key 从不离开本进程。Gateway 在这里对它做哈希，只发送哈希，因此用户的凭据
// 从不出现在控制面的内存里、它的请求日志里，或两者之间的抓包里。哈希足以查到一个 key，
// 却无法用来在别处冒充它通过认证。
//
// 校验不能每个请求付出一次往返。每一次推理调用都携带 key，为每一次都去问控制面，等于
// 在每个 token 前面垫上一跳 HTTP。进程内缓存让常见情况零开销；代价是一个被吊销的 key
// 仍然可用的有界窗口，那正是 CacheTTL 所限制、也是服务 README 所记录的东西。
package controlplaneclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"AIServeWeave/common/apikey"
	"AIServeWeave/common/quota"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// Default bounds. CacheTTL is short because it is the window a revoked key
// keeps working in, and revocation is an incident response action.
//
// 默认上限。CacheTTL 很短，因为它就是一个被吊销的 key 仍能继续工作的窗口，而吊销是
// 一个应急响应动作。
const (
	DefaultCacheTTL     = 30 * time.Second
	DefaultCacheEntries = 4096
	DefaultTimeout      = 3 * time.Second
)

// Config configures a Verifier.
//
// Config 配置一个 Verifier。
type Config struct {
	// Endpoint is the control plane's base URL, e.g. http://127.0.0.1:8090.
	//
	// Endpoint 是控制面的基础 URL，例如 http://127.0.0.1:8090。
	Endpoint string
	// Token authenticates this Gateway to the control plane's internal
	// endpoint. It must match the control plane's InternalToken.
	//
	// Token 用于本 Gateway 向控制面的内部端点表明身份。它必须与控制面的
	// InternalToken 一致。
	Token string
	// CacheTTL bounds how long a verification is trusted without asking
	// again. Zero uses DefaultCacheTTL.
	//
	// CacheTTL 限制一次校验结果在不再询问的前提下被信任多久。为零时使用
	// DefaultCacheTTL。
	CacheTTL time.Duration
	// CacheEntries bounds the cache. Zero uses DefaultCacheEntries.
	//
	// CacheEntries 限制缓存条目数。为零时使用 DefaultCacheEntries。
	CacheEntries int
	// Timeout bounds one verification call. It is short: this call is on the
	// request path, and a slow control plane must degrade to a fast 503
	// rather than holding inference requests open.
	//
	// Timeout 限制单次校验调用。它很短：这次调用位于请求路径上，一个变慢的控制面
	// 必须迅速退化为 503，而不是把推理请求一直挂着。
	Timeout time.Duration
	// HTTPClient is used for the call. Nil builds one with Timeout.
	//
	// HTTPClient 用于发起该调用。为 nil 时会用 Timeout 构造一个。
	HTTPClient *http.Client
	// Clock supplies time for cache expiry. Nil uses the system clock.
	//
	// Clock 为缓存过期提供时间。为 nil 时使用系统时钟。
	Clock runtime.Clock
}

// Verifier resolves API keys against the control plane.
//
// Verifier 对着控制面解析 API Key。
type Verifier struct {
	endpoint string
	token    string
	client   *http.Client
	clock    runtime.Clock

	ttl     time.Duration
	maxSize int
	mu      sync.Mutex
	entries map[string]entry
}

// entry is one cached verification and the instant it stops being trusted.
//
// entry 是一条缓存的校验结果，以及它不再被信任的时刻。
type entry struct {
	identity httpapi.Identity
	expiry   time.Time
}

// New returns a Verifier. It validates what a typo would otherwise turn into
// an outage discovered by the first user request.
//
// New 返回一个 Verifier。它校验那些一旦写错、就会变成「由第一个用户请求发现的故障」
// 的东西。
func New(cfg Config) (*Verifier, error) {
	endpoint := strings.TrimSuffix(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("controlplaneclient: an endpoint is required")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, errors.New("controlplaneclient: the endpoint must include a scheme, e.g. http://127.0.0.1:8090")
	}
	if cfg.Token == "" {
		return nil, errors.New("controlplaneclient: a token is required; it must match the control plane's InternalToken")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	maxSize := cfg.CacheEntries
	if maxSize <= 0 {
		maxSize = DefaultCacheEntries
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}

	return &Verifier{
		endpoint: endpoint,
		token:    cfg.Token,
		client:   client,
		clock:    clock,
		ttl:      ttl,
		maxSize:  maxSize,
		entries:  make(map[string]entry),
	}, nil
}

var _ httpapi.KeyVerifier = (*Verifier)(nil)

// Verify resolves key to the identity it authenticates.
//
// Verify 把 key 解析为它所认证的身份。
func (v *Verifier) Verify(ctx context.Context, key string) (httpapi.Identity, error) {
	// A malformed key is refused without a round trip. A scanner probing with
	// "Bearer test" must not be able to make this Gateway generate control
	// plane traffic.
	//
	// 格式不对的 key 无需往返即被拒绝。用 "Bearer test" 试探的扫描器，不得能让本
	// Gateway 产生通往控制面的流量。
	hash, err := apikey.HashOf(key)
	if err != nil {
		return httpapi.Identity{}, fmt.Errorf("%w: malformed key", httpapi.ErrKeyRejected)
	}

	if identity, ok := v.cached(hash); ok {
		return identity, nil
	}

	identity, err := v.ask(ctx, hash)
	if err != nil {
		return httpapi.Identity{}, err
	}
	v.store(hash, identity)
	return identity, nil
}

// cached returns a verification that has not expired.
//
// cached 返回一条尚未过期的校验结果。
func (v *Verifier) cached(hash string) (httpapi.Identity, bool) {
	now := v.clock.Now()

	v.mu.Lock()
	defer v.mu.Unlock()
	found, ok := v.entries[hash]
	if !ok {
		return httpapi.Identity{}, false
	}
	if !now.Before(found.expiry) {
		delete(v.entries, hash)
		return httpapi.Identity{}, false
	}
	return found.identity, true
}

// store caches one verification, evicting expired entries when the cache is
// full.
//
// Only successful verifications are cached. Caching rejections would let a
// caller probing with invented keys fill this map with entries that serve
// nobody, and a rejection is already the path that costs a round trip nobody
// is waiting on.
//
// store 缓存一条校验结果，缓存满时驱逐已过期的条目。
//
// 只缓存成功的校验。缓存拒绝结果会让一个用编造 key 试探的调用方把这张表塞满不为任何人
// 服务的条目，而拒绝本来就是那条「往返一次、且没人在等」的路径。
func (v *Verifier) store(hash string, identity httpapi.Identity) {
	now := v.clock.Now()

	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.entries) >= v.maxSize {
		for cached, found := range v.entries {
			if !now.Before(found.expiry) {
				delete(v.entries, cached)
			}
		}
		// Still full: every entry is live, and the cache is smaller than the
		// number of keys actually in use. Dropping it wholesale costs one
		// round trip per key rather than growing without bound, and the
		// operator should raise CacheEntries.
		//
		// 仍然是满的：每个条目都还有效，说明缓存比实际在用的 key 数量还小。整体丢弃
		// 的代价是每个 key 多一次往返，好过无界增长，同时运维应当调高 CacheEntries。
		if len(v.entries) >= v.maxSize {
			clear(v.entries)
		}
	}
	v.entries[hash] = entry{identity: identity, expiry: now.Add(v.ttl)}
}

// ask performs one verification call against the control plane.
//
// ask 向控制面发起一次校验调用。
func (v *Verifier) ask(ctx context.Context, hash string) (httpapi.Identity, error) {
	body, err := json.Marshal(map[string]string{"hash": hash})
	if err != nil {
		return httpapi.Identity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		v.endpoint+"/internal/v1/apikeys/verify", bytes.NewReader(body))
	if err != nil {
		return httpapi.Identity{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.token)

	resp, err := v.client.Do(req)
	if err != nil {
		return httpapi.Identity{}, fmt.Errorf("controlplaneclient: reaching the control plane: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var decoded struct {
			TenantID string       `json:"tenant_id"`
			KeyID    string       `json:"key_id"`
			Limits   quota.Limits `json:"limits"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			return httpapi.Identity{}, fmt.Errorf("controlplaneclient: decoding the verification: %w", err)
		}
		if decoded.TenantID == "" {
			// A verification with no tenant would attribute the request to
			// nobody, and "nobody" is one join away from "everybody".
			//
			// 一条没有租户的校验结果会把请求归属给「没有人」，而「没有人」离「所有人」
			// 只差一次 join。
			return httpapi.Identity{}, errors.New("controlplaneclient: the control plane returned no tenant")
		}
		return httpapi.Identity{TenantID: decoded.TenantID, KeyID: decoded.KeyID, Limits: decoded.Limits}, nil

	case http.StatusNotFound:
		return httpapi.Identity{}, httpapi.ErrKeyRejected

	case http.StatusUnauthorized, http.StatusForbidden:
		// This Gateway's own token was refused. That is a deployment fault,
		// not a caller's: reporting it as a rejected key would send every
		// user to regenerate keys that are perfectly valid.
		//
		// 是本 Gateway 自己的 token 被拒绝了。那是部署故障，不是调用方的问题：把它
		// 报成 key 被拒，会让每个用户跑去重新生成一批本来完全有效的 key。
		return httpapi.Identity{}, errors.New("controlplaneclient: this Gateway's internal token was refused by the control plane")

	default:
		return httpapi.Identity{}, fmt.Errorf("controlplaneclient: the control plane answered %d", resp.StatusCode)
	}
}

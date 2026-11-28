package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"AIServeWeave/common/arkvideo"
	"AIServeWeave/common/objectstore"
	"AIServeWeave/common/runtime"
)

// DefaultArkCacheTTL is how long the Ark key a tenant's video node uses is
// reused before it is read from the control plane again, and so how long a
// key saved in the Console can take to reach this replica.
//
// DefaultArkCacheTTL 是租户视频节点所用的方舟 Key 在重新向控制面读取之前被复用的时长，
// 也就是在 Console 保存的 Key 最多多久才到达本副本。
const DefaultArkCacheTTL = 30 * time.Second

// arkStaleLimit bounds how long a key already read keeps being used while
// the control plane cannot be asked again.
//
// arkStaleLimit 限定在无法再次询问控制面时，已读到的 Key 还能继续使用多久。
const arkStaleLimit = 10 * time.Minute

// Sources of an Ark key, as the control plane reports them.
// 控制面报告的方舟 Key 来源。
const (
	ArkSourceTenant   = "tenant"
	ArkSourcePlatform = "platform"
)

// ArkCredential is the Volcengine Ark key a tenant's video node uses, as the
// control plane reports it: the tenant's own (Source "tenant") or the
// platform's ("platform"). Configured is false when there is neither. With
// the platform key, Models are the only models the tenant may use. APIKey
// must never be logged.
//
// ArkCredential 是控制面报告的租户视频节点所用的火山方舟 Key：租户自己的（Source 为
// "tenant"）或平台的（"platform"）。两者都没有时 Configured 为 false。使用平台 Key 时，
// Models 是租户唯一可用的模型。APIKey 绝不能写入日志。
type ArkCredential struct {
	Configured bool
	Source     string
	Version    uint64
	APIKey     string
	BaseURL    string
	Models     []string
}

// allows reports whether a video node may name model with this key: any
// model with the tenant's own key, only a listed one with the platform's,
// whose use the platform bills.
//
// allows 报告视频节点能否用这个 Key 指定 model：用租户自己的 Key 时任意模型都可以；用平台
// Key 时只能是列出的模型，平台按它们计费。
func (c ArkCredential) allows(model string) bool {
	if !c.Configured || model == "" {
		return false
	}
	return c.Source == ArkSourceTenant || slices.Contains(c.Models, model)
}

// ArkSource reads the Ark key a tenant's video node uses from the control
// plane; controlplaneclient's ArkClient implements it.
//
// ArkSource 从控制面读取租户视频节点所用的方舟 Key；controlplaneclient 的 ArkClient 实现它。
type ArkSource interface {
	// Ark reads tenantID's key. An error means it could not be read, never
	// that the tenant has none.
	//
	// Ark 读取 tenantID 的 Key。返回错误表示读不到，从不表示租户没有。
	Ark(ctx context.Context, tenantID string) (ArkCredential, error)
}

// arkVideoClient is what a video node asks of Ark; *arkvideo.Client
// implements it.
//
// arkVideoClient 是视频节点向方舟发起的调用；*arkvideo.Client 实现它。
type arkVideoClient interface {
	Create(ctx context.Context, req arkvideo.TaskRequest) (string, error)
	Get(ctx context.Context, id string) (arkvideo.Task, error)
	Delete(ctx context.Context, id string) error
}

// errArkUnavailable means a tenant's Ark key could not be read.
// errArkUnavailable 表示读不到租户的方舟 Key。
var errArkUnavailable = errors.New("httpapi: ark key unavailable")

// arkStore caches each tenant's Ark key and builds clients with it.
//
// arkStore 缓存各租户的方舟 Key，并用它构建客户端。
type arkStore struct {
	source ArkSource
	clock  runtime.Clock
	logger *slog.Logger
	ttl    time.Duration
	// api carries the calls to Ark's API; fetch downloads the results, from
	// URLs Ark hands out, and so refuses internal addresses.
	// api 承载对方舟 API 的调用；fetch 从方舟给出的地址下载结果，因此拒绝内网地址。
	api       *http.Client
	fetch     *http.Client
	newClient func(cfg arkvideo.Config) arkVideoClient

	mu      sync.Mutex
	entries map[string]arkEntry
}

type arkEntry struct {
	cred    ArkCredential
	fetched time.Time
}

func newArkStore(source ArkSource, api, fetch *http.Client, clock runtime.Clock, logger *slog.Logger) *arkStore {
	if fetch == nil {
		fetch = objectstore.GuardedHTTPClient(false)
	}
	return &arkStore{
		source: source, clock: clock, logger: logger, ttl: DefaultArkCacheTTL, api: api, fetch: fetch,
		newClient: func(cfg arkvideo.Config) arkVideoClient { return arkvideo.New(cfg) },
		entries:   map[string]arkEntry{},
	}
}

// credential returns tenantID's Ark key; a zero ArkCredential when the
// Gateway reads no keys at all. errArkUnavailable means it could not be read.
//
// credential 返回 tenantID 的方舟 Key；Gateway 完全不读取 Key 时返回零值 ArkCredential。
// errArkUnavailable 表示读不到。
func (a *arkStore) credential(ctx context.Context, tenantID string) (ArkCredential, error) {
	if a == nil || a.source == nil || tenantID == "" {
		return ArkCredential{}, nil
	}
	now := a.clock.Now()
	a.mu.Lock()
	entry, cached := a.entries[tenantID]
	a.mu.Unlock()
	if cached && now.Sub(entry.fetched) < a.ttl {
		return entry.cred, nil
	}
	cred, err := a.source.Ark(ctx, tenantID)
	if err != nil {
		if ctx.Err() != nil {
			return ArkCredential{}, ctx.Err()
		}
		if cached && now.Sub(entry.fetched) < arkStaleLimit {
			a.logger.Warn("reading a tenant's ark key failed; using the copy read earlier",
				slog.String("tenant_id", tenantID), slog.Any("error", err))
			return entry.cred, nil
		}
		a.logger.Warn("reading a tenant's ark key failed", slog.String("tenant_id", tenantID), slog.Any("error", err))
		return ArkCredential{}, errArkUnavailable
	}
	a.mu.Lock()
	a.entries[tenantID] = arkEntry{cred: cred, fetched: now}
	a.mu.Unlock()
	return cred, nil
}

func (a *arkStore) client(cred ArkCredential) arkVideoClient {
	return a.newClient(arkvideo.Config{APIKey: cred.APIKey, BaseURL: cred.BaseURL, HTTPClient: a.api})
}

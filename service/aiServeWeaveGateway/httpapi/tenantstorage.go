package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"AIServeWeave/common/objectstore"
	"AIServeWeave/common/runtime"
)

// DefaultTenantStorageCacheTTL is how long a tenant's storage settings are
// reused before they are read from the control plane again, and so how long
// a change a tenant saves can take to reach this replica.
//
// DefaultTenantStorageCacheTTL 是租户存储设置在重新向控制面读取之前被复用的时长，也就是租户
// 保存的修改最多多久才到达本副本。
const DefaultTenantStorageCacheTTL = 30 * time.Second

// tenantStorageStaleLimit bounds how long settings already read keep being
// used while the control plane cannot be asked again.
//
// tenantStorageStaleLimit 限定在无法再次询问控制面时，已读到的设置还能继续使用多久。
const tenantStorageStaleLimit = 10 * time.Minute

// TenantStorageConfig is one tenant's own storage as the control plane
// reports it. Configured is false when the tenant has none. Backend carries
// credentials and must never be logged.
//
// TenantStorageConfig 是控制面报告的某个租户的自有存储。租户没有配置时 Configured 为
// false。Backend 含凭据，绝不能写入日志。
type TenantStorageConfig struct {
	TenantID      string
	Configured    bool
	AllowPrivate  bool
	Version       uint64
	Backend       objectstore.Config
	RetentionDays int
}

// TenantStorageSource reads tenants' own storage from the control plane. It
// is an interface for the reason JobPersistClient is; controlplaneclient's
// TenantStorageClient implements it.
//
// TenantStorageSource 从控制面读取租户自有存储。它是接口的理由与 JobPersistClient 相同；
// controlplaneclient 的 TenantStorageClient 实现它。
type TenantStorageSource interface {
	// TenantStorage reads tenantID's storage. An error means it could not
	// be read, never that the tenant has none.
	//
	// TenantStorage 读取 tenantID 的存储。返回错误表示读不到，从不表示租户没有配置。
	TenantStorage(ctx context.Context, tenantID string) (TenantStorageConfig, error)
	// RetainedTenantStorage reads one page of storages with a positive
	// retention after the tenant id after, and the next cursor ("" at the
	// end).
	//
	// RetainedTenantStorage 读取一页保留天数为正、租户 id 排在 after 之后的存储，以及下一页
	// 游标（末页为 ""）。
	RetainedTenantStorage(ctx context.Context, after string) ([]TenantStorageConfig, string, error)
}

// errTenantStorageUnavailable means a tenant's storage settings could not be
// read. A write must then fail rather than land in the deployment's storage,
// which the tenant may have chosen not to use.
//
// errTenantStorageUnavailable 表示读不到租户的存储设置。此时写入必须失败，而不是落进租户
// 可能明确不想使用的部署存储。
var errTenantStorageUnavailable = errors.New("httpapi: tenant storage settings unavailable")

// fileStore decides where a tenant's files are written and read: the
// tenant's own storage when it has one, the deployment's otherwise. Reads
// also try the deployment's storage after the tenant's, for files written
// before the tenant configured its own; keys carry the tenant id and a
// random id, so this never reaches another tenant's object.
//
// fileStore 决定租户文件写到哪里、从哪里读：租户有自有存储时用它，否则用部署存储。读取在
// 租户存储之后还会尝试部署存储，以找到租户配置自有存储之前写入的文件；key 含租户 id 与随机
// id，因此不会读到其他租户的对象。
type fileStore struct {
	deployment objectstore.Backend
	source     TenantStorageSource
	clock      runtime.Clock
	logger     *slog.Logger
	ttl        time.Duration
	build      func(objectstore.Config, bool) (objectstore.Backend, error)

	mu      sync.Mutex
	entries map[string]*tenantStorageEntry
}

type tenantStorageEntry struct {
	cfg     TenantStorageConfig
	backend objectstore.Backend
	fetched time.Time
}

func newFileStore(deployment objectstore.Backend, source TenantStorageSource, clock runtime.Clock, logger *slog.Logger, ttl time.Duration) *fileStore {
	if ttl <= 0 {
		ttl = DefaultTenantStorageCacheTTL
	}
	return &fileStore{
		deployment: deployment, source: source, clock: clock, logger: logger, ttl: ttl,
		build: objectstore.NewTenant, entries: map[string]*tenantStorageEntry{},
	}
}

// tenant returns tenantID's own backend, nil when it has none.
// tenant 返回 tenantID 的自有后端；没有时为 nil。
func (f *fileStore) tenant(ctx context.Context, tenantID string) (objectstore.Backend, error) {
	if f == nil || f.source == nil || tenantID == "" {
		return nil, nil
	}
	now := f.clock.Now()
	f.mu.Lock()
	entry := f.entries[tenantID]
	f.mu.Unlock()
	if entry != nil && now.Sub(entry.fetched) < f.ttl {
		return entry.backend, nil
	}

	cfg, err := f.source.TenantStorage(ctx, tenantID)
	if err != nil {
		if entry != nil && now.Sub(entry.fetched) < tenantStorageStaleLimit {
			f.logger.Warn("reading tenant storage settings failed; using the copy read earlier",
				slog.String("tenant_id", tenantID), slog.Any("error", err))
			return entry.backend, nil
		}
		f.logger.Warn("reading tenant storage settings failed", slog.String("tenant_id", tenantID), slog.Any("error", err))
		return nil, errTenantStorageUnavailable
	}
	next := &tenantStorageEntry{cfg: cfg, fetched: now}
	switch {
	case !cfg.Configured:
	case entry != nil && entry.backend != nil && entry.cfg.Backend == cfg.Backend && entry.cfg.AllowPrivate == cfg.AllowPrivate:
		next.backend = entry.backend
	default:
		backend, err := f.build(cfg.Backend, cfg.AllowPrivate)
		if err != nil {
			f.logger.Warn("a tenant's storage settings do not build a backend", slog.String("tenant_id", tenantID), slog.Any("error", err))
			return nil, errTenantStorageUnavailable
		}
		next.backend = backend
	}
	f.mu.Lock()
	f.entries[tenantID] = next
	f.mu.Unlock()
	return next.backend, nil
}

// writer returns where tenantID's new files go: its own storage, else the
// deployment's (nil when there is none). errTenantStorageUnavailable means
// the tenant's settings could not be read.
//
// writer 返回 tenantID 新文件的去处：其自有存储，否则部署存储（没有时为 nil）。
// errTenantStorageUnavailable 表示读不到租户的设置。
func (f *fileStore) writer(ctx context.Context, tenantID string) (objectstore.Backend, error) {
	if f == nil {
		return nil, nil
	}
	backend, err := f.tenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if backend != nil {
		return backend, nil
	}
	return f.deployment, nil
}

// hasAny reports whether any storage could hold tenantID's files, without
// asking the control plane: false only when there is no deployment storage
// and no tenant storage source.
//
// hasAny 报告是否可能有存储容纳 tenantID 的文件，不询问控制面：只有既没有部署存储、也没有
// 租户存储来源时才为 false。
func (f *fileStore) hasAny(tenantID string) bool {
	return f != nil && (f.deployment != nil || (f.source != nil && tenantID != ""))
}

// open reads key for tenantID from its own storage, then the deployment's.
// ErrNotFound when neither holds it; otherwise the first other failure.
//
// open 先从 tenantID 的自有存储、再从部署存储读取 key。两者都没有时返回 ErrNotFound；否则
// 返回遇到的第一个其他错误。
func (f *fileStore) open(ctx context.Context, tenantID, key string) (io.ReadCloser, objectstore.ObjectInfo, error) {
	if f == nil {
		return nil, objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	var firstErr error
	tenant, err := f.tenant(ctx, tenantID)
	if err != nil {
		firstErr = err
	}
	for _, backend := range []objectstore.Backend{tenant, f.deployment} {
		if backend == nil {
			continue
		}
		body, info, err := backend.Open(ctx, key)
		if err == nil {
			return body, info, nil
		}
		if !errors.Is(err, objectstore.ErrNotFound) && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return nil, objectstore.ObjectInfo{}, firstErr
	}
	return nil, objectstore.ObjectInfo{}, objectstore.ErrNotFound
}

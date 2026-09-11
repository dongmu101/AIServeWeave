// Package gormstore is the PostgreSQL implementation of the control plane's
// store interfaces, on gorm.
//
// It is the only package in the service that knows gorm exists. Everything
// above it depends on the interfaces in store, which is what keeps a change of
// ORM — or a second backing store for one entity — from reaching the logic
// layer.
//
// gormstore 是控制面 store 接口在 PostgreSQL 上的实现，基于 gorm。
//
// 它是本服务中唯一知道 gorm 存在的包。它之上的一切都依赖 store 中的接口，这正是让
// 「更换 ORM」——或者为某个实体接入第二种存储——不会波及 logic 层的原因。
package gormstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/common/quota"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Store implements store.Store against a gorm connection.
//
// Store 基于一个 gorm 连接实现 store.Store。
type Store struct {
	db *gorm.DB
}

// New returns a Store over db.
//
// New 基于 db 返回一个 Store。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// Migrate applies the versioned base schema, preserving existing data.
// Migrate 应用基础表的版本化迁移，并保留已有数据。
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.migrateOne(ctx, "base")
	return err
}

// -----------------------------------------------------------------------
// Tenants
// -----------------------------------------------------------------------

// CreateTenant inserts one tenant.
//
// CreateTenant 插入一个租户。
func (s *Store) CreateTenant(ctx context.Context, tenant *model.Tenant) error {
	return translate(s.db.WithContext(ctx).Create(tenant).Error)
}

// GetTenant reads one tenant by id.
//
// GetTenant 按 id 读取一个租户。
func (s *Store) GetTenant(ctx context.Context, id string) (model.Tenant, error) {
	var out model.Tenant
	err := s.db.WithContext(ctx).Where("id = ?", id).Take(&out).Error
	return out, translate(err)
}

// UpdateTenantLimits writes the tenant's quota. It updates the three columns
// by name rather than saving the struct: a full save would write back every
// column read a moment earlier, turning a concurrent status change into a
// silent rollback.
//
// UpdateTenantLimits 写入租户的配额。它按列名更新那三列，而不是保存整个结构体：整体
// 保存会把片刻之前读到的每一列都写回去，从而把一次并发的状态变更变成一次无声的回滚。
func (s *Store) UpdateTenantLimits(ctx context.Context, id string, limits quota.Limits) error {
	result := s.db.WithContext(ctx).Model(&model.Tenant{}).Where("id = ?", id).Updates(map[string]any{
		"requests_per_minute": limits.RequestsPerMinute,
		"tokens_per_minute":   limits.TokensPerMinute,
		"max_concurrent":      limits.MaxConcurrent,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// -----------------------------------------------------------------------
// Users
// -----------------------------------------------------------------------

// CreateUser inserts one user.
//
// CreateUser 插入一个用户。
func (s *Store) CreateUser(ctx context.Context, user *model.User) error {
	return translate(s.db.WithContext(ctx).Create(user).Error)
}

// GetUserByEmail reads one user by sign-in identifier.
//
// GetUserByEmail 按登录标识读取一个用户。
func (s *Store) GetUserByEmail(ctx context.Context, email string) (model.User, error) {
	var out model.User
	err := s.db.WithContext(ctx).Where("email = ?", email).Take(&out).Error
	return out, translate(err)
}

// ListUsers reads one tenant's users, newest first.
//
// ListUsers 读取某个租户的用户，最新的在前。
func (s *Store) ListUsers(ctx context.Context, tenantID string, query store.ListQuery, filter store.UserFilter) (store.Page[model.User], error) {
	db := s.db.WithContext(ctx).Model(&model.User{}).Where("tenant_id = ?", tenantID)
	if filter.Role != "" {
		db = db.Where("role = ?", filter.Role)
	}
	if filter.Query != "" {
		like := likePattern(filter.Query)
		db = db.Where("LOWER(email) LIKE ? OR LOWER(name) LIKE ?", like, like)
	}
	return readPage(db, query, func(u model.User) (time.Time, string) { return u.CreatedAt, u.ID })
}

// MarkUserLogin records a successful sign-in.
//
// MarkUserLogin 记录一次成功登录。
func (s *Store) MarkUserLogin(ctx context.Context, id string, at time.Time) error {
	return translate(s.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ?", id).
		Update("last_login_at", at).Error)
}

// -----------------------------------------------------------------------
// Platform operators (STATUS.md's P01)
// -----------------------------------------------------------------------

// CreatePlatformOperator inserts one platform operator.
//
// CreatePlatformOperator 插入一个平台运维账户。
func (s *Store) CreatePlatformOperator(ctx context.Context, operator *model.PlatformOperator) error {
	return translate(s.db.WithContext(ctx).Create(operator).Error)
}

// GetPlatformOperatorByEmail reads one platform operator by sign-in identifier.
//
// GetPlatformOperatorByEmail 按登录标识读取一个平台运维账户。
func (s *Store) GetPlatformOperatorByEmail(ctx context.Context, email string) (model.PlatformOperator, error) {
	var out model.PlatformOperator
	err := s.db.WithContext(ctx).Where("email = ?", email).Take(&out).Error
	return out, translate(err)
}

// MarkPlatformOperatorLogin records a successful sign-in.
//
// MarkPlatformOperatorLogin 记录一次成功登录。
func (s *Store) MarkPlatformOperatorLogin(ctx context.Context, id string, at time.Time) error {
	return translate(s.db.WithContext(ctx).
		Model(&model.PlatformOperator{}).
		Where("id = ?", id).
		Update("last_login_at", at).Error)
}

// -----------------------------------------------------------------------
// API keys
// -----------------------------------------------------------------------

// CreateAPIKey inserts one key.
//
// CreateAPIKey 插入一个 key。
func (s *Store) CreateAPIKey(ctx context.Context, key *model.APIKey) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var creator model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND status = ?", key.CreatedBy, key.TenantID, model.StatusActive).
			Take(&creator).Error; err != nil {
			return err
		}
		return tx.Create(key).Error
	})
	return translate(err)
}

// GetAPIKeyByHash reads one key by its stored hash. This is the Gateway's
// verification path and the hottest query in the service, which is why Hash
// carries a unique index rather than a plain one: the lookup must be an index
// probe, never a scan.
//
// GetAPIKeyByHash 按存储的哈希读取一个 key。这是 Gateway 的校验路径，也是本服务中
// 最热的查询，这正是 Hash 上是唯一索引而不是普通索引的原因：这次查找必须是一次索引
// 探测，绝不能是扫描。
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (model.APIKey, error) {
	var out model.APIKey
	err := s.db.WithContext(ctx).Where("hash = ?", hash).Take(&out).Error
	return out, translate(err)
}

// ListAPIKeys reads one tenant's keys, newest first.
//
// ListAPIKeys 读取某个租户的 key，最新的在前。
func (s *Store) ListAPIKeys(ctx context.Context, tenantID string, query store.ListQuery, filter store.APIKeyFilter) (store.Page[model.APIKey], error) {
	db := s.db.WithContext(ctx).Model(&model.APIKey{}).Where("tenant_id = ?", tenantID)
	if filter.Status != "" {
		db = db.Where("status = ?", filter.Status)
	}
	if filter.Query != "" {
		like := likePattern(filter.Query)
		db = db.Where("LOWER(name) LIKE ? OR LOWER(display) LIKE ?", like, like)
	}
	return readPage(db, query, func(k model.APIKey) (time.Time, string) { return k.CreatedAt, k.ID })
}

// GetAPIKey reads one key by id, scoped to its tenant. The tenant is part of
// the WHERE clause rather than a check on the result: another tenant's id
// must miss, not match and then be rejected.
//
// GetAPIKey 按 id 读取一个 key，并限定在其租户范围内。租户是 WHERE 子句的一部分，
// 而不是对结果的一次检查：别的租户的 id 必须查不到，而不是先查到再被拒绝。
func (s *Store) GetAPIKey(ctx context.Context, tenantID, id string) (model.APIKey, error) {
	var key model.APIKey
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&key).Error
	return key, translate(err)
}

// RevokeAPIKey marks one key revoked, scoped to its tenant.
//
// The update is conditional on the key still being active, and a zero
// RowsAffected is reported as ErrNotFound. That makes revocation idempotent in
// the way that matters: a second revoke does not silently rewrite the
// timestamp of the first, so the audit trail keeps the moment the key actually
// stopped working.
//
// RevokeAPIKey 将一个 key 标记为已吊销，并限定在其租户范围内。
//
// 这次更新以「该 key 仍处于 active」为条件，RowsAffected 为零时报 ErrNotFound。这让
// 吊销在真正要紧的意义上具备幂等性：第二次吊销不会悄悄改写第一次的时间戳，因此审计
// 线索保留的是该 key 实际停止工作的那一刻。
func (s *Store) RevokeAPIKey(ctx context.Context, tenantID, id string, at time.Time) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.APIKey{}).
			Where("id = ? AND tenant_id = ? AND status = ?", id, tenantID, model.StatusActive).
			Updates(map[string]any{"status": model.StatusRevoked, "revoked_at": at})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return store.ErrNotFound
		}
		return enqueueRevocation(tx)
	})
	return translate(err)
}

// MarkAPIKeyUsed records a coarse last-used timestamp.
//
// The Gateway must not call this on every request: it would turn a read-only
// verification into a write on the hottest row in the schema, and inference
// traffic would spend its latency budget updating a column nobody reads in
// real time. The caller rate limits it — see the Gateway's verifier — and this
// method stays a plain unconditional update so a late write from a slower
// replica cannot fail.
//
// MarkAPIKeyUsed 记录一个粗粒度的最后使用时间。
//
// Gateway 不得在每次请求都调用它：那会把一次只读校验变成对 schema 中最热的那一行的
// 写入，推理流量会把自己的延迟预算花在更新一个没人实时读的列上。频率由调用方控制
// ——见 Gateway 侧的 verifier——而这个方法保持为一次朴素的无条件更新，好让来自较慢
// 副本的迟到写入不会失败。
func (s *Store) MarkAPIKeyUsed(ctx context.Context, id string, at time.Time) error {
	return translate(s.db.WithContext(ctx).
		Model(&model.APIKey{}).
		Where("id = ?", id).
		Update("last_used_at", at).Error)
}

// -----------------------------------------------------------------------
// Audit
// -----------------------------------------------------------------------

// AppendAudit inserts one audit record.
//
// AppendAudit 插入一条审计记录。
func (s *Store) AppendAudit(ctx context.Context, entry *model.AuditLog) error {
	return translate(s.db.WithContext(ctx).Create(entry).Error)
}

// ListAudit reads one tenant's audit trail, newest first.
//
// ListAudit 读取某个租户的审计线索，最新的在前。
func (s *Store) ListAudit(ctx context.Context, tenantID string, query store.ListQuery, filter store.AuditFilter) (store.Page[model.AuditLog], error) {
	db := s.db.WithContext(ctx).Model(&model.AuditLog{}).Where("tenant_id = ?", tenantID)
	if filter.Action != "" {
		db = db.Where("action = ?", filter.Action)
	}
	if filter.ActorID != "" {
		db = db.Where("actor_id = ?", filter.ActorID)
	}
	if !filter.Since.IsZero() {
		db = db.Where("created_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		db = db.Where("created_at < ?", filter.Until)
	}
	return readPage(db, query, func(e model.AuditLog) (time.Time, string) { return e.CreatedAt, e.ID })
}

// readPage applies the keyset cursor, the ordering and the page size, and
// reports whether another page follows.
//
// The cursor comparison is written out as two predicates rather than as the
// row-value form `(created_at, id) < (?, ?)`: both supported engines accept
// the row-value syntax, but the expanded form is what an index on
// (tenant_id, created_at, id) is used for on either of them, and it is the
// form somebody debugging a slow query will recognize.
//
// One extra row is fetched, never returned, and used only to decide whether
// there is a next page. Counting the table instead would put the query that
// gets slow first on every page load.
//
// readPage 施加 keyset 游标、排序与分页大小，并报告后面是否还有一页。
//
// 游标比较写成两个谓词，而不是行值形式 `(created_at, id) < (?, ?)`：两种受支持的引擎
// 都接受行值语法，但展开后的形式才是 (tenant_id, created_at, id) 索引在两者上都会被用到
// 的写法，也是排查慢查询的人一眼能认出的写法。
//
// 这里会多取一行，它绝不会被返回，只用来判断是否还有下一页。改为统计整张表，等于把
// 最先变慢的那种查询放在每一次翻页上。
func readPage[T any](db *gorm.DB, query store.ListQuery, key func(T) (time.Time, string)) (store.Page[T], error) {
	at, id, err := store.DecodeCursor(query.Cursor)
	if err != nil {
		return store.Page[T]{}, err
	}
	if query.Cursor != "" {
		db = db.Where("created_at < ? OR (created_at = ? AND id < ?)", at, at, id)
	}

	size := query.Size()
	var out []T
	if err := db.Order("created_at DESC, id DESC").Limit(size + 1).Find(&out).Error; err != nil {
		return store.Page[T]{}, translate(err)
	}
	if len(out) <= size {
		return store.Page[T]{Items: out}, nil
	}
	lastAt, lastID := key(out[size-1])
	return store.Page[T]{Items: out[:size], NextCursor: store.EncodeCursor(lastAt, lastID)}, nil
}

// likePattern builds a case-insensitive contains pattern, escaping the two
// wildcards so a query containing them filters for them rather than with them.
//
// likePattern 构造一个不区分大小写的「包含」模式，并转义两个通配符，好让含有它们的
// 查询是在筛选它们，而不是拿它们来筛选。
func likePattern(query string) string {
	escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(strings.ToLower(query))
	return "%" + escaped + "%"
}

// translate maps gorm's errors onto the store package's vocabulary, so no
// caller above this package imports gorm to interpret a failure.
//
// translate 把 gorm 的错误映射到 store 包的词汇上，这样本包之上的调用方都不必为了
// 解释一个失败而去 import gorm。
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return store.ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return store.ErrConflict
	default:
		return err
	}
}

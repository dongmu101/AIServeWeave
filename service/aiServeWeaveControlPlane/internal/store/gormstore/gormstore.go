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
	"time"

	"gorm.io/gorm"

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

// Migrate creates or updates the four tables this service owns.
//
// AutoMigrate is a deliberate starting point, not the end state: it cannot
// express a down migration, it will not drop a column, and it leaves no
// record of what ran when. It is enough while this service owns four tables
// and no production data; the moment either changes, this becomes versioned
// SQL files. That transition is named in the service README so it is a
// scheduled decision rather than a surprise.
//
// Migrate 创建或更新本服务拥有的四张表。
//
// 用 AutoMigrate 是一个有意为之的起点，而非终态：它无法表达回滚迁移，不会删除列，
// 也不留下「何时执行了什么」的记录。在本服务只拥有四张表、且没有生产数据期间它足够
// 用；一旦其中任何一条不再成立，这里就要换成带版本的 SQL 文件。这个切换点写在服务
// README 里，好让它成为一个排上日程的决定，而不是一次意外。
func (s *Store) Migrate(ctx context.Context) error {
	return s.db.WithContext(ctx).AutoMigrate(
		&model.Tenant{},
		&model.User{},
		&model.APIKey{},
		&model.AuditLog{},
	)
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
func (s *Store) ListUsers(ctx context.Context, tenantID string) ([]model.User, error) {
	var out []model.User
	err := s.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Find(&out).Error
	return out, translate(err)
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
// API keys
// -----------------------------------------------------------------------

// CreateAPIKey inserts one key.
//
// CreateAPIKey 插入一个 key。
func (s *Store) CreateAPIKey(ctx context.Context, key *model.APIKey) error {
	return translate(s.db.WithContext(ctx).Create(key).Error)
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
func (s *Store) ListAPIKeys(ctx context.Context, tenantID string) ([]model.APIKey, error) {
	var out []model.APIKey
	err := s.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Find(&out).Error
	return out, translate(err)
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
	result := s.db.WithContext(ctx).
		Model(&model.APIKey{}).
		Where("id = ? AND tenant_id = ? AND status = ?", id, tenantID, model.StatusActive).
		Updates(map[string]any{"status": model.StatusRevoked, "revoked_at": at})
	if err := translate(result.Error); err != nil {
		return err
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
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
func (s *Store) ListAudit(ctx context.Context, tenantID string, limit int) ([]model.AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []model.AuditLog
	err := s.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at DESC").
		Limit(limit).
		Find(&out).Error
	return out, translate(err)
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

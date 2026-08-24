// Package store is the control plane's persistence boundary: four narrow
// interfaces the logic layer depends on, and the errors they agree to speak.
//
// The interfaces exist so the logic layer can be tested without a database.
// They are split by entity rather than gathered into one wide Store, because a
// handler that only revokes keys should not be handed something that can also
// delete tenants.
//
// Every method that reads or writes a tenant-owned row takes tenantID as its
// first argument, and every implementation applies it as a filter rather than
// as a check after the fact. That is what tenant isolation is here: not a
// rule handlers must remember, but a parameter they cannot omit.
//
// store 包是控制面的持久化边界：logic 层所依赖的四个窄接口，以及它们约定使用的错误。
//
// 这些接口的存在是为了让 logic 层无需数据库即可测试。它们按实体拆分而不是合并成一个
// 宽大的 Store，因为一个只负责吊销 key 的 handler，不应该拿到一个还能删除租户的东西。
//
// 每个读写租户所属行的方法都以 tenantID 作为第一个参数，且每个实现都把它作为过滤条件
// 使用，而不是事后再做一次检查。这就是这里的租户隔离：它不是 handler 必须记住的规则，
// 而是它们无法省略的参数。
package store

import (
	"context"
	"errors"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// ErrNotFound is returned for a row that does not exist, or that exists but
// belongs to another tenant. The two are deliberately indistinguishable: a
// caller who can tell "no such key" from "not your key" can enumerate the ids
// of every other tenant.
//
// ErrNotFound 用于不存在的行，也用于存在但属于其他租户的行。两者刻意无法区分：能够
// 分辨「没有这个 key」与「这不是你的 key」的调用方，可以据此枚举出其他每个租户的 id。
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when a write would violate a uniqueness constraint —
// a duplicate email, or the astronomically unlikely duplicate key hash.
//
// ErrConflict 用于会违反唯一性约束的写入——重复的 email，或者概率低到近乎不可能的
// 重复 key 哈希。
var ErrConflict = errors.New("store: conflict")

// Tenants persists isolation boundaries.
//
// Tenants 持久化隔离边界。
type Tenants interface {
	CreateTenant(ctx context.Context, tenant *model.Tenant) error
	GetTenant(ctx context.Context, id string) (model.Tenant, error)
}

// Users persists the people who sign in to the Console.
//
// Users 持久化登录 Console 的人。
type Users interface {
	CreateUser(ctx context.Context, user *model.User) error
	// GetUserByEmail is the sign-in lookup, and the one read in this package
	// that is not scoped by tenant: at sign-in time nobody has said which
	// tenant they belong to yet, which is exactly what this call determines.
	//
	// GetUserByEmail 是登录时的查询，也是本包中唯一不按租户限定范围的读取：登录时
	// 还没有人说明自己属于哪个租户，而这正是这次调用要确定的事。
	GetUserByEmail(ctx context.Context, email string) (model.User, error)
	ListUsers(ctx context.Context, tenantID string) ([]model.User, error)
	MarkUserLogin(ctx context.Context, id string, at time.Time) error
}

// APIKeys persists the credentials callers present to the Gateway.
//
// APIKeys 持久化调用方向 Gateway 出示的凭据。
type APIKeys interface {
	CreateAPIKey(ctx context.Context, key *model.APIKey) error
	// GetAPIKeyByHash is the Gateway's verification lookup. It is not scoped
	// by tenant because the hash is what determines the tenant — that is the
	// whole point of the call.
	//
	// GetAPIKeyByHash 是 Gateway 的校验查询。它不按租户限定范围，因为哈希本身就是
	// 用来确定租户的——这正是这次调用的全部意义。
	GetAPIKeyByHash(ctx context.Context, hash string) (model.APIKey, error)
	ListAPIKeys(ctx context.Context, tenantID string) ([]model.APIKey, error)
	RevokeAPIKey(ctx context.Context, tenantID, id string, at time.Time) error
	// MarkAPIKeyUsed records a coarse last-used timestamp. Callers must rate
	// limit it themselves; see the gorm implementation for why this is not
	// written on every request.
	//
	// MarkAPIKeyUsed 记录一个粗粒度的最后使用时间。调用方必须自己做频率控制；不在
	// 每次请求都写入的原因见 gorm 实现。
	MarkAPIKeyUsed(ctx context.Context, id string, at time.Time) error
}

// Audit appends administrative actions. It has no update and no delete, which
// is the interface stating the table's append-only rule in a form no
// implementation can quietly break.
//
// Audit 追加管理操作记录。它没有更新也没有删除，这是接口以一种任何实现都无法悄悄
// 违反的形式，陈述了该表只追加的规则。
type Audit interface {
	AppendAudit(ctx context.Context, entry *model.AuditLog) error
	ListAudit(ctx context.Context, tenantID string, limit int) ([]model.AuditLog, error)
}

// Store is every persistence capability the service has, for wiring at
// startup. Handlers and logic take the narrow interfaces above, never this.
//
// Store 是本服务全部的持久化能力，供启动时装配使用。handler 与 logic 取用上面那些
// 窄接口，绝不取用它。
type Store interface {
	Tenants
	Users
	APIKeys
	Audit
}

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
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"AIServeWeave/common/quota"
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

// ErrInvalidCursor is returned for a cursor this package did not produce, or
// one that has been edited. It is separate from ErrNotFound because a caller
// can act on it — start over from the first page — whereas a missing row means
// something else entirely.
//
// ErrInvalidCursor 用于本包并未产生、或者被改动过的游标。它与 ErrNotFound 分开，因为
// 调用方可以据此行动——从第一页重新开始——而一行记录的缺失完全是另一回事。
var ErrInvalidCursor = errors.New("store: invalid cursor")

// DefaultPageSize and MaxPageSize bound one list read.
//
// The cap is not a suggestion: these lists grow without limit — an audit trail
// most of all — and a request that could ask for all of it would let one
// caller pull an unbounded result set into this process's memory, and from
// there into the Console's. A caller that wants more pages for more.
//
// DefaultPageSize 与 MaxPageSize 限制单次列表读取。
//
// 这个上限不是建议：这些列表会无限增长——审计线索尤甚——而一个能索取全部的请求，会让
// 单个调用方把一个无界的结果集拉进本进程的内存，再从那里拉进 Console 的内存。想要更多
// 的调用方去翻页。
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// Page is one slice of a list, together with the cursor that continues it.
//
// NextCursor is empty when this page is the last one. It is deliberately not a
// total count: counting the whole table on every page is the query that gets
// slow first, and a total is stale by the time it is rendered anyway.
//
// Page 是一份列表中的一段，连同能够接着往下取的游标。
//
// 这一页是最后一页时 NextCursor 为空。它刻意不是总数：在每一页都统计整张表，是最先变慢
// 的那种查询，而且总数在渲染出来的时候本来就已经过期了。
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// ListQuery bounds one list read and says where to continue from.
//
// ListQuery 限制一次列表读取的规模，并说明从哪里接着读。
type ListQuery struct {
	// Limit is how many rows the caller wants. Zero means DefaultPageSize,
	// and anything above MaxPageSize is clamped rather than refused: a
	// caller asking for too much gets a smaller page, not an error page.
	//
	// Limit 是调用方想要的行数。零表示 DefaultPageSize，超过 MaxPageSize 会被截断
	// 而不是被拒绝：索取过多的调用方得到的是一页更少的数据，而不是一页错误。
	Limit int
	// Cursor continues a previous page. Empty starts at the newest row.
	//
	// Cursor 从上一页继续。为空则从最新的一行开始。
	Cursor string
}

// Size returns the effective page size, clamped.
//
// Size 返回生效的分页大小，已做截断。
func (q ListQuery) Size() int {
	switch {
	case q.Limit <= 0:
		return DefaultPageSize
	case q.Limit > MaxPageSize:
		return MaxPageSize
	default:
		return q.Limit
	}
}

// UserFilter narrows a user list. An empty field does not filter.
//
// UserFilter 收窄用户列表。字段为空表示不筛选。
type UserFilter struct {
	Role string
	// Query matches an email or a name, case-insensitively, as a substring.
	//
	// Query 以不区分大小写的子串匹配 email 或姓名。
	Query string
}

// APIKeyFilter narrows a key list. An empty field does not filter.
//
// APIKeyFilter 收窄 key 列表。字段为空表示不筛选。
type APIKeyFilter struct {
	Status string
	// Query matches a key's name or its display form. It never matches the
	// hash: a filter that did would be an oracle for confirming a guessed
	// credential.
	//
	// Query 匹配 key 的名称或其 display 形式。它绝不匹配哈希：会匹配哈希的筛选，
	// 等于给「猜一个凭据并求证」提供了一个预言机。
	Query string
}

// AuditFilter narrows an audit list. A zero time does not bound that end.
//
// AuditFilter 收窄审计列表。时间为零值表示该端不设边界。
type AuditFilter struct {
	Action  string
	ActorID string
	// Since is inclusive and Until is exclusive, so consecutive windows
	// tile without overlapping and without dropping a row between them.
	//
	// Since 含端点、Until 不含，因此相邻的时间窗可以无缝拼接：既不重叠，也不会在
	// 两者之间漏掉某一行。
	Since time.Time
	Until time.Time
}

// EncodeCursor builds the cursor for the row a page ended on.
//
// It is keyset rather than an offset: an offset shifts every time a row is
// inserted ahead of it, and these lists are written to while they are being
// read — an audit trail continuously. With an offset, paging through an audit
// list during activity skips rows and repeats others, silently.
//
// The value is not signed. It is not a capability: it names a position in a
// list the caller is already authorized to read, and every query it feeds is
// still scoped by tenant. Editing it can only move the reader within their own
// tenant's rows, which they could reach by paging anyway.
//
// EncodeCursor 为某一页结束处的那一行构造游标。
//
// 它是 keyset 而不是 offset：只要有新行插到前面，offset 就会整体错位，而这些列表正是
// 在被读取的同时被写入——审计线索更是持续不断。用 offset，在有活动的情况下翻阅审计列表
// 会跳过一些行、重复另一些行，而且悄无声息。
//
// 这个值没有签名。它不是一份凭能：它指出的是一个调用方本来就有权读取的列表中的位置，
// 而以它为输入的每个查询依然按租户限定范围。改动它，至多只能把读取者移到自己租户的
// 另一些行上，而那些行他本来翻页也能到达。
func EncodeCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id),
	)
}

// DecodeCursor reads a cursor back. An empty cursor decodes to the zero
// position, which every implementation reads as "start at the newest row".
//
// DecodeCursor 读回一个游标。空游标解码为零位置，每个实现都把它读作「从最新的一行
// 开始」。
func DecodeCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return time.Time{}, "", ErrInvalidCursor
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return parsed, id, nil
}

// Tenants persists isolation boundaries.
//
// Tenants 持久化隔离边界。
type Tenants interface {
	CreateTenant(ctx context.Context, tenant *model.Tenant) error
	GetTenant(ctx context.Context, id string) (model.Tenant, error)
	// UpdateTenantLimits writes the tenant's quota. It takes the whole set
	// rather than one dimension: a partial update would need a way to say
	// "leave this one alone" that is distinct from "set it to unlimited",
	// and zero already means unlimited.
	//
	// UpdateTenantLimits 写入租户的配额。它接受整组而不是单个维度：部分更新需要一种
	// 区别于「设为不限制」的方式来表达「这个不动」，而零已经表示不限制了。
	UpdateTenantLimits(ctx context.Context, id string, limits quota.Limits) error
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
	ListUsers(ctx context.Context, tenantID string, query ListQuery, filter UserFilter) (Page[model.User], error)
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
	// GetAPIKey reads one key by id, scoped to its tenant. It exists so a
	// revocation can find its target with a lookup rather than by walking
	// the tenant's list — which stopped being possible the moment that list
	// became a page.
	//
	// GetAPIKey 按 id 读取一个 key，并限定在其租户范围内。它的存在是为了让一次吊销
	// 能通过查询找到目标，而不是遍历该租户的列表——自从那个列表变成一页之后，遍历
	// 就已经不可行了。
	GetAPIKey(ctx context.Context, tenantID, id string) (model.APIKey, error)
	ListAPIKeys(ctx context.Context, tenantID string, query ListQuery, filter APIKeyFilter) (Page[model.APIKey], error)
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
	ListAudit(ctx context.Context, tenantID string, query ListQuery, filter AuditFilter) (Page[model.AuditLog], error)
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

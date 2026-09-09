// Package memstore is an in-memory store.Store for tests.
//
// It exists so the logic layer's tests state business rules rather than
// database setup: a test about "a revoked key stops authenticating" should not
// need a PostgreSQL instance to say so. The gorm implementation is verified
// separately against a real database (see gormstore's live test), because the
// two answer different questions — this one answers whether the logic is
// right, that one answers whether the SQL is.
//
// It enforces the same tenant scoping and the same uniqueness constraints as
// the real store. A fake that is more permissive than production is a fake
// that makes tests pass for code that would fail.
//
// memstore 是供测试使用的内存版 store.Store。
//
// 它的存在是为了让 logic 层的测试陈述业务规则，而不是陈述数据库准备工作：一个关于
// 「被吊销的 key 不再能通过认证」的测试，不该为了说清这件事而需要一个 PostgreSQL 实例。
// gorm 实现另有针对真实数据库的验证（见 gormstore 的 live 测试），因为两者回答的是不同
// 的问题——这边回答逻辑对不对，那边回答 SQL 对不对。
//
// 它执行与真实 store 相同的租户限定与相同的唯一性约束。一个比生产实现更宽松的假件，
// 会让本该失败的代码通过测试。
package memstore

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"AIServeWeave/common/quota"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Store is an in-memory store.Store. The zero value is not usable; use New.
//
// Store 是内存版 store.Store。零值不可用，请使用 New。
type Store struct {
	mu                sync.Mutex
	routes            []model.RouteRevision
	workflowTemplates []model.WorkflowTemplateRevision
	tenants           map[string]model.Tenant
	users             map[string]model.User
	platformOperators map[string]model.PlatformOperator
	keys              map[string]model.APIKey
	audit             []model.AuditLog
	jobs              map[string]model.Job
	artifacts         map[string]model.JobArtifact
}

// New returns an empty store.
//
// New 返回一个空的 store。
func New() *Store {
	return &Store{
		tenants:           map[string]model.Tenant{},
		users:             map[string]model.User{},
		platformOperators: map[string]model.PlatformOperator{},
		keys:              map[string]model.APIKey{},
		jobs:              map[string]model.Job{},
		artifacts:         map[string]model.JobArtifact{},
	}
}

var _ store.Store = (*Store)(nil)

// CreateTenant inserts one tenant.
//
// CreateTenant 插入一个租户。
func (s *Store) CreateTenant(_ context.Context, tenant *model.Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tenants[tenant.ID]; exists {
		return store.ErrConflict
	}
	stamp(&tenant.CreatedAt, &tenant.UpdatedAt)
	s.tenants[tenant.ID] = *tenant
	return nil
}

// GetTenant reads one tenant by id.
//
// GetTenant 按 id 读取一个租户。
func (s *Store) GetTenant(_ context.Context, id string) (model.Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tenant, ok := s.tenants[id]
	if !ok {
		return model.Tenant{}, store.ErrNotFound
	}
	return tenant, nil
}

// PutTenant overwrites a tenant row wholesale. It exists for tests that need a
// tenant in a state no logic-layer method produces yet — suspension, for
// instance, has no admin endpoint. It is on this store only: the gorm store
// has no equivalent, and nothing outside a test may call it.
//
// PutTenant 整行覆盖一个租户。它为那些需要把租户置于 logic 层方法尚无法产生的状态的
// 测试而存在——比如「暂停」目前还没有对应的管理端点。它只属于本 store：gorm 那个没有
// 等价物，且测试之外的任何地方都不得调用它。
func (s *Store) PutTenant(tenant model.Tenant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[tenant.ID] = tenant
}

// UpdateTenantLimits writes the tenant's quota.
//
// UpdateTenantLimits 写入租户的配额。
func (s *Store) UpdateTenantLimits(_ context.Context, id string, limits quota.Limits) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tenant, ok := s.tenants[id]
	if !ok {
		return store.ErrNotFound
	}
	tenant.RequestsPerMinute = limits.RequestsPerMinute
	tenant.TokensPerMinute = limits.TokensPerMinute
	tenant.MaxConcurrent = limits.MaxConcurrent
	stamp(&tenant.CreatedAt, &tenant.UpdatedAt)
	s.tenants[id] = tenant
	return nil
}

// CreateUser inserts one user, rejecting a duplicate email the way the unique
// index does.
//
// CreateUser 插入一个用户，并像唯一索引那样拒绝重复的 email。
func (s *Store) CreateUser(_ context.Context, user *model.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.users {
		if existing.Email == user.Email {
			return store.ErrConflict
		}
	}
	stamp(&user.CreatedAt, &user.UpdatedAt)
	s.users[user.ID] = *user
	return nil
}

// GetUserByEmail reads one user by sign-in identifier.
//
// GetUserByEmail 按登录标识读取一个用户。
func (s *Store) GetUserByEmail(_ context.Context, email string) (model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.users {
		if user.Email == email {
			return user, nil
		}
	}
	return model.User{}, store.ErrNotFound
}

// ListUsers reads one tenant's users, newest first.
//
// ListUsers 读取某个租户的用户，最新的在前。
func (s *Store) ListUsers(_ context.Context, tenantID string, query store.ListQuery, filter store.UserFilter) (store.Page[model.User], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.User
	for _, user := range s.users {
		if user.TenantID != tenantID {
			continue
		}
		if filter.Role != "" && user.Role != filter.Role {
			continue
		}
		if !matches(filter.Query, user.Email, user.Name) {
			continue
		}
		out = append(out, user)
	}
	sortNewestFirst(out, func(u model.User) (time.Time, string) { return u.CreatedAt, u.ID })
	return paginate(out, query, func(u model.User) (time.Time, string) { return u.CreatedAt, u.ID })
}

// MarkUserLogin records a successful sign-in.
//
// MarkUserLogin 记录一次成功登录。
func (s *Store) MarkUserLogin(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return store.ErrNotFound
	}
	user.LastLoginAt = &at
	s.users[id] = user
	return nil
}

// CreatePlatformOperator inserts one platform operator, rejecting a duplicate
// email the way the unique index does.
//
// CreatePlatformOperator 插入一个平台运维账户，并像唯一索引那样拒绝重复的 email。
func (s *Store) CreatePlatformOperator(_ context.Context, operator *model.PlatformOperator) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.platformOperators {
		if existing.Email == operator.Email {
			return store.ErrConflict
		}
	}
	stamp(&operator.CreatedAt, &operator.UpdatedAt)
	s.platformOperators[operator.ID] = *operator
	return nil
}

// GetPlatformOperatorByEmail reads one platform operator by sign-in identifier.
//
// GetPlatformOperatorByEmail 按登录标识读取一个平台运维账户。
func (s *Store) GetPlatformOperatorByEmail(_ context.Context, email string) (model.PlatformOperator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, operator := range s.platformOperators {
		if operator.Email == email {
			return operator, nil
		}
	}
	return model.PlatformOperator{}, store.ErrNotFound
}

// MarkPlatformOperatorLogin records a successful sign-in.
//
// MarkPlatformOperatorLogin 记录一次成功登录。
func (s *Store) MarkPlatformOperatorLogin(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	operator, ok := s.platformOperators[id]
	if !ok {
		return store.ErrNotFound
	}
	operator.LastLoginAt = &at
	s.platformOperators[id] = operator
	return nil
}

// CreateAPIKey inserts one key, rejecting a duplicate hash the way the unique
// index does.
//
// CreateAPIKey 插入一个 key，并像唯一索引那样拒绝重复的哈希。
func (s *Store) CreateAPIKey(_ context.Context, key *model.APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.keys {
		if existing.Hash == key.Hash {
			return store.ErrConflict
		}
	}
	stamp(&key.CreatedAt, &key.UpdatedAt)
	s.keys[key.ID] = *key
	return nil
}

// GetAPIKeyByHash reads one key by its stored hash.
//
// GetAPIKeyByHash 按存储的哈希读取一个 key。
func (s *Store) GetAPIKeyByHash(_ context.Context, hash string) (model.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range s.keys {
		if key.Hash == hash {
			return key, nil
		}
	}
	return model.APIKey{}, store.ErrNotFound
}

// ListAPIKeys reads one tenant's keys, newest first.
//
// ListAPIKeys 读取某个租户的 key，最新的在前。
func (s *Store) ListAPIKeys(_ context.Context, tenantID string, query store.ListQuery, filter store.APIKeyFilter) (store.Page[model.APIKey], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.APIKey
	for _, key := range s.keys {
		if key.TenantID != tenantID {
			continue
		}
		if filter.Status != "" && key.Status != filter.Status {
			continue
		}
		if !matches(filter.Query, key.Name, key.Display) {
			continue
		}
		out = append(out, key)
	}
	sortNewestFirst(out, func(k model.APIKey) (time.Time, string) { return k.CreatedAt, k.ID })
	return paginate(out, query, func(k model.APIKey) (time.Time, string) { return k.CreatedAt, k.ID })
}

// GetAPIKey reads one key by id, scoped to its tenant.
//
// GetAPIKey 按 id 读取一个 key，并限定在其租户范围内。
func (s *Store) GetAPIKey(_ context.Context, tenantID, id string) (model.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[id]
	if !ok || key.TenantID != tenantID {
		return model.APIKey{}, store.ErrNotFound
	}
	return key, nil
}

// RevokeAPIKey marks one active key revoked, scoped to its tenant.
//
// RevokeAPIKey 将一个处于 active 的 key 标记为已吊销，并限定在其租户范围内。
func (s *Store) RevokeAPIKey(_ context.Context, tenantID, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[id]
	if !ok || key.TenantID != tenantID || key.Status != model.StatusActive {
		return store.ErrNotFound
	}
	key.Status = model.StatusRevoked
	key.RevokedAt = &at
	s.keys[id] = key
	return nil
}

// MarkAPIKeyUsed records a coarse last-used timestamp.
//
// MarkAPIKeyUsed 记录一个粗粒度的最后使用时间。
func (s *Store) MarkAPIKeyUsed(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[id]
	if !ok {
		return store.ErrNotFound
	}
	key.LastUsedAt = &at
	s.keys[id] = key
	return nil
}

// ReplacePlatformOperator overwrites one stored platform operator, for the
// same reason ReplaceUser exists — a suspended platform operator account, in
// particular, which no handler can currently produce.
//
// ReplacePlatformOperator 覆写一个已存储的平台运维账户，理由与 ReplaceUser
// 相同——尤其是一个已停用的平台运维账户，目前没有任何 handler 能产生它。
func (s *Store) ReplacePlatformOperator(operator model.PlatformOperator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.platformOperators[operator.ID] = operator
}

// ReplaceUser overwrites one stored user. Like ReplaceAPIKey, it exists only
// so a test can construct a state the API cannot reach — a suspended account,
// for instance, which no handler can currently produce.
//
// ReplaceUser 覆写一个已存储的用户。与 ReplaceAPIKey 一样，它的存在只是为了让测试
// 构造出 API 无法抵达的状态——例如一个已停用的账户，目前没有任何 handler 能产生它。
func (s *Store) ReplaceUser(user model.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[user.ID] = user
}

// ReplaceAPIKey overwrites one stored key. It is not part of store.APIKeys and
// never will be: it exists so a test can construct a state the API cannot
// reach directly — a key created by a member, say, when members are not
// allowed to mint one. Production code has no business setting a row wholesale.
//
// ReplaceAPIKey 覆写一个已存储的 key。它不属于 store.APIKeys，将来也不会属于：它的
// 存在是为了让测试构造出 API 无法直接抵达的状态——比如一个由 member 创建的 key，
// 而 member 本身并不被允许铸造 key。生产代码没有理由整行设置一条记录。
func (s *Store) ReplaceAPIKey(key model.APIKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[key.ID] = key
}

// AppendAudit appends one audit record.
//
// AppendAudit 追加一条审计记录。
func (s *Store) AppendAudit(_ context.Context, entry *model.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	s.audit = append(s.audit, *entry)
	return nil
}

// ListAudit reads one tenant's audit trail, newest first.
//
// ListAudit 读取某个租户的审计线索，最新的在前。
func (s *Store) ListAudit(_ context.Context, tenantID string, query store.ListQuery, filter store.AuditFilter) (store.Page[model.AuditLog], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.AuditLog
	for _, entry := range s.audit {
		if entry.TenantID != tenantID {
			continue
		}
		if filter.Action != "" && entry.Action != filter.Action {
			continue
		}
		if filter.ActorID != "" && entry.ActorID != filter.ActorID {
			continue
		}
		if !filter.Since.IsZero() && entry.CreatedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !entry.CreatedAt.Before(filter.Until) {
			continue
		}
		out = append(out, entry)
	}
	sortNewestFirst(out, func(e model.AuditLog) (time.Time, string) { return e.CreatedAt, e.ID })
	return paginate(out, query, func(e model.AuditLog) (time.Time, string) { return e.CreatedAt, e.ID })
}

// CreateJob inserts one job, rejecting a duplicate id the way the primary
// key does.
//
// CreateJob 插入一个 job，并像主键那样拒绝重复的 id。
func (s *Store) CreateJob(_ context.Context, job *model.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return store.ErrConflict
	}
	stamp(&job.CreatedAt, &job.UpdatedAt)
	s.jobs[job.ID] = *job
	return nil
}

// GetJob reads one job by id, scoped to its tenant.
//
// GetJob 按 id 读取一个 job，并限定在其租户范围内。
func (s *Store) GetJob(_ context.Context, tenantID, id string) (model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok || job.TenantID != tenantID {
		return model.Job{}, store.ErrNotFound
	}
	return job, nil
}

// ListJobs reads one tenant's jobs, newest first.
//
// ListJobs 读取某个租户的 job，最新的在前。
func (s *Store) ListJobs(_ context.Context, tenantID string, query store.ListQuery, filter store.JobFilter) (store.Page[model.Job], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Job
	for _, job := range s.jobs {
		if job.TenantID != tenantID {
			continue
		}
		if filter.State != "" && job.State != filter.State {
			continue
		}
		if filter.WorkflowID != "" && job.WorkflowID != filter.WorkflowID {
			continue
		}
		if !filter.Since.IsZero() && job.CreatedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !job.CreatedAt.Before(filter.Until) {
			continue
		}
		out = append(out, job)
	}
	sortNewestFirst(out, func(j model.Job) (time.Time, string) { return j.CreatedAt, j.ID })
	return paginate(out, query, func(j model.Job) (time.Time, string) { return j.CreatedAt, j.ID })
}

// UpdateJobState applies update if it is newer than the job's stored
// ObservedSeq and the job is not already terminal. See store.Jobs for the
// full contract.
//
// UpdateJobState 在 update 比该 job 已存储的 ObservedSeq 更新、且该 job 尚未处于
// 终态时应用它。完整契约见 store.Jobs。
func (s *Store) UpdateJobState(_ context.Context, tenantID, id string, update store.JobStateUpdate) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok || job.TenantID != tenantID {
		return false, store.ErrNotFound
	}
	if job.Terminal() || update.ObservedSeq <= job.ObservedSeq {
		return false, nil
	}
	job.State = update.State
	job.ErrorSummary = update.ErrorSummary
	job.ObservedSeq = update.ObservedSeq
	job.UpdatedAt = update.At
	if job.Terminal() {
		terminalAt := update.At
		job.TerminalAt = &terminalAt
	}
	s.jobs[id] = job
	return true, nil
}

// ListActiveJobsForRoute returns up to store.MaxActiveJobsForRoute
// non-terminal jobs bound to (nodeID, runtimeID), oldest first, across every
// tenant.
//
// ListActiveJobsForRoute 返回最多 store.MaxActiveJobsForRoute 个绑定到
// (nodeID, runtimeID) 的非终态 job，最旧的在前，跨越所有租户。
func (s *Store) ListActiveJobsForRoute(_ context.Context, nodeID, runtimeID string) ([]model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.Job
	for _, job := range s.jobs {
		if job.NodeID == nodeID && job.RuntimeID == runtimeID && !job.Terminal() {
			out = append(out, job)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].CreatedAt.Equal(out[k].CreatedAt) {
			return out[i].ID < out[k].ID
		}
		return out[i].CreatedAt.Before(out[k].CreatedAt)
	})
	if len(out) > store.MaxActiveJobsForRoute {
		out = out[:store.MaxActiveJobsForRoute]
	}
	return out, nil
}

// CreateJobArtifact inserts one artifact record, rejecting a duplicate id
// the way the primary key does.
//
// CreateJobArtifact 插入一个产物记录，并像主键那样拒绝重复的 id。
func (s *Store) CreateJobArtifact(_ context.Context, artifact *model.JobArtifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.artifacts[artifact.ID]; exists {
		return store.ErrConflict
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now()
	}
	s.artifacts[artifact.ID] = *artifact
	return nil
}

// ListJobArtifacts reads one job's artifacts, scoped to its tenant.
//
// ListJobArtifacts 读取一个 job 的产物，并限定在其租户范围内。
func (s *Store) ListJobArtifacts(_ context.Context, tenantID, jobID string) ([]model.JobArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.JobArtifact
	for _, artifact := range s.artifacts {
		if artifact.JobID == jobID && artifact.TenantID == tenantID {
			out = append(out, artifact)
		}
	}
	sortNewestFirst(out, func(a model.JobArtifact) (time.Time, string) { return a.CreatedAt, a.ID })
	return out, nil
}

// ListJobArtifactsBefore returns up to store.MaxExpiredJobArtifacts
// artifacts of artifactType older than cutoff, across every tenant, oldest
// first — the order a cleanup sweep wants, so the most-overdue artifacts are
// reaped first when there are more than one tick's cap.
//
// ListJobArtifactsBefore 返回最多 store.MaxExpiredJobArtifacts 个、类型为
// artifactType 且早于 cutoff 的产物，跨越所有租户，最旧的在前——这正是一次
// 清理扫描想要的顺序，当数量超过一轮的上限时，逾期最久的产物会被优先回收。
func (s *Store) ListJobArtifactsBefore(_ context.Context, artifactType string, cutoff time.Time) ([]model.JobArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.JobArtifact
	for _, artifact := range s.artifacts {
		if artifact.Type == artifactType && artifact.CreatedAt.Before(cutoff) {
			out = append(out, artifact)
		}
	}
	sortOldestFirst(out, func(a model.JobArtifact) (time.Time, string) { return a.CreatedAt, a.ID })
	if len(out) > store.MaxExpiredJobArtifacts {
		out = out[:store.MaxExpiredJobArtifacts]
	}
	return out, nil
}

// DeleteJobArtifact removes one artifact record. A missing id is not an
// error, matching gormstore's own DeleteJobArtifact.
//
// DeleteJobArtifact 移除一个产物记录。不存在的 id 不算错误，与 gormstore 自己的
// DeleteJobArtifact 一致。
func (s *Store) DeleteJobArtifact(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.artifacts, id)
	return nil
}

// sortOldestFirst is sortNewestFirst's reverse, for the one list in this
// package a cleanup sweep reads in age order rather than the user-facing
// newest-first convention every other list follows.
//
// sortOldestFirst 是 sortNewestFirst 的反向排序，供本包中唯一一个按年龄顺序
// （而不是其余每个列表都遵循的、面向用户的最新在前约定）读取的列表使用，
// 即一次清理扫描。
func sortOldestFirst[T any](items []T, key func(T) (time.Time, string)) {
	sort.Slice(items, func(i, j int) bool {
		leftAt, leftID := key(items[i])
		rightAt, rightID := key(items[j])
		if leftAt.Equal(rightAt) {
			return leftID < rightID
		}
		return leftAt.Before(rightAt)
	})
}

// sortNewestFirst orders rows the way every list in this package is read:
// newest first, with the id breaking ties. The tie break is what makes the
// order total — two rows written in the same instant would otherwise swap
// places between calls, and a keyset cursor pointing at one of them would skip
// or repeat the other.
//
// sortNewestFirst 按本包每个列表被读取的方式排序：最新的在前，id 用于打破平手。正是
// 这个平局判定让顺序成为全序——否则同一瞬间写入的两行会在多次调用之间互换位置，而指向
// 其中一行的 keyset 游标会跳过或重复另一行。
func sortNewestFirst[T any](items []T, key func(T) (time.Time, string)) {
	sort.Slice(items, func(i, j int) bool {
		leftAt, leftID := key(items[i])
		rightAt, rightID := key(items[j])
		if leftAt.Equal(rightAt) {
			return leftID > rightID
		}
		return leftAt.After(rightAt)
	})
}

// paginate cuts an already-ordered slice at the cursor and to the page size.
//
// paginate 把一个已排好序的切片按游标与分页大小裁开。
func paginate[T any](items []T, query store.ListQuery, key func(T) (time.Time, string)) (store.Page[T], error) {
	at, id, err := store.DecodeCursor(query.Cursor)
	if err != nil {
		return store.Page[T]{}, err
	}
	if query.Cursor != "" {
		rest := items[:0:0]
		for _, item := range items {
			itemAt, itemID := key(item)
			if itemAt.After(at) || (itemAt.Equal(at) && itemID >= id) {
				continue
			}
			rest = append(rest, item)
		}
		items = rest
	}

	size := query.Size()
	if len(items) <= size {
		return store.Page[T]{Items: items}, nil
	}
	last := items[size-1]
	lastAt, lastID := key(last)
	return store.Page[T]{Items: items[:size], NextCursor: store.EncodeCursor(lastAt, lastID)}, nil
}

// matches reports whether any field contains the query, case-insensitively.
// An empty query matches everything, which is how "no filter" is expressed.
//
// matches 报告是否有任一字段以不区分大小写的方式包含该查询词。空查询匹配一切，
// 「不筛选」就是这么表达的。
func matches(query string, fields ...string) bool {
	if query == "" {
		return true
	}
	needle := strings.ToLower(query)
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), needle) {
			return true
		}
	}
	return false
}

// stamp fills the timestamps gorm would set on insert, so a test that orders
// by CreatedAt sees the same shape it would in production.
//
// stamp 填入 gorm 在插入时会设置的时间戳，好让按 CreatedAt 排序的测试看到与生产一致
// 的形态。
func stamp(createdAt, updatedAt *time.Time) {
	now := time.Now()
	if createdAt.IsZero() {
		*createdAt = now
	}
	*updatedAt = now
}

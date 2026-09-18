package httpapi

import "context"

// PersistedArtifact is one artifact a previous listing (on this replica or
// another) already minted a public id for, and the control plane has since
// confirmed via CreateJobArtifact. listArtifacts reads these back instead of
// asking a node again when this replica has no local record of the job that
// produced them (STATUS.md's Gateway 故障切换收尾).
//
// PersistedArtifact 是此前某次列举（无论在本副本还是另一个副本上）已经铸造过
// 公开 id、且控制面已经通过 CreateJobArtifact 确认过的一个产物。当本副本没有
// 产出它们的那个 job 的本地记录时，listArtifacts 读取这些记录作答，而不是
// 再去问一次节点（STATUS.md 的「Gateway 故障切换收尾」）。
type PersistedArtifact struct {
	ArtifactID string
	Filename   string
	Type       string
}

// ArtifactRoute is the control plane's full record of one artifact — enough
// to serve a download without ever having listed it on this replica: an
// object storage key if bytes were ever persisted there, or the node/runtime
// that produced it to pull live otherwise.
//
// ArtifactRoute 是控制面对一个产物的完整记录——足以在本副本从未列举过它的
// 情况下依然提供服务：字节曾被持久化时是一个对象存储键，否则是产出它的
// node/runtime，供实时拉取。
type ArtifactRoute struct {
	JobID       string
	TenantID    string
	Filename    string
	Subfolder   string
	Type        string
	ContentType string
	SizeBytes   int64
	StorageKey  string
	NodeID      string
	RuntimeID   string
}

// ArtifactRecoveryClient is what listArtifacts and downloadArtifact fall
// back to when this replica has no local record of a job or artifact — a
// terminal job the J06 recovery sweep never restored (that sweep only
// covers non-terminal jobs), one submitted to a different replica, or one
// from before this replica's own restart.
//
// It is declared here with primitive-typed parameters and returns for the
// same reason JobPersistClient is: controlplaneclient already imports this
// package, so the reverse import would be a cycle.
//
// Nil leaves both handlers exactly as they behaved before this existed: a
// local miss answers 404 (or "job not found") without ever asking anything
// else.
//
// ArtifactRecoveryClient 是 listArtifacts 与 downloadArtifact 在本副本没有某个
// job 或产物的本地记录时的回退——一个 J06 恢复扫描从未恢复过的终态 job（那次
// 扫描只覆盖非终态 job）、一个提交给了另一个副本的 job，或者本副本自己重启
// 之前的 job。
//
// 这里用原始类型的参数与返回值声明它，理由与 JobPersistClient 相同：
// controlplaneclient 已经导入了本包，反向导入就会成环。
//
// 为 nil 时两个 handler 的行为与本功能存在之前完全一样：一次本地未命中直接
// 应答 404（或"job not found"），从不会再去问别的什么。
type ArtifactRecoveryClient interface {
	// JobExists reports whether jobID is on record for tenantID. It is its
	// own method, not inferred from ListPersistedArtifacts returning
	// nothing, because a job with zero persisted artifacts and a job that
	// does not exist must answer differently — "job_not_found" vs an empty
	// list.
	//
	// JobExists 报告 jobID 是否在 tenantID 名下有记录。它是独立的一个方法，
	// 而不是从 ListPersistedArtifacts 返回空推断出来的，因为「零个已持久化
	// 产物的 job」与「不存在的 job」必须给出不同的答复——"job_not_found"
	// 还是一份空列表。
	JobExists(ctx context.Context, tenantID, jobID string) (bool, error)
	// ListPersistedArtifacts reads back exactly the ids and filenames a
	// previous listing already minted and the control plane confirmed —
	// never a fresh set, which would invalidate ids a caller may already be
	// holding.
	//
	// ListPersistedArtifacts 原样读回此前某次列举已经铸造、且控制面已确认的
	// id 与文件名——绝不是一套新铸造的，那会让调用方手上还攥着的 id 失效。
	ListPersistedArtifacts(ctx context.Context, tenantID, jobID string) ([]PersistedArtifact, error)
	// ArtifactRoute reads one artifact by its bare public id, with no job id
	// to scope by — the one identifier a download request carries.
	//
	// ArtifactRoute 按裸公开 id 读取一个产物，没有 job id 可供限定范围——那是
	// 一次下载请求携带的唯一标识符。
	ArtifactRoute(ctx context.Context, tenantID, artifactID string) (ArtifactRoute, error)
}

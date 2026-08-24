package httpapi

import (
	"sync"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// DefaultMaxJobs bounds how many jobs one Gateway replica remembers. The
// store is in-memory, so this is the whole of README's 「任何一跳都不得无界缓冲」
// for the job table: past the limit the oldest entry is dropped rather than
// letting a submit loop grow the process without end. Durable job history
// belongs in the control plane's jobs table, which does not exist yet.
//
// DefaultMaxJobs 限制单个 Gateway 副本记住多少个 job。存储在内存里，因此这就是
// README「任何一跳都不得无界缓冲」在 job 表上的全部落实：超过上限就丢弃最旧的一条，
// 而不是让一个提交循环把进程无限撑大。持久的 job 历史属于控制面的 jobs 表，那张表
// 还不存在。
const DefaultMaxJobs = 10000

// job is one submitted workflow run as this Gateway knows it.
//
// Two identifiers matter here and must not be confused: ID is the public job
// id this Gateway minted, and RunID is the backend's own prompt_id. README is
// explicit that the second never becomes the first — it is not ours to hand
// out, and it is only unique within one ComfyUI.
//
// job 是本 Gateway 所知的一次已提交工作流运行。
//
// 这里有两个标识符，不能混为一谈：ID 是本 Gateway 铸造的公开 job id，RunID 是后端
// 自己的 prompt_id。README 明确要求后者永远不充当前者——它不是我们该派发的东西，而且
// 只在单个 ComfyUI 内部唯一。
type job struct {
	ID            string
	WorkflowID    string
	TenantID      string
	Candidate     scheduler.Candidate
	RunID         string
	State         runtime.WorkflowState
	QueuePosition int
	ErrorSummary  string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// artifactIDs maps a backend artifact reference to the public id minted
	// for it, so re-listing a job answers with the ids a caller already has
	// rather than a fresh set. ArtifactRef is four strings and therefore
	// comparable, which is what lets it key this map.
	//
	// artifactIDs 把后端的产物引用映射到为它铸造的公开 id，这样重复列举一个 job 时
	// 回答的是调用方已经拿到的那些 id，而不是新的一套。ArtifactRef 由四个字符串组成，
	// 因而可比较，这正是它能做本映射键的原因。
	artifactIDs map[runtime.ArtifactRef]string
}

// artifactRecord is what a public artifact id resolves to: which job it
// belongs to, who may read it, and where to fetch it from.
//
// artifactRecord 是一个公开产物 id 解析出来的东西：它属于哪个 job、谁可以读它，
// 以及从哪里取。
type artifactRecord struct {
	JobID     string
	TenantID  string
	Candidate scheduler.Candidate
	Ref       runtime.ArtifactRef
}

// terminal reports whether the run has finished, in which case its state can
// be answered without asking the node again.
//
// terminal 报告该次运行是否已结束；已结束的话，回答它的状态无需再问节点。
func (j job) terminal() bool {
	switch j.State {
	case runtime.WorkflowSucceeded, runtime.WorkflowFailed, runtime.WorkflowCancelled:
		return true
	default:
		return false
	}
}

// jobStore is the in-memory job table. Every method hands back a copy, so a
// caller reading a job cannot race a status update writing one.
//
// jobStore 是内存版 job 表。每个方法交还的都是副本，因此读取 job 的调用方不会与写入
// 状态更新的调用方产生竞态。
type jobStore struct {
	mu    sync.Mutex
	byID  map[string]job
	order []string
	max   int
	// artifacts resolves a public artifact id. It is keyed independently of
	// byID because a download addresses an artifact without naming its job,
	// and it is pruned alongside the job it belongs to so an evicted job
	// cannot leave its artifacts reachable.
	//
	// artifacts 解析公开产物 id。它独立于 byID 建键，因为下载在寻址一个产物时并不
	// 指名它的 job；它随所属 job 一同被清理，因此被逐出的 job 不会留下仍可访问的产物。
	artifacts map[string]artifactRecord
}

func newJobStore(max int) *jobStore {
	if max <= 0 {
		max = DefaultMaxJobs
	}
	return &jobStore{
		byID:      make(map[string]job),
		artifacts: make(map[string]artifactRecord),
		max:       max,
	}
}

// add records a new job, evicting the oldest once the store is full.
//
// add 记录一个新 job；存储已满时逐出最旧的一条。
func (s *jobStore) add(j job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[j.ID] = j
	s.order = append(s.order, j.ID)
	for len(s.order) > s.max {
		oldest := s.order[0]
		s.order = s.order[1:]
		s.evictLocked(oldest)
	}
}

// evictLocked drops a job and every artifact id that resolved to it. Leaving
// the ids behind would keep an evicted job's outputs downloadable from a
// table that no longer bounds itself against anything.
//
// evictLocked 丢弃一个 job 以及每一个解析到它的产物 id。把这些 id 留下，会让一个已被
// 逐出的 job 的产物仍可下载，而承载它们的那张表已经不再受任何东西约束。
func (s *jobStore) evictLocked(id string) {
	evicted, ok := s.byID[id]
	if !ok {
		return
	}
	for _, artifactID := range evicted.artifactIDs {
		delete(s.artifacts, artifactID)
	}
	delete(s.byID, id)
}

// recordArtifacts mints a public id per artifact reference, reusing the id a
// previous listing already assigned. The returned slice is parallel to refs.
//
// recordArtifacts 为每个产物引用铸造一个公开 id，若此前的列举已分配过则复用那个 id。
// 返回的切片与 refs 一一对应。
func (s *jobStore) recordArtifacts(jobID string, refs []runtime.ArtifactRef) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[jobID]
	if !ok {
		return make([]string, len(refs))
	}
	if j.artifactIDs == nil {
		j.artifactIDs = make(map[runtime.ArtifactRef]string, len(refs))
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		id, seen := j.artifactIDs[ref]
		if !seen {
			id = "art_" + newRequestID()
			j.artifactIDs[ref] = id
		}
		ids[i] = id
		s.artifacts[id] = artifactRecord{
			JobID:     j.ID,
			TenantID:  j.TenantID,
			Candidate: j.Candidate,
			Ref:       ref,
		}
	}
	s.byID[jobID] = j
	return ids
}

// artifact resolves a public artifact id, but only for the tenant that owns
// the job it came from. An artifact is the generated image itself, so this is
// the strictest of the tenant checks in this package, not the loosest.
//
// artifact 解析一个公开产物 id，但只对拥有其来源 job 的租户解析。产物就是生成出来的
// 图像本身，因此这是本包中最严的一处租户校验，而不是最松的。
func (s *jobStore) artifact(id, tenantID string) (artifactRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.artifacts[id]
	if !ok || rec.TenantID != tenantID {
		return artifactRecord{}, false
	}
	return rec, true
}

// get returns the job with id, but only to the tenant that submitted it. A
// job belonging to someone else is reported exactly as a job that does not
// exist: the difference between the two is itself information.
//
// get 返回 id 对应的 job，但只对提交它的租户返回。属于别人的 job 与不存在的 job 得到
// 完全相同的答复：两者之间的差别本身就是信息。
func (s *jobStore) get(id, tenantID string) (job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok || j.TenantID != tenantID {
		return job{}, false
	}
	return j, true
}

// update applies the node's latest status to a stored job. A job evicted in
// the meantime is not resurrected — the answer already went out to the
// caller, and re-adding it would let an eviction be undone by a status poll.
//
// update 把节点的最新状态应用到已存的 job 上。期间已被逐出的 job 不会被复活——答复
// 早已发给调用方，重新加回去等于让一次状态轮询撤销一次逐出。
func (s *jobStore) update(id string, status runtime.WorkflowStatus, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok {
		return
	}
	j.State = status.State
	j.QueuePosition = status.QueuePosition
	j.ErrorSummary = status.ErrorSummary
	j.UpdatedAt = now
	s.byID[id] = j
}

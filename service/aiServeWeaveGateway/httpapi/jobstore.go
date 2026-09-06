package httpapi

import (
	"sort"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/workflowview"
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
	// syncFailures counts consecutive failed background sync attempts. It is
	// distinct from any state a caller can observe — a node being briefly
	// unreachable is not evidence the run itself changed — and it drives
	// nextSyncAt's backoff so a node that has disappeared is not re-asked
	// every tick. A foreground observation (a status poll or an SSE event)
	// resets it: whatever made the job briefly hard to reach evidently no
	// longer applies once something did reach it.
	//
	// syncFailures 计数连续失败的后台同步尝试。它与调用方能观察到的任何状态都无关——
	// 一个节点短暂不可达，不能证明这次运行本身发生了变化——它驱动 nextSyncAt 的退避，
	// 好让一个已经消失的节点不会每一轮都被重新询问。一次前台观测（一次状态轮询或一个
	// SSE 事件）会将其清零：既然确实有什么触达到了它，此前让它一度难以触达的原因，
	// 显然已不再成立。
	syncFailures int
	// nextSyncAt is when the background syncer may next ask about this job.
	// The zero value is always due, so a freshly submitted job is eligible
	// from its very first tick without add needing to set this explicitly.
	//
	// nextSyncAt 是后台同步器下一次可以询问这个 job 的时间。零值永远视为已到期，
	// 因此一个刚提交的 job 从它的第一轮起就已合格，无需 add 特意设置这个字段。
	nextSyncAt time.Time
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
	// evicted records that the bound has been hit at least once, so a reader
	// of this table knows its list is not the whole story. It is never reset:
	// once runs have been dropped, no later quiet period makes the table
	// complete again.
	//
	// evicted 记录上限至少被触及过一次，好让这张表的读取者知道它的列表并非全部。它
	// 从不被重置：一旦有运行被丢弃，之后再怎么清闲，这张表也回不到完整。
	evicted bool
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
		s.evicted = true
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
// This is a foreground observation — a caller's own poll or an SSE event —
// so it also clears any backoff the background syncer had accumulated for
// this job: whatever made it briefly hard to reach evidently no longer
// applies, and the syncer should not keep waiting out a penalty a more
// recent, successful observation has already overtaken.
//
// update 把节点的最新状态应用到已存的 job 上。期间已被逐出的 job 不会被复活——答复
// 早已发给调用方，重新加回去等于让一次状态轮询撤销一次逐出。
//
// 这是一次前台观测——调用方自己的轮询或一次 SSE 事件——因此它也会清空后台同步器
// 为这个 job 累积的退避：既然显然已经有什么触达到了它，此前让它一度难以触达的原因
// 就不再成立，同步器不该继续等一个已经被更新、更成功的观测超过的惩罚期。
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
	j.syncFailures = 0
	j.nextSyncAt = now
	s.byID[id] = j
}

// syncCandidate is what the background syncer needs to ask a node about one
// job. It carries none of the job's other fields — the syncer has no
// business reading them, only dispatching on them.
//
// syncCandidate 是后台同步器询问某个 job 所需的全部信息。它不携带 job 的其他字段——
// 同步器没有理由读取它们，只需要靠它们去分派。
type syncCandidate struct {
	ID        string
	Candidate scheduler.Candidate
	RunID     string
}

// dueForSync returns up to max non-terminal jobs whose next sync attempt is
// at or before now, longest-overdue first, and immediately pushes each
// returned job's nextSyncAt out to now.Add(claimFor). That push is a claim:
// a job handed out here will not be handed out again until claimFor elapses,
// so a call still in flight when the next tick starts is not dispatched a
// second time. The caller is expected to report back sooner via
// syncSucceeded or syncFailed, both of which set a more specific nextSyncAt
// that supersedes this placeholder.
//
// dueForSync 返回最多 max 个下次同步时间不晚于 now 的非终态 job，逾期最久的排在最
// 前面，并立即把每一个被返回 job 的下次同步时间推到 now.Add(claimFor)。这一推就是
// 一次认领：这里派发出去的 job，在 claimFor 过去之前不会被再次派发，因此一次仍在
// 进行中的调用不会在下一轮开始时被重复分派。调用方应当更早地通过 syncSucceeded 或
// syncFailed 回报结果，两者都会设置一个更具体的 nextSyncAt，取代这个占位值。
func (s *jobStore) dueForSync(now time.Time, max int, claimFor time.Duration) []syncCandidate {
	s.mu.Lock()
	defer s.mu.Unlock()

	type dueJob struct {
		id string
		at time.Time
	}
	candidates := make([]dueJob, 0, len(s.order))
	for id, j := range s.byID {
		if j.terminal() || j.nextSyncAt.After(now) {
			continue
		}
		candidates = append(candidates, dueJob{id: id, at: j.nextSyncAt})
	}
	sort.Slice(candidates, func(i, k int) bool {
		if candidates[i].at.Equal(candidates[k].at) {
			return candidates[i].id < candidates[k].id
		}
		return candidates[i].at.Before(candidates[k].at)
	})
	if len(candidates) > max {
		candidates = candidates[:max]
	}

	out := make([]syncCandidate, 0, len(candidates))
	claimed := now.Add(claimFor)
	for _, d := range candidates {
		j := s.byID[d.id]
		j.nextSyncAt = claimed
		s.byID[d.id] = j
		out = append(out, syncCandidate{ID: j.ID, Candidate: j.Candidate, RunID: j.RunID})
	}
	return out
}

// syncSucceeded applies a background-fetched status the same way update
// does, and is the syncer's own entry point for it — kept separate so
// jobs.go's foreground path and jobsync.go's background path each have a
// name that says which one it is, even though the body is identical.
//
// syncSucceeded 以与 update 相同的方式应用一次后台取得的状态，是同步器自己的入口——
// 与前台路径分开命名，好让 jobs.go 的前台路径与 jobsync.go 的后台路径各自的名字都
// 说明自己是哪一个，即便两者的函数体相同。
func (s *jobStore) syncSucceeded(id string, status runtime.WorkflowStatus, now time.Time) {
	s.update(id, status, now)
}

// syncFailed records a failed background sync attempt without touching the
// job's observed state — a node being briefly unreachable is not evidence
// the run itself changed, and README requires state to stay the last thing
// actually observed. backoff computes how long to wait before this job is
// due again, based on the consecutive-failure count now on record; the
// caller supplies it so jobsync.go owns the backoff curve and this method
// only owns where the count and the resulting deadline are stored.
//
// syncFailed 记录一次失败的后台同步尝试,但不触碰 job 的已观测状态——节点短暂不可达
// 不能证明这次运行本身发生了变化,而 README 要求 state 保持为最后一次真正观测到的
// 结果。backoff 依据当前记录的连续失败次数,算出这个 job 下一次到期还要等多久；
// 由调用方提供它，好让 jobsync.go 拥有退避曲线本身，这个方法只负责存放次数与由此
// 得出的截止时间。
func (s *jobStore) syncFailed(id string, now time.Time, backoff func(failures int) time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok {
		return
	}
	j.syncFailures++
	j.nextSyncAt = now.Add(backoff(j.syncFailures))
	s.byID[id] = j
}

// forTenant returns one tenant's jobs, newest first, and whether the table has
// evicted anything to stay within its bound.
//
// The eviction flag is not about this tenant: the table is shared and bounded
// across all of them, so a busy neighbour can push this tenant's older runs
// out. A caller shown a short list without that flag would read it as "nothing
// else ran", which is the one conclusion an in-memory, bounded, per-replica
// table cannot support.
//
// forTenant 返回某一个租户的 job，最新的在前，并报告该表是否为守住上限而逐出过内容。
//
// 逐出标志说的不是这个租户：这张表由所有租户共享且有上限，因此一个繁忙的邻居可以把本
// 租户较早的运行挤出去。一个看到短列表却没有这个标志的调用方，会把它读成「没有别的运行
// 过」——而那恰恰是一张进程内、有上限、且每副本各自持有的表最无法支撑的结论。
func (s *jobStore) forTenant(tenantID string) ([]workflowview.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]workflowview.Job, 0, len(s.order))
	// order is oldest first; the caller wants newest first, which is how a
	// person reads a job list.
	//
	// order 是最早的在前；调用方要的是最新的在前，那才是人读 job 列表的方式。
	for i := len(s.order) - 1; i >= 0; i-- {
		j, ok := s.byID[s.order[i]]
		if !ok || j.TenantID != tenantID {
			continue
		}
		artifacts := make([]string, 0, len(j.artifactIDs))
		for _, id := range j.artifactIDs {
			artifacts = append(artifacts, id)
		}
		sort.Strings(artifacts)
		out = append(out, workflowview.Job{
			ID:            j.ID,
			WorkflowID:    j.WorkflowID,
			State:         string(j.State),
			QueuePosition: j.QueuePosition,
			ErrorSummary:  j.ErrorSummary,
			CreatedAt:     j.CreatedAt,
			UpdatedAt:     j.UpdatedAt,
			ArtifactIDs:   artifacts,
		})
	}
	return out, s.evicted
}

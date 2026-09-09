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
	ID         string
	WorkflowID string
	// WorkflowVersion is the template's own opaque version string at the
	// moment this job was submitted (P03) — empty for a template loaded from
	// a local file, which was never versioned. It is captured here rather
	// than looked up again later because a template can be republished while
	// this job is still running, and this job ran the version it bound
	// against, not whatever is current now.
	//
	// WorkflowVersion 是本 job 提交那一刻，模板自身的不透明版本字符串（P03）——
	// 文件加载的模板从未被版本化，因此留空。之所以在这里捕获而不是之后再查，是因为
	// 模板可能在本 job 仍在运行时被重新发布，而本 job 跑的是它绑定时的那个版本，
	// 不是此刻的当前版本。
	WorkflowVersion string
	TenantID        string
	Candidate       scheduler.Candidate
	RunID           string
	State           runtime.WorkflowState
	QueuePosition   int
	ErrorSummary    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
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
	// ObservedSeq counts real observations of this job's state — it
	// increments in update() whenever State or ErrorSummary actually
	// changes, never on a poll that confirms the same thing again. It is
	// what the control plane's jobs table calls ObservedSeq too (see the
	// ControlPlane README's 「Job 持久化契约」): this Gateway replica is the
	// one party positioned to assign it, since it is the one asking the
	// node and receiving events in the first place.
	//
	// ObservedSeq 计数这个 job 状态的真实观测次数——每当 State 或 ErrorSummary
	// 确有变化时，它就在 update() 里自增，而一次只是重新确认同一件事的轮询
	// 不会让它变化。这也是控制面 jobs 表所说的 ObservedSeq（见 ControlPlane
	// README「Job 持久化契约」）：本 Gateway 副本正是有资格赋予它的那一方，
	// 因为归根结底是它在询问节点、接收事件。
	ObservedSeq int64
	// persisted reports whether the control plane has ever confirmed a
	// CreateJob for this job. It is separate from persistedSeq because the
	// first fact ("a row exists") and the ongoing one ("the row reflects
	// ObservedSeq") fail independently: the control plane can be reachable
	// for a create and then vanish before the first state update, or vice
	// versa.
	//
	// persisted 报告控制面是否已经确认过这个 job 的一次 CreateJob。它与
	// persistedSeq 分开，因为第一个事实（「这一行存在」）与持续的那个事实
	// （「这一行反映了 ObservedSeq」）会独立地失败：控制面可能在一次创建时可达，
	// 随后在第一次状态更新之前就不可达了，反过来也一样。
	persisted bool
	// persistedSeq is the highest ObservedSeq the control plane has
	// confirmed receiving, via either CreateJob (which implicitly confirms
	// seq 0, the initial state) or UpdateJobState.
	//
	// persistedSeq 是控制面已确认收到的最高 ObservedSeq，途径是 CreateJob
	// （隐含确认了 seq 0，即初始状态）或 UpdateJobState。
	persistedSeq int64
	// persistFailures and nextPersistAt are the persistence backoff's own
	// bookkeeping, kept separate from syncFailures/nextSyncAt above because
	// the two failure domains are independent: the control plane being
	// unreachable says nothing about whether the node is, and conflating
	// their backoff timers would have one outage silence retries for the
	// other.
	//
	// persistFailures 与 nextPersistAt 是持久化退避自己的记账，与上面的
	// syncFailures/nextSyncAt 分开保存，因为这两个故障域互不相关：控制面不可达
	// 说明不了节点是否可达，把两者的退避计时器混为一谈，会让一处故障压制住另一处
	// 本该继续的重试。
	persistFailures int
	nextPersistAt   time.Time
	// pendingArtifacts lists artifacts recordArtifacts has minted a public id
	// for but the control plane has not yet confirmed via CreateJobArtifact.
	// jobPersister drains this the same way it drains job state above — a
	// side channel that never gates listArtifacts answering the caller.
	//
	// pendingArtifacts 列出 recordArtifacts 已经铸造过公开 id、但控制面尚未
	// 通过 CreateJobArtifact 确认的产物。jobPersister 排空它的方式与上面排空
	// job 状态相同——是一条旁路，绝不会拦住 listArtifacts 对调用方的应答。
	pendingArtifacts      []pendingArtifact
	artifactPersistFails  int
	nextArtifactPersistAt time.Time
}

// pendingArtifact is one artifact awaiting a CreateJobArtifact confirmation.
//
// pendingArtifact 是一个等待 CreateJobArtifact 确认的产物。
type pendingArtifact struct {
	ArtifactID string
	Filename   string
	Subfolder  string
	Type       string
}

// needsPersist reports whether the control plane's record of this job is
// missing or behind this replica's own latest observation.
//
// needsPersist 报告控制面对这个 job 的记录是缺失的，还是落后于本副本自己最新
// 的观测。
func (j job) needsPersist() bool {
	return !j.persisted || j.persistedSeq < j.ObservedSeq
}

// artifactRecord is what a public artifact id resolves to: which job it
// belongs to, who may read it, and where to fetch it from.
//
// StorageKey, ContentType and Size describe a copy of this artifact this
// replica has made into its configured object storage (STATUS.md's P04);
// StorageKey is empty until jobPersister.persistArtifacts succeeds, or
// forever if ArtifactStorage is not configured, in which case downloadArtifact
// falls back to the Candidate/Ref pair as it always has.
//
// artifactRecord 是一个公开产物 id 解析出来的东西：它属于哪个 job、谁可以读它，
// 以及从哪里取。
//
// StorageKey、ContentType 与 Size 描述的是本副本把这个产物复制进其配置的对象
// 存储所得到的一份副本（STATUS.md 的 P04）；在 jobPersister.persistArtifacts
// 成功之前 StorageKey 为空，若未配置 ArtifactStorage 则永远为空，此时
// downloadArtifact 照旧回退到 Candidate/Ref 这一对。
type artifactRecord struct {
	JobID       string
	TenantID    string
	Candidate   scheduler.Candidate
	Ref         runtime.ArtifactRef
	StorageKey  string
	ContentType string
	Size        int64
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
	s.insertLocked(j)
}

// recoverIfMissing inserts j only if no job with j.ID is already known, and
// reports whether it did. It exists for STATUS.md's J06: a background
// recovery sweep asking the control plane about a route binding may be told
// about a job this replica already knows — because it submitted it, because
// an earlier sweep already recovered it, or because another goroutine's
// sweep is racing this one — and inserting it again would give s.order two
// entries for one id, corrupting eviction order and duplicating the job in
// every tenant's listing.
//
// recoverIfMissing 仅在尚不存在 j.ID 对应的 job 时才插入它，并报告是否插入了。
// 它为 STATUS.md 的 J06 而存在：一次后台恢复扫描向控制面询问某个路由绑定，
// 可能被告知一个本副本已经知道的 job——因为是本副本自己提交的，因为更早的一轮
// 扫描已经恢复过它，或者因为另一个协程的扫描正与这次竞争——重复插入会让
// s.order 里出现两条同一个 id 的记录，既破坏逐出顺序，也会让这个 job 在每个
// 租户的列表里重复出现。
func (s *jobStore) recoverIfMissing(j job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[j.ID]; exists {
		return false
	}
	s.insertLocked(j)
	return true
}

// insertLocked is add and recoverIfMissing's shared body. Callers must hold
// s.mu.
//
// insertLocked 是 add 与 recoverIfMissing 共用的实现主体。调用方必须持有 s.mu。
func (s *jobStore) insertLocked(j job) {
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
			j.pendingArtifacts = append(j.pendingArtifacts, pendingArtifact{
				ArtifactID: id, Filename: ref.Filename, Subfolder: ref.Subfolder, Type: ref.Type,
			})
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
	// ObservedSeq advances only on a real change. QueuePosition moving on
	// its own does not count: the control plane's jobs table has no column
	// for it (see model.Job), so a poll that only confirms a new queue
	// position would otherwise burn a persistence write on nothing the
	// control plane can even store.
	//
	// ObservedSeq 只在真正发生变化时前进。QueuePosition 单独变动不算数：控制面
	// 的 jobs 表根本没有对应的列（见 model.Job），因此一次只确认了新排队位置的
	// 轮询，若也推进它，只会为一件控制面根本存不下的事白白消耗一次持久化写入。
	if j.State != status.State || j.ErrorSummary != status.ErrorSummary {
		j.ObservedSeq++
	}
	j.State = status.State
	j.QueuePosition = status.QueuePosition
	j.ErrorSummary = status.ErrorSummary
	j.UpdatedAt = now
	j.syncFailures = 0
	j.nextSyncAt = now
	s.byID[id] = j
}

// forPersist returns id's full internal record for the background
// persister, or false if it has since been evicted. It is not tenant-scoped:
// the caller is this package's own persister, already trusted with every
// job's routing data — the same trust boundary jobSyncer's dueForSync
// crosses to reach Candidate and RunID.
//
// forPersist 为后台持久化器返回 id 的完整内部记录，若已被逐出则返回 false。
// 它不按租户限定范围：调用方是本包自己的持久化器，早已被信任持有每个 job 的
// 路由数据——与 jobSyncer 的 dueForSync 为触及 Candidate 与 RunID 所跨越的
// 是同一条信任边界。
func (s *jobStore) forPersist(id string) (job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	return j, ok
}

// dueForPersist returns up to max job ids whose control-plane record is
// missing or behind (see job.needsPersist) and whose next persistence
// attempt is at or before now, longest-overdue first. Like dueForSync, it
// claims each returned id by pushing its nextPersistAt out to
// now.Add(claimFor), so a call still in flight when the next tick starts is
// not dispatched a second time.
//
// Unlike dueForSync this does not exclude terminal jobs — a terminal run's
// final state is exactly the record most worth not losing, and it is the
// one case jobSyncer's own polling loop stops covering the moment a job
// reaches it.
//
// dueForPersist 返回最多 max 个满足以下条件的 job id：其控制面记录缺失或落后
// （见 job.needsPersist），且下一次持久化尝试的时间不晚于 now，逾期最久的排在
// 最前面。与 dueForSync 一样，它通过把每个被返回 id 的下一次尝试时间推到
// now.Add(claimFor) 来完成认领，这样一次仍在进行中的调用不会在下一轮开始时被
// 重复分派。
//
// 与 dueForSync 不同，这里不排除终态 job——一次运行的最终状态恰恰是最不该丢失
// 的那份记录，而这正是 jobSyncer 自己的轮询循环在一个 job 到达终态那一刻起就
// 不再覆盖的情形。
func (s *jobStore) dueForPersist(now time.Time, max int, claimFor time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	type dueJob struct {
		id string
		at time.Time
	}
	candidates := make([]dueJob, 0, len(s.order))
	for id, j := range s.byID {
		if !j.needsPersist() || j.nextPersistAt.After(now) {
			continue
		}
		candidates = append(candidates, dueJob{id: id, at: j.nextPersistAt})
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

	claimed := now.Add(claimFor)
	out := make([]string, 0, len(candidates))
	for _, d := range candidates {
		j := s.byID[d.id]
		j.nextPersistAt = claimed
		s.byID[d.id] = j
		out = append(out, d.id)
	}
	return out
}

// persistedCreate records that the control plane has confirmed a CreateJob
// for id, implicitly confirming ObservedSeq 0 — the initial state that call
// carried. A job evicted in the meantime is left alone, matching update's
// own rule against resurrecting an evicted row.
//
// persistedCreate 记录控制面已确认一次针对 id 的 CreateJob，隐含确认了
// ObservedSeq 0——那次调用所携带的初始状态。期间已被逐出的 job 保持不变，
// 与 update 自己「不复活已逐出行」的规则一致。
func (s *jobStore) persistedCreate(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok {
		return
	}
	j.persisted = true
	j.persistedSeq = 0
	j.persistFailures = 0
	j.nextPersistAt = now
	s.byID[id] = j
}

// persistedState records that the control plane has confirmed an
// UpdateJobState carrying seq. The caller passes the seq it sent — read from
// forPersist just before the call — rather than this method reading
// ObservedSeq itself, because ObservedSeq may have advanced again while the
// call was in flight; recording exactly what was confirmed, not whatever is
// current now, is what keeps this bookkeeping accurate.
//
// persistedState 记录控制面已确认一次携带 seq 的 UpdateJobState。调用方传入
// 它发送时的那个 seq——在调用前从 forPersist 读到的——而不是让本方法自己去读
// ObservedSeq，因为调用在途期间 ObservedSeq 可能已经又前进了；记录「确切被
// 确认的是什么」而不是「此刻是什么」，才能让这份记账保持准确。
func (s *jobStore) persistedState(id string, seq int64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok {
		return
	}
	j.persistedSeq = seq
	j.persistFailures = 0
	j.nextPersistAt = now
	s.byID[id] = j
}

// persistFailed records a failed persistence attempt without touching
// anything about the job the control plane still lacks — the point of this
// bookkeeping is exactly to remember that it is still owed. backoff computes
// how long to wait before this job is due again, based on the
// consecutive-failure count now on record, mirroring syncFailed's own
// contract in jobsync.go.
//
// persistFailed 记录一次失败的持久化尝试，且不触碰控制面依然欠缺的那部分——
// 这份记账存在的意义正是记住它仍然欠着。backoff 依据当前记录的连续失败次数，
// 算出这个 job 下一次到期还要等多久，与 jobsync.go 里 syncFailed 自己的契约
// 一致。
func (s *jobStore) persistFailed(id string, now time.Time, backoff func(failures int) time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok {
		return
	}
	j.persistFailures++
	j.nextPersistAt = now.Add(backoff(j.persistFailures))
	s.byID[id] = j
}

// dueForArtifactPersist returns up to max job ids with artifacts awaiting a
// CreateJobArtifact confirmation, longest-overdue first, claiming each by
// pushing nextArtifactPersistAt out to now.Add(claimFor) — the same claim
// discipline dueForPersist uses, so a batch still in flight is not
// dispatched a second time.
//
// dueForArtifactPersist 返回最多 max 个存在待确认产物的 job id，逾期最久的
// 排在最前面，并通过把 nextArtifactPersistAt 推到 now.Add(claimFor) 来认领
// 每一个——与 dueForPersist 相同的认领纪律，防止一个仍在进行中的批次被重复
// 分派。
func (s *jobStore) dueForArtifactPersist(now time.Time, max int, claimFor time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	type dueJob struct {
		id string
		at time.Time
	}
	candidates := make([]dueJob, 0, len(s.order))
	for id, j := range s.byID {
		if len(j.pendingArtifacts) == 0 || j.nextArtifactPersistAt.After(now) {
			continue
		}
		candidates = append(candidates, dueJob{id: id, at: j.nextArtifactPersistAt})
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

	claimed := now.Add(claimFor)
	out := make([]string, 0, len(candidates))
	for _, d := range candidates {
		j := s.byID[d.id]
		j.nextArtifactPersistAt = claimed
		s.byID[d.id] = j
		out = append(out, d.id)
	}
	return out
}

// artifactsForPersist returns a snapshot of id's pending artifacts, along
// with the tenant they belong to and the route binding (Candidate, RunID)
// needed to pull their bytes from the node that produced them. Like
// forPersist, it reads fresh rather than trusting whatever
// dueForArtifactPersist last saw, since recordArtifacts may have appended
// more in the meantime.
//
// artifactsForPersist 返回 id 待确认产物的一份快照，连同它们所属的租户，以及
// 从产出它们的节点拉取字节所需的路由绑定（Candidate、RunID）。与 forPersist
// 一样，它读取的是当下的数据，而不是信任 dueForArtifactPersist 上次看到的
// 那份，因为 recordArtifacts 可能同时又追加了更多。
func (s *jobStore) artifactsForPersist(id string) (tenantID string, candidate scheduler.Candidate, runID string, artifacts []pendingArtifact, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, exists := s.byID[id]
	if !exists {
		return "", scheduler.Candidate{}, "", nil, false
	}
	out := make([]pendingArtifact, len(j.pendingArtifacts))
	copy(out, j.pendingArtifacts)
	return j.TenantID, j.Candidate, j.RunID, out, true
}

// artifactStored records that artifactID's bytes have been copied into
// object storage under storageKey, so downloadArtifact can serve it from
// there instead of pulling it live from the node again. A since-evicted
// artifact is left alone, matching update's own rule against resurrecting an
// evicted row.
//
// artifactStored 记录 artifactID 的字节已被复制进对象存储、键为 storageKey，
// 这样 downloadArtifact 就能从那里作答，而不必再次从节点实时拉取。期间已被
// 逐出的产物保持不变，与 update 自己「不复活已逐出行」的规则一致。
func (s *jobStore) artifactStored(artifactID, storageKey, contentType string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.artifacts[artifactID]
	if !ok {
		return
	}
	rec.StorageKey = storageKey
	rec.ContentType = contentType
	rec.Size = size
	s.artifacts[artifactID] = rec
}

// artifactPersisted removes artifactID from id's pending list once the
// control plane has confirmed it, and resets the failure count — the same
// per-artifact granularity as artifactsForPersist, so one artifact failing
// does not block another in the same job from being marked done.
//
// artifactPersisted 在控制面确认某个产物后，把 artifactID 从 id 的待确认列表
// 中移除，并清零失败计数——与 artifactsForPersist 相同的逐产物粒度，因此同一
// job 里一个产物的失败不会拦住另一个被标记完成。
func (s *jobStore) artifactPersisted(id, artifactID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok {
		return
	}
	kept := j.pendingArtifacts[:0]
	for _, a := range j.pendingArtifacts {
		if a.ArtifactID != artifactID {
			kept = append(kept, a)
		}
	}
	j.pendingArtifacts = kept
	j.artifactPersistFails = 0
	s.byID[id] = j
}

// artifactPersistFailed records a failed artifact-persistence batch for id
// without touching its pending list — those artifacts are still owed — and
// re-arms nextArtifactPersistAt via backoff, mirroring persistFailed's own
// contract for job state.
//
// artifactPersistFailed 为 id 记录一次失败的产物持久化批次，不触碰它的待确认
// 列表——那些产物依然欠着——并通过 backoff 重新设定 nextArtifactPersistAt，
// 与 persistFailed 对 job 状态的做法一致。
func (s *jobStore) artifactPersistFailed(id string, now time.Time, backoff func(failures int) time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok {
		return
	}
	j.artifactPersistFails++
	j.nextArtifactPersistAt = now.Add(backoff(j.artifactPersistFails))
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

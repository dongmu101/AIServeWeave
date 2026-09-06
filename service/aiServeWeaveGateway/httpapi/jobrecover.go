package httpapi

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// Defaults for the background job recoverer. A fleet has orders of magnitude
// fewer connected nodes than a job table has jobs, so these are looser than
// jobSyncer's or jobPersister's: DefaultRecoverInterval is longer because
// recovery matters only right after a restart and is cheap insurance the
// rest of the time, and DefaultRecoverConcurrency is smaller because there
// is rarely enough fan-out to need more.
//
// 后台 job 恢复器的默认值。一个机群已连接的节点数量，比一张 job 表里的 job 数量
// 小好几个数量级，因此这些默认值比 jobSyncer 或 jobPersister 的更宽松：
// DefaultRecoverInterval 更长，因为恢复只在重启后那一刻要紧，其余时间只是一份
// 廉价的保险；DefaultRecoverConcurrency 更小，因为很少有足够的扇出需要更多。
const (
	DefaultRecoverInterval    = 30 * time.Second
	DefaultRecoverConcurrency = 4
	DefaultRecoverCallTimeout = 5 * time.Second
)

// RecoveredJob is the control plane's persisted record of one non-terminal
// run bound to a route binding this replica just asked about. It carries the
// same route-binding fields job.forPersist would send to the control plane
// (NodeID, RuntimeID, BackendRunID) because recovery is that flow run in
// reverse: instead of this replica telling the control plane what it knows,
// the control plane is telling this replica what the replica has forgotten.
//
// RecoveredJob 是控制面对一次非终态运行的持久化记录，绑定在本副本刚刚问起的
// 那个路由绑定上。它携带的路由绑定字段（NodeID、RuntimeID、BackendRunID）与
// job.forPersist 会发给控制面的相同，因为恢复正是把那个流程反过来跑一遍：
// 不是本副本告诉控制面自己知道什么，而是控制面告诉本副本，它自己忘记了什么。
type RecoveredJob struct {
	JobID           string
	TenantID        string
	WorkflowID      string
	WorkflowVersion string
	NodeID          string
	RuntimeID       string
	BackendRunID    string
	State           string
	ErrorSummary    string
	ObservedSeq     int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// JobRecoveryClient is what the background recoverer needs to ask the
// control plane which non-terminal jobs are bound to a route this replica
// can currently reach.
//
// It is declared here with primitive-typed parameters for the same reason
// JobPersistClient is: controlplaneclient already imports this package, so
// the reverse import would be a cycle. controlplaneclient's GatewayPersister
// satisfies this interface too, alongside JobPersistClient — recovery reads
// and persistence writes are different concerns, but nothing stops one
// adapter from answering both.
//
// JobRecoveryClient 是后台恢复器询问控制面「哪些非终态 job 绑定在本副本此刻
// 够得着的路由上」所需要的东西。
//
// 它用原始类型参数在这里声明，理由与 JobPersistClient 相同：
// controlplaneclient 已经导入了本包，反向导入就会成环。controlplaneclient
// 的 GatewayPersister 同时满足这个接口与 JobPersistClient——恢复读取与持久化
// 写入是不同的关切，但没有什么阻止一个适配器同时回答两者。
type JobRecoveryClient interface {
	ListActiveJobsForRoute(ctx context.Context, nodeID, runtimeID string) ([]RecoveredJob, error)
}

// jobRecoverCandidates is the one scheduler method the recoverer needs. It
// is an interface rather than a concrete *scheduler.Scheduler for the same
// reason workflowStatusAsker is in jobsync.go: a test drives it against a
// stub without a tunnel.
//
// jobRecoverCandidates 是恢复器需要的唯一一个 scheduler 方法。它是一个接口
// 而不是具体的 *scheduler.Scheduler，理由与 jobsync.go 的 workflowStatusAsker
// 相同：测试能针对一个桩来驱动它，而无需搭起隧道。
type jobRecoverCandidates interface {
	WorkflowCapableCandidates() []scheduler.Candidate
}

// jobRecoverConfig collects the recoverer's tunable bounds, defaulted by
// newJobRecoverer so callers only need to set what they want to override.
//
// jobRecoverConfig 收集恢复器的可调上限，由 newJobRecoverer 补上默认值，
// 因此调用方只需设置想要覆盖的部分。
type jobRecoverConfig struct {
	Interval    time.Duration
	Concurrency int
	CallTimeout time.Duration
}

// jobRecoverer is STATUS.md's J06: it periodically asks the control plane,
// for every node/runtime currently connected to this replica, which
// non-terminal jobs are bound to it — and adds back any this replica does
// not already know, restoring exactly the route binding
// (job.Candidate + job.RunID) that cancel, artifact access and the status
// endpoints need, without ever holding or deserializing a connection
// object: NodeRuntime already resolves nodeID fresh on every call (see
// tunnelserver's node_runtime.go), so a recovered job's Candidate is nothing
// more than the two strings it always was.
//
// Recovery authority is deliberately not exclusive. Any replica this
// node/runtime pair is connected to may recover and act on the same job —
// STATUS.md's own P2 notes an Agent may hold connections to more than one
// Gateway replica at once — and nothing here elects one of them. Two
// replicas independently syncing and persisting the same job is already
// safe by construction: the control plane's ObservedSeq gate (see the
// ControlPlane README's 「Job 持久化契约」) resolves concurrent writes without
// coordination, the same way it already resolves a foreground poll racing a
// background sync on one replica. There is no lock to acquire because there
// is nothing a lock would need to protect.
//
// A node that never reconnects to any replica is not treated as a failure:
// this recoverer never marks an unreachable job's state on its own
// initiative, matching README's standing rule against fabricating a result
// nobody reported. The job's persisted record simply stays at its last
// observed state, exactly as it would if the node had merely gone quiet
// without a restart in between — see jobsync.go's own handling of a node
// that has disappeared.
//
// jobRecoverer 是 STATUS.md 的 J06：它周期性地就本副本此刻连接的每一个
// 节点/runtime，向控制面询问绑定在它上面的非终态 job 有哪些——并把本副本尚不
// 知道的补回来，精确恢复取消、产物访问与状态端点所需要的那份路由绑定
// （job.Candidate + job.RunID），且从不持有或反序列化任何连接对象：
// NodeRuntime 本就在每次调用时重新解析 nodeID（见 tunnelserver 的
// node_runtime.go），因此一个被恢复 job 的 Candidate 不过是它一直以来的那
// 两个字符串。
//
// 恢复的执行权刻意不是排他的。这个节点/runtime 连接到的任何副本，都可以恢复
// 并操作同一个 job——STATUS.md 自己的 P2 提到一个 Agent 可能同时持有到不止
// 一个 Gateway 副本的连接——这里没有任何东西会在它们之间选出一个。两个副本
// 各自独立地同步与持久化同一个 job，在构造上就是安全的：控制面的
// ObservedSeq 门槛（见 ControlPlane README「Job 持久化契约」）无需协调即可
// 化解并发写入，就像它已经化解单个副本上一次前台轮询与一次后台同步的竞争
// 那样。这里没有锁要拿，因为没有什么东西需要锁来保护。
//
// 一个再也没有重新连接到任何副本的节点，不会被当作一次失败处理：本恢复器
// 从不主动为一个够不着的 job 标记状态，这与 README 一贯反对编造没人报告过的
// 结果的规则一致。该 job 持久化的记录只会停在最后观测到的状态，与节点在两次
// 重启之间只是安静下来时完全一样——见 jobsync.go 自己对一个已消失节点的处理。
type jobRecoverer struct {
	jobs   *jobStore
	sched  jobRecoverCandidates
	client JobRecoveryClient
	clock  runtime.Clock
	logger *slog.Logger
	cfg    jobRecoverConfig

	stop chan struct{}
	done chan struct{}
}

// newJobRecoverer builds a recoverer with cfg's zero fields replaced by
// defaults. It does not start the background loop; call run in a goroutine
// for that.
//
// newJobRecoverer 用默认值填补 cfg 里的零值字段来构建一个恢复器。它不会启动
// 后台循环，要启动需要以协程方式调用 run。
func newJobRecoverer(jobs *jobStore, sched jobRecoverCandidates, client JobRecoveryClient, clock runtime.Clock, logger *slog.Logger, cfg jobRecoverConfig) *jobRecoverer {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultRecoverInterval
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultRecoverConcurrency
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultRecoverCallTimeout
	}
	return &jobRecoverer{
		jobs: jobs, sched: sched, client: client, clock: clock, logger: logger, cfg: cfg,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// run drives the periodic loop until Stop is called. It is meant to be
// started as `go recoverer.run()`.
//
// run 驱动周期循环，直到 Stop 被调用。它应当以 `go recoverer.run()` 的方式
// 启动。
func (jr *jobRecoverer) run() {
	defer close(jr.done)

	ch, stopTimer := jr.clock.NewTimer(jr.cfg.Interval)
	defer stopTimer()
	for {
		select {
		case <-jr.stop:
			return
		default:
		}
		select {
		case <-jr.stop:
			return
		case <-ch:
			jr.tick()
			ch, stopTimer = jr.clock.NewTimer(jr.cfg.Interval)
		}
	}
}

// tick asks about every currently connected workflow-capable node/runtime,
// bounded by a semaphore, and waits for the whole sweep to finish before
// returning — the same one-round-at-a-time discipline jobSyncer and
// jobPersister follow, so a slow control plane delays the next sweep rather
// than piling concurrent sweeps on top of each other.
//
// tick 就每一个此刻已连接、具备工作流能力的节点/runtime 发问，由一个信号量
// 限定并发，并等待整轮扫描完成后才返回——与 jobSyncer 和 jobPersister 相同的
// 「一次一轮」纪律，好让一个变慢的控制面推迟下一轮扫描，而不是让并发的多轮
// 扫描堆在一起。
func (jr *jobRecoverer) tick() {
	candidates := jr.sched.WorkflowCapableCandidates()
	if len(candidates) == 0 {
		return
	}

	sem := make(chan struct{}, jr.cfg.Concurrency)
	var wg sync.WaitGroup
	for _, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(c scheduler.Candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			jr.recoverRoute(c)
		}(c)
	}
	wg.Wait()
}

// recoverRoute asks about one node/runtime and inserts every job this
// replica did not already know, seeding each one's ObservedSeq and
// persistence bookkeeping from the control plane's own record — not from
// zero — so a freshly recovered job does not have to re-earn, one local
// observation at a time, sequence numbers the control plane already has on
// file. Without this seeding, jobPersister's first few real observations on
// a recovered job would look stale to the control plane (their local
// ObservedSeq starting over from 0) and would be silently dropped as
// no-ops until this replica's own count caught back up.
//
// recoverRoute 就一个节点/runtime 发问，并插入本副本尚不知道的每一个
// job，为每一个都用控制面自己的记录（而不是从零）播种它的 ObservedSeq 与
// 持久化记账——这样一个刚被恢复的 job，就不必靠本地一次次的观测，重新挣得
// 控制面早已存档的序号。没有这份播种，jobPersister 对一个刚恢复的 job 做出的
// 头几次真实观测，会因为本地 ObservedSeq 从 0 重新计数，而在控制面看来是
// 陈旧的，被无声地当作空操作丢弃，直到本副本自己的计数追上为止。
func (jr *jobRecoverer) recoverRoute(c scheduler.Candidate) {
	ctx, cancel := context.WithTimeout(context.Background(), jr.cfg.CallTimeout)
	defer cancel()

	recovered, err := jr.client.ListActiveJobsForRoute(ctx, c.NodeID, c.RuntimeID)
	if err != nil {
		jr.logger.Warn("job recovery did not reach the control plane; any jobs still owed to this route stay unrecovered until the next sweep",
			slog.String("node_id", c.NodeID), slog.String("runtime_id", c.RuntimeID), slog.Any("error", err))
		return
	}

	now := jr.clock.Now()
	for _, rj := range recovered {
		added := jr.jobs.recoverIfMissing(job{
			ID:           rj.JobID,
			WorkflowID:   rj.WorkflowID,
			TenantID:     rj.TenantID,
			Candidate:    scheduler.Candidate{NodeID: rj.NodeID, RuntimeID: rj.RuntimeID},
			RunID:        rj.BackendRunID,
			State:        runtime.WorkflowState(rj.State),
			ErrorSummary: rj.ErrorSummary,
			CreatedAt:    rj.CreatedAt,
			UpdatedAt:    rj.UpdatedAt,
			ObservedSeq:  rj.ObservedSeq,
			// This job's control-plane record is exactly what was just
			// read, so it is persisted by definition — persistedSeq is
			// seeded to match ObservedSeq, not left at zero.
			//
			// 这个 job 控制面的记录正是刚刚读到的这份，因此它按定义就是
			// 已持久化的——persistedSeq 被播种为与 ObservedSeq 一致，
			// 而不是留在零值。
			persisted:     true,
			persistedSeq:  rj.ObservedSeq,
			nextPersistAt: now,
			nextSyncAt:    now,
		})
		if added {
			jr.logger.Info("recovered a job's route binding after this replica forgot it",
				slog.String("job_id", rj.JobID), slog.String("node_id", rj.NodeID), slog.String("runtime_id", rj.RuntimeID))
		}
	}
}

// Stop ends the background loop and waits for the current sweep, if any, to
// finish — bounded by the number of currently connected nodes, concurrency
// and per-call timeout, so this wait is itself bounded rather than an
// open-ended drain.
//
// Stop 结束后台循环，并等待正在进行的一轮扫描（如果有）跑完——受当前已连接
// 节点数、并发度与单次调用超时约束，因此这个等待本身是有界的，而不是一次
// 无限期的排空。
func (jr *jobRecoverer) Stop() {
	close(jr.stop)
	<-jr.done
}

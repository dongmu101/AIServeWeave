package modelpull

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"

	"AIServeWeave/common/modelpullstatus"
	"AIServeWeave/common/runtime"
)

// Puller tracks and drives on-demand pulls of a fixed set of named Specs —
// the Agent-local manifest loaded once at construction. Trigger is safe to
// call repeatedly and concurrently (from the tunnel Control stream's own
// goroutine, and once more at Agent startup for the whole manifest); it
// never blocks on a download.
//
// Only one download runs at a time, the same "no concurrent downloads"
// restraint RunManifest documents: a Trigger arriving mid-download enqueues
// behind whatever is already running rather than starting a second one.
// Config.QuotaBytes is established fresh each time the queue goes from empty
// to non-empty (a "worker session") and shared by everything that session
// processes; a Trigger that arrives while a session is already running joins
// that session's queue and shares its remaining budget rather than getting
// its own.
//
// Puller 围绕一份固定的命名 Spec 集合——构造时加载一次的 Agent 本地清单
// ——追踪并驱动按需拉取。Trigger 可以被反复、并发调用（来自隧道 Control 流
// 自己的 goroutine，以及 Agent 启动时对整份清单再调用一次），它从不会在下
// 载上阻塞。
//
// 同一时间只有一个下载在跑，与 RunManifest 文档的"不做并发下载"是同一种克
// 制：一次下载进行中收到的 Trigger 会排在它后面，而不是另起一个。
// Config.QuotaBytes 在队列从空变为非空的那一刻（一个"worker session"）重新
// 建立，被这次 session 处理的所有条目共享；一次 session 进行中收到的
// Trigger 会加入这次 session 的队列，共享它剩余的预算，而不是获得自己的
// 一份。
type Puller struct {
	// ctx is the Agent's lifetime context, canceled at shutdown. It is the
	// context every download runs under, so a shutdown aborts an in-flight
	// fetch the same way it already did before Puller existed.
	//
	// ctx 是 Agent 的生命周期 context，在关闭时被取消。每一次下载都在它之
	// 下运行，因此关闭会像 Puller 存在之前一样中止正在进行的获取。
	ctx   context.Context
	cfg   Config
	clock runtime.Clock

	byName map[string]Spec

	mu            sync.Mutex
	status        map[string]modelpullstatus.Status
	queue         []string
	queued        map[string]struct{}
	activeWorkers int
	budget        *atomic.Int64
}

// NewPuller builds a Puller over specs. Every name in specs is recorded with
// StateUnspecified until first triggered; a duplicate name is rejected the
// same way validateSpec rejects other malformed entries, by simply never
// making it into byName — the last one wins silently is not an option
// because it would make Trigger's "which Spec does this name mean" question
// ambiguous, so instead the first occurrence wins and later duplicates are
// dropped, the same as a Go map literal would do implicitly, but spelled out
// here since it decides which URL an operator's name resolves to.
//
// NewPuller 基于 specs 构造一个 Puller。specs 里的每个名字在首次被触发之前
// 都记为 StateUnspecified；重复的名字不会像 validateSpec 拒绝其他格式错误
// 的条目那样被拒绝——"后一个覆盖前一个"不是一个选项，因为那会让 Trigger 的
// "这个名字到底指哪个 Spec" 变得含糊，所以改为第一次出现的生效、后面重复
// 的被丢弃，这与 Go map 字面量的隐式行为一样，只是在这里写明，因为它决定
// 了运维的一个名字最终解析到哪个 URL。
func NewPuller(ctx context.Context, cfg Config, specs []Spec, clock runtime.Clock) *Puller {
	byName := make(map[string]Spec, len(specs))
	status := make(map[string]modelpullstatus.Status, len(specs))
	for _, spec := range specs {
		if spec.Name == "" {
			continue
		}
		if _, dup := byName[spec.Name]; dup {
			continue
		}
		byName[spec.Name] = spec
		status[spec.Name] = modelpullstatus.Status{Name: spec.Name, State: modelpullstatus.StateUnspecified, BytesTotal: spec.SizeBytes, UpdatedAt: clock.Now()}
	}
	return &Puller{
		ctx:    ctx,
		cfg:    cfg,
		clock:  clock,
		byName: byName,
		status: status,
		queued: make(map[string]struct{}),
	}
}

// Trigger asks the Puller to start pulling names. A name absent from the
// manifest is recorded as StateFailed/ReasonUnknownName immediately and
// never queued — it never touches the network. A name already pending,
// downloading, queued, or done is left alone: Trigger is idempotent for a
// name that is in flight or finished, not a request to restart it. Trigger
// itself never blocks; it starts worker goroutines only when the queue was
// empty before this call, up to min(Config.MaxConcurrency, len(queue)) of
// them (subtask 4 — see Config.MaxConcurrency's doc). A Trigger arriving
// while workers are already running joins the same queue and shares its
// budget rather than spawning more workers than that first session started.
//
// Trigger 请求 Puller 开始拉取 names。清单里没有的名字立即记为
// StateFailed/ReasonUnknownName，从不入队——也从不触碰网络。已经是
// pending、downloading、已排队或已完成的名字保持不变：对一个在途或已完成
// 的名字调用 Trigger 是幂等的，不是要求重新开始。Trigger 本身从不阻塞；只
// 有在这次调用之前队列为空时，它才会启动最多 min(Config.MaxConcurrency,
// len(queue)) 个 worker goroutine（子任务四，见 Config.MaxConcurrency 的文
// 档）。worker 已经在跑时到达的 Trigger 加入同一个队列、共享它的预算，而
// 不会比第一次 session 启动时多起 worker。
func (p *Puller) Trigger(names []string) {
	p.mu.Lock()
	var workersToStart int
	for _, name := range names {
		spec, ok := p.byName[name]
		if !ok {
			p.status[name] = modelpullstatus.Status{Name: name, State: modelpullstatus.StateFailed, Reason: modelpullstatus.ReasonUnknownName, UpdatedAt: p.clock.Now()}
			continue
		}
		if cur, known := p.status[name]; known && (cur.State == modelpullstatus.StatePending || cur.State == modelpullstatus.StateDownloading || cur.State == modelpullstatus.StateDone) {
			continue
		}
		if _, alreadyQueued := p.queued[name]; alreadyQueued {
			continue
		}
		p.status[name] = modelpullstatus.Status{Name: name, State: modelpullstatus.StatePending, BytesTotal: spec.SizeBytes, UpdatedAt: p.clock.Now()}
		p.queued[name] = struct{}{}
		p.queue = append(p.queue, name)
	}
	if p.activeWorkers == 0 && len(p.queue) > 0 {
		if p.cfg.QuotaBytes > 0 {
			p.budget = new(atomic.Int64)
			p.budget.Store(p.cfg.QuotaBytes)
		} else {
			p.budget = nil
		}
		workersToStart = p.cfg.MaxConcurrency
		if workersToStart < 1 {
			workersToStart = 1
		}
		if workersToStart > len(p.queue) {
			workersToStart = len(p.queue)
		}
		p.activeWorkers = workersToStart
	}
	p.mu.Unlock()

	for i := 0; i < workersToStart; i++ {
		go p.worker()
	}
}

// Snapshot returns the current status of every name the manifest declares,
// sorted by name so repeated calls with no change produce identical output
// — the Agent's tunnel Control session relies on that for its "only report
// when something changed" throttling.
//
// Snapshot 返回清单里每一个名字的当前状态，按名字排序，这样没有变化时反复
// 调用会产出完全一致的输出——Agent 隧道 Control 会话的"只在有变化时上报"节
// 流机制依赖这一点。
func (p *Puller) Snapshot() []modelpullstatus.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]modelpullstatus.Status, 0, len(p.status))
	for _, st := range p.status {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// worker drains the queue one name at a time until it is empty, then exits.
// Multiple workers (up to Config.MaxConcurrency, subtask 4) drain the same
// queue concurrently — each iteration pops under p.mu, so two workers never
// take the same name. A later Trigger that finds the queue non-empty relies
// on p.activeWorkers to decide whether new workers are needed; each worker
// decrements it, right before returning, under the same lock a concurrent
// Trigger takes — so Trigger and every worker never race on how many workers
// are live.
//
// worker 一次一个地清空队列，直到队列为空后退出。多个 worker（最多
// Config.MaxConcurrency 个，子任务四）会并发清空同一个队列——每次取值都在
// p.mu 之下，因此两个 worker 不会取到同一个名字。之后如果 Trigger 发现队
// 列非空，靠 p.activeWorkers 判断是否需要新的 worker；每个 worker 递减它
// 的时机都在自己返回之前、用的是并发 Trigger 会用到的同一把锁——因此
// Trigger 与每个 worker 永远不会在"有多少个 worker 存活"这件事上竞态。
func (p *Puller) worker() {
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.activeWorkers--
			p.mu.Unlock()
			return
		}
		name := p.queue[0]
		p.queue = p.queue[1:]
		delete(p.queued, name)
		spec := p.byName[name]
		budget := p.budget
		p.mu.Unlock()

		p.runOne(name, spec, budget)
	}
}

// runOne pulls one named spec and records its outcome in p.status. It never
// returns an error: by the time a name reaches here it has already left
// Trigger's synchronous path, so there is no caller left to hand one to —
// every outcome, success or failure, is a status update instead.
//
// runOne 拉取一个命名 spec 并把结果记入 p.status。它从不返回错误：一个名
// 字走到这里时早已离开了 Trigger 的同步路径，已经没有调用方可以接收错误
// ——无论成功还是失败，结果都变成一次状态更新。
func (p *Puller) runOne(name string, spec Spec, budget *atomic.Int64) {
	if err := validateSpec(spec); err != nil {
		p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateFailed, Reason: modelpullstatus.ReasonInvalidSpec, UpdatedAt: p.clock.Now()})
		return
	}
	if spec.Kind == KindOllama {
		p.runOllama(name, spec)
		return
	}
	if alreadySatisfied(spec) {
		p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateDone, BytesTotal: spec.SizeBytes, BytesDownloaded: spec.SizeBytes, UpdatedAt: p.clock.Now()})
		return
	}
	if !sourceAllowed(spec.SourceURL, p.cfg.Allowlist) {
		p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateFailed, Reason: modelpullstatus.ReasonNotAllowlisted, UpdatedAt: p.clock.Now()})
		return
	}

	p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateDownloading, BytesTotal: spec.SizeBytes, UpdatedAt: p.clock.Now()})

	client := p.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	err := pullOne(p.ctx, client, spec, pullOpts{
		Budget:              budget,
		Ledger:              p.cfg.Ledger,
		LedgerQuotaBytes:    p.cfg.LedgerQuotaBytes,
		DiskFreeMarginBytes: p.cfg.DiskFreeMarginBytes,
		OnProgress: func(downloaded int64) {
			p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateDownloading, BytesTotal: spec.SizeBytes, BytesDownloaded: downloaded, UpdatedAt: p.clock.Now()})
		},
	})
	if err != nil {
		p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateFailed, Reason: classifyPullError(err), UpdatedAt: p.clock.Now()})
		return
	}
	p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateDone, BytesTotal: spec.SizeBytes, BytesDownloaded: spec.SizeBytes, UpdatedAt: p.clock.Now()})
}

// runOllama pulls a KindOllama spec via pullOllama and records its outcome,
// the Ollama-native counterpart to runOne's KindHTTP branch. It skips
// p.cfg.Allowlist and budget entirely — neither concept applies to a spec
// with no SourceURL and no byte count this process controls (see pullOllama's
// doc and docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md).
//
// runOllama 经由 pullOllama 拉取一个 KindOllama 的 spec 并记录结果，是
// runOne 的 KindHTTP 分支在 Ollama 原生一侧的对应实现。它完全跳过
// p.cfg.Allowlist 与配额——两者都不适用于一个没有 SourceURL、也没有这个进
// 程能控制的字节数的 spec（见 pullOllama 的文档与
// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md）。
func (p *Puller) runOllama(name string, spec Spec) {
	p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateDownloading, UpdatedAt: p.clock.Now()})

	client := p.cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	var lastDownloaded, lastTotal int64
	err := pullOllama(p.ctx, client, p.cfg.OllamaBaseURL, spec, func(downloaded, total int64) {
		lastDownloaded, lastTotal = downloaded, total
		p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateDownloading, BytesDownloaded: downloaded, BytesTotal: total, UpdatedAt: p.clock.Now()})
	})
	if err != nil {
		p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateFailed, Reason: classifyPullError(err), UpdatedAt: p.clock.Now()})
		return
	}
	p.setStatus(name, modelpullstatus.Status{Name: name, State: modelpullstatus.StateDone, BytesDownloaded: lastDownloaded, BytesTotal: lastTotal, UpdatedAt: p.clock.Now()})
}

func (p *Puller) setStatus(name string, st modelpullstatus.Status) {
	p.mu.Lock()
	p.status[name] = st
	p.mu.Unlock()
}

// classifyPullError maps a pullOne error onto the closed wire vocabulary via
// errors.Is against the package's sentinels, never by inspecting err's own
// message text — which may embed spec.SourceURL and must not.
//
// classifyPullError 通过 errors.Is 与本包的哨兵错误比对，把一个 pullOne 错
// 误映射到封闭的线上词表，从不检查 err 自己的消息文本——那可能带有
// spec.SourceURL，绝不能这样做。
func classifyPullError(err error) modelpullstatus.FailureReason {
	switch {
	case errors.Is(err, errQuotaExceeded):
		return modelpullstatus.ReasonQuotaExceeded
	case errors.Is(err, errUnexpectedStatus):
		return modelpullstatus.ReasonUnexpectedStatus
	case errors.Is(err, errChecksumMismatch):
		return modelpullstatus.ReasonChecksumMismatch
	case errors.Is(err, errStorageFailed):
		return modelpullstatus.ReasonStorageError
	case errors.Is(err, errOllamaUnconfigured):
		return modelpullstatus.ReasonOllamaUnconfigured
	case errors.Is(err, errOllamaPullFailed):
		return modelpullstatus.ReasonOllamaPullFailed
	case errors.Is(err, errLedgerQuotaExceeded):
		return modelpullstatus.ReasonLedgerQuotaExceeded
	case errors.Is(err, errDiskSpaceLow):
		return modelpullstatus.ReasonDiskSpaceLow
	default:
		// errFetchFailed and any error pullOne did not wrap in a sentinel
		// (there should be none) both land here: a transport failure is the
		// most likely explanation either way.
		//
		// errFetchFailed 与任何 pullOne 没有包进哨兵错误的错误（理论上不
		// 应该有）都落在这里：无论哪种情况，传输失败都是最可能的解释。
		return modelpullstatus.ReasonFetchFailed
	}
}

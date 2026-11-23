package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// This file shares rate-limit cooldowns across Gateway replicas. Without it
// each replica keeps its own: an account one replica has learned is spent is
// still tried by every other replica, each paying a failed attempt to learn
// the same thing.
//
// The store is the authority once sharing is on. A replica's own rate_limited
// answers are written to it, every replica pulls the full set on an interval,
// and a success anywhere clears the entry for all. Nothing here sits on the
// request path: record and limited touch only memory, and one goroutine
// (RunSharedCooldowns) does every store round trip. A store that is down
// costs sharing, not requests — each replica keeps its last pulled set and
// its own writes until the store answers again.
//
// 本文件在 Gateway 副本之间共享限流冷却。没有它时每个副本各管各的：一个副本已经得知
// 用完的账户，其他副本仍会去尝试，每个都要付出一次失败才能得知同一件事。
//
// 开启共享后，存储是权威。副本自己收到的 rate_limited 写入存储，每个副本按间隔拉取
// 全集，任何一处的成功都会为所有副本清除该条目。这里没有任何东西位于请求路径上：
// record 与 limited 只访问内存，所有存储往返由一个协程（RunSharedCooldowns）完成。
// 存储故障损失的是共享，而不是请求——每个副本保留最近一次拉到的集合与自己的写入，直到
// 存储重新应答。

// Cooldown is one candidate's rate-limit cooldown: Since is when the backend
// refused it, Until when the cooldown ends. Since is what lets a quota
// snapshot end a cooldown early — only a check taken after the refusal can
// say the quota came back (quota.go).
//
// Cooldown 是一个候选的限流冷却：Since 是后端拒绝它的时刻，Until 是冷却结束的时刻。
// 有了 Since，额度快照才能提前结束冷却——只有在拒绝之后做的检查才能说明额度已经恢复
// （quota.go）。
type Cooldown struct {
	Since time.Time
	Until time.Time
}

// SharedCooldowns is a fleet-wide record of rate-limit cooldowns: which
// candidates are set aside, since and until when.
//
// SharedCooldowns 是集群级的限流冷却记录：哪些候选被搁置、从何时起、到何时止。
type SharedCooldowns interface {
	// Mark records cd for c, replacing any earlier entry for c.
	//
	// Mark 为 c 记录 cd，替换 c 此前的任何条目。
	Mark(ctx context.Context, c Candidate, cd Cooldown) error
	// Clear removes c's entry, if any.
	//
	// Clear 删除 c 的条目（如果有）。
	Clear(ctx context.Context, c Candidate) error
	// Active returns every entry still in force at now.
	//
	// Active 返回在 now 时刻仍然有效的全部条目。
	Active(ctx context.Context, now time.Time) (map[Candidate]Cooldown, error)
}

// sharedCooldownWriteBuffer bounds the writes waiting for RunSharedCooldowns.
// Rate-limit answers are rare, so a full buffer means the loop is stuck on a
// slow store; the write is then dropped rather than block a dispatch.
//
// sharedCooldownWriteBuffer 限定等待 RunSharedCooldowns 处理的写入数量。限流应答很少，
// 缓冲已满意味着循环卡在缓慢的存储上；此时丢弃写入，而不是阻塞派发。
const sharedCooldownWriteBuffer = 256

// sharedCooldownTimeout bounds one store round trip, so a hung store delays
// the next pull by at most this long.
//
// sharedCooldownTimeout 限定一次存储往返，使挂起的存储最多让下一次拉取延迟这么久。
const sharedCooldownTimeout = 2 * time.Second

// DefaultSharedCooldownInterval is how often RunSharedCooldowns pulls the
// fleet's cooldowns when the caller gives no interval.
//
// DefaultSharedCooldownInterval 是调用方未给出间隔时 RunSharedCooldowns 拉取集群冷却的
// 频率。
const DefaultSharedCooldownInterval = 2 * time.Second

// cooldownWrite is one change waiting to reach the store: a zero cooldown
// clears the entry.
//
// cooldownWrite 是一项等待写入存储的变化：冷却为零值表示清除条目。
type cooldownWrite struct {
	c  Candidate
	cd Cooldown
}

// shareCooldowns switches the registry to shared cooldowns. It is called once,
// before the registry is shared.
//
// shareCooldowns 把注册表切换为共享冷却。它只在注册表被共享之前调用一次。
func (r *breakerRegistry) shareCooldowns() {
	r.writes = make(chan cooldownWrite, sharedCooldownWriteBuffer)
	r.shared = make(map[Candidate]Cooldown)
	r.pending = make(map[Candidate]Cooldown)
}

// cooldownLocked returns c's rate-limit cooldown; a zero Until means it has
// none. With sharing on, a change not yet written to the store
// (pending) wins over the last pulled set, so a pull that raced the write
// cannot undo it. Callers hold r.mu.
//
// cooldownLocked 返回 c 的限流冷却；Until 为零表示没有冷却。开启共享时，尚未写入
// 存储的变化（pending）优先于最近一次拉取的集合，使与写入赛跑的拉取无法撤销它。调用方
// 持有 r.mu。
func (r *breakerRegistry) cooldownLocked(c Candidate) Cooldown {
	if r.writes == nil {
		if st, ok := r.entries[c]; ok {
			return st.limited
		}
		return Cooldown{}
	}
	if cd, ok := r.pending[c]; ok {
		return cd
	}
	return r.shared[c]
}

// setLimitedLocked sets c's rate-limit cooldown to cd (a zero Until clears
// it) and, with sharing on, queues the change for the store. A change the
// queue cannot take is applied to this replica's pulled set only, so the next
// pull replaces it with whatever the store says; dropped reports that, for
// the caller to log once it has released r.mu. Callers hold r.mu.
//
// setLimitedLocked 把 c 的限流冷却设为 cd（Until 为零表示清除），开启共享时把这项
// 变化排队写入存储。队列装不下的变化只作用于本副本拉取到的集合，下一次拉取会用存储里的
// 内容替换它；dropped 报告这种情况，由调用方在释放 r.mu 之后记录日志。调用方持有 r.mu。
func (r *breakerRegistry) setLimitedLocked(c Candidate, cd Cooldown) (dropped bool) {
	if cd.Until.IsZero() {
		cd = Cooldown{}
	}
	if r.writes == nil {
		st, ok := r.entries[c]
		if !ok {
			st = &breakerState{}
			r.entries[c] = st
		}
		st.limited = cd
		return false
	}
	if cd.Until.IsZero() {
		delete(r.shared, c)
	} else {
		r.shared[c] = cd
	}
	select {
	case r.writes <- cooldownWrite{c: c, cd: cd}:
		r.pending[c] = cd
		return false
	default:
		delete(r.pending, c)
		return true
	}
}

// settleWrite marks w as having reached the store (or failed to), so the next
// pull is authoritative for it again. The written value is also applied to
// the pulled set: the last pull predates the write (RunSharedCooldowns runs
// both in one goroutine), so without this the entry would vanish until the
// next pull. A newer change for the same candidate stays pending and keeps
// overriding.
//
// settleWrite 标记 w 已写入存储（或写入失败），使下一次拉取重新对它具有权威。写入的值
// 同时作用到拉取的集合：最近一次拉取早于这次写入（RunSharedCooldowns 在同一个协程里做
// 两件事），不这样做的话，条目会消失到下一次拉取为止。同一候选更新的变化仍保持待写入，
// 并继续优先生效。
func (r *breakerRegistry) settleWrite(w cooldownWrite) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cd, ok := r.pending[w.c]
	if !ok || !cd.Until.Equal(w.cd.Until) || !cd.Since.Equal(w.cd.Since) {
		return
	}
	delete(r.pending, w.c)
	if w.cd.Until.IsZero() {
		delete(r.shared, w.c)
	} else {
		r.shared[w.c] = w.cd
	}
}

// replaceShared installs a freshly pulled set.
//
// replaceShared 装入一份新拉取的集合。
func (r *breakerRegistry) replaceShared(active map[Candidate]Cooldown) {
	if active == nil {
		active = make(map[Candidate]Cooldown)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shared = active
}

// RunSharedCooldowns keeps this replica's rate-limit cooldowns in step with
// Config.SharedCooldowns until ctx ends: it writes this replica's changes as
// they happen and pulls the fleet's set every interval (zero uses
// DefaultSharedCooldownInterval). It returns at once when no store is
// configured.
//
// RunSharedCooldowns 使本副本的限流冷却与 Config.SharedCooldowns 保持同步，直到 ctx
// 结束：本副本的变化发生时即写入，并每隔 interval（零值使用
// DefaultSharedCooldownInterval）拉取集群的集合。未配置存储时立即返回。
func (s *Scheduler) RunSharedCooldowns(ctx context.Context, interval time.Duration) {
	if s.sharedCooldowns == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultSharedCooldownInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	healthy := true
	report := func(err error) {
		// Log transitions only: a store that is down would otherwise log on
		// every pull.
		// 只记录状态变化：否则故障的存储会在每次拉取时都打一条日志。
		switch {
		case err != nil && healthy:
			s.logger.Warn("shared cooldown store unavailable; keeping this replica's last known cooldowns", slog.Any("error", err))
		case err == nil && !healthy:
			s.logger.Info("shared cooldown store available again")
		}
		healthy = err == nil
	}
	pull := func() {
		opCtx, cancel := context.WithTimeout(ctx, sharedCooldownTimeout)
		active, err := s.sharedCooldowns.Active(opCtx, s.clock.Now())
		cancel()
		if err == nil {
			s.breakers.replaceShared(active)
		}
		report(err)
	}
	pull()
	for {
		select {
		case <-ctx.Done():
			return
		case w := <-s.breakers.writes:
			opCtx, cancel := context.WithTimeout(ctx, sharedCooldownTimeout)
			var err error
			if w.cd.Until.IsZero() {
				err = s.sharedCooldowns.Clear(opCtx, w.c)
			} else {
				err = s.sharedCooldowns.Mark(opCtx, w.c, w.cd)
			}
			cancel()
			s.breakers.settleWrite(w)
			report(err)
		case <-ticker.C:
			pull()
		}
	}
}

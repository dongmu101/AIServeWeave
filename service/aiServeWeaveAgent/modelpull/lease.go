package modelpull

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

const (
	// defaultLeaseBytes is how many bytes a ledgerLease charges to the
	// Ledger at a time. A 10GiB download therefore rewrites the ledger file
	// about 160 times instead of about 330,000 times (one per 32KiB chunk).
	//
	// defaultLeaseBytes 是一个 ledgerLease 每次向 Ledger 计费的字节数。因此
	// 一次 10GiB 的下载大约只重写账本文件 160 次，而不是约 33 万次（每 32KiB
	// 分块一次）。
	defaultLeaseBytes int64 = 64 << 20
	// minLeaseBytes keeps a tiny quota or a tiny size hint from degrading
	// the lease back into a per-chunk reservation.
	//
	// minLeaseBytes 防止极小的配额或极小的大小提示把租约退化回逐分块预留。
	minLeaseBytes int64 = 1 << 20
	// leaseQuotaDivisor bounds one lease to a fraction of the ledger quota,
	// so a few concurrent downloads cannot park most of a small quota in
	// unused lease room.
	//
	// leaseQuotaDivisor 把单个租约限制在账本配额的一个比例之内，避免几个并发
	// 下载把一个较小配额的大部分停放在未使用的租约空间里。
	leaseQuotaDivisor int64 = 16
)

// ledgerLease charges a Ledger in large blocks on behalf of one download and
// hands the unused part back when the download ends. The ledger's persisted
// BytesUsed always includes every byte a live lease has been granted, so
// concurrent downloads sharing one quota can never jointly exceed it: the
// quota check and the grant happen together under Ledger.mu, exactly as a
// per-chunk Reserve did, just once per block instead of once per chunk.
//
// A ledgerLease belongs to one goroutine (the download's copy loop); only the
// Ledger it talks to is shared.
//
// ledgerLease 代表一次下载以大块方式向 Ledger 计费，并在下载结束时归还未使
// 用的部分。账本持久化的 BytesUsed 始终包含所有存活租约已被授予的字节，因
// 此共享同一份配额的并发下载永远不可能合计超额：配额检查与授予在
// Ledger.mu 下一起完成，与逐分块的 Reserve 完全一样，只是每个大块一次而不
// 是每个分块一次。
//
// ledgerLease 归属于一个 goroutine（下载的拷贝循环）；只有它所对话的
// Ledger 是共享的。
type ledgerLease struct {
	ledger     *Ledger
	id         string
	path       string
	quotaBytes int64
	chunk      int64

	// entryBase is the file offset at which the charge recorded in the
	// ledger entry begins; granted is how many bytes that entry holds and
	// used how many of them this download has consumed so far.
	//
	// entryBase 是账本条目所记的这笔计费开始处的文件偏移；granted 是该条目
	// 持有的字节数，used 是本次下载目前已经消耗的字节数。
	entryBase int64
	granted   int64
	used      int64
	opened    bool
	closed    bool
}

// newLease prepares a lease for a download of path whose next byte lands at
// file offset base. sizeHint is the number of bytes the download expects to
// write (<= 0 if unknown). Nothing is charged, and the ledger file is not
// touched, until the first Consume.
//
// newLease 为一次下载 path 的任务准备租约，其下一个字节落在文件偏移 base。
// sizeHint 是下载预计写入的字节数（未知则 <= 0）。在第一次 Consume 之前不
// 计费，也不碰账本文件。
func (l *Ledger) newLease(path string, base, quotaBytes, sizeHint int64) *ledgerLease {
	chunk := defaultLeaseBytes
	if quotaBytes > 0 && quotaBytes/leaseQuotaDivisor < chunk {
		chunk = quotaBytes / leaseQuotaDivisor
	}
	if sizeHint > 0 && sizeHint < chunk {
		chunk = sizeHint
	}
	if chunk < minLeaseBytes {
		chunk = minLeaseBytes
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return &ledgerLease{
		ledger:     l,
		id:         hex.EncodeToString(raw[:]),
		path:       path,
		quotaBytes: quotaBytes,
		chunk:      chunk,
		entryBase:  base,
	}
}

// Consume takes n bytes out of the lease, extending it from the ledger when
// the granted room runs out. It returns an error wrapping
// errLedgerQuotaExceeded when the ledger cannot cover n more bytes.
//
// Consume 从租约中取走 n 字节，授予的空间用完时向账本续租。当账本无法再覆
// 盖 n 字节时，返回包裹 errLedgerQuotaExceeded 的错误。
func (ls *ledgerLease) Consume(n int64) error {
	if n <= ls.granted-ls.used {
		ls.used += n
		return nil
	}
	if err := ls.extend(n); err != nil {
		return err
	}
	ls.used += n
	return nil
}

// Refund gives back n bytes that Consume handed out but the writer below did
// not actually accept (a short write).
//
// Refund 归还 n 字节：Consume 给出了它们，但下层 writer 实际没有接受（短写）。
func (ls *ledgerLease) Refund(n int64) {
	ls.used -= n
	if ls.used < 0 {
		ls.used = 0
	}
}

// extend charges the ledger for enough room to consume n more bytes (plus a
// whole chunk when the quota allows it) and records the grant.
//
// extend 向账本为再消耗 n 字节申请足够的空间（配额允许时再多要一整块），并
// 记录这次授予。
func (ls *ledgerLease) extend(n int64) error {
	l := ls.ledger
	l.mu.Lock()
	defer l.mu.Unlock()

	state, _, err := l.loadLocked()
	if err != nil {
		return err
	}

	// If the entry vanished while we held it, a Period reset wiped the
	// ledger: bytes written so far belong to the previous period, so start a
	// fresh entry from the current file offset.
	//
	// 如果持有期间条目消失了，说明 Period 重置清空了账本：到目前为止写入的
	// 字节属于上一个周期，因此从当前文件偏移开始一个全新的条目。
	_, present := state.Leases[ls.id]
	granted, used, entryBase := ls.granted, ls.used, ls.entryBase
	if ls.opened && !present {
		entryBase += used
		granted, used = 0, 0
	}

	need := n - (granted - used)
	ext := ls.chunk
	if ext < need {
		ext = need
	}
	if ls.quotaBytes > 0 {
		if room := ls.quotaBytes - state.BytesUsed; ext > room {
			ext = room
		}
		if ext < need {
			return fmt.Errorf("%w: need %d bytes, %d of %d already used", errLedgerQuotaExceeded, need, state.BytesUsed, ls.quotaBytes)
		}
	}

	if state.Leases == nil {
		state.Leases = make(map[string]ledgerLeaseEntry)
	}
	entry := state.Leases[ls.id]
	if !present {
		entry = ledgerLeaseEntry{Path: ls.path, Base: entryBase}
	}
	entry.Bytes += ext
	state.Leases[ls.id] = entry
	state.BytesUsed += ext
	if err := l.save(state); err != nil {
		return err
	}
	l.recovered = true

	ls.granted, ls.used, ls.entryBase = granted+ext, used, entryBase
	ls.opened = true
	return nil
}

// Close hands the part of the lease the download never used back to the
// ledger and removes the lease's entry. It is idempotent. If it fails the
// entry stays in the ledger file and is reconciled against the partial
// file's real size on the next process start, so a failure here costs
// accuracy until then, never correctness.
//
// Close 把租约中下载从未用到的部分还给账本，并删除租约的条目。它是幂等的。
// 如果失败，条目会留在账本文件里，下次进程启动时按部分文件的真实大小对账，
// 所以这里失败在那之前只损失精度，不影响正确性。
func (ls *ledgerLease) Close() error {
	if ls.closed || !ls.opened {
		ls.closed = true
		return nil
	}
	ls.closed = true

	l := ls.ledger
	l.mu.Lock()
	defer l.mu.Unlock()

	state, _, err := l.loadLocked()
	if err != nil {
		return err
	}
	entry, present := state.Leases[ls.id]
	if !present {
		return nil
	}
	if unused := entry.Bytes - ls.used; unused > 0 {
		state.BytesUsed -= unused
		if state.BytesUsed < 0 {
			state.BytesUsed = 0
		}
	}
	delete(state.Leases, ls.id)
	if len(state.Leases) == 0 {
		state.Leases = nil
	}
	if err := l.save(state); err != nil {
		return err
	}
	l.recovered = true
	return nil
}

// loadLocked loads the state a mutation should start from: it applies the
// Period reset, and on this instance's first call reconciles the leases a
// crashed predecessor left behind. The caller must hold l.mu, must save the
// returned state if it goes on to charge anything, and must set l.recovered
// once that save succeeded (until then the next call redoes the
// reconciliation, so a failed save never loses it).
//
// loadLocked 加载一次变更应当作为起点的状态：应用 Period 重置，并在本实例
// 第一次调用时对账崩溃的前任遗留的租约。调用方必须持有 l.mu；如果接下来要
// 记账就必须保存返回的状态；并且在保存成功之后设置 l.recovered（在此之前，
// 下一次调用会重做对账，因此保存失败不会丢掉对账结果）。
func (l *Ledger) loadLocked() (state ledgerState, dirty bool, err error) {
	state, err = l.load()
	if err != nil {
		return ledgerState{}, false, err
	}
	now := l.now()
	if l.Period > 0 && now.Sub(state.PeriodStart) >= l.Period {
		state = ledgerState{PeriodStart: now}
		dirty = true
	}
	if state.PeriodStart.IsZero() {
		state.PeriodStart = now
		dirty = true
	}
	if !l.recovered && len(state.Leases) > 0 {
		recoverOrphans(&state)
		dirty = true
	}
	return state, dirty, nil
}

// recoverOrphans settles every lease found in state. They all belong to a
// process that no longer exists, because this instance has not created any
// yet. A lease was charged in full when granted, but a crash means only the
// bytes that actually reached the partial file were used, so the charge is
// trimmed to the file's growth beyond the lease's Base. That is at least as
// accurate as the per-chunk design it replaces, which could over-charge by
// one chunk after a crash. If the file is gone (renamed or deleted just as
// the process died) the full lease stays charged: over-counting is the safe
// direction for a quota.
//
// recoverOrphans 结清 state 里的每一个租约。它们都属于一个已不存在的进程，
// 因为本实例还没有创建过任何租约。租约在授予时已全额计费，但崩溃意味着只有
// 真正落到部分文件里的字节被用掉了，所以把计费修剪为文件相对租约 Base 的增
// 长量。这至少与它所取代的逐分块设计一样准确（后者崩溃后最多多算一个分
// 块）。如果文件不见了（进程死亡时恰好被改名或删除），则保留全额计费：对配
// 额来说多算是安全的方向。
func recoverOrphans(state *ledgerState) {
	for _, e := range state.Leases {
		if fi, err := os.Stat(e.Path); err == nil {
			actual := fi.Size() - e.Base
			if actual < 0 {
				actual = 0
			}
			if actual < e.Bytes {
				state.BytesUsed -= e.Bytes - actual
			}
		}
	}
	if state.BytesUsed < 0 {
		state.BytesUsed = 0
	}
	state.Leases = nil
}

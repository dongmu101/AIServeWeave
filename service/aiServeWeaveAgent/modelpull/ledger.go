package modelpull

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

// errLedgerQuotaExceeded classifies a Ledger.Reserve call that would push the
// cumulative total past LedgerQuotaBytes. It is distinct from
// errQuotaExceeded (Config.QuotaBytes' single-call budget) so an operator's
// log tells the two knobs apart — see
// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md
// 第 2.2 节.
//
// errLedgerQuotaExceeded 归类一次会把累计总量推过 LedgerQuotaBytes 的
// Ledger.Reserve 调用。它与 errQuotaExceeded（Config.QuotaBytes 的单次调用
// 预算）刻意分开，运维看日志才能分清该调哪个数字——见
// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md
// 第 2.2 节。
var errLedgerQuotaExceeded = errors.New("modelpull: ledger quota exceeded")

// errDiskSpaceLow classifies a write aborted because the target filesystem's
// free space fell below Config.DiskFreeMarginBytes.
//
// errDiskSpaceLow 归类一次因目标文件系统剩余空间低于
// Config.DiskFreeMarginBytes 而中止的写入。
var errDiskSpaceLow = errors.New("modelpull: disk space low")

// Ledger persists a cumulative byte total across Agent restarts, unlike
// Config.QuotaBytes which resets to full every time RunManifest or a Puller
// worker session starts. See
// docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md
// 第 2.2 节 for the full design.
//
// Ledger 跨 Agent 重启持久化一个累计字节总量，这与 Config.QuotaBytes 每次
// RunManifest 调用或 Puller worker session 开始时都重新计满不同。完整设计
// 见 docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md
// 第 2.2 节。
type Ledger struct {
	// Path is the JSON file the ledger's state is persisted to.
	//
	// Path 是账本状态持久化的 JSON 文件路径。
	Path string
	// Period is the ledger's rolling window: once Clock.Now() passes
	// PeriodStart+Period, the ledger resets to zero on its next Reserve.
	// <= 0 means the ledger never resets on its own — a true lifetime total.
	//
	// Period 是账本的滚动窗口：一旦 Clock.Now() 超过
	// PeriodStart+Period，账本会在下一次 Reserve 时重置为零。<= 0 表示账本
	// 从不自行重置——真正的生涯总量。
	Period time.Duration
	// Clock provides the current time. A nil Clock uses the real wall
	// clock.
	//
	// Clock 提供当前时间。nil 使用真实的墙钟。
	Clock runtime.Clock

	mu sync.Mutex
}

// ledgerState is Ledger's on-disk JSON shape.
//
// ledgerState 是 Ledger 落盘的 JSON 形状。
type ledgerState struct {
	PeriodStart time.Time `json:"period_start"`
	BytesUsed   int64     `json:"bytes_used"`
}

// now returns l.Clock.Now(), defaulting to the real wall clock.
//
// now 返回 l.Clock.Now()，缺省时使用真实墙钟。
func (l *Ledger) now() time.Time {
	if l.Clock == nil {
		return time.Now()
	}
	return l.Clock.Now()
}

// Reserve records n additional bytes against the ledger's quotaBytes budget.
// A value <= 0 for quotaBytes means the ledger itself imposes no limit (the
// file is still written, so Period resets still work for a caller that
// tightens the quota later). On success it returns a rollback function the
// caller must call with the number of bytes that were reserved but never
// actually written (0 if the full amount was written), so a partial download
// does not permanently consume ledger room it never used.
//
// Reserve 把 n 字节额外记入账本的 quotaBytes 预算。quotaBytes <= 0 表示账本本
// 身不设限（文件仍会写入，这样以后调紧配额时 Period 重置仍然生效）。成功
// 时返回一个 rollback 函数，调用方必须用"预留了但从未真正写入的字节数"
// （如果全部写入了就传 0）调用它一次，这样一次没写完的下载就不会永久占用
// 它从未真正用到的账本空间。
func (l *Ledger) Reserve(n int64, quotaBytes int64) (rollback func(int64), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	state, err := l.load()
	if err != nil {
		return nil, err
	}

	now := l.now()
	if l.Period > 0 && now.Sub(state.PeriodStart) >= l.Period {
		state = ledgerState{PeriodStart: now}
	}
	if state.PeriodStart.IsZero() {
		state.PeriodStart = now
	}

	if quotaBytes > 0 && state.BytesUsed+n > quotaBytes {
		return nil, fmt.Errorf("%w: need %d bytes, %d of %d already used", errLedgerQuotaExceeded, n, state.BytesUsed, quotaBytes)
	}

	state.BytesUsed += n
	if err := l.save(state); err != nil {
		return nil, err
	}

	return func(unused int64) {
		if unused <= 0 {
			return
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		st, err := l.load()
		if err != nil {
			return
		}
		st.BytesUsed -= unused
		if st.BytesUsed < 0 {
			st.BytesUsed = 0
		}
		l.save(st)
	}, nil
}

// load reads the ledger's current state, treating a missing file as a fresh,
// empty ledger rather than an error.
//
// load 读取账本当前状态，文件不存在视为一个全新的空账本而不是错误。
func (l *Ledger) load() (ledgerState, error) {
	data, err := os.ReadFile(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return ledgerState{}, nil
	}
	if err != nil {
		return ledgerState{}, fmt.Errorf("%w: read ledger %q: %v", errStorageFailed, l.Path, err)
	}
	var state ledgerState
	if err := json.Unmarshal(data, &state); err != nil {
		return ledgerState{}, fmt.Errorf("%w: parse ledger %q: %v", errStorageFailed, l.Path, err)
	}
	return state, nil
}

// save writes state to l.Path atomically: a temporary file in the same
// directory, then a rename, the same pattern pullOne uses for the artifact
// itself.
//
// save 把 state 原子写入 l.Path：先在同一目录写临时文件，再改名，与
// pullOne 对制品本身使用的模式相同。
func (l *Ledger) save(state ledgerState) error {
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return fmt.Errorf("%w: create ledger directory: %v", errStorageFailed, err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("%w: encode ledger: %v", errStorageFailed, err)
	}
	tmp := l.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("%w: write ledger: %v", errStorageFailed, err)
	}
	if err := os.Rename(tmp, l.Path); err != nil {
		return fmt.Errorf("%w: finalize ledger: %v", errStorageFailed, err)
	}
	return nil
}

package codexcli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// app-server keeps an index of the account's sessions directory in the
// state database under sqlite_home, and builds it from scratch whenever it
// starts on an empty sqlite_home. Every process here gets a fresh private
// sqlite_home, so with a large history every start used to cost seconds of
// CPU. A seed is a copy of that index taken from a finished read process;
// each new process starts from a copy of the newest seed and only indexes
// what changed since. Only the state database is copied: memories, goals,
// queue and logs always start empty, so nothing crosses requests.
//
// app-server 在 sqlite_home 下的状态库中保存账户 sessions 目录的索引，每次在空的
// sqlite_home 中启动时都会从头构建它。这里每个进程都使用新的私有 sqlite_home，因此
// 历史较大时每次启动都要耗数秒 CPU。种子是从已结束的读取进程中取得的该索引副本；每个
// 新进程从最新种子的副本启动，只为此后的变化建索引。只复制状态库：memories、goals、
// queue 与 logs 始终从空开始，因此不会有任何内容跨请求。

const (
	// seedRefresh is how old the newest seed must be before a read process
	// replaces it; the next process indexes only what changed since.
	// seedRefresh 是读取进程替换最新种子前该种子须达到的年龄；下一个进程只为此后
	// 的变化建索引。
	seedRefresh = 5 * time.Minute
	// seedGrace keeps a superseded seed this long, so a process still copying
	// it is never left with half a copy.
	// seedGrace 是被取代的种子的保留时长，使仍在复制它的进程不会只拿到半份。
	seedGrace = time.Minute
	// seedPrefix names a published seed directory; the rest of the name is
	// its creation time in zero-padded Unix nanoseconds, so names sort by age.
	// seedPrefix 是已发布种子目录的名称前缀；其余部分是补零的 Unix 纳秒创建时间，
	// 因此名称按年龄排序。
	seedPrefix = "seed-"
	// seedStaging names a seed still being written; it is never read.
	// seedStaging 是仍在写入的种子的名称前缀，从不被读取。
	seedStaging = ".staging-"
)

// userCacheDir locates seedRoot's parent; tests replace it.
// userCacheDir 定位 seedRoot 的上级目录；测试会替换它。
var userCacheDir = os.UserCacheDir

// seedRoot returns, creating it, the private directory holding the seeds of
// env's account directory: under the user cache directory and named by a
// digest of the account directory, so two accounts never share one. It
// returns "" when either directory cannot be resolved to an absolute path or
// the directory cannot be created; processes then index from scratch, which
// is slower but otherwise the same.
//
// seedRoot 返回并创建保存 env 所指账户目录的种子的私有目录：位于用户缓存目录下，以账户
// 目录的摘要命名，两个账户永不共用。任一目录无法解析为绝对路径、或目录无法创建时返回
// ""，进程随即从头建索引：更慢，但其余行为相同。
func seedRoot(env []string) string {
	account := codexHomeDir(env)
	if !filepath.IsAbs(account) {
		return ""
	}
	root, err := userCacheDir()
	if err != nil || !filepath.IsAbs(root) {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.Clean(account)))
	dir := filepath.Join(root, "AIServeWeave", "codex-state", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
}

// isStateFile reports whether name is part of app-server's state database:
// state_<version>.sqlite or its write-ahead log. The shared-memory file is
// rebuilt on open and is never copied.
//
// isStateFile 报告 name 是否属于 app-server 的状态库：state_<版本>.sqlite 或其预写
// 日志。共享内存文件在打开时重建，从不复制。
func isStateFile(name string) bool {
	return strings.HasPrefix(name, "state_") && (strings.HasSuffix(name, ".sqlite") || strings.HasSuffix(name, ".sqlite-wal"))
}

// copyStateFiles copies src's state database files into dst and returns how
// many it copied. On error dst may hold some of them; the caller removes them.
// copyStateFiles 把 src 中的状态库文件复制到 dst，并返回复制的数量。出错时 dst 中可能
// 留有部分文件，由调用方删除。
func copyStateFiles(src, dst string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	copied := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isStateFile(entry.Name()) {
			continue
		}
		if err := copyFile(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return copied, err
		}
		copied++
	}
	return copied, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// seedTime parses a published seed's name; ok is false for anything else.
// seedTime 解析已发布种子的名称；其他名称返回 ok 为 false。
func seedTime(name string) (time.Time, bool) {
	digits, found := strings.CutPrefix(name, seedPrefix)
	if !found || len(digits) != 20 {
		return time.Time{}, false
	}
	nanos, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || nanos <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, nanos), true
}

// newestSeed returns the newest published seed directory under root and its
// creation time, or "" when there is none.
// newestSeed 返回 root 下最新的已发布种子目录及其创建时间；没有时返回 ""。
func newestSeed(root string) (string, time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", time.Time{}
	}
	var newest string
	var at time.Time
	for _, entry := range entries {
		if t, ok := seedTime(entry.Name()); ok && entry.IsDir() && t.After(at) {
			newest, at = filepath.Join(root, entry.Name()), t
		}
	}
	return newest, at
}

// restoreSeed copies the newest seed's state database into dir. It is best
// effort: when the copy fails part-way it removes what it copied, so
// app-server indexes from scratch instead of opening half a database.
//
// restoreSeed 把最新种子的状态库复制到 dir。它尽力而为：中途复制失败时删除已复制的
// 文件，使 app-server 从头建索引，而不是打开半个数据库。
func restoreSeed(root, dir string) {
	seed, _ := newestSeed(root)
	if seed == "" {
		return
	}
	if _, err := copyStateFiles(seed, dir); err != nil {
		removeStateFiles(dir)
	}
}

func removeStateFiles(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if isStateFile(entry.Name()) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// promoteSeed publishes the state database a finished read process left in
// dir as root's newest seed, unless the newest is younger than seedRefresh.
// The copy is written under a staging name and renamed into place, so a
// reader only ever sees complete seeds. It then prunes superseded seeds past
// seedGrace and leftovers of interrupted promotions. Every failure leaves the
// existing seeds as they were.
//
// promoteSeed 把已结束的读取进程留在 dir 中的状态库发布为 root 的最新种子，除非最新
// 种子还不到 seedRefresh。副本先以暂存名称写入再改名就位，读取方只会看到完整的种子。
// 随后清理超过 seedGrace 的被取代种子及中断的提升留下的残余。任何失败都不改动已有种子。
func promoteSeed(root, dir string, now time.Time) {
	// A newest seed moments ahead is a concurrent promotion and counts as young.
	// 只超前片刻的最新种子是并发的提升，视为尚新。
	if _, at := newestSeed(root); !at.IsZero() && now.Sub(at) > -seedGrace && now.Sub(at) < seedRefresh {
		return
	}
	staging, err := os.MkdirTemp(root, seedStaging)
	if err != nil {
		return
	}
	copied, err := copyStateFiles(dir, staging)
	if err != nil || copied == 0 {
		_ = os.RemoveAll(staging)
		return
	}
	name := filepath.Join(root, fmt.Sprintf("%s%020d", seedPrefix, now.UnixNano()))
	if err := os.Rename(staging, name); err != nil {
		_ = os.RemoveAll(staging)
		return
	}
	pruneSeeds(root, name, now)
}

// pruneSeeds removes seeds other than keep that were superseded more than
// seedGrace ago or are dated more than seedGrace after now, staging directories older than an hour, and loose database
// files an earlier version kept directly under root. Removal is best effort:
// a file still open elsewhere is retried on the next promotion.
//
// pruneSeeds 删除 keep 以外、被取代超过 seedGrace 或时间晚于 now 超过 seedGrace 的种子，超过一小时的暂存目录，以及旧
// 版本直接放在 root 下的数据库文件。删除尽力而为：仍被其他地方打开的文件留到下次提升时
// 再试。
func pruneSeeds(root, keep string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		switch {
		case path == keep:
		case strings.HasPrefix(entry.Name(), seedPrefix):
			// A seed dated more than seedGrace after now (the clock moved back)
			// would otherwise stay the newest for that long; a concurrent
			// promotion is only moments ahead and is kept.
			// 晚于当前时间超过 seedGrace 的种子（时钟回拨）否则会在这段时间内一直是
			// 最新的；并发的提升只超前片刻，予以保留。
			if t, ok := seedTime(entry.Name()); ok && (now.Sub(t) > seedGrace || t.Sub(now) > seedGrace) {
				_ = os.RemoveAll(path)
			}
		case strings.HasPrefix(entry.Name(), seedStaging):
			if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(path)
			}
		case entry.Type().IsRegular() && strings.Contains(entry.Name(), ".sqlite"):
			_ = os.Remove(path)
		}
	}
}

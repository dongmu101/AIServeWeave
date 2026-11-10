package modelpull

import (
	"context"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"

	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
)

// copyBufSize is the buffer size for every bulk copy and hash in this
// package: 1MiB instead of io.Copy's 32KiB cuts the syscall count 32-fold on
// multi-GB model files.
//
// copyBufSize 是本包里每一次批量拷贝与哈希使用的缓冲区大小：1MiB 而不是
// io.Copy 的 32KiB，在数 GB 的模型文件上把系统调用次数减少到 1/32。
const copyBufSize = 1 << 20

var copyBufPool = sync.Pool{New: func() any {
	b := make([]byte, copyBufSize)
	return &b
}}

// hashSidecarSuffix is appended to TargetPath to name the hash cache file.
//
// hashSidecarSuffix 附加在 TargetPath 之后，作为哈希缓存文件的名字。
const hashSidecarSuffix = ".sha256cache"

// hashSidecar caches the SHA-256 of a finished artifact together with the
// size and modification time it had when it was hashed. It is only a cache
// that lets alreadySatisfied skip re-reading a multi-GB file at every start
// and every trigger; it is never consulted for a file whose size or mtime
// differs, and any read or parse problem falls back to the full hash.
//
// Trust boundary: the sidecar lives in the same directory as the artifact, so
// whoever can write that directory (the Agent's own OS user, root) can
// already replace the model itself, and the sidecar gives them nothing new
// to attack. What it does lose against a full re-hash is detection of
// content changed without touching size or mtime (silent disk corruption, or
// a deliberate edit followed by os.Chtimes). Deleting the sidecar forces the
// next check to hash the file again.
//
// hashSidecar 缓存已完成制品的 SHA-256，连同被哈希时的大小与修改时间。它只
// 是一个缓存，让 alreadySatisfied 不必在每次启动、每次触发时都重新读一遍数
// GB 的文件；大小或 mtime 不同的文件永远不会采信它，任何读取或解析问题都退
// 回完整哈希。
//
// 信任边界：sidecar 与制品在同一目录，能写这个目录的人（Agent 自己的 OS 用
// 户、root）本来就能替换模型本身，sidecar 没有给他们任何新的攻击面。相比
// 完整重新哈希，它确实丢掉的是对"内容变了但大小与 mtime 没变"的检测（静默
// 磁盘损坏，或蓄意修改后再 os.Chtimes）。删除 sidecar 会迫使下一次检查重新
// 哈希文件。
type hashSidecar struct {
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"mtime_unix_nano"`
	SHA256          string `json:"sha256"`
}

// readHashSidecar returns the cached digest for the file described by fi, or
// false when there is no sidecar, it cannot be parsed, or it was recorded for
// a file of a different size or mtime.
//
// readHashSidecar 返回 fi 所描述的文件的缓存摘要；没有 sidecar、解析失败、
// 或者它记录的是另一个大小或 mtime 的文件时返回 false。
func readHashSidecar(target string, fi os.FileInfo) (string, bool) {
	data, err := os.ReadFile(target + hashSidecarSuffix)
	if err != nil {
		return "", false
	}
	var sc hashSidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return "", false
	}
	if sc.Size != fi.Size() || sc.ModTimeUnixNano != fi.ModTime().UnixNano() || len(sc.SHA256) != sha256HexLen {
		return "", false
	}
	return sc.SHA256, true
}

// writeHashSidecar records sum as the digest of the file described by fi. The
// caller passes a FileInfo taken before the file was hashed (or, for a file
// this process just wrote, right after the rename), so a file modified
// during hashing can only ever produce a sidecar that fails to match later.
// Failure is harmless — the cache is simply absent — so callers ignore it.
//
// writeHashSidecar 把 sum 记为 fi 所描述的文件的摘要。调用方传入的是文件被
// 哈希之前取得的 FileInfo（对本进程刚写完的文件，则是改名之后立刻取得的），
// 所以哈希期间被修改的文件至多产生一个以后匹配不上的 sidecar。失败无害——
// 缓存只是缺席——所以调用方忽略它。
func writeHashSidecar(target string, fi os.FileInfo, sum string) error {
	data, err := json.Marshal(hashSidecar{Size: fi.Size(), ModTimeUnixNano: fi.ModTime().UnixNano(), SHA256: sum})
	if err != nil {
		return err
	}
	// A plain temp file plus Replace, the same pattern Ledger.save uses: the
	// artifact directory belongs to the operator, so localstate.WriteFile's
	// directory hardening must not be applied to it.
	//
	// 普通临时文件加 Replace，与 Ledger.save 用的模式相同：制品目录归运维所
	// 有，不能对它套用 localstate.WriteFile 的目录加固。
	path := target + hashSidecarSuffix
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := localstate.Replace(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// hashWriter feeds exactly the bytes its underlying writer accepted into h,
// so the running digest always describes what is on disk even across a short
// write.
//
// hashWriter 只把下层 writer 实际接受的字节喂给 h，所以即使发生短写，运行
// 中的摘要也始终描述磁盘上的内容。
type hashWriter struct {
	w io.Writer
	h hash.Hash
}

func (hw *hashWriter) Write(p []byte) (int, error) {
	n, err := hw.w.Write(p)
	hw.h.Write(p[:n])
	return n, err
}

// ctxReader fails reads once ctx is done, so hashing a huge partial file
// cannot outlive Agent shutdown.
//
// ctxReader 在 ctx 结束后让读取失败，这样对一个巨大部分文件的哈希不会比
// Agent 关闭活得更久。
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// hashPrefix feeds the first n bytes of the file at path into h. It fails if
// the file holds fewer than n bytes.
//
// hashPrefix 把 path 处文件的前 n 字节喂给 h。文件不足 n 字节时失败。
func hashPrefix(ctx context.Context, h hash.Hash, path string, n int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	bufp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bufp)
	copied, err := io.CopyBuffer(h, io.LimitReader(ctxReader{ctx: ctx, r: f}, n), *bufp)
	if err != nil {
		return err
	}
	if copied != n {
		return fmt.Errorf("partial file holds %d of the expected %d bytes", copied, n)
	}
	return nil
}

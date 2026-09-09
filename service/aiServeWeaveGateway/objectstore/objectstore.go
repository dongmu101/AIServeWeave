// Package objectstore stores and retrieves generated artifact and uploaded
// input bytes for STATUS.md's P04. It is one interface with three
// implementations because a deployment's storage is a deployment decision,
// not one this package makes for everyone: a single-node install has a disk
// and nothing else (Local), a fleet needs artifacts reachable from every
// Gateway replica (S3), and many home/office NAS boxes expose WebDAV but no
// S3 gateway at all (WebDAV) — see README.md's "文件与产物（目标方案）"
// section, which names local and S3-compatible storage and leaves WebDAV as
// this package's addition for that NAS case.
//
// Every implementation streams: Put reads its body and Open returns a body
// the caller reads, neither ever buffering a whole object in memory, per
// AGENTS.md's "任何一跳都不得无界缓冲". A caller that needs the object's
// byte size or hash for its own metadata computes it while streaming (e.g.
// with io.TeeReader), not by asking this package to read the object twice.
//
// objectstore 包为 STATUS.md 的 P04 存储生成产物与上传输入的字节。这是一个接口
// 加三个实现，因为一次部署用什么存储是部署方的决定，不是本包替所有人做的决定：
// 单机部署只有一块盘，别无他物（Local）；一个集群需要产物能被每个 Gateway 副本
// 访问到（S3）；很多家用/办公室 NAS 只暴露 WebDAV、压根没有 S3 网关（WebDAV）——
// 见 README.md「文件与产物（目标方案）」一节，那里点名了本地与 S3-compatible
// 存储，WebDAV 是本包为 NAS 场景补上的第三个选项。
//
// 每个实现都是流式的：Put 读取它的 body，Open 返回一个调用方自行读取的 body，
// 两者都不会把整个对象缓冲进内存，对应 AGENTS.md 的「任何一跳都不得无界缓冲」。
// 调用方如果需要对象的字节数或哈希用于自己的元数据，应当在流式传输的同时算出
// （比如用 io.TeeReader），而不是要求本包把对象读两遍。
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotFound is returned by Open and Stat when key names no object. It is a
// sentinel rather than a per-backend type so a caller can branch with
// errors.Is regardless of which Backend is configured.
//
// ErrNotFound 在 key 不指向任何对象时由 Open 与 Stat 返回。它是一个哨兵值而不是
// 各后端各自的类型，这样调用方可以用 errors.Is 分支判断，而不必关心配置的是
// 哪一个 Backend。
var ErrNotFound = errors.New("objectstore: object not found")

// ObjectInfo describes an object without its bytes, as returned alongside the
// body from Open. Size is -1 when the backend cannot report it without
// reading the body (no backend here does that today, but a future one might).
// ContentType is empty when the backend has no notion of one (WebDAV's
// PROPFIND does not carry it) — a caller that needs a reliable content type
// must keep it in its own metadata rather than relying on the backend to
// remember it.
//
// ObjectInfo 描述一个对象但不含其字节，随 body 一起由 Open 返回。Size 为 -1
// 表示该后端不读 body 就报不出大小（本包目前没有这样的后端，但未来可能有）。
// ContentType 为空表示该后端本就没有这个概念（WebDAV 的 PROPFIND 不携带它）——
// 需要可靠内容类型的调用方应当把它保存在自己的元数据里，而不是指望后端替它记住。
type ObjectInfo struct {
	Size        int64
	ContentType string
}

// Backend stores and retrieves objects by key. Key is a caller-chosen,
// slash-separated relative path (e.g. "tenant_a/job_123/output.png") — never
// an absolute path or one containing "..", which Put, Open and Delete all
// reject via validateKey before touching a disk, bucket or WebDAV collection,
// so a caller-influenced filename can never escape the storage root or
// address an unrelated tenant's object.
//
// Delete is idempotent: removing a key that does not exist is not an error,
// because the only caller (a retention/cleanup sweep, or a compensating
// delete after a failed multi-step write) wants "this key is gone" and does
// not care whether it was this call or an earlier one that made it so.
//
// Backend 按 key 存取对象。Key 是调用方选择的、以斜杠分隔的相对路径（例如
// "tenant_a/job_123/output.png")——绝不能是绝对路径或含有 ".."，Put、Open 与
// Delete 都会在接触磁盘、bucket 或 WebDAV 集合之前经 validateKey 拒绝这类值，
// 因此一个受调用方影响的文件名永远无法逃出存储根目录或指向别的租户的对象。
//
// Delete 是幂等的：删除一个不存在的 key 不算错误，因为唯一的调用方（保留期
// 清理扫描，或者一次多步写入失败后的补偿删除）想要的是「这个 key 不存在了」，
// 不关心是这一次调用还是更早的某次调用让它变成这样。
type Backend interface {
	// Put stores body under key. size is the exact number of bytes the
	// caller will supply, or -1 if unknown; when known it bounds the read
	// (a body that keeps producing bytes past size is an error, not silently
	// truncated or silently accepted) so a miscounted caller cannot make this
	// call buffer without limit.
	//
	// Put 把 body 存到 key 下。size 是调用方将提供的确切字节数，未知则为 -1；
	// 已知时它会限制读取量（body 在 size 之后仍产出字节是一个错误，而不是被
	// 默默截断或默默接受），这样一个计数出错的调用方也不会让这次调用无限缓冲。
	Put(ctx context.Context, key string, body io.Reader, size int64) error

	// Open returns key's object body and its ObjectInfo. The caller must
	// Close the body. It returns ErrNotFound when key names no object.
	//
	// Open 返回 key 对应对象的 body 与 ObjectInfo。调用方必须 Close 这个
	// body。key 不指向任何对象时返回 ErrNotFound。
	Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)

	// Delete removes key's object. See the type doc comment for why a
	// missing key is not an error.
	//
	// Delete 移除 key 对应的对象。key 本就不存在为何不算错误，见类型的
	// 文档注释。
	Delete(ctx context.Context, key string) error
}

// validateKey rejects a key that could escape the storage root: empty,
// absolute, containing a ".." segment, or containing a backslash (which
// would be a literal filename character on every backend here but is also
// how a Windows-style traversal attempt would be spelled, so it is refused
// rather than interpreted either way).
//
// validateKey 拒绝一个可能逃出存储根目录的 key：空、绝对路径、含有 ".." 段，
// 或含有反斜杠（反斜杠在这里的每个后端上都只是普通文件名字符，但它也是
// Windows 风格路径穿越尝试的写法，因此一律拒绝，而不去猜它是哪种意图）。
// newBoundedReader wraps r so that reading exactly size bytes and then
// hitting EOF succeeds, but a short body (EOF before size bytes) or a long
// one (any byte beyond size) surfaces as an error from Read instead of
// silently passing through — the shared implementation behind every
// Backend's Put doing what its doc comment promises for a caller-supplied
// size. Callers pass size == -1 to opt out and get r back unwrapped.
//
// newBoundedReader 包装 r，使得恰好读到 size 字节后遇到 EOF 视为成功，但一个
// 偏短的 body（不到 size 字节就 EOF）或偏长的 body（超过 size 的任何一个
// 字节）都会从 Read 冒出一个错误，而不是被默默放行——这是每个 Backend 的 Put
// 对调用方给定的 size 兑现其文档注释承诺所共用的实现。调用方传 size == -1
// 表示不做限制，会原样拿回 r。
func newBoundedReader(r io.Reader, size int64) io.Reader {
	if size < 0 {
		return r
	}
	return &boundedReader{r: r, want: size}
}

type boundedReader struct {
	r    io.Reader
	want int64
	read int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	// Ask the underlying reader for at most one byte more than still
	// permitted, so a single Read call is enough to notice an overrun
	// without ever reading arbitrarily far past the declared size.
	//
	// 向底层 reader 最多多要一个字节，这样一次 Read 调用就足以发现超限，
	// 而不会读到远超声明大小之外的地方。
	if remaining := b.want - b.read + 1; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.want {
		return n, fmt.Errorf("objectstore: body exceeds declared size %d", b.want)
	}
	if err == io.EOF && b.read < b.want {
		return n, fmt.Errorf("objectstore: body ended after %d bytes, want %d", b.read, b.want)
	}
	return n, err
}

func validateKey(key string) error {
	if key == "" {
		return errors.New("objectstore: key must not be empty")
	}
	if strings.HasPrefix(key, "/") {
		return errors.New("objectstore: key must not be absolute")
	}
	if strings.Contains(key, "\\") {
		return errors.New("objectstore: key must not contain '\\\\'")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." {
			return errors.New("objectstore: key must not contain empty or '.' segments")
		}
		if seg == ".." {
			return errors.New("objectstore: key must not contain '..' segments")
		}
	}
	return nil
}

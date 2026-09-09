package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/studio-b12/gowebdav"

	"AIServeWeave/common/runtime"
)

// WebDAVConfig configures WebDAV. URL is the server's WebDAV root (e.g.
// "https://nas.example.internal/webdav"). Dir is a path prefix under that
// root this backend confines itself to, analogous to S3Config.Prefix,
// letting one WebDAV share be split between purposes or environments.
//
// WebDAVConfig 配置 WebDAV。URL 是服务端的 WebDAV 根（例如
// "https://nas.example.internal/webdav"）。Dir 是这个后端把自己限制在该根
// 之下的一个路径前缀，作用类似 S3Config.Prefix，让一个 WebDAV 共享目录可以
// 被多个用途或环境分用。
type WebDAVConfig struct {
	URL      string
	Username string
	Password string
	Dir      string
}

// WebDAV stores objects on a WebDAV server — the protocol most home and
// office NAS boxes (Synology, QNAP, TrueNAS and generic Apache/nginx WebDAV
// modules) expose even when they have no S3 gateway. See the package doc
// comment.
//
// WebDAV 把对象存在一个 WebDAV 服务端上——这是大多数家用与办公 NAS（群晖、
// QNAP、TrueNAS 以及通用的 Apache/nginx WebDAV 模块）即使没有 S3 网关也会
// 暴露的协议。见包的文档注释。
type WebDAV struct {
	client *gowebdav.Client
	dir    string
	redact func(string) string
}

// NewWebDAV validates cfg and constructs a WebDAV backend. Like NewS3, it
// does not make a network call; see NewS3's doc comment for why.
//
// NewWebDAV 校验 cfg 并构造一个 WebDAV 后端。和 NewS3 一样，它不发起网络
// 调用；理由见 NewS3 的文档注释。
func NewWebDAV(cfg WebDAVConfig) (*WebDAV, error) {
	if cfg.URL == "" {
		return nil, errors.New("objectstore: webdav url must not be empty")
	}
	dir := path.Clean("/" + cfg.Dir)
	if dir == "/" {
		dir = ""
	}
	return &WebDAV{
		client: gowebdav.NewClient(cfg.URL, cfg.Username, cfg.Password),
		dir:    dir,
		redact: func(s string) string { return runtime.Redact(s, cfg.Password) },
	}, nil
}

func (b *WebDAV) fullPath(key string) string { return b.dir + "/" + key }

// Put uploads body under key, creating any missing parent collections first
// — WebDAV, unlike S3's flat keyspace, requires a PUT's parent collection to
// already exist. WriteStreamWithLength streams body rather than buffering it
// whole.
//
// Put 把 body 上传到 key 下，事先创建所有缺失的父集合——与 S3 的扁平键空间
// 不同，WebDAV 要求 PUT 的父集合必须预先存在。WriteStreamWithLength 会流式
// 传输 body 而不是整体缓冲它。
func (b *WebDAV) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if err := validateKey(key); err != nil {
		return err
	}
	full := b.fullPath(key)
	if dir := path.Dir(full); dir != "/" && dir != "." {
		if err := b.client.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("objectstore: webdav mkdir %q: %s", dir, b.redact(err.Error()))
		}
	}
	src := newBoundedReader(body, size)
	if size >= 0 {
		if err := b.client.WriteStreamWithLength(full, src, size, 0o644); err != nil {
			return fmt.Errorf("objectstore: webdav put %q: %s", key, b.redact(err.Error()))
		}
		return nil
	}
	if err := b.client.WriteStream(full, src, 0o644); err != nil {
		return fmt.Errorf("objectstore: webdav put %q: %s", key, b.redact(err.Error()))
	}
	return nil
}

// Open returns key's object. It calls Stat first to report Size, since
// gowebdav's streaming read does not surface Content-Length itself; that
// costs one extra round trip per Open, which is the price of this backend
// giving callers the same ObjectInfo shape the other backends give for free.
//
// Open 返回 key 对应的对象。它先调用 Stat 来获得 Size，因为 gowebdav 的流式
// 读取本身不会给出 Content-Length；这带来每次 Open 多一次往返的代价，是这个
// 后端为了给出与其他后端一致的 ObjectInfo 形状所付出的成本。
func (b *WebDAV) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	full := b.fullPath(key)
	info, err := b.client.Stat(full)
	if err != nil {
		if gowebdav.IsErrNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: webdav stat %q: %s", key, b.redact(err.Error()))
	}
	body, err := b.client.ReadStream(full)
	if err != nil {
		if gowebdav.IsErrNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: webdav read %q: %s", key, b.redact(err.Error()))
	}
	return body, ObjectInfo{Size: info.Size()}, nil
}

// Delete removes key's object. See the Backend doc comment for why a
// missing key is not an error; gowebdav's Remove already treats a 404
// response as success, so no extra mapping is needed here.
//
// Delete 移除 key 对应的对象。key 本就不存在为何不算错误，见 Backend 的
// 文档注释；gowebdav 的 Remove 本就把 404 响应当作成功处理，因此这里不需要
// 额外映射。
func (b *WebDAV) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := b.client.Remove(b.fullPath(key)); err != nil {
		return fmt.Errorf("objectstore: webdav delete %q: %s", key, b.redact(err.Error()))
	}
	return nil
}

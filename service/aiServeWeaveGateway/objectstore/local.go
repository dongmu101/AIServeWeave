package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LocalConfig configures Local. Dir is created on New if it does not already
// exist, so a fresh single-node deployment does not need an operator to
// pre-create it.
//
// LocalConfig 配置 Local。Dir 若尚不存在，New 会将其创建，这样全新的单机部署
// 不需要运维预先建好它。
type LocalConfig struct {
	Dir string
}

// Local stores objects as files under a root directory, for single-node
// deployments that have a disk and nothing else — see the package doc
// comment.
//
// Local 把对象存成根目录下的文件，供只有一块盘、别无他物的单机部署使用——见
// 包的文档注释。
type Local struct {
	dir string
}

// NewLocal opens (creating if absent) dir as a Local backend's root. It
// fails fast if dir exists and is not a directory, or cannot be created,
// rather than deferring that discovery to the first Put.
//
// NewLocal 打开（不存在则创建）dir 作为 Local 后端的根目录。dir 存在但不是
// 目录、或无法创建时立即失败，而不是把这个发现推迟到第一次 Put。
func NewLocal(cfg LocalConfig) (*Local, error) {
	if cfg.Dir == "" {
		return nil, errors.New("objectstore: local dir must not be empty")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("objectstore: create local dir: %w", err)
	}
	info, err := os.Stat(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("objectstore: stat local dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("objectstore: local dir %q is not a directory", cfg.Dir)
	}
	return &Local{dir: cfg.Dir}, nil
}

// Put writes body to a temporary file in the same directory as the final
// path and renames it into place, so a reader can never observe a partially
// written object: Open either sees the file before this Put's rename (and
// gets the previous object or ErrNotFound) or after it (and gets this Put's
// complete bytes), never a half-written one.
//
// Put 把 body 写入与最终路径同目录的一个临时文件，再原子改名到位，因此读者
// 永远不会看到一个写到一半的对象：Open 要么在这次 Put 改名之前看到（拿到上一个
// 对象或 ErrNotFound），要么在之后看到（拿到这次 Put 完整的字节），绝不会是
// 写了一半的状态。
func (l *Local) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if err := validateKey(key); err != nil {
		return err
	}
	dest := filepath.Join(l.dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("objectstore: create parent dir for %q: %w", key, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".upload-*")
	if err != nil {
		return fmt.Errorf("objectstore: create temp file for %q: %w", key, err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// Reached only on an error path below; the success path returns
		// before here after the rename has already consumed tmpPath.
		//
		// 只在下方的错误路径上才会走到这里；成功路径在改名消费掉 tmpPath 之后
		// 就已经返回了。
		_ = os.Remove(tmpPath)
	}()

	_, err = io.Copy(tmp, newBoundedReader(body, size))
	closeErr := tmp.Close()
	if err != nil {
		return fmt.Errorf("objectstore: write %q: %w", key, err)
	}
	if closeErr != nil {
		return fmt.Errorf("objectstore: close temp file for %q: %w", key, closeErr)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("objectstore: finalize %q: %w", key, err)
	}
	return nil
}

// Open returns the file at key. See the Backend doc comment for the
// ErrNotFound and streaming contracts.
//
// Open 返回 key 对应的文件。ErrNotFound 与流式契约见 Backend 的文档注释。
func (l *Local) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	f, err := os.Open(filepath.Join(l.dir, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: open %q: %w", key, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: stat %q: %w", key, err)
	}
	return f, ObjectInfo{Size: info.Size()}, nil
}

// Delete removes the file at key. See the Backend doc comment for why a
// missing key is not an error.
//
// Delete 移除 key 对应的文件。key 本就不存在为何不算错误，见 Backend 的
// 文档注释。
func (l *Local) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(l.dir, filepath.FromSlash(key)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("objectstore: delete %q: %w", key, err)
	}
	return nil
}

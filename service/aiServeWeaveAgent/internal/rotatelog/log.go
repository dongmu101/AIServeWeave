// Package rotatelog bounds private desktop logs without an external log daemon.
// Package rotatelog 无需外部日志服务即可限制桌面端私有日志大小。
package rotatelog

import (
	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Writer serializes writes and retains a fixed number of bounded files.
// Writer 串行写入并保留固定数量且大小受限的文件。
type Writer struct {
	mu      sync.Mutex
	path    string
	limit   int64
	backups int
	file    *os.File
	size    int64
}

// Open opens a private append-only log with bounded rotation.
// Open 打开权限受限且支持有界轮转的追加日志。
func Open(path string, limit int64, backups int) (*Writer, error) {
	if limit <= 0 || backups < 1 || backups > 10 {
		return nil, errors.New("invalid log bounds")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if err := localstate.WriteFile(path, nil, 0o600); err != nil {
			return nil, err
		}
	}
	if err := localstate.CheckPrivate(path, false); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Writer{path: path, limit: limit, backups: backups, file: f, size: info.Size()}, nil
}

// Write rotates before exceeding the limit and truncates a single oversized record.
// Write 在超过上限前轮转，并截断单条超长记录。
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(p)
	if int64(len(p)) > w.limit {
		p = p[:w.limit]
	}
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size+int64(len(p)) > w.limit {
		if err := w.file.Close(); err != nil {
			return 0, err
		}
		w.file = nil
		for i := w.backups; i >= 1; i-- {
			source := w.path
			if i > 1 {
				source = fmt.Sprintf("%s.%d", w.path, i-1)
			}
			target := fmt.Sprintf("%s.%d", w.path, i)
			if err := localstate.Replace(source, target); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
		}
		if err := localstate.WriteFile(w.path, nil, 0o600); err != nil {
			return 0, err
		}
		var err error
		w.file, err = os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		w.size = 0
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	return original, nil
}

// Close releases the current log handle.
// Close 释放当前日志句柄。
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// Package localstate protects and replaces Agent-local state across platforms.
// Package localstate 跨平台保护并替换 Agent 本地状态。
package localstate

import (
	"os"
	"path/filepath"
)

// WriteFile persists data through a protected temporary file and replacement.
// WriteFile 通过受保护的临时文件及替换操作持久化数据。
func WriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := ensureDirectory(dir); err != nil {
		return err
	}
	f, err := createTemp(dir)
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return Replace(name, path)
}

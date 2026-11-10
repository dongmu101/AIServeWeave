package identitystore

import "os"

// SetWriteFileForTest replaces how the store persists its ledger, so a test can
// block or fail a write. It must be called before any concurrent use.
//
// SetWriteFileForTest 替换账本的落盘方式，使测试可以阻塞或让一次写入失败。
// 必须在任何并发使用之前调用。
func SetWriteFileForTest(s *Store, fn func(path string, data []byte, mode os.FileMode) error) {
	s.writeFile = fn
}

// SyncDirForTest exposes syncDir.
//
// SyncDirForTest 暴露 syncDir。
func SyncDirForTest(dir string) error { return syncDir(dir) }

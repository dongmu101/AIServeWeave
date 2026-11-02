package agentconfig

import (
	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxAPIKeyBytes bounds an API key file. Provider keys are well under 1 KiB;
// the bound only stops a mistyped path from pulling a large file into memory.
//
// MaxAPIKeyBytes 是 API Key 文件的大小上限。供应商 Key 远小于 1 KiB；这个上限只用来
// 防止填错路径时把一个大文件读进内存。
const MaxAPIKeyBytes = 16 << 10

// keyDirName is the folder, next to the config file, holding key files the
// setup page writes.
//
// keyDirName 是配置文件旁边保存设置页面所写 Key 文件的目录。
const keyDirName = "keys"

// safeKeyName matches runtime ids usable verbatim as a file name.
// safeKeyName 匹配可以原样用作文件名的运行时 id。
var safeKeyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ErrAPIKeyEmpty is returned by WriteAPIKeyFile for a key that is empty after
// trimming, or that contains a line break.
//
// ErrAPIKeyEmpty 在 Key 去除首尾空白后为空、或包含换行时由 WriteAPIKeyFile 返回。
var ErrAPIKeyEmpty = errors.New("agentconfig: API key must be one non-empty line")

// KeyFilePath returns where the setup page stores runtime id's key for the
// config file at configPath: keys/<id>.key next to it. An id that is not a
// safe file name is replaced by a hash of it, so no id can escape the folder.
//
// KeyFilePath 返回设置页面为 configPath 处的配置文件保存运行时 id 的 Key 的位置：配置
// 文件旁的 keys/<id>.key。不能安全用作文件名的 id 换成它的哈希，任何 id 都逃不出该目录。
func KeyFilePath(configPath, id string) string {
	name := id
	if !safeKeyName.MatchString(id) {
		sum := sha256.Sum256([]byte(id))
		name = "rt-" + hex.EncodeToString(sum[:8])
	}
	return filepath.Join(managedKeyDir(configPath), name+".key")
}

// IsManagedKeyFile reports whether path lies in the keys/ folder the setup
// page manages for configPath. Only such files are deleted when a key is
// cleared; a path an operator wrote by hand is left alone.
//
// IsManagedKeyFile 报告 path 是否位于设置页面为 configPath 管理的 keys/ 目录中。清除
// Key 时只删除这类文件；运维手写的路径保持不动。
func IsManagedKeyFile(configPath, path string) bool {
	dir, err := filepath.Abs(managedKeyDir(configPath))
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return filepath.Dir(abs) == dir
}

// WriteAPIKeyFile stores key at path with mode 0600 through localstate's
// protected replacement, creating the folder with mode 0700.
//
// WriteAPIKeyFile 经 localstate 的受保护替换把 key 以 0600 权限写入 path，并以 0700
// 权限创建所在目录。
func WriteAPIKeyFile(path, key string) error {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return ErrAPIKeyEmpty
	}
	if len(key) > MaxAPIKeyBytes {
		return fmt.Errorf("agentconfig: API key exceeds %d bytes", MaxAPIKeyBytes)
	}
	return localstate.WriteFile(path, []byte(key), 0o600)
}

// ReadAPIKeyFile reads the key stored at path, trimming surrounding
// whitespace. The error never contains file content.
//
// ReadAPIKeyFile 读取 path 处保存的 Key 并去除首尾空白。错误文本从不包含文件内容。
func ReadAPIKeyFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("agentconfig: open API key file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxAPIKeyBytes+1))
	if err != nil {
		return "", errors.New("agentconfig: read API key file failed")
	}
	if len(data) > MaxAPIKeyBytes {
		return "", fmt.Errorf("agentconfig: API key file exceeds %d bytes", MaxAPIKeyBytes)
	}
	return strings.TrimSpace(string(data)), nil
}

// managedKeyDir is the keys/ folder next to configPath.
// managedKeyDir 是 configPath 旁边的 keys/ 目录。
func managedKeyDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), keyDirName)
}

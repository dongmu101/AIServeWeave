package cliprocess

import (
	"strings"
	"sync"
)

// stderrTailBytes is how much of a CLI's most recent standard error a
// StderrTail keeps; a CLI's failure reason is in its last lines.
// stderrTailBytes 是 StderrTail 保留的 CLI 最近标准错误的字节数；CLI 的失败原因在最后几行。
const stderrTailBytes = 8 << 10

// StderrTail keeps the end of a CLI's standard error so a failure can be
// classified. Its text never leaves the Agent: Category reduces it to a fixed
// vocabulary, because standard error may echo prompts, paths or credentials.
// Write never fails or blocks, so a chatty CLI cannot stall on a full pipe.
//
// StderrTail 保留 CLI 标准错误的末尾，用于给失败分类。其文本从不离开 Agent：Category 把它
// 归入固定词汇，因为标准错误可能回显提示词、路径或凭据。Write 从不失败或阻塞，输出很多的
// CLI 不会因管道写满而停住。
type StderrTail struct {
	mu   sync.Mutex
	data []byte
}

// Write keeps the last stderrTailBytes of everything written.
// Write 保留写入内容的最后 stderrTailBytes 字节。
func (t *StderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(p) >= stderrTailBytes {
		t.data = append(t.data[:0], p[len(p)-stderrTailBytes:]...)
		return len(p), nil
	}
	if over := len(t.data) + len(p) - stderrTailBytes; over > 0 {
		t.data = append(t.data[:0], t.data[over:]...)
	}
	t.data = append(t.data, p...)
	return len(p), nil
}

// stderrCategories maps lower-case markers to the category they report,
// checked in order; the first match wins.
// stderrCategories 把小写标记映射到它报告的分类，按顺序检查，第一个匹配者胜出。
var stderrCategories = []struct {
	category string
	markers  []string
}{
	{"out_of_memory", []string{"out of memory", "cannot allocate memory", "enomem"}},
	{"disk_full", []string{"no space left on device", "enospc"}},
	{"too_many_open_files", []string{"too many open files", "emfile"}},
	{"login_required", []string{"not logged in", "please run /login", "claude auth login", "codex login", "invalid api key", "login expired", "refresh token"}},
	{"unknown_option", []string{"unknown option", "unknown feature flag", "unexpected argument", "unrecognized option"}},
	{"config_invalid", []string{"invalid settings", "config_file_invalid", "error loading config", "failed to parse config", "invalid configuration"}},
	{"database_locked", []string{"database is locked"}},
	{"permission_denied", []string{"permission denied", "eacces", "operation not permitted"}},
	{"network", []string{"econnrefused", "enotfound", "etimedout", "getaddrinfo", "connection refused", "dns error"}},
	{"crashed", []string{"panicked at", "segmentation fault", "fatal error", "uncaught exception"}},
}

// Category reduces the kept standard error to a fixed category: one of
// stderrCategories, "other" for unrecognized output, or "" when the CLI
// wrote nothing.
// Category 把保留的标准错误归入固定分类：stderrCategories 之一；无法识别的输出为
// "other"；CLI 什么都没写时为 ""。
func (t *StderrTail) Category() string {
	t.mu.Lock()
	text := strings.ToLower(strings.TrimSpace(string(t.data)))
	t.mu.Unlock()
	if text == "" {
		return ""
	}
	for _, entry := range stderrCategories {
		for _, marker := range entry.markers {
			if strings.Contains(text, marker) {
				return entry.category
			}
		}
	}
	return "other"
}

package cliprocess

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// versionTimeout bounds one `--version` call; it never submits a prompt.
// versionTimeout 限定一次 `--version` 调用的时长；该调用从不提交提示词。
const versionTimeout = 5 * time.Second

// versionOutputLimit caps what is read from `--version`; real output is one line.
// versionOutputLimit 限定从 `--version` 读取的字节数；真实输出只有一行。
const versionOutputLimit = 4 * 1024

// versionPattern matches the first dotted release number, such as 0.50.0,
// v1.2.3 or 2.0.14-beta.1; group 1 is the number without a leading "v". It
// must not start inside a longer number, and RE2 has no lookbehind, so the
// preceding character is matched explicitly.
//
// versionPattern 匹配第一个点分版本号，例如 0.50.0、v1.2.3 或 2.0.14-beta.1；
// 第 1 组是不带前缀 "v" 的版本号。它不能从更长数字的中间开始，而 RE2 不支持后行断言，
// 因此显式匹配前一个字符。
var versionPattern = regexp.MustCompile(`(?:^|[^0-9.])(\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.\-]*[0-9A-Za-z])?)`)

// Version runs `program --version` with env and returns the first release
// number it prints, or "" when the CLI is missing, fails, times out or prints
// none. Only that number leaves this function, so no other output (paths,
// account names) can reach a report or a log.
//
// Version 以 env 运行 `program --version`，返回其输出中的第一个版本号；CLI 不存在、
// 失败、超时或未输出版本号时返回 ""。只有版本号离开本函数，其余输出（路径、账户名）
// 不会进入上报或日志。
func Version(ctx context.Context, program string, env []string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "aisw-cli-version-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, program, "--version")
	output := &limitedBuffer{limit: versionOutputLimit}
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, output, io.Discard
	cmd.WaitDelay = time.Second
	if err := Configure(cmd); err != nil {
		return ""
	}
	if err := Run(cmd); err != nil {
		return ""
	}
	return parseVersion(output.Bytes())
}

// parseVersion extracts the first release number from `--version` output.
// parseVersion 从 `--version` 输出中提取第一个版本号。
func parseVersion(output []byte) string {
	match := versionPattern.FindSubmatch(output)
	if match == nil {
		return ""
	}
	return string(match[1])
}

// limitedBuffer keeps at most limit bytes and silently drops the rest, so a
// misbehaving CLI cannot grow memory or block on a full pipe. The buffer is a
// named field, not embedded: an embedded bytes.Buffer would promote ReadFrom,
// and io.Copy would then bypass Write and its limit.
//
// limitedBuffer 最多保留 limit 字节并静默丢弃其余部分，使异常的 CLI 既不能撑大内存，
// 也不会因管道写满而阻塞。缓冲区是具名字段而非嵌入：嵌入的 bytes.Buffer 会提升出
// ReadFrom，io.Copy 随之绕过 Write 及其上限。
type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

// Write keeps what still fits under the limit and reports the whole write as consumed.
// Write 保留仍在上限内的部分，并把整次写入报告为已消费。
func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buffer.Len(); room > 0 {
		b.buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// Bytes returns the kept bytes.
// Bytes 返回已保留的字节。
func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }

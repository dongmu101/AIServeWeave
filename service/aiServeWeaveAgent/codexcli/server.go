package codexcli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

// withServer owns one isolated process and reaps its whole process group.
// withServer 管理一个隔离进程，并在每条退出路径回收整个进程组。
func withServer(ctx context.Context, executable string, env []string, run func(*protocol, string) error) error {
	return startServer(ctx, executable, env, false, "", run)
}

// withReadServer is withServer for a process that never runs inference: a
// catalogue, account or quota read. Its sqlite_home is readStateDir(env), kept
// across processes, because app-server indexes the account's whole sessions
// directory into an empty sqlite_home on every start; with a large history
// that costs seconds of CPU per start. Inference processes never use it:
// Codex keeps memories and goals there, which must not cross requests.
//
// withReadServer 是用于从不执行推理的进程（目录、账户或额度读取）的 withServer。它的
// sqlite_home 取 readStateDir(env)，跨进程保留：app-server 每次在空的 sqlite_home 中
// 启动都会把账户的整个 sessions 目录建一遍索引，历史较大时每次启动要耗数秒 CPU。推理
// 进程从不使用它：Codex 会在其中保存 memories 与 goals，它们不能跨请求共享。
func withReadServer(ctx context.Context, executable string, env []string, run func(*protocol, string) error) error {
	return startServer(ctx, executable, env, false, readStateDir(env), run)
}

// userCacheDir locates readStateDir's root; tests replace it.
// userCacheDir 定位 readStateDir 的根目录；测试会替换它。
var userCacheDir = os.UserCacheDir

// readStateDir returns the private sqlite_home that read processes for env's
// account directory share, creating it, under the user cache directory and
// named by a digest of the account directory so two accounts never share one.
// It returns "" when the account directory or the cache directory cannot be
// resolved to an absolute path or the directory cannot be created; the caller
// then falls back to a fresh directory per process, which is slower but
// otherwise the same.
//
// readStateDir 返回 env 所指账户目录下的读取进程共用的私有 sqlite_home，并创建它；它
// 位于用户缓存目录下，以账户目录的摘要命名，两个账户永不共用。账户目录或缓存目录无法
// 解析为绝对路径、或目录无法创建时返回 ""，调用方随即退回每个进程一个新目录：更慢，
// 但其余行为相同。
func readStateDir(env []string) string {
	account := codexHomeDir(env)
	if !filepath.IsAbs(account) {
		return ""
	}
	root, err := userCacheDir()
	if err != nil || !filepath.IsAbs(root) {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.Clean(account)))
	dir := filepath.Join(root, "AIServeWeave", "codex-state", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
}

// withServerMode is withServer with image generation optionally enabled for
// this one process; chat processes never get the image tool.
// withServerMode 与 withServer 相同，但可只为这一个进程开启图像生成；聊天进程
// 永远拿不到生图工具。
func withServerMode(ctx context.Context, executable string, env []string, image bool, run func(*protocol, string) error) error {
	return startServer(ctx, executable, env, image, "", run)
}

// startServer runs one isolated app-server in a fresh working directory. An
// empty state keeps sqlite_home in that directory too; otherwise sqlite_home
// is state.
// startServer 在新建的工作目录中运行一个隔离的 app-server。state 为空时 sqlite_home
// 也放在该目录中，否则 sqlite_home 为 state。
func startServer(ctx context.Context, executable string, env []string, image bool, state string, run func(*protocol, string) error) error {
	path, err := exec.LookPath(executable)
	if err != nil {
		return ErrProcess
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return ErrProcess
	}
	dir, err := os.MkdirTemp("", "aisw-codex-runtime-")
	if err != nil {
		return ErrProcess
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, path, serverArgsIn(dir, state, image)...)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, env, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cliprocess.Configure(cmd); err != nil {
		return err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return ErrProcess
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return ErrProcess
	}
	defer out.Close()
	if err := cliprocess.Start(cmd); err != nil {
		return ErrProcess
	}
	defer func() { _ = cmd.Cancel(); _ = in.Close(); _ = out.Close(); _ = cmd.Wait() }()
	p := &protocol{scanner: bufio.NewScanner(out), output: in, image: image}
	limit := maxFrameBytes
	if image {
		limit = maxImageFrameBytes
	}
	p.scanner.Buffer(make([]byte, 4096), limit+1)
	return run(p, dir)
}

func initializeServer(p *protocol) error {
	if err := p.request(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "aisw-codex-runtime", "version": "0.1.0"}, "capabilities": map[string]bool{"experimentalApi": true}}, new(map[string]any)); err != nil {
		return err
	}
	if err := p.write(map[string]string{"method": "initialized"}); err != nil {
		return err
	}
	var result struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if err := p.request(2, "account/read", map[string]bool{"refreshToken": false}, &result); err != nil {
		return err
	}
	if result.Account == nil || result.Account.Type != "chatgpt" {
		return ErrAuthentication
	}
	return nil
}

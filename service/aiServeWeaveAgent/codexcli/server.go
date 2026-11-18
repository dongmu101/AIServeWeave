package codexcli

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

// withServer owns one isolated process and reaps its whole process group.
// withServer 管理一个隔离进程，并在每条退出路径回收整个进程组。
func withServer(ctx context.Context, executable string, env []string, run func(*protocol, string) error) error {
	return startServer(ctx, executable, env, false, false, run)
}

// withReadServer is withServer for a process that never runs inference: a
// catalogue, account or quota read. After it succeeds, its state index may
// become the account's next seed (see promoteSeed); an inference process's
// never does, because it holds that request's threads.
//
// withReadServer 是用于从不执行推理的进程（目录、账户或额度读取）的 withServer。它成功
// 结束后，其状态索引可以成为该账户的下一个种子（见 promoteSeed）；推理进程的索引从不
// 提升，因为其中有该请求的会话。
func withReadServer(ctx context.Context, executable string, env []string, run func(*protocol, string) error) error {
	return startServer(ctx, executable, env, false, true, run)
}

// withServerMode is withServer with image generation optionally enabled for
// this one process; chat processes never get the image tool.
// withServerMode 与 withServer 相同，但可只为这一个进程开启图像生成；聊天进程
// 永远拿不到生图工具。
func withServerMode(ctx context.Context, executable string, env []string, image bool, run func(*protocol, string) error) error {
	return startServer(ctx, executable, env, image, false, run)
}

// startServer runs one isolated app-server whose working directory and
// sqlite_home are a fresh private directory, seeded with the account's state
// index (see restoreSeed) so app-server does not index the whole sessions
// directory again. promote offers the index to promoteSeed once the process
// has exited after run succeeded.
//
// startServer 运行一个隔离的 app-server，其工作目录与 sqlite_home 是新建的私有目录，
// 并预置该账户的状态索引（见 restoreSeed），使 app-server 不必再为整个 sessions 目录
// 建索引。promote 表示 run 成功且进程退出后，把索引交给 promoteSeed。
func startServer(ctx context.Context, executable string, env []string, image, promote bool, run func(*protocol, string) error) error {
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
	seeds := seedRoot(env)
	if seeds != "" {
		restoreSeed(seeds, dir)
	}
	stderr := &cliprocess.StderrTail{}
	cmd := exec.CommandContext(ctx, path, serverArgs(dir, image)...)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, env, stderr
	cmd.WaitDelay = processStopGrace
	kill, err := cliprocess.ConfigureGraceful(cmd, processStopGrace)
	if err != nil {
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
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			// A canceled request stops through ctx, which lets app-server
			// handle SIGTERM first (see processStopGrace); every other stop
			// is immediate. The second kill reaps what outlived the leader.
			// 被取消的请求经 ctx 停止，让 app-server 先处理 SIGTERM（见 processStopGrace）；
			// 其他停止一律立即执行。第二次 kill 回收比组长进程活得更久的进程。
			if ctx.Err() == nil {
				_ = kill()
			}
			_ = in.Close()
			_ = out.Close()
			_ = cmd.Wait()
			_ = kill()
		}
	}
	defer stop()
	p := &protocol{scanner: bufio.NewScanner(out), output: in, image: image}
	limit := maxFrameBytes
	if image {
		limit = maxImageFrameBytes
	}
	p.scanner.Buffer(make([]byte, 4096), limit+1)
	err = run(p, dir)
	if err == nil && promote && seeds != "" {
		// Only a process that has exited leaves its database files at rest.
		// 只有已退出的进程才会让数据库文件处于静止状态。
		stop()
		promoteSeed(seeds, dir, time.Now())
	}
	if err != nil && ctx.Err() == nil {
		// Wait has drained standard error once stop returns.
		// stop 返回时 Wait 已读完标准错误。
		stop()
		err = withStderr(err, stderr)
	}
	return err
}

// processStopGrace is how long a canceled app-server has to handle SIGTERM
// before its process group is killed; codex-cli 0.169.0 exits about 0.08 s
// after SIGTERM, so this rarely runs out. It lets a process canceled while
// refreshing its login save the new token instead of losing it.
// processStopGrace 是被取消的 app-server 在进程组被杀之前处理 SIGTERM 的时间；codex-cli
// 0.169.0 收到 SIGTERM 后约 0.08 秒退出，因此很少用完。它让在刷新登录时被取消的进程能保存
// 新令牌，而不是将其丢失。
const processStopGrace = 2 * time.Second

// stderrNote attaches the fixed category of a failed app-server's standard
// error (see cliprocess.StderrTail) to its failure; the text never leaves
// the Agent.
// stderrNote 把失败 app-server 标准错误的固定分类（见 cliprocess.StderrTail）附加到其失败上；
// 文本从不离开 Agent。
type stderrNote struct {
	error
	category string
}

func (n *stderrNote) Unwrap() error { return n.error }

func withStderr(err error, stderr *cliprocess.StderrTail) error {
	if category := stderr.Category(); category != "" {
		return &stderrNote{error: err, category: category}
	}
	return err
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

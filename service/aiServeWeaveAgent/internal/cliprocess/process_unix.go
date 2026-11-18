//go:build linux || darwin

package cliprocess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Configure puts a command in its own cancellable process group.
// Configure 将命令放入独立且可取消的进程组。
func Configure(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// Start starts a configured process group.
// Start 启动已配置的进程组。
func Start(cmd *exec.Cmd) error { return cmd.Start() }

// ConfigureGraceful is Configure for a CLI that may be writing state when it
// is canceled, such as a refreshed login token. Ending the command's context
// sends SIGTERM to the group and SIGKILL after grace, so the CLI can finish
// that write; kill stops the group at once and is what callers use for
// every other stop (a completed round, an isolation failure). grace must not
// exceed cmd.WaitDelay, or Wait kills the leader first.
//
// ConfigureGraceful 是用于取消时可能正在写入状态（例如刷新后的登录令牌）的 CLI 的
// Configure。命令的上下文结束时向进程组发送 SIGTERM，grace 之后再发送 SIGKILL，让 CLI
// 写完；kill 立即停止进程组，调用方在其他所有停止场合（回合完成、隔离失败）使用它。
// grace 不得超过 cmd.WaitDelay，否则 Wait 会先杀掉组长进程。
func ConfigureGraceful(cmd *exec.Cmd, grace time.Duration) (kill func() error, err error) {
	if err := Configure(cmd); err != nil {
		return nil, err
	}
	kill = cmd.Cancel
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		// Reaps the group even when the leader has already exited.
		// 即使组长进程已经退出，也回收整个进程组。
		time.AfterFunc(grace, func() { _ = kill() })
		return err
	}
	return kill, nil
}

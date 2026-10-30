// Package cliprocess bounds the lifetime of local CLI process trees.
// Package cliprocess 限定本地 CLI 进程树的生命周期。
package cliprocess

import "os/exec"

// Run starts and waits for a configured command, then reclaims descendants.
// Run 启动并等待已配置的命令，随后回收其后代。
func Run(cmd *exec.Cmd) error {
	if err := Start(cmd); err != nil {
		return err
	}
	defer cmd.Cancel()
	return cmd.Wait()
}

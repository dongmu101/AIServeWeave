package codexcli

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// withServer owns one isolated process and reaps its whole process group.
// withServer 管理一个隔离进程，并在每条退出路径回收整个进程组。
func withServer(ctx context.Context, executable string, env []string, run func(*protocol, string) error) error {
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
	cmd := exec.CommandContext(ctx, path, appServerArgs(dir)...)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, env, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := configureProcess(cmd); err != nil {
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
	if err := cmd.Start(); err != nil {
		return ErrProcess
	}
	defer func() { _ = cmd.Cancel(); _ = in.Close(); _ = out.Close(); _ = cmd.Wait() }()
	p := &protocol{scanner: bufio.NewScanner(out), output: in}
	p.scanner.Buffer(make([]byte, 4096), maxFrameBytes+1)
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

//go:build linux || darwin

package cliprocess_test

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

// TestGracefulCancellationSendsSIGTERM lets a canceled CLI handle SIGTERM and
// exit on its own, while kill still stops it at once.
// TestGracefulCancellationSendsSIGTERM 让被取消的 CLI 处理 SIGTERM 并自行退出，而 kill
// 仍会立即停止它。
func TestGracefulCancellationSendsSIGTERM(t *testing.T) {
	if os.Getenv("AISW_GRACEFUL_HELPER") == "1" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		_, _ = os.Stdout.WriteString("ready\n")
		<-signals
		os.Exit(7) // a clean exit chosen by the CLI itself / CLI 自己选择的正常退出
	}
	start := func(ctx context.Context) (*exec.Cmd, func() error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGracefulCancellationSendsSIGTERM$")
		cmd.Env = append(os.Environ(), "AISW_GRACEFUL_HELPER=1")
		cmd.WaitDelay = 5 * time.Second
		kill, err := cliprocess.ConfigureGraceful(cmd, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cliprocess.Start(cmd); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 6)
		if _, err := out.Read(buf); err != nil {
			t.Fatal(err)
		}
		return cmd, kill
	}

	ctx, cancel := context.WithCancel(t.Context())
	cmd, _ := start(ctx)
	cancel()
	_ = cmd.Wait()
	if code := cmd.ProcessState.ExitCode(); code != 7 {
		t.Fatalf("exit code=%d, want the CLI's own exit after SIGTERM", code)
	}

	cmd, kill := start(t.Context())
	started := time.Now()
	_ = kill()
	_ = cmd.Wait()
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL || time.Since(started) > 2*time.Second {
		t.Fatalf("state=%v, want an immediate SIGKILL", cmd.ProcessState)
	}
}

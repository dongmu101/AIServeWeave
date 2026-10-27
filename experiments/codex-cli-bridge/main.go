// Command codex-cli-bridge runs isolated Codex app-server compatibility probes.
// codex-cli-bridge 命令运行隔离的 Codex app-server 兼容性实验。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/codexcli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("codex-cli-bridge", flag.ContinueOnError)
	flags.SetOutput(out)
	live := flags.Bool("live", false, "use the existing local ChatGPT login")
	program := flags.String("codex", "codex", "trusted local Codex executable")
	model := flags.String("model", "", "optional model override; empty preserves CLI configuration")
	sessions := flags.Int("sessions", 1, "isolated simultaneous sessions (1 or 2)")
	timeout := flags.Duration("timeout", 90*time.Second, "per-session timeout, at most 2m")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*live {
		flags.PrintDefaults()
		return nil
	}
	if flags.NArg() != 0 || *sessions < 1 || *sessions > 2 || *timeout <= 0 || *timeout > 2*time.Minute {
		return errors.New("require one or two sessions and a timeout in (0, 2m]")
	}
	type result struct {
		Session int `json:"session"`
		codexcli.Report
		ResponsesCompatibility string `json:"responses_api_compatibility"`
	}
	reports := make([]result, *sessions)
	errs := make([]error, *sessions)
	var wg sync.WaitGroup
	for i := range *sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i].Session = i + 1
			reports[i].ResponsesCompatibility = "not_tested"
			reports[i].Report, errs[i] = codexcli.Probe(ctx, codexcli.Config{Executable: *program, Model: *model, Timeout: *timeout})
		}()
	}
	wg.Wait()
	if err := json.NewEncoder(out).Encode(reports); err != nil {
		return errors.New("could not write probe report")
	}
	return errors.Join(errs...)
}

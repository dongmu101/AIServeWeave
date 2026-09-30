// Command claude-code-bridge probes CLI/MCP compatibility without serving inference.
// claude-code-bridge 命令验证 CLI/MCP 兼容性，不提供推理服务。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"time"
)

var errProbeOptions = errors.New("invalid probe options: require a model, one or two sessions, and a timeout in (0, 2m]")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := runCommand(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("claude-code-bridge", flag.ContinueOnError)
	flags.SetOutput(out)
	live := flags.Bool("live", false, "explicitly use the local Claude subscription")
	cli := flags.String("claude", "claude", "Claude executable path")
	model := flags.String("model", "sonnet", "model alias for the synthetic probe")
	sessions := flags.Int("sessions", 1, "simultaneous isolated sessions (1 or 2)")
	scenario := flags.String("scenario", "sequential", "tool pattern: sequential or parallel")
	limit := flags.Duration("timeout", 90*time.Second, "overall live deadline (maximum 2m)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*live {
		flags.PrintDefaults()
		return nil
	}
	if flags.NArg() != 0 || *model == "" || *sessions < 1 || *sessions > 2 || *limit <= 0 || *limit > 2*time.Minute || (*scenario != "sequential" && *scenario != "parallel") {
		return errProbeOptions
	}
	program, err := exec.LookPath(*cli)
	if err != nil {
		return errCLIStart
	}
	program, err = filepath.Abs(program)
	if err != nil {
		return errCLIStart
	}
	ctx, cancel := context.WithTimeout(ctx, *limit)
	defer cancel()
	if err := checkAuth(ctx, program, probeEnv(os.Environ())); err != nil {
		return err
	}
	reports := make([]probeReport, *sessions)
	errs := make([]error, *sessions)
	var wg sync.WaitGroup
	for i := range *sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i], errs[i] = runProbe(ctx, program, *model, fmt.Sprintf("probe-%d", i+1), *scenario)
		}()
	}
	wg.Wait()
	if err := json.NewEncoder(out).Encode(reports); err != nil {
		return errors.New("could not write probe report")
	}
	return errors.Join(errs...)
}

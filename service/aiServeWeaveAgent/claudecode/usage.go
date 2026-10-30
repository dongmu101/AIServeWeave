package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/cliusage"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

const accountUsageID = "aisw_account_usage"

// ReadAccountUsage uses Claude Code's native get_usage control request without inference.
// ReadAccountUsage 通过 Claude Code 原生 get_usage 控制请求查询额度，不执行推理。
func ReadAccountUsage(ctx context.Context) (cliusage.Account, error) {
	return AccountUsageReader("")(ctx)
}

// AccountUsageReader is ReadAccountUsage for the login under accountDir
// (runtime.Config.AccountDir); an empty accountDir reads the Agent's own.
//
// AccountUsageReader 是针对 accountDir（runtime.Config.AccountDir）下那份登录的
// ReadAccountUsage；accountDir 为空时读取 Agent 自己的登录。
func AccountUsageReader(accountDir string) func(context.Context) (cliusage.Account, error) {
	env := withAccountDir(probeEnv(os.Environ()), accountDir)
	return func(ctx context.Context) (cliusage.Account, error) {
		return readAccountUsage(ctx, "claude", env)
	}
}

func readAccountUsage(ctx context.Context, program string, env []string) (cliusage.Account, error) {
	if err := checkAuth(ctx, program, env); err != nil {
		if errors.Is(err, errLoginRequired) {
			return cliusage.Account{}, cliusage.ErrUnauthorized
		}
		return cliusage.Account{}, err
	}
	dir, err := os.MkdirTemp("", "aisw-claude-usage-")
	if err != nil {
		return cliusage.Account{}, errProbeSetup
	}
	defer os.RemoveAll(dir)
	args := []string{"--print", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--restricted", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false}`,
		"--disable-slash-commands", "--no-chrome", "--no-session-persistence"}
	var requests bytes.Buffer
	encoder := json.NewEncoder(&requests)
	_ = encoder.Encode(map[string]any{"type": "control_request", "request_id": "aisw_usage_init", "request": map[string]any{"subtype": "initialize"}})
	_ = encoder.Encode(map[string]any{"type": "control_request", "request_id": accountUsageID, "request": map[string]any{"subtype": "get_usage", "skip_behaviors": true}})
	cmd := exec.CommandContext(ctx, program, args...)
	output := boundedCapture{limit: maxEventBytes}
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, env, &requests, &output, io.Discard
	cmd.WaitDelay = time.Second
	if cliprocess.Configure(cmd) != nil {
		return cliusage.Account{}, errCLIStart
	}
	defer func() { _ = cmd.Cancel() }()
	if cliprocess.Run(cmd) != nil {
		return cliusage.Account{}, errCLIExit
	}
	return parseAccountUsage(output.buffer.Bytes())
}

func parseAccountUsage(data []byte) (cliusage.Account, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxEventBytes+1)
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "control_response" || event.Response.RequestID != accountUsageID {
			continue
		}
		if event.Response.Subtype != "success" {
			return cliusage.Account{}, cliusage.ErrUnavailable
		}
		type quotaWindow struct {
			Used  *float64 `json:"utilization"`
			Reset string   `json:"resets_at"`
		}
		var result struct {
			Available bool `json:"rate_limits_available"`
			Windows   struct {
				FiveHour *quotaWindow `json:"five_hour"`
				SevenDay *quotaWindow `json:"seven_day"`
				Sonnet   *quotaWindow `json:"seven_day_sonnet"`
				Opus     *quotaWindow `json:"seven_day_opus"`
			} `json:"rate_limits"`
		}
		if json.Unmarshal(event.Response.Response, &result) != nil {
			return cliusage.Account{}, errCLIProtocol
		}
		if !result.Available {
			return cliusage.Account{}, cliusage.ErrUnavailable
		}
		var account cliusage.Account
		for _, entry := range []struct {
			name   string
			window *quotaWindow
		}{
			{"five_hour", result.Windows.FiveHour}, {"seven_day", result.Windows.SevenDay},
			{"seven_day_sonnet", result.Windows.Sonnet}, {"seven_day_opus", result.Windows.Opus},
		} {
			w := entry.window
			if w == nil {
				continue
			}
			reset, err := time.Parse(time.RFC3339, w.Reset)
			if w.Used == nil || *w.Used < 0 || err != nil {
				continue
			}
			account.Windows = append(account.Windows, cliusage.Window{Name: entry.name, UsedPercent: *w.Used, ResetsAt: reset})
		}
		if len(account.Windows) == 0 {
			return account, cliusage.ErrUnavailable
		}
		return account, nil
	}
	return cliusage.Account{}, cliusage.ErrUnavailable
}

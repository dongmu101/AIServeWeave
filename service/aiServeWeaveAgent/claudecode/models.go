package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

const modelDiscoveryID = "aisw_model_discovery"

// discoverModels initializes an isolated CLI without submitting a user turn.
// discoverModels 初始化隔离的 CLI，不提交用户回合。
func discoverModels(ctx context.Context, program string, env []string) ([]string, error) {
	dir, err := os.MkdirTemp("", "aisw-claude-models-")
	if err != nil {
		return nil, errProbeSetup
	}
	defer os.RemoveAll(dir)
	args := []string{"--print", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--restricted", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false}`,
		"--disable-slash-commands", "--no-chrome", "--no-session-persistence"}
	request, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": modelDiscoveryID, "request": map[string]string{"subtype": "initialize"}})
	cmd := exec.CommandContext(ctx, program, args...)
	output := boundedCapture{limit: maxEventBytes}
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, env, bytes.NewReader(append(request, '\n')), &output, io.Discard
	cmd.WaitDelay = time.Second
	if cliprocess.Configure(cmd) != nil {
		return nil, errCLIStart
	}
	defer func() { _ = cmd.Cancel() }()
	if cliprocess.Run(cmd) != nil {
		return nil, errCLIExit
	}
	return parseModels(output.buffer.Bytes())
}

// parseModels publishes deduplicated resolved IDs from the matching initialize response.
// parseModels 从匹配的 initialize 响应中发布去重后的真实模型 ID。
func parseModels(data []byte) ([]string, error) {
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
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "control_response" || event.Response.RequestID != modelDiscoveryID {
			continue
		}
		if event.Response.Subtype != "success" {
			return nil, errCLIProtocol
		}
		var result struct {
			Models []struct {
				ResolvedModel string `json:"resolvedModel"`
			} `json:"models"`
		}
		if json.Unmarshal(event.Response.Response, &result) != nil || len(result.Models) == 0 || len(result.Models) > 256 {
			return nil, errCLIProtocol
		}
		models := make([]string, 0, len(result.Models))
		seen := make(map[string]bool, len(result.Models))
		for _, model := range result.Models {
			id := strings.TrimSpace(model.ResolvedModel)
			if id == "" {
				continue
			}
			if len(id) > 256 || strings.ContainsAny(id, "\r\n\x00") {
				return nil, errCLIProtocol
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			models = append(models, id)
		}
		if len(models) == 0 {
			return nil, errCLIProtocol
		}
		return models, nil
	}
	return nil, errCLIProtocol
}

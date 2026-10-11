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
)

const modelDiscoveryID = "aisw_model_discovery"

type discoveredModel struct {
	id, resolved, displayName string
}

// discoverModels initializes an isolated CLI without submitting a user turn.
// discoverModels 初始化隔离的 CLI，不提交用户回合。
func discoverModels(ctx context.Context, program string, env []string) ([]discoveredModel, error) {
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
	if configureProcess(cmd) != nil {
		return nil, errCLIStart
	}
	defer func() { _ = cmd.Cancel() }()
	if cmd.Run() != nil {
		return nil, errCLIExit
	}
	return parseModels(output.buffer.Bytes())
}

// parseModels accepts only the matching successful initialize response.
// parseModels 只接受请求 ID 匹配且成功的 initialize 响应。
func parseModels(data []byte) ([]discoveredModel, error) {
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
				Value         string `json:"value"`
				ID            string `json:"id"`
				ResolvedModel string `json:"resolvedModel"`
				DisplayName   string `json:"displayName"`
				DisplaySnake  string `json:"display_name"`
			} `json:"models"`
		}
		if json.Unmarshal(event.Response.Response, &result) != nil || len(result.Models) == 0 || len(result.Models) > 256 {
			return nil, errCLIProtocol
		}
		models := make([]discoveredModel, 0, len(result.Models))
		seen := make(map[string]bool, len(result.Models))
		for _, model := range result.Models {
			id := strings.TrimSpace(model.Value)
			if id == "" {
				id = strings.TrimSpace(model.ID)
			}
			if id == "" {
				continue
			}
			resolved := strings.TrimSpace(model.ResolvedModel)
			if resolved == "" {
				resolved = id
			}
			if len(id) > 256 || len(resolved) > 256 || strings.ContainsAny(id+resolved, "\r\n\x00") {
				return nil, errCLIProtocol
			}
			if seen[id] {
				continue
			}
			name := strings.TrimSpace(model.DisplayName)
			if name == "" {
				name = strings.TrimSpace(model.DisplaySnake)
			}
			if len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
				return nil, errCLIProtocol
			}
			seen[id] = true
			models = append(models, discoveredModel{id: id, resolved: resolved, displayName: name})
		}
		if len(models) == 0 {
			return nil, errCLIProtocol
		}
		return models, nil
	}
	return nil, errCLIProtocol
}

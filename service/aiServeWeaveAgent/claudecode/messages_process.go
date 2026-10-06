package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func bridgeToolName(index int) string { return "tool_" + strconv.Itoa(index) }

func (b *messagesBridge) runSession(s *messagesSession) {
	err := b.runProcess(s)
	if err != nil && s.ctx.Err() == nil {
		_ = s.send(nil, err)
	}
}

// runProcess retains a CLI while remote tools execute and reaps its entire group.
// runProcess 在远端执行工具时保留 CLI，并回收其完整进程组。
func (b *messagesBridge) runProcess(s *messagesSession) error {
	dir, err := os.MkdirTemp("", "aisw-claude-messages-")
	if err != nil {
		return errProbeSetup
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return errProbeSetup
	}
	defer listener.Close()
	token := randomID()
	server := &http.Server{Handler: bridgeMCPHandler(s, token, listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return s.ctx }}
	served := make(chan struct{})
	go func() { defer close(served); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-served }()
	config := map[string]any{"mcpServers": map[string]any{"aisw_bridge": map[string]any{"type": "http", "url": "http://" + listener.Addr().String() + "/mcp", "headers": map[string]string{"Authorization": "Bearer " + token}}}}
	data, _ := json.Marshal(config)
	configPath := filepath.Join(dir, "mcp.json")
	if os.WriteFile(configPath, data, 0600) != nil {
		return errProbeSetup
	}
	system, _ := textBlocks(s.request.System)
	systemPath := filepath.Join(dir, "system.txt")
	if os.WriteFile(systemPath, []byte(system), 0600) != nil {
		return errProbeSetup
	}
	model := b.model
	if model == "sonnet" {
		model = "claude-sonnet-4-6"
	}
	args := probeArgs(model, configPath)
	allowed := make([]string, len(s.request.Tools))
	for i := range s.request.Tools {
		allowed[i] = "mcp__aisw_bridge__" + bridgeToolName(i)
	}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--allowedTools":
			args[i+1] = strings.Join(allowed, ",")
		case "--system-prompt":
			args[i], args[i+1] = "--system-prompt-file", systemPath
		}
	}
	if s.request.OutputConfig != nil {
		args = append(args, "--effort", s.request.OutputConfig.Effort)
	}
	prompt, _ := textBlocks(s.request.Messages[0].Content)
	cmd := exec.CommandContext(s.ctx, b.program, args...)
	cmd.Dir, cmd.Stdin, cmd.Stderr = dir, strings.NewReader(prompt), io.Discard
	cmd.Env = append(probeEnv(os.Environ()), "CLAUDE_CODE_MAX_OUTPUT_TOKENS="+strconv.Itoa(s.request.MaxTokens), "MAX_THINKING_TOKENS=0", "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1", "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1", "DISABLE_PROMPT_CACHING=1")
	if s.request.Temperature != nil {
		extra, _ := json.Marshal(map[string]float64{"temperature": *s.request.Temperature})
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_EXTRA_BODY="+string(extra))
	}
	cmd.WaitDelay = 2 * time.Second
	if configureProcess(cmd) != nil {
		return errCLIStart
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return errCLIStart
	}
	if cmd.Start() != nil {
		_ = out.Close()
		return errCLIStart
	}
	defer func() { _ = cmd.Cancel() }()
	final, err := bridgeEvents(out, s)
	if err != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if err != nil {
		return err
	}
	if waitErr != nil {
		return errCLIExit
	}
	return s.send(final, nil)
}

// bridgeEvents uses message_stop rather than silence to delimit model rounds.
// bridgeEvents 使用 message_stop 划分模型回合，不使用静默时间猜测边界。
func bridgeEvents(reader io.Reader, s *messagesSession) (json.RawMessage, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxEventBytes+1)
	var final json.RawMessage
	var current messageBlock
	var arguments string
	var stopReason string
	finished, active, blockActive := false, false, false
	index, tools := 0, 0
	seenIDs := make(map[string]bool)
	for count := 0; scanner.Scan(); count++ {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if len(line) > maxEventBytes || count >= 10000 || finished {
			return nil, errCLIProtocol
		}
		var envelope struct {
			Type    string          `json:"type"`
			Subtype string          `json:"subtype"`
			IsError bool            `json:"is_error"`
			Tools   []string        `json:"tools"`
			Event   json.RawMessage `json:"event"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			return nil, errCLIProtocol
		}
		switch envelope.Type {
		case "system":
			if envelope.Subtype == "init" {
				for _, name := range envelope.Tools {
					ok := name == "EndConversation"
					for i := range s.request.Tools {
						if name == "mcp__aisw_bridge__"+bridgeToolName(i) {
							ok = true
						}
					}
					if !ok {
						return nil, errUnsafeTools
					}
				}
			}
		case "assistant", "user", "tool_progress", "tool_use_summary", "rate_limit_event":
		case "result":
			if envelope.IsError || envelope.Subtype != "success" || len(final) == 0 || active {
				return nil, errCLIResult
			}
			finished = true
		case "stream_event":
			var event struct {
				Type  string       `json:"type"`
				Index int          `json:"index"`
				Block messageBlock `json:"content_block"`
				Delta struct {
					Type        string `json:"type"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
			}
			if json.Unmarshal(envelope.Event, &event) != nil {
				return nil, errCLIProtocol
			}
			switch event.Type {
			case "message_start":
				if active || len(final) > 0 {
					return nil, errCLIProtocol
				}
				active, stopReason, index = true, "", 0
			case "content_block_start":
				if !active || blockActive || event.Index != index {
					return nil, errCLIProtocol
				}
				current, arguments, blockActive = event.Block, "", true
				if current.Type == "tool_use" {
					found := false
					for i, t := range s.request.Tools {
						if current.Name == "mcp__aisw_bridge__"+bridgeToolName(i) {
							current.Name = t.Name
							found = true
						}
					}
					if !found || current.ID == "" || len(current.ID) > 256 || tools >= maxCalls {
						return nil, errUnsafeTools
					}
					if seenIDs[current.ID] {
						return nil, errCLIProtocol
					}
					seenIDs[current.ID] = true
					current.ID = "toolu_aisw_" + randomID()
					tools++
					var obj map[string]json.RawMessage
					_ = json.Unmarshal(envelope.Event, &obj)
					obj["content_block"], _ = json.Marshal(current)
					envelope.Event, _ = json.Marshal(obj)
				} else if current.Type != "text" {
					return nil, errCLIProtocol
				}
			case "content_block_delta":
				if !active || !blockActive || event.Index != index {
					return nil, errCLIProtocol
				}
				if current.Type == "tool_use" && event.Delta.Type == "input_json_delta" {
					arguments += event.Delta.PartialJSON
					if len(arguments) > maxEventBytes {
						return nil, errCapacity
					}
				} else if current.Type != "text" || event.Delta.Type != "text_delta" {
					return nil, errCLIProtocol
				}
			case "content_block_stop":
				if !active || !blockActive || event.Index != index {
					return nil, errCLIProtocol
				}
				if current.Type == "tool_use" {
					if arguments != "" {
						current.Input = json.RawMessage(arguments)
					}
					var input map[string]any
					if json.Unmarshal(current.Input, &input) != nil || input == nil {
						return nil, errCLIProtocol
					}
					s.mu.Lock()
					if len(s.pending) >= maxPending || s.pending[current.ID] != nil {
						s.mu.Unlock()
						return nil, errCapacity
					}
					for _, pending := range s.pending {
						if !pending.delivered && pending.block.Name == current.Name && canonical(normalizedToolInput(pending.block.Name, pending.block.Input, s.request.Tools)) == canonical(normalizedToolInput(current.Name, current.Input, s.request.Tools)) {
							s.mu.Unlock()
							return nil, errCLIProtocol
						}
					}
					s.pending[current.ID] = &pendingTool{block: current, result: make(chan toolResult, 1)}
					s.signalLocked()
					s.mu.Unlock()
				}
				blockActive = false
				index++
			case "message_delta":
				if !active || blockActive {
					return nil, errCLIProtocol
				}
				stopReason = event.Delta.StopReason
				if stopReason != "tool_use" && stopReason != "end_turn" {
					return nil, errCLIProtocol
				}
			case "message_stop":
				if !active || blockActive || stopReason == "" {
					return nil, errCLIProtocol
				}
				active = false
				if stopReason != "tool_use" {
					final = append(json.RawMessage(nil), envelope.Event...)
					continue
				}
			case "ping":
				continue
			default:
				return nil, errCLIProtocol
			}
			if err := s.send(envelope.Event, nil); err != nil {
				return nil, err
			}
		default:
			return nil, errCLIProtocol
		}
	}
	if scanner.Err() != nil {
		return nil, errCLIProtocol
	}
	if !finished {
		return nil, errMissingResult
	}
	return final, nil
}

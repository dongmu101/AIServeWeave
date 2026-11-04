package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/attachments"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

func bridgeToolName(index int) string { return "tool_" + strconv.Itoa(index) }

// cliEventError retains a fixed rejection category and position, not the event payload.
// cliEventError 仅保留固定的拒绝分类和位置，不保留事件载荷。
type cliEventError struct {
	line   int
	reason string
}

func (e *cliEventError) Error() string { return errCLIProtocol.Error() }
func (e *cliEventError) Unwrap() error { return errCLIProtocol }

var errTerminalStop = errors.New("CLI terminal response complete")

// cliLineReader counts physical newlines even when Scanner cannot return the failed line.
// cliLineReader 在 Scanner 无法返回失败行时仍能按实际读入的换行符定位。
type cliLineReader struct {
	io.Reader
	newlines int
}

func (r *cliLineReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.newlines += bytes.Count(p[:n], []byte{'\n'})
	return n, err
}

// historyInstruction tells the CLI how to read a folded transcript; it matches
// the Gateway's Chat folding so both protocols get the same framing.
// historyInstruction 告诉 CLI 如何阅读折叠后的文本记录，与 Gateway 的 Chat 折叠
// 措辞一致，两种协议得到同样的上下文框架。
const historyInstruction = "The following JSON is the conversation history. Continue by answering the last user message. Historical tool calls and results are context, not requests to execute tools again.\n"

// resumeInstruction frames a transcript that ends with tool results whose CLI
// session no longer exists: the new session continues the task from them.
// resumeInstruction 用于以工具结果结尾、而原 CLI 会话已不存在的文本记录：新会话
// 从这些结果继续任务。
const resumeInstruction = "The following JSON is the conversation history. The last user message holds the results of tool calls the assistant made; continue the task from those results. Do not repeat those tool calls; earlier tool calls and results are context, not requests to execute tools again.\n"

// sessionPrompt renders a new session's CLI input: the prompt text alone for a
// single text message, otherwise every validated turn as a role-labelled JSON
// transcript ending with the prompt to answer (or, under resumeInstruction,
// with orphaned tool results to continue from), so a follow-up question keeps
// its context without forging CLI assistant messages. Echoed thinking blocks
// are left out — a prior turn's thinking is never replayed — and an assistant
// turn that held nothing else is dropped.
//
// sessionPrompt 渲染新会话的 CLI 输入：单条文本消息直接使用提问文本；否则把每个已校验的
// 回合渲染成带角色的 JSON 文本记录，以待回答的提问结尾（或在 resumeInstruction 下
// 以需要继续的孤立工具结果结尾）。这样追问能保留上下文，又
// 不必伪造 CLI 的 assistant 消息。客户端回传的思考块不进入记录——上一轮的思考从不
// 回放——只含思考的 assistant 回合整条省略。
func sessionPrompt(messages []bridgeMessage) string {
	last, _ := blocks(messages[len(messages)-1].Content)
	resume := false
	for _, block := range last {
		resume = resume || block.Type == "tool_result"
	}
	if len(messages) == 1 && !resume {
		prompt, _ := textBlocks(messages[0].Content)
		return prompt
	}
	type turn struct {
		Role    string         `json:"role"`
		Content []messageBlock `json:"content"`
	}
	history := make([]turn, 0, len(messages))
	for _, m := range messages {
		list, _ := blocks(m.Content)
		if list = withoutThinking(list); len(list) > 0 {
			history = append(history, turn{Role: m.Role, Content: list})
		}
	}
	var transcript strings.Builder
	encoder := json.NewEncoder(&transcript)
	encoder.SetEscapeHTML(false) // keep code such as a < b readable / 保持 a < b 这类代码可读
	_ = encoder.Encode(history)
	instruction := historyInstruction
	if resume {
		instruction = resumeInstruction
	}
	return instruction + strings.TrimSuffix(transcript.String(), "\n")
}

func (b *messagesBridge) runSession(s *messagesSession) {
	err := b.runProcess(s)
	if err != nil && s.ctx.Err() == nil {
		_ = s.send(nil, err)
	}
}

// runProcess retains a CLI while remote tools execute and reaps its entire
// group. Thinking stays off (MAX_THINKING_TOKENS=0) unless the request asked
// for it; then the budget becomes MAX_THINKING_TOKENS and
// --thinking-display summarized asks the CLI to hand up readable thinking
// text on models whose default is to omit it.
//
// runProcess 在远端执行工具时保留 CLI，并回收其完整进程组。除非请求要求思考，思考
// 保持关闭（MAX_THINKING_TOKENS=0）；要求时预算写入 MAX_THINKING_TOKENS，并以
// --thinking-display summarized 要求 CLI 在默认省略思考文本的模型上也给出可读文本。
func (b *messagesBridge) runProcess(s *messagesSession) error {
	messages, prepareErr := s.prepareMessages(s.ctx)
	if prepareErr != nil {
		return prepareErr
	}
	guarded := len(s.request.Safeguards) > 0
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
	servers := make(map[string]any)
	if !guarded {
		servers["aisw_bridge"] = map[string]any{"type": "http", "url": "http://" + listener.Addr().String() + "/mcp", "headers": map[string]string{"Authorization": "Bearer " + token}}
	}
	config := map[string]any{"mcpServers": servers}
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
	model := s.request.cliModel
	if model == "" {
		model = b.model
		if model == "sonnet" {
			model = "claude-sonnet-4-6"
		}
	}
	args := probeArgs(model, configPath)
	var allowed []string
	if !guarded {
		for i := range s.request.Tools {
			allowed = append(allowed, "mcp__aisw_bridge__"+bridgeToolName(i))
		}
	}
	settingsPath := filepath.Join(dir, "settings.json")
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--allowedTools":
			args[i+1] = strings.Join(allowed, ",")
		case "--system-prompt":
			args[i], args[i+1] = "--system-prompt-file", systemPath
		case "--permission-mode":
			if guarded {
				args[i+1] = "auto"
			}
		case "--settings":
			if guarded {
				args[i+1] = settingsPath
			}
		}
	}
	if s.request.OutputConfig != nil {
		args = append(args, "--effort", s.request.OutputConfig.Effort)
	}
	budget := s.request.thinkingBudget()
	if budget > 0 {
		args = append(args, "--thinking-display", "summarized")
	}
	prompt, structured := sessionInput(messages)
	if len(prompt) > maxRequestBytes {
		return &attachmentFailure{attachments.ErrLimit}
	}
	if guarded {
		// Guarded rounds carry the native request in the API body, not stdin.
		// 受保护回合通过 API 请求体携带原生请求，不经 stdin。
		prompt, structured = "Continue the supplied native Messages request.", false
	}
	if structured {
		args = append(args, "--input-format", "stream-json")
	}
	cmd := exec.CommandContext(s.ctx, b.program, args...)
	cmd.Dir, cmd.Stdin, cmd.Stderr = dir, strings.NewReader(prompt), io.Discard
	cmd.Env = append(slices.Clone(b.cliEnv()), "CLAUDE_CODE_MAX_OUTPUT_TOKENS="+strconv.Itoa(s.request.MaxTokens), "MAX_THINKING_TOKENS="+strconv.Itoa(budget), "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1", "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1", "DISABLE_PROMPT_CACHING=1")
	extra := make(map[string]any)
	if s.request.Temperature != nil {
		extra["temperature"] = *s.request.Temperature
	}
	var betas []string
	for _, beta := range strings.Split(s.request.betaFeatures, ",") {
		if beta = strings.TrimSpace(beta); beta != "" && !slices.Contains(betas, beta) {
			betas = append(betas, beta)
		}
	}
	if guarded {
		for key, value := range s.request.nativeDocument {
			extra[key] = value
		}
		if _, ok := extra["system"]; !ok {
			extra["system"] = []any{}
		}
		extra["model"], extra["stream"] = model, true
		extra["safeguards"] = s.request.Safeguards
		// Preserve tool identity for server rules; no caller tool is registered for local execution.
		// 保留服务端策略判断所依据的原始工具身份；调用方工具不在本机注册执行。
		if _, ok := extra["tools"]; !ok {
			extra["tools"] = s.request.Tools
		}
		if !slices.Contains(betas, "dangerous-tool-use-2026-09-03") {
			betas = append(betas, "dangerous-tool-use-2026-09-03")
		}
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_AUTO_MODE_SERVER=1")
	}
	if len(betas) > 0 {
		extra["betas"] = betas
	}
	if s.request.apiVersion != "" {
		cmd.Env = append(cmd.Env, "ANTHROPIC_CUSTOM_HEADERS=anthropic-version: "+s.request.apiVersion)
	}
	if len(extra) > 0 {
		var encoded strings.Builder
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if encoder.Encode(extra) != nil {
			return errMessages
		}
		body := strings.TrimSuffix(encoded.String(), "\n")
		if guarded {
			// Load the large inference extension after process start, avoiding exec's environment-size limit.
			// 由 CLI 启动后加载大型推理扩展，避开 exec 对环境变量大小的限制。
			settings, err := json.Marshal(map[string]any{"disableAllHooks": true, "autoMemoryEnabled": false, "env": map[string]string{"CLAUDE_CODE_EXTRA_BODY": body}})
			if err != nil || os.WriteFile(settingsPath, settings, 0600) != nil {
				return errProbeSetup
			}
		} else {
			cmd.Env = append(cmd.Env, "CLAUDE_CODE_EXTRA_BODY="+body)
		}
	}
	cmd.WaitDelay = 2 * time.Second
	if cliprocess.Configure(cmd) != nil {
		return errCLIStart
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return errCLIStart
	}
	if cliprocess.Start(cmd) != nil {
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
	// A terminal stop completes the public round before the CLI can recover or continue it.
	// 终止事件完成公开回合，CLI 随后被停止，避免其自动恢复或续写越过调用方边界。
	if errors.Is(err, errStopSequence) || errors.Is(err, errTerminalStop) {
		return s.send(final, nil)
	}
	if err != nil {
		return err
	}
	if waitErr != nil {
		return errCLIExit
	}
	return s.send(final, nil)
}

// bridgeEvents delimits rounds with message_stop. Thinking and
// redacted_thinking blocks are forwarded, signature included, only when the
// request enabled thinking; otherwise they stay CLI-internal and public
// content indices are renumbered to stay contiguous. Either way the retained
// CLI keeps the thinking history; what a client echoes back is discarded.
//
// bridgeEvents 使用 message_stop 划分回合。只有请求开启了思考时，thinking 与
// redacted_thinking 块才连同签名一起转发；否则它们留在 CLI 内部，对外内容索引重新
// 编号以保持连续。无论哪种情况，思考历史都由保留的 CLI 持有，客户端回传的部分被丢弃。
//
// With stop_sequences, text blocks pass through a stopMatcher. At the first
// match the text before it is released, the block is closed, and nothing the
// CLI produces afterwards reaches the client. The round then ends with a
// stop_sequence message_delta carrying the CLI's usage, or at once if the CLI
// starts a tool call first, since the API would never have produced it;
// bridgeEvents returns errStopSequence so runProcess stops the CLI.
//
// 请求带 stop_sequences 时，文本块经过 stopMatcher。第一次命中时放出命中位置之前的文本
// 并关闭该块，此后 CLI 产生的任何内容都不再到达客户端。回合随后以带 CLI 用量的
// stop_sequence message_delta 结束；若 CLI 先开始了工具调用则立即结束，因为 API 根本不会
// 产生它。bridgeEvents 返回 errStopSequence，由 runProcess 停止 CLI。
//
// Like the Messages API, nothing caps the rounds, tool calls or lines of a
// session; only the duplicate tool ID check is scoped to a round and restarts
// at each message_stop.
//
// 与 Messages API 一样，会话的回合数、工具调用数与行数都不设上限；只有重复工具 ID 检查
// 以回合为范围，在每次 message_stop 后重新开始。
func bridgeEvents(reader io.Reader, s *messagesSession) (json.RawMessage, error) {
	position := &cliLineReader{Reader: reader}
	scanner := bufio.NewScanner(position)
	scanner.Buffer(make([]byte, 4096), maxEventBytes+1)
	var final json.RawMessage
	var current messageBlock
	var arguments string
	var stopReason string
	// quota is set once the CLI reports its account's limit rejected this
	// round; the failure that follows is then a quota error, not a broken CLI.
	// quota 在 CLI 报告其账户限额拒绝了本回合后被设置；随后的失败据此视为额度错误，
	// 而不是 CLI 故障。
	var quota *quotaError
	finished, active, blockActive := false, false, false
	guarded := len(s.request.Safeguards) > 0
	showThinking := s.request.thinkingBudget() > 0 || (guarded && nativeThinkingEnabled(s.request.Thinking))
	index, publicIndex := 0, 0
	seenIDs := make(map[string]bool)
	publicIDs := make(map[string]string)
	matcher := stopMatcher{sequences: s.request.StopSequences}
	if guarded {
		matcher.sequences = nil
	} // the native provider applies stop_sequences / 原生提供方处理停止序列
	stopped := "" // the matched stop sequence, once found / 命中的停止序列（命中后设置）
	lineNumber := 0
	protocolError := func(reason string) error { return &cliEventError{line: lineNumber, reason: reason} }
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Any output is progress: restart the idle phase timer so only a CLI
		// that falls silent for generationTimeout is reclaimed.
		// 任何输出都算进展：重启空闲阶段计时器，只回收沉默满 generationTimeout 的 CLI。
		s.touch()
		if len(line) > maxEventBytes {
			return nil, protocolError("oversized_event")
		}
		var envelope struct {
			Type          string          `json:"type"`
			Subtype       string          `json:"subtype"`
			IsError       bool            `json:"is_error"`
			Tools         []string        `json:"tools"`
			Event         json.RawMessage `json:"event"`
			RateLimitInfo struct {
				Status   string `json:"status"`
				ResetsAt int64  `json:"resetsAt"`
			} `json:"rate_limit_info"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			return nil, protocolError("invalid_json")
		}
		if finished {
			// The CLI may append lifecycle notices or a prompt suggestion after its result.
			// CLI 可能在结果之后追加生命周期通知或下一步提示；它们不属于模型回复。
			if envelope.Type == "prompt_suggestion" || (envelope.Type == "system" && envelope.Subtype != "" && envelope.Subtype != "init") {
				continue
			}
			return nil, protocolError("unexpected_trailing_event")
		}
		switch envelope.Type {
		case "system":
			if envelope.Subtype == "init" {
				for _, name := range envelope.Tools {
					ok := name == "EndConversation"
					for i := range s.request.Tools {
						if len(s.request.Safeguards) == 0 && name == "mcp__aisw_bridge__"+bridgeToolName(i) {
							ok = true
						}
					}
					if !ok {
						return nil, errUnsafeTools
					}
				}
			}
		case "assistant", "user", "tool_progress", "tool_use_summary":
		case "rate_limit_event":
			if envelope.RateLimitInfo.Status == "rejected" {
				quota = &quotaError{}
				if envelope.RateLimitInfo.ResetsAt > 0 {
					quota.resetsAt = time.Unix(envelope.RateLimitInfo.ResetsAt, 0)
				}
			}
		case "result":
			if envelope.IsError || envelope.Subtype != "success" || len(final) == 0 || active {
				if quota != nil {
					return nil, quota
				}
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
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
				} `json:"delta"`
				Usage json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(envelope.Event, &event) != nil {
				return nil, protocolError("invalid_stream_event")
			}
			switch event.Type {
			case "message_start":
				if active || len(final) > 0 {
					return nil, protocolError("unexpected_message_start")
				}
				active, stopReason, index, publicIndex = true, "", 0, 0
			case "content_block_start":
				if !active || blockActive || event.Index != index {
					return nil, protocolError("unexpected_block_start")
				}
				if stopped != "" && event.Block.Type == "tool_use" && len(s.request.Safeguards) == 0 {
					// The API stops before this call; never register it.
					// API 会在这次调用之前停止；绝不登记它。
					if err := s.send(stopSequenceDelta(stopped, nil), nil); err != nil {
						return nil, err
					}
					return json.RawMessage(`{"type":"message_stop"}`), errStopSequence
				}
				current, arguments, blockActive = event.Block, "", true
				if current.Type == "tool_use" {
					found := false
					for i, t := range s.request.Tools {
						expected := "mcp__aisw_bridge__" + bridgeToolName(i)
						if len(s.request.Safeguards) > 0 {
							expected = t.Name
						}
						if current.Name == expected {
							current.Name = t.Name
							found = true
						}
					}
					if !found || current.ID == "" {
						return nil, errUnsafeTools
					}
					if seenIDs[current.ID] {
						return nil, protocolError("duplicate_tool_id")
					}
					seenIDs[current.ID] = true
					upstreamID := current.ID
					if !guarded {
						current.ID = "toolu_aisw_" + randomID()
					}
					publicIDs[upstreamID] = current.ID
					if stopped != "" {
						publicIDs[upstreamID] = "" // reviewed upstream, hidden by the stop sequence / 上游已检查，但被停止序列隐藏
					}
					if !guarded {
						var obj map[string]json.RawMessage
						_ = json.Unmarshal(envelope.Event, &obj)
						obj["content_block"], _ = json.Marshal(current)
						envelope.Event, _ = json.Marshal(obj)
					}
				} else if !guarded && current.Type != "text" && current.Type != "thinking" && current.Type != "redacted_thinking" {
					return nil, protocolError("unsupported_block")
				}
			case "content_block_delta":
				if !active || !blockActive || event.Index != index {
					return nil, protocolError("unexpected_block_delta")
				}
				if (current.Type == "tool_use" || (guarded && current.Type == "server_tool_use")) && event.Delta.Type == "input_json_delta" {
					arguments += event.Delta.PartialJSON
					if len(arguments) > maxEventBytes {
						return nil, errCapacity
					}
				} else if !(current.Type == "text" && event.Delta.Type == "text_delta") && !(current.Type == "thinking" && (event.Delta.Type == "thinking_delta" || event.Delta.Type == "signature_delta")) {
					return nil, protocolError("unsupported_block_delta")
				}
			case "content_block_stop":
				if !active || !blockActive || event.Index != index {
					return nil, protocolError("unexpected_block_stop")
				}
				if current.Type == "tool_use" && stopped == "" && !guarded {
					if arguments != "" {
						current.Input = json.RawMessage(arguments)
					}
					var input map[string]any
					if json.Unmarshal(current.Input, &input) != nil || input == nil {
						return nil, protocolError("invalid_tool_input")
					}
					s.mu.Lock()
					if s.pending[current.ID] != nil {
						s.mu.Unlock()
						return nil, protocolError("duplicate_pending_tool")
					}
					if len(s.request.Safeguards) == 0 {
						for _, pending := range s.pending {
							if !pending.delivered && pending.block.Name == current.Name && canonical(normalizedToolInput(pending.block.Name, pending.block.Input, s.request.Tools)) == canonical(normalizedToolInput(current.Name, current.Input, s.request.Tools)) {
								s.mu.Unlock()
								return nil, protocolError("ambiguous_tool_call")
							}
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
					return nil, protocolError("unexpected_message_delta")
				}
				var results json.RawMessage
				if !guarded {
					mapped, mappedResults, err := remapSafeguardDelta(envelope.Event, publicIDs)
					if err != nil {
						return nil, protocolError("invalid_safeguard_results")
					}
					envelope.Event, results = mapped, mappedResults
				}
				if stopped != "" {
					// Whatever the CLI's own reason, the round ended at the sequence.
					// 无论 CLI 自己给出什么原因，回合都已在停止序列处结束。
					stopReason = "stop_sequence"
					if err := s.send(stopSequenceDelta(stopped, event.Usage, results), nil); err != nil {
						return nil, err
					}
					continue
				}
				stopReason = event.Delta.StopReason
				switch stopReason {
				case "tool_use", "end_turn", "max_tokens", "stop_sequence", "refusal", "model_context_window_exceeded":
				case "pause_turn":
					if !guarded {
						return nil, protocolError("unsupported_stop_reason")
					}
				default:
					return nil, protocolError("unsupported_stop_reason")
				}
			case "message_stop":
				if !active || blockActive || stopReason == "" {
					return nil, protocolError("unexpected_message_stop")
				}
				if stopped != "" {
					return append(json.RawMessage(nil), envelope.Event...), errStopSequence
				}
				active = false
				// Tool IDs need only be unique within a round.
				// 工具 ID 只需在回合内唯一。
				clear(seenIDs)
				clear(publicIDs)
				if stopReason == "tool_use" && len(s.request.Safeguards) > 0 {
					return append(json.RawMessage(nil), envelope.Event...), errTerminalStop
				}
				if stopReason != "tool_use" {
					final = append(json.RawMessage(nil), envelope.Event...)
					if stopReason != "end_turn" {
						return final, errTerminalStop
					}
					continue
				}
			case "ping":
				continue
			default:
				return nil, protocolError("unsupported_stream_event")
			}
			switch event.Type {
			case "content_block_start", "content_block_delta", "content_block_stop":
				// Hidden thinking is skipped; public content indices stay contiguous.
				// 不公开的思考被跳过，对外内容索引保持连续。
				if current.isThinking() && !showThinking {
					continue
				}
				// Nothing after a stop sequence reaches the client.
				// 停止序列之后的内容都不到达客户端。
				if stopped != "" {
					continue
				}
				if current.Type == "text" && len(matcher.sequences) > 0 {
					switch event.Type {
					case "content_block_delta":
						emit, matched := matcher.feed(event.Delta.Text)
						if emit != "" {
							if err := s.send(textDeltaEvent(publicIndex, emit), nil); err != nil {
								return nil, err
							}
						}
						if matched != "" {
							stopped = matched
							if err := s.send(blockStopEvent(publicIndex), nil); err != nil {
								return nil, err
							}
							publicIndex++
						}
						continue
					case "content_block_stop":
						if rest := matcher.flush(); rest != "" {
							if err := s.send(textDeltaEvent(publicIndex, rest), nil); err != nil {
								return nil, err
							}
						}
					}
				}
				if event.Index != publicIndex {
					var obj map[string]json.RawMessage
					_ = json.Unmarshal(envelope.Event, &obj)
					obj["index"], _ = json.Marshal(publicIndex)
					envelope.Event, _ = json.Marshal(obj)
				}
				if event.Type == "content_block_stop" {
					publicIndex++
				}
			}
			if err := s.send(envelope.Event, nil); err != nil {
				return nil, err
			}
		default:
			return nil, protocolError("unsupported_envelope")
		}
	}
	if scanner.Err() != nil {
		return nil, &cliEventError{line: position.newlines + 1, reason: "stream_read_failed"}
	}
	if !finished {
		if quota != nil {
			return nil, quota
		}
		return nil, errMissingResult
	}
	return final, nil
}

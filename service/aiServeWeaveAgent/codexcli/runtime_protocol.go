package codexcli

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"AIServeWeave/common/runtime"
)

// runChat turns canonical history into isolated app-server events. Reply
// deltas become Content; reasoning summary deltas
// (item/reasoning/summaryTextDelta) become ReasoningContent, with a blank
// line between successive summary parts or reasoning items. Codex reports a
// summary of its reasoning, not the raw chain of thought: raw
// item/reasoning/textDelta is only sent when show_raw_agent_reasoning is on,
// which this isolated server never enables, and is ignored if it arrives so
// the two never interleave. Reply and summary share one maxFrameBytes bound.
//
// runChat 把规范化历史转换为隔离的 app-server 事件。回复增量成为 Content；思考摘要
// 增量（item/reasoning/summaryTextDelta）成为 ReasoningContent，相邻的摘要段或
// reasoning 条目之间以空行分隔。Codex 报告的是思考摘要，而不是原始思考链：原始的
// item/reasoning/textDelta 只在开启 show_raw_agent_reasoning 时发送，本隔离服务从不
// 开启；万一到达也会被忽略，避免两者交错。回复与摘要共用一个 maxFrameBytes 上限。
func runChat(p *protocol, dir string, req runtime.ChatRequest, input chatInput, instructions []string, emit func(runtime.ChatEvent) error) error {
	if err := initializeServer(p); err != nil {
		return err
	}
	names := make([]string, 0, len(input.allowed))
	for name := range input.allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	params := map[string]any{
		"cwd": dir, "ephemeral": true, "sandbox": "read-only", "approvalPolicy": "never", "environments": []any{},
		"baseInstructions":      "You are an assistant serving a remote client. Follow the supplied conversation. Use only the supplied dynamic tools; the caller executes them on its own machine. Never access the server's files or execute server shell, MCP or plugin tools.",
		"developerInstructions": "Request client actions using these registered dynamic tool names: " + strings.Join(names, ", ") + ". Client tools may read files or execute commands on the caller's machine. Disabled server shell tools do not disable the client's tools. Return their calls to the caller; never execute client code locally.", "dynamicTools": input.tools,
	}
	if req.Model != "" {
		params["model"] = req.Model
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model              string   `json:"model"`
		InstructionSources []string `json:"instructionSources"`
	}
	if err := p.request(3, "thread/start", params, &thread); err != nil {
		return err
	}
	if thread.Thread.ID == "" || thread.Model == "" || len(thread.Model) > 256 {
		return ErrProtocol
	}
	for _, path := range thread.InstructionSources {
		allowed := false
		for _, expected := range instructions {
			if filepath.Clean(path) == expected {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrIsolation
		}
	}
	if err := p.request(4, "thread/inject_items", map[string]any{"threadId": thread.Thread.ID, "items": input.items}, new(map[string]any)); err != nil {
		return err
	}
	turnParams := map[string]any{"threadId": thread.Thread.ID, "input": []any{}}
	if len(input.schema) > 0 {
		turnParams["outputSchema"] = input.schema
	}
	if input.effort != "" {
		turnParams["effort"] = input.effort
	}
	if input.summary != "" {
		turnParams["summary"] = input.summary
	}
	if err := p.write(map[string]any{"id": 5, "method": "turn/start", "params": turnParams}); err != nil {
		return err
	}
	var turnID string
	var early []frame
	earlyBytes := 0
	for turnID == "" {
		msg, err := p.read()
		if err != nil {
			return err
		}
		if msg.Method != "" {
			if len(msg.ID) > 0 {
				return ErrUnexpectedTool
			}
			earlyBytes += len(msg.Params)
			if earlyBytes > maxFrameBytes || len(early) >= 64 {
				return ErrProtocol
			}
			early = append(early, msg)
			continue
		}
		var id int
		if json.Unmarshal(msg.ID, &id) != nil || id != 5 {
			return ErrProtocol
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return ErrRPC
		}
		var result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(msg.Result, &result) != nil || result.Turn.ID == "" {
			return ErrProtocol
		}
		turnID = result.Turn.ID
	}
	event := runtime.ChatEvent{ID: turnID, Model: thread.Model}
	var usage *runtime.Usage
	textBytes := 0    // reply and reasoning summary together / 回复与思考摘要合计
	summaryPart := "" // item and part of the last summary delta / 上一段摘要增量所属的条目与段
	for {
		var msg frame
		if len(early) > 0 {
			msg = early[0]
			early = early[1:]
		} else {
			var err error
			msg, err = p.read()
			if err != nil {
				return err
			}
		}
		var n struct {
			ThreadID     string          `json:"threadId"`
			TurnID       string          `json:"turnId"`
			SummaryIndex int64           `json:"summaryIndex"`
			CallID       string          `json:"callId"`
			Tool         string          `json:"tool"`
			Namespace    *string         `json:"namespace"`
			Arguments    json.RawMessage `json:"arguments"`
			Delta        string          `json:"delta"`
			ItemID       string          `json:"itemId"`
			Item         struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"item"`
			Turn struct {
				ID     string          `json:"id"`
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"turn"`
			Error      json.RawMessage `json:"error"`
			WillRetry  bool            `json:"willRetry"`
			TokenUsage struct {
				Last struct {
					Input     int `json:"inputTokens"`
					Output    int `json:"outputTokens"`
					Reasoning int `json:"reasoningOutputTokens"`
					Total     int `json:"totalTokens"`
				} `json:"last"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(msg.Params, &n) != nil {
			return ErrProtocol
		}
		if n.ThreadID != "" && n.ThreadID != thread.Thread.ID {
			return ErrProtocol
		}
		if n.TurnID != "" && n.TurnID != turnID {
			return ErrProtocol
		}
		if len(msg.ID) > 0 {
			if msg.Method != "item/tool/call" {
				return ErrUnexpectedTool
			}
			if !validRequestID(msg.ID) || n.ThreadID != thread.Thread.ID || n.TurnID != turnID || n.Namespace != nil || !input.allowed[n.Tool] || n.CallID == "" || len(n.CallID) > 256 || !json.Valid(n.Arguments) {
				return ErrProtocol
			}
			var args map[string]any
			if json.Unmarshal(n.Arguments, &args) != nil || args == nil {
				return ErrProtocol
			}
			event.Delta = runtime.ChatMessageDelta{Role: "assistant", ToolCalls: []runtime.ToolCallDelta{{Index: 0, ID: n.CallID, Type: "function", Function: runtime.FunctionCallDelta{Name: n.Tool, Arguments: string(n.Arguments)}}}}
			if err := emit(event); err != nil {
				return err
			}
			event.Delta = runtime.ChatMessageDelta{}
			event.FinishReason = "tool_calls"
			event.Usage = usage
			// Stop at the callback boundary; never synthesize a result or execute it locally.
			// 在回调边界停止，绝不伪造工具结果或在本机执行客户端工具。
			return emit(event)
		}
		switch msg.Method {
		case "item/agentMessage/delta":
			if n.ThreadID != thread.Thread.ID || n.TurnID != turnID || n.ItemID == "" {
				return ErrProtocol
			}
			textBytes += len(n.Delta)
			if textBytes > maxFrameBytes {
				return ErrProtocol
			}
			event.Delta = runtime.ChatMessageDelta{Role: "assistant", Content: n.Delta}
			if err := emit(event); err != nil {
				return err
			}
		case "item/reasoning/summaryTextDelta":
			if n.ThreadID != thread.Thread.ID || n.TurnID != turnID || n.ItemID == "" || n.SummaryIndex < 0 {
				return ErrProtocol
			}
			delta := n.Delta
			if part := n.ItemID + "\x00" + strconv.FormatInt(n.SummaryIndex, 10); part != summaryPart && delta != "" {
				if summaryPart != "" {
					delta = "\n\n" + delta
				}
				summaryPart = part
			}
			textBytes += len(delta)
			if textBytes > maxFrameBytes {
				return ErrProtocol
			}
			if delta == "" {
				continue
			}
			event.Delta = runtime.ChatMessageDelta{Role: "assistant", ReasoningContent: delta}
			if err := emit(event); err != nil {
				return err
			}
		case "thread/tokenUsage/updated":
			last := n.TokenUsage.Last
			if n.ThreadID != thread.Thread.ID || n.TurnID != turnID || last.Input < 0 || last.Output < 0 || last.Reasoning < 0 || last.Total < 0 {
				return ErrProtocol
			}
			// reasoningOutputTokens is the reasoning share of outputTokens.
			// reasoningOutputTokens 是 outputTokens 中用于思考的部分。
			usage = &runtime.Usage{PromptTokens: last.Input, CompletionTokens: last.Output, TotalTokens: last.Total, ReasoningTokens: last.Reasoning}
		case "turn/completed":
			if n.ThreadID != thread.Thread.ID || n.Turn.ID != turnID {
				return ErrProtocol
			}
			if n.Turn.Status != "completed" || (len(n.Turn.Error) > 0 && string(n.Turn.Error) != "null") {
				return turnFailure(p, n.Turn.Error)
			}
			event.Delta = runtime.ChatMessageDelta{}
			event.FinishReason = "stop"
			event.Usage = usage
			return emit(event)
		case "item/started", "item/completed":
			if n.ThreadID != thread.Thread.ID || n.TurnID != turnID {
				return ErrProtocol
			}
		case "error":
			// Codex reports a transient upstream failure it is about to retry
			// itself (a dropped stream, a 5xx) with willRetry; the turn is still
			// running and its own turn/completed settles the outcome.
			// Codex 用 willRetry 报告它自己即将重试的瞬时上游故障（断流、5xx）；回合仍在
			// 进行，最终结果由它自己的 turn/completed 给出。
			if n.WillRetry {
				continue
			}
			return turnFailure(p, n.Error)
		}
	}
}

// rateLimitReadID is the JSON-RPC id of the account/rateLimits/read request
// turnFailure sends after a quota failure; the turn itself used ids 3 to 5.
//
// rateLimitReadID 是 turnFailure 在额度失败后发送的 account/rateLimits/read 请求的
// JSON-RPC id；回合本身用掉了 3 到 5。
const rateLimitReadID = 6

// turnFailure classifies a failed turn's TurnError. usageLimitExceeded and
// rateLimitExceeded mean the account, not the node, is spent: they become a
// quotaError, and while the app-server is still up it is asked — without
// inference — when the exhausted window resets, so the Gateway can set this
// account aside for exactly that long. Any other failure stays ErrTurn. The
// upstream message is never read.
//
// turnFailure 对失败回合的 TurnError 分类。usageLimitExceeded 与 rateLimitExceeded
// 表示用完的是账户而不是节点：它们成为 quotaError；趁 app-server 还在，再（不执行推理地）
// 询问用尽的窗口何时重置，让 Gateway 恰好把这个账户搁置那么久。其他失败仍是 ErrTurn。
// 上游的错误文本从不读取。
func turnFailure(p *protocol, raw json.RawMessage) error {
	switch failureCode(raw) {
	case "usageLimitExceeded", "rateLimitExceeded":
	default:
		return ErrTurn
	}
	failure := &quotaError{}
	var usage json.RawMessage
	if p.request(rateLimitReadID, "account/rateLimits/read", map[string]any{}, &usage) == nil {
		if account, err := parseAccountUsage(usage); err == nil {
			failure.resetsAt = exhaustedReset(account)
		}
	}
	return failure
}

package claudecode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

const maxRequestBytes = 8 << 20

var errMessages = errors.New("invalid or unsupported Messages request")

type messageBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Thinking, Signature and Data belong to "thinking" and
	// "redacted_thinking" blocks only. The retained CLI keeps its own
	// thinking history, so such blocks a client echoes back are accepted and
	// then discarded, never replayed.
	// Thinking、Signature 与 Data 只属于 "thinking" 与 "redacted_thinking" 块。保留的
	// CLI 自己持有思考历史，因此客户端回传的这类块被接受后即丢弃，从不回放。
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type bridgeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type bridgeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type messagesRequest struct {
	cliModel     string
	Model        string              `json:"model"`
	MaxTokens    int                 `json:"max_tokens"`
	Stream       bool                `json:"stream,omitempty"`
	System       json.RawMessage     `json:"system,omitempty"`
	Messages     []bridgeMessage     `json:"messages"`
	Tools        []bridgeTool        `json:"tools,omitempty"`
	ToolChoice   json.RawMessage     `json:"tool_choice,omitempty"`
	Metadata     json.RawMessage     `json:"metadata,omitempty"`
	Thinking     json.RawMessage     `json:"thinking,omitempty"`
	OutputConfig *bridgeOutputConfig `json:"output_config,omitempty"`
	Temperature  *float64            `json:"temperature,omitempty"`
	// StopSequences end a round at the first match, emulated over the CLI's
	// text output (see stopMatcher).
	// StopSequences 在第一处命中时结束回合，基于 CLI 的文本输出模拟（见 stopMatcher）。
	StopSequences []string `json:"stop_sequences,omitempty"`
}

type bridgeOutputConfig struct {
	Effort string `json:"effort"`
}

// decodeStrict rejects unknown fields without including submitted values in errors.
// decodeStrict 拒绝未知字段，错误中不包含提交的字段值。
func decodeStrict(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errMessages
	}
	return nil
}

func blocks(raw json.RawMessage) ([]messageBlock, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []messageBlock{{Type: "text", Text: text}}, nil
	}
	var result []messageBlock
	if decodeStrict(raw, &result) != nil || len(result) == 0 || len(result) > 256 {
		return nil, errMessages
	}
	return result, nil
}

func textBlocks(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	list, err := blocks(raw)
	if err != nil {
		return "", err
	}
	var out string
	for _, b := range list {
		if b.Type != "text" {
			return "", errMessages
		}
		out += b.Text
	}
	return out, nil
}

// isThinking reports whether b is a thinking or redacted_thinking block.
// isThinking 报告 b 是否为 thinking 或 redacted_thinking 块。
func (b messageBlock) isThinking() bool { return b.Type == "thinking" || b.Type == "redacted_thinking" }

// withoutThinking returns list minus its thinking blocks: the CLI owns that
// history, so it never enters a transcript or a history comparison.
// withoutThinking 返回去掉思考块之后的 list：这段历史归 CLI 所有，因此从不进入文本
// 记录或历史比对。
func withoutThinking(list []messageBlock) []messageBlock {
	out := make([]messageBlock, 0, len(list))
	for _, b := range list {
		if !b.isThinking() {
			out = append(out, b)
		}
	}
	return out
}

// thinkingBudget returns the requested thinking budget, or 0 when thinking
// is off; validate has already checked the request.
// thinkingBudget 返回请求的思考预算；思考关闭时返回 0。请求已经过 validate 校验。
func (r messagesRequest) thinkingBudget() int {
	var thinking struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	}
	if json.Unmarshal(r.Thinking, &thinking) != nil || thinking.Type != "enabled" {
		return 0
	}
	return thinking.BudgetTokens
}

// validate accepts thinking {"type":"disabled"} or {"type":"enabled",
// "budget_tokens":N} with 1024 <= N < max_tokens and no temperature, as the
// Messages API does; adaptive thinking is refused here because the CLI runs
// with adaptive thinking disabled and a fixed MAX_THINKING_TOKENS. Requests
// through the Gateway never hit this: it rewrites adaptive thinking into an
// enabled budget first (see the Gateway's nativeThinking). Thinking blocks
// may appear only on assistant turns and are discarded later.
//
// validate 接受 thinking {"type":"disabled"}，或与 Messages API 一致的
// {"type":"enabled","budget_tokens":N}（1024 <= N < max_tokens，且不带 temperature）；
// 这里拒绝 adaptive thinking，因为 CLI 以关闭 adaptive、固定 MAX_THINKING_TOKENS 的方式
// 运行。经 Gateway 的请求不会走到这一步：Gateway 会先把 adaptive 改写成带预算的
// enabled（见 Gateway 的 nativeThinking）。思考块只能出现在 assistant 回合里，之后会被丢弃。
//
// stop_sequences may hold up to maxStopSequences non-empty sequences of at
// most maxStopSequenceBytes each.
//
// stop_sequences 最多 maxStopSequences 个非空序列，每个不超过 maxStopSequenceBytes 字节。
func (r messagesRequest) validate(model string) error {
	if r.Model != model || r.MaxTokens < 1 || len(r.Messages) == 0 || len(r.Messages) > 2048 || len(r.Tools) > 128 {
		return errMessages
	}
	if _, err := textBlocks(r.System); err != nil {
		return err
	}
	if len(r.Thinking) > 0 {
		var thinking struct {
			Type         string `json:"type"`
			BudgetTokens *int   `json:"budget_tokens"`
		}
		if decodeStrict(r.Thinking, &thinking) != nil {
			return errMessages
		}
		switch thinking.Type {
		case "disabled":
			if thinking.BudgetTokens != nil {
				return errMessages
			}
		case "enabled":
			if thinking.BudgetTokens == nil || *thinking.BudgetTokens < 1024 || *thinking.BudgetTokens >= r.MaxTokens || r.Temperature != nil {
				return errMessages
			}
		default:
			return errMessages
		}
	}
	if r.OutputConfig != nil {
		switch r.OutputConfig.Effort {
		case "low", "medium", "high", "xhigh", "max":
		default:
			return errMessages
		}
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 1) {
		return errMessages
	}
	if len(r.StopSequences) > maxStopSequences {
		return errMessages
	}
	for _, sequence := range r.StopSequences {
		if sequence == "" || len(sequence) > maxStopSequenceBytes {
			return errMessages
		}
	}
	if len(r.ToolChoice) > 0 {
		var choice struct {
			Type string `json:"type"`
		}
		if decodeStrict(r.ToolChoice, &choice) != nil || choice.Type != "auto" {
			return errMessages
		}
	}
	names := make(map[string]bool)
	for _, tool := range r.Tools {
		var schema map[string]json.RawMessage
		if tool.Name == "" || len(tool.Name) > 128 || names[tool.Name] || json.Unmarshal(tool.InputSchema, &schema) != nil || schema == nil {
			return errMessages
		}
		names[tool.Name] = true
	}
	for _, m := range r.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return errMessages
		}
		list, err := blocks(m.Content)
		if err != nil {
			return err
		}
		for _, b := range list {
			if !b.isThinking() && (b.Thinking != "" || b.Signature != "" || b.Data != "") {
				return errMessages
			}
			switch b.Type {
			case "thinking", "redacted_thinking":
				if m.Role != "assistant" {
					return errMessages
				}
			case "text":
			case "tool_use":
				var args map[string]json.RawMessage
				if m.Role != "assistant" || b.ID == "" || !names[b.Name] || json.Unmarshal(b.Input, &args) != nil || args == nil {
					return errMessages
				}
			case "tool_result":
				if m.Role != "user" || b.ToolUseID == "" || len(b.Content) > maxEventBytes {
					return errMessages
				}
				if _, err := textBlocks(b.Content); err != nil {
					return err
				}
			default:
				return errMessages
			}
		}
	}
	return nil
}

type bridgeEvent struct {
	data json.RawMessage
	err  error
}

type pendingTool struct {
	block     messageBlock
	result    chan toolResult
	claimed   bool
	delivered bool
}

type messagesSession struct {
	owner    principal
	request  messagesRequest
	messages []bridgeMessage
	history  [32]byte
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	events   chan bridgeEvent
	mu       sync.Mutex
	busy     bool
	waiting  bool
	pending  map[string]*pendingTool
	changed  chan struct{}
	activity chan bool
	mcpCalls int
	// resumed holds the tool results a resumed session started from. They
	// count as held while the session lives and become consumed only when its
	// first round completes, so a round that fails before then can be retried.
	// Guarded by mu.
	//
	// resumed 保存恢复会话所依据的工具结果。会话存活期间它们视为被持有，首个回合完成后
	// 才记为已消费，因此在此之前失败的回合可以重试。受 mu 保护。
	resumed map[string]toolResult
}

type messagesBridge struct {
	program, model string
	keys           map[string]principal
	clock          runtime.Clock
	mu             sync.Mutex
	sessions       map[*messagesSession]bool
	closed         bool
	capacity       int
	wg             sync.WaitGroup
	// env is the CLI environment, carrying CLAUDE_CONFIG_DIR when the
	// runtime declares an AccountDir; nil uses the Agent's own environment.
	// env 是 CLI 的环境变量，运行时声明了 AccountDir 时带有 CLAUDE_CONFIG_DIR；
	// 为 nil 时使用 Agent 自己的环境。
	env []string
	// start is replaced by offline fixtures, never by HTTP input.
	// start 仅由离线测试替换，从不取自 HTTP 输入。
	start func(*messagesSession)
	// consumed maps tool IDs whose results were delivered to their expiry,
	// so a replay is rejected even after its session ended. Guarded by mu.
	// consumed 记录已投递结果的工具 ID 及其到期时间，会话结束后重放仍被拒绝。受 mu 保护。
	consumed map[string]time.Time
}

// Replay memory. It outlives the Gateway's own used marker so either side
// alone refuses a replay; maxConsumed keeps it bounded under any load.
//
// 重放记忆。它比 Gateway 的已使用标记存活更久，任何一侧单独都能拒绝重放；
// maxConsumed 保证任何负载下都有界。
const (
	// consumedTTL is how long a delivered tool ID stays refused.
	// consumedTTL 是已投递工具 ID 保持被拒绝的时长。
	consumedTTL = 24 * time.Hour
	// maxConsumed caps the remembered IDs; past it the oldest are dropped.
	// maxConsumed 限制记住的 ID 数量，超出后丢弃最早的。
	maxConsumed = 8192
)

func newMessagesBridge(program, model string, keys map[string]principal, clock runtime.Clock) *messagesBridge {
	b := &messagesBridge{program: program, model: model, keys: keys, clock: clock, sessions: make(map[*messagesSession]bool), capacity: 2, consumed: make(map[string]time.Time)}
	b.start = b.runSession
	return b
}

// consumedLocked reports whether any of results was already delivered and is
// still remembered. The caller holds b.mu.
//
// consumedLocked 报告 results 中是否有已投递且仍在记忆中的工具 ID。调用方持有 b.mu。
func (b *messagesBridge) consumedLocked(results map[string]toolResult) bool {
	now := b.clock.Now()
	for id := range results {
		if expiry, ok := b.consumed[id]; ok && now.Before(expiry) {
			return true
		}
	}
	return false
}

// consumeLocked remembers delivered tool IDs, first dropping expired entries
// and then, if still full, the entries closest to expiry. The caller holds b.mu.
//
// consumeLocked 记住已投递的工具 ID：先清除过期项，仍然满时再丢弃最接近到期的项。
// 调用方持有 b.mu。
func (b *messagesBridge) consumeLocked(results map[string]toolResult) {
	now := b.clock.Now()
	if len(b.consumed)+len(results) > maxConsumed {
		for id, expiry := range b.consumed {
			if !now.Before(expiry) {
				delete(b.consumed, id)
			}
		}
		for len(b.consumed)+len(results) > maxConsumed {
			oldest, first := "", time.Time{}
			for id, expiry := range b.consumed {
				if oldest == "" || expiry.Before(first) {
					oldest, first = id, expiry
				}
			}
			delete(b.consumed, oldest)
		}
	}
	for id := range results {
		b.consumed[id] = now.Add(consumedTTL)
	}
}

// cliEnv returns the environment every CLI process of this bridge starts with.
// cliEnv 返回本桥接启动每个 CLI 进程时使用的环境变量。
func (b *messagesBridge) cliEnv() []string {
	if b.env != nil {
		return b.env
	}
	return probeEnv(os.Environ())
}

func (b *messagesBridge) close() {
	b.mu.Lock()
	b.closed = true
	for s := range b.sessions {
		s.cancel()
	}
	b.mu.Unlock()
	b.wg.Wait()
}

func (b *messagesBridge) authenticate(r *http.Request) (principal, bool) {
	token := r.Header.Get("X-Api-Key")
	if token == "" {
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if len(h) >= len(prefix) && h[:len(prefix)] == prefix {
			token = h[len(prefix):]
		}
	}
	for key, owner := range b.keys {
		if key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(token)) == 1 {
			return owner, true
		}
	}
	return principal{}, false
}

func messagesError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": message}})
}

// ServeHTTP implements a loopback-only experimental Messages front door.
// ServeHTTP 实现仅用于回环实验的 Messages 前门。
func (b *messagesBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		messagesError(w, 403, "browser origin rejected")
		return
	}
	owner, ok := b.authenticate(r)
	if !ok {
		messagesError(w, 401, "authentication required")
		return
	}
	if r.URL.Path != "/v1/messages" {
		messagesError(w, 404, "endpoint not supported")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		messagesError(w, 405, "POST required")
		return
	}
	media, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || media != "application/json" {
		messagesError(w, 415, "JSON required")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		messagesError(w, 413, "request size limit exceeded")
		return
	}
	var req messagesRequest
	if decodeStrict(data, &req) != nil || req.validate(b.model) != nil {
		messagesError(w, 400, "unsupported Messages shape or extension: thinking must be disabled or enabled with budget_tokens (the Gateway rewrites adaptive thinking), and prompt caching and output formats are not supported")
		return
	}
	s, err := b.acquire(owner, req)
	if err != nil {
		messagesError(w, 409, "session unavailable, history changed, or capacity exceeded")
		return
	}
	kept := false
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
		if !kept {
			s.cancel()
		}
	}()
	if err := b.respond(r.Context(), w, s, req.Stream); err == nil {
		s.mu.Lock()
		kept = s.waiting
		s.mu.Unlock()
	}
}

func canonical(v any) [32]byte {
	data, _ := json.Marshal(v)
	var normalized any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	_ = d.Decode(&normalized)
	data, _ = json.Marshal(normalized)
	return sha256.Sum256(data)
}

func sessionConfig(req messagesRequest) [32]byte {
	req.Messages, req.Stream, req.Metadata = nil, false, nil
	return canonical(req)
}

// normalizedToolInput equates omitted defaults with values declared by the tool schema.
// normalizedToolInput 将省略的默认值与工具 schema 明确声明的默认值视为等价。
func normalizedToolInput(name string, input json.RawMessage, tools []bridgeTool) json.RawMessage {
	var args map[string]json.RawMessage
	if json.Unmarshal(input, &args) != nil || args == nil {
		return input
	}
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		var schema struct {
			Properties map[string]struct {
				Default json.RawMessage `json:"default"`
			} `json:"properties"`
		}
		if json.Unmarshal(tool.InputSchema, &schema) != nil {
			return input
		}
		for key, property := range schema.Properties {
			if _, exists := args[key]; !exists && len(property.Default) > 0 {
				args[key] = property.Default
			}
		}
	}
	result, _ := json.Marshal(args)
	return result
}

func historyHash(messages []bridgeMessage, toolSets ...[]bridgeTool) [32]byte {
	var tools []bridgeTool
	if len(toolSets) > 0 {
		tools = toolSets[0]
	}
	normalized := make([]bridgeMessage, len(messages))
	for i, m := range messages {
		// Thinking is the CLI's own history: a client may echo it back or
		// not, and either must match the retained session.
		// 思考是 CLI 自己的历史：客户端回传与否都必须能匹配保留的会话。
		list, _ := blocks(m.Content)
		list = withoutThinking(list)
		for j := range list {
			if list[j].Type == "tool_use" {
				list[j].Input = normalizedToolInput(list[j].Name, list[j].Input, tools)
			}
		}
		content, _ := json.Marshal(list)
		normalized[i] = bridgeMessage{Role: m.Role, Content: content}
	}
	return canonical(normalized)
}

// historyHash matches Chat's separate text/tool fields without weakening native block ordering.
// historyHash 按 Chat 分离的文本和工具字段匹配历史，原生协议仍保留块顺序校验。
func (s *messagesSession) historyHash(messages []bridgeMessage) [32]byte {
	var metadata struct {
		Chat bool `json:"aisw_chat_completions"`
	}
	_ = json.Unmarshal(s.request.Metadata, &metadata)
	if metadata.Chat {
		messages = append([]bridgeMessage(nil), messages...)
		for i, message := range messages {
			if message.Role != "assistant" {
				continue
			}
			list, _ := blocks(message.Content)
			var text string
			var calls []messageBlock
			for _, block := range list {
				if block.Type == "text" {
					text += block.Text
				} else {
					calls = append(calls, block)
				}
			}
			if text != "" {
				calls = append([]messageBlock{{Type: "text", Text: text}}, calls...)
			}
			messages[i].Content, _ = json.Marshal(calls)
		}
	}
	return historyHash(messages, s.request.Tools)
}

// acquire atomically validates all tool results before advancing a session.
// acquire 原子校验全部工具结果后才推进会话。
func (b *messagesBridge) acquire(owner principal, req messagesRequest) (*messagesSession, error) {
	last, _ := blocks(req.Messages[len(req.Messages)-1].Content)
	results := make(map[string]toolResult)
	for _, block := range last {
		if block.Type == "tool_result" {
			if _, exists := results[block.ToolUseID]; exists {
				return nil, errMessages
			}
			text, _ := textBlocks(block.Content)
			results[block.ToolUseID] = toolResult{Text: text, IsError: block.IsError}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errClosed
	}
	if len(results) > 0 {
		held := false
		for s := range b.sessions {
			s.mu.Lock()
			for id := range results {
				_, resuming := s.resumed[id] // a live resume of the same round / 同一回合仍在进行的恢复
				held = held || s.pending[id] != nil || (resuming && s.ctx.Err() == nil)
			}
			matches := s.owner == owner && !s.busy && s.waiting && s.ctx.Err() == nil && sessionConfig(s.request) == sessionConfig(req) && s.historyHash(req.Messages[:len(req.Messages)-1]) == s.history && len(results) == len(s.pending) && len(last) == len(results)
			if matches {
				for id := range results {
					if s.pending[id] == nil || s.pending[id].delivered {
						matches = false
					}
				}
			}
			if matches {
				s.busy, s.waiting, s.messages = true, false, req.Messages
				for id, result := range results {
					s.pending[id].result <- result
					s.pending[id].delivered = true
				}
				b.consumeLocked(results)
				s.signalLocked()
				s.mu.Unlock()
				s.touch()
				return s, nil
			}
			s.mu.Unlock()
		}
		// A session that still holds an ID saw a mismatched request (or is
		// already resuming the same round), and an ID already delivered is a
		// replay. Otherwise the CLI that asked is gone (expired, Agent
		// restarted, or the Gateway lease lapsed), so the round resumes in a
		// new session that reads the results from the transcript; the tools
		// already ran on the caller's side and nothing runs twice. A resume
		// whose round failed delivered nothing, so its retry resumes again.
		//
		// 仍有会话持有某个 ID，说明请求与会话不匹配（或同一回合正在恢复）；ID 已投递过
		// 则是重放。否则说明发起调用的 CLI 已不存在（过期、Agent 重启或 Gateway 租约
		// 失效），此时在新会话中从文本记录读取这些结果继续该回合；工具已在调用方执行，
		// 不会重复执行。回合失败的恢复没有投递任何内容，其重试会再次恢复。
		if held || b.consumedLocked(results) {
			return nil, errCallRejected
		}
	}
	// Public CLI input cannot faithfully inject assistant history, so a new
	// session must end with a user message: a prompt, or the results that
	// resume an orphaned round. Earlier turns reach the CLI only as the
	// role-labelled transcript sessionPrompt renders.
	// 公开 CLI 输入无法保真注入 assistant 历史，因此新会话必须以一条用户消息结尾：
	// 提问，或恢复孤立回合的工具结果。此前的回合只以 sessionPrompt 渲染的带角色
	// 文本记录交给 CLI。
	if req.Messages[len(req.Messages)-1].Role != "user" {
		return nil, errMessages
	}
	for _, block := range last {
		if block.Type != "text" && block.Type != "tool_result" {
			return nil, errMessages
		}
	}
	if len(b.sessions) >= b.capacity {
		return nil, errCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &messagesSession{owner: owner, request: req, messages: req.Messages, ctx: ctx, cancel: cancel, done: make(chan struct{}), events: make(chan bridgeEvent), busy: true, pending: make(map[string]*pendingTool), changed: make(chan struct{}), activity: make(chan bool, 1)}
	if len(results) > 0 {
		s.resumed = results // consumed when the round completes / 回合完成时才记为已用
	}
	b.sessions[s] = true
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer close(s.done)
		defer func() { b.mu.Lock(); delete(b.sessions, s); b.mu.Unlock() }()
		timerDone := make(chan struct{})
		go func() { defer close(timerDone); b.expire(s) }()
		b.start(s)
		s.cancel()
		<-timerDone
	}()
	return s, nil
}

// commitResumed marks the tool results s resumed from as consumed once a round
// has completed, so a later replay of them is refused. It locks b.mu before
// s.mu, the order acquire uses, so no acquire sees the IDs neither held nor
// consumed.
//
// commitResumed 在回合完成后把 s 恢复所依据的工具结果记为已消费，之后重放这批结果会被
// 拒绝。它先锁 b.mu 再锁 s.mu，与 acquire 的顺序一致，因此任何 acquire 都不会看到这些
// ID 既未被持有也未被消费。
func (b *messagesBridge) commitResumed(s *messagesSession) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s.mu.Lock()
	resumed := s.resumed
	s.resumed = nil
	s.mu.Unlock()
	if len(resumed) > 0 {
		b.consumeLocked(resumed)
	}
}

func (s *messagesSession) signalLocked() { close(s.changed); s.changed = make(chan struct{}) }

// touch asks expire to restart the phase timer without ever blocking: a signal
// already buffered covers this one, and once expire has returned nobody reads.
//
// touch 请求 expire 重启阶段计时器，且永不阻塞：缓冲中已有未读信号时本次可以丢弃，
// expire 返回后不再有读方，阻塞发送会让调用方永久挂起。
func (s *messagesSession) touch() {
	select {
	case s.activity <- true:
	default:
	}
}

// Session timers. A caller may take as long as its tools need, up to the
// Gateway's continuation TTL, while a CLI that stops producing output is
// reclaimed quickly so it does not hold a capacity slot.
//
// 会话计时器。调用方执行工具可以花费所需时间，上限与 Gateway 续接 TTL 一致；
// CLI 停止输出时则尽快回收，避免占住会话名额。
const (
	// sessionLife bounds a whole session, including every tool wait. It is
	// longer than toolWaitTimeout so an active tool chain is not cut short;
	// abandoned rounds are reclaimed by toolWaitTimeout instead.
	//
	// sessionLife 限定整个会话的寿命，包含所有工具等待。它长于 toolWaitTimeout，
	// 避免中途截断仍在进行的工具链；被放弃的回合由 toolWaitTimeout 回收。
	sessionLife = 24 * time.Hour
	// toolWaitTimeout bounds one wait for the caller's tool results.
	// toolWaitTimeout 限定一次等待调用方工具结果的时长。
	toolWaitTimeout = time.Hour
	// generationTimeout bounds how long a CLI generation phase may go without
	// output; every line the CLI writes restarts it (see bridgeEvents), so a
	// long but active generation is never cut short.
	//
	// generationTimeout 限定 CLI 生成阶段无输出的时长；CLI 每写出一行都会重新计时（见
	// bridgeEvents），因此耗时长但持续输出的生成不会被截断。
	generationTimeout = 2 * time.Minute
)

// expire cancels s when its life or current phase runs out. Each activity
// signal restarts the phase timer with the limit for s.waiting at that moment,
// so a dropped duplicate signal still yields the latest phase.
//
// expire 在会话寿命或当前阶段时限耗尽时取消 s。每次活动信号按当时的 s.waiting
// 重启阶段计时器，因此被丢弃的重复信号不影响取到最新阶段。
func (b *messagesBridge) expire(s *messagesSession) {
	life, stopLife := b.clock.NewTimer(sessionLife)
	defer stopLife()
	phase, stopPhase := b.clock.NewTimer(generationTimeout)
	defer func() { stopPhase() }()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-life:
			s.cancel()
			return
		case <-phase:
			s.cancel()
			return
		case <-s.activity:
			stopPhase()
			s.mu.Lock()
			limit := generationTimeout
			if s.waiting {
				limit = toolWaitTimeout
			}
			s.mu.Unlock()
			phase, stopPhase = b.clock.NewTimer(limit)
		}
	}
}

func (s *messagesSession) send(data json.RawMessage, err error) error {
	select {
	case s.events <- bridgeEvent{data: data, err: err}:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (b *messagesBridge) respond(ctx context.Context, w http.ResponseWriter, s *messagesSession, stream bool) (resultErr error) {
	response := map[string]any{}
	var content []messageBlock
	total := 0
	started := false
	defer func() {
		if resultErr == nil || ctx.Err() != nil {
			return
		}
		if !started {
			messagesError(w, 502, "CLI bridge failed; output withheld")
			return
		}
		_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"CLI bridge failed; output withheld\"}}\n\n")
		_ = http.NewResponseController(w).Flush()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ctx.Done():
			return s.ctx.Err()
		case item := <-s.events:
			if item.err != nil {
				return item.err
			}
			var ev struct {
				Type    string         `json:"type"`
				Message map[string]any `json:"message"`
				Index   int            `json:"index"`
				Block   messageBlock   `json:"content_block"`
				Delta   struct {
					Type         string `json:"type"`
					Text         string `json:"text"`
					Thinking     string `json:"thinking"`
					Signature    string `json:"signature"`
					PartialJSON  string `json:"partial_json"`
					StopReason   string `json:"stop_reason"`
					StopSequence string `json:"stop_sequence"`
				} `json:"delta"`
				Usage json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(item.data, &ev) != nil {
				return errCLIProtocol
			}
			total += len(item.data)
			if total > maxRequestBytes {
				return errCapacity
			}
			switch ev.Type {
			case "message_start":
				response = ev.Message
				response["model"] = s.request.Model
			case "content_block_start":
				if ev.Index != len(content) {
					return errCLIProtocol
				}
				content = append(content, ev.Block)
			case "content_block_delta":
				if ev.Index < 0 || ev.Index >= len(content) {
					return errCLIProtocol
				}
				switch ev.Delta.Type {
				case "text_delta":
					content[ev.Index].Text += ev.Delta.Text
				case "thinking_delta":
					content[ev.Index].Thinking += ev.Delta.Thinking
				case "signature_delta":
					content[ev.Index].Signature += ev.Delta.Signature
				}
				if ev.Delta.Type == "input_json_delta" {
					if string(content[ev.Index].Input) == "{}" {
						content[ev.Index].Input = nil
					}
					content[ev.Index].Input = append(content[ev.Index].Input, ev.Delta.PartialJSON...)
				}
			case "message_delta":
				response["stop_reason"] = ev.Delta.StopReason
				if ev.Delta.StopSequence != "" {
					response["stop_sequence"] = ev.Delta.StopSequence
				}
				if len(ev.Usage) > 0 {
					var usage map[string]any
					_ = json.Unmarshal(ev.Usage, &usage)
					prior, _ := response["usage"].(map[string]any)
					if prior == nil {
						return errCLIProtocol
					}
					for k, v := range usage {
						prior[k] = v
					}
					response["usage"] = prior
				}
			case "message_stop":
				waiting := response["stop_reason"] == "tool_use"
				s.mu.Lock()
				s.waiting = waiting
				if waiting {
					if len(s.pending) == 0 {
						s.mu.Unlock()
						return errCLIProtocol
					}
					body, _ := json.Marshal(content)
					history := append(append([]bridgeMessage(nil), s.messages...), bridgeMessage{Role: "assistant", Content: body})
					s.history = s.historyHash(history)
				}
				s.mu.Unlock()
				if waiting {
					s.touch()
				}
				response["content"] = content
			}
			if stream {
				if !started {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Cache-Control", "no-cache")
					started = true
				}
				data := item.data
				if ev.Type == "message_start" {
					var obj map[string]any
					_ = json.Unmarshal(data, &obj)
					obj["message"] = response
					data, _ = json.Marshal(obj)
				}
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data); err != nil {
					return err
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					return err
				}
			}
			if ev.Type == "message_stop" {
				b.commitResumed(s)
				if !stream {
					w.Header().Set("Content-Type", "application/json")
					return json.NewEncoder(w).Encode(response)
				}
				return nil
			}
		}
	}
}

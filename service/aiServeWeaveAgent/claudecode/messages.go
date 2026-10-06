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
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

const maxRequestBytes = 8 << 20

var errMessages = errors.New("invalid or unsupported Messages request")

type messageBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
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

func (r messagesRequest) validate(model string) error {
	if r.Model != model || r.MaxTokens < 1 || r.MaxTokens > 64000 || len(r.Messages) == 0 || len(r.Messages) > 2048 || len(r.Tools) > 128 {
		return errMessages
	}
	if _, err := textBlocks(r.System); err != nil {
		return err
	}
	if len(r.Thinking) > 0 {
		var thinking struct {
			Type string `json:"type"`
		}
		if decodeStrict(r.Thinking, &thinking) != nil || thinking.Type != "disabled" {
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
			switch b.Type {
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
	// start is replaced by offline fixtures, never by HTTP input.
	// start 仅由离线测试替换，从不取自 HTTP 输入。
	start func(*messagesSession)
}

func newMessagesBridge(program, model string, keys map[string]principal, clock runtime.Clock) *messagesBridge {
	b := &messagesBridge{program: program, model: model, keys: keys, clock: clock, sessions: make(map[*messagesSession]bool), capacity: 2}
	b.start = b.runSession
	return b
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
		messagesError(w, 400, "unsupported Messages shape or extension: adaptive thinking, prompt caching and output formats are not supported")
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
		list, _ := blocks(m.Content)
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
		for s := range b.sessions {
			s.mu.Lock()
			matches := s.owner == owner && !s.busy && s.waiting && s.ctx.Err() == nil && sessionConfig(s.request) == sessionConfig(req) && historyHash(req.Messages[:len(req.Messages)-1], s.request.Tools) == s.history && len(results) == len(s.pending) && len(last) == len(results)
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
				s.signalLocked()
				s.mu.Unlock()
				s.activity <- false
				return s, nil
			}
			s.mu.Unlock()
		}
		return nil, errCallRejected
	}
	// Public CLI input cannot faithfully inject arbitrary assistant history.
	// 公开 CLI 输入无法保真注入任意 assistant 历史。
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		return nil, errMessages
	}
	for _, block := range last {
		if block.Type != "text" {
			return nil, errMessages
		}
	}
	if len(b.sessions) >= b.capacity {
		return nil, errCapacity
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &messagesSession{owner: owner, request: req, messages: req.Messages, ctx: ctx, cancel: cancel, done: make(chan struct{}), events: make(chan bridgeEvent), busy: true, pending: make(map[string]*pendingTool), changed: make(chan struct{}), activity: make(chan bool, 1)}
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

func (s *messagesSession) signalLocked() { close(s.changed); s.changed = make(chan struct{}) }

func (b *messagesBridge) expire(s *messagesSession) {
	life, stopLife := b.clock.NewTimer(10 * time.Minute)
	defer stopLife()
	phase, stopPhase := b.clock.NewTimer(2 * time.Minute)
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
			phase, stopPhase = b.clock.NewTimer(2 * time.Minute)
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
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
					StopReason  string `json:"stop_reason"`
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
				if ev.Delta.Type == "text_delta" {
					content[ev.Index].Text += ev.Delta.Text
				}
				if ev.Delta.Type == "input_json_delta" {
					if string(content[ev.Index].Input) == "{}" {
						content[ev.Index].Input = nil
					}
					content[ev.Index].Input = append(content[ev.Index].Input, ev.Delta.PartialJSON...)
				}
			case "message_delta":
				response["stop_reason"] = ev.Delta.StopReason
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
					s.history = historyHash(history, s.request.Tools)
				}
				s.mu.Unlock()
				if waiting {
					s.activity <- true
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
				if !stream {
					w.Header().Set("Content-Type", "application/json")
					return json.NewEncoder(w).Encode(response)
				}
				return nil
			}
		}
	}
}

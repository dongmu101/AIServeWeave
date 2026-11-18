package codexcli

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliphase"
)

// maxFrameBytes bounds one frame app-server sends, and the reply and reasoning
// summary of one turn together; only a finished image may exceed it.
// maxFrameBytes 限制 app-server 发来的单帧，以及一个回合的回复与思考摘要合计；只有
// 已完成的图像可以超过它。
const maxFrameBytes = 1 << 20

// maxHistoryBytes bounds the history injected into one thread at the 32 MiB a
// Claude Messages request may carry. app-server itself accepts far larger
// frames (a 17 MB thread/inject_items on codex-cli 0.169.0), so whether a
// history is too long is the model's context window to decide, reported as
// contextWindowExceeded, not this adapter's.
//
// maxHistoryBytes 以 Claude Messages 请求可携带的 32 MiB 限制注入单个线程的历史。
// app-server 本身接受大得多的帧（codex-cli 0.169.0 上 17 MB 的 thread/inject_items），
// 因此历史是否过长由模型上下文窗口判断并以 contextWindowExceeded 报告，而不是由本适配器判断。
const maxHistoryBytes = 32 << 20

// maxWriteFrameBytes bounds one request this adapter writes: the injected
// history or thread/start's tool definitions, plus the JSON-RPC envelope.
// maxWriteFrameBytes 限制本适配器写出的单个请求：注入的历史或 thread/start 的工具定义，
// 再加上 JSON-RPC 外层。
const maxWriteFrameBytes = maxHistoryBytes + 1<<20

type frame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type protocol struct {
	scanner  *bufio.Scanner
	output   io.Writer
	report   Report
	activity func()
	// image admits imageGeneration items, and only their completion frame may
	// exceed maxFrameBytes, up to maxImageFrameBytes.
	// image 放行 imageGeneration 条目，且只有其完成帧可以超过 maxFrameBytes，
	// 上限为 maxImageFrameBytes。
	image bool
	// pending is the method of the request awaiting its reply, so a timeout
	// can say which step stalled.
	// pending 是正在等待应答的请求方法，超时时据此说明卡在哪一步。
	pending string
	// phases, when not nil, records when each start-up reply arrived (see replyPhase).
	// phases 不为 nil 时记录各启动应答到达的时刻（见 replyPhase）。
	phases *cliphase.Timeline
}

// replyPhase names the phase a successful reply to method completes.
// replyPhase 给出 method 的成功应答所完成的阶段。
var replyPhase = map[string]string{
	"initialize":          cliphase.CLIReady,
	"account/read":        cliphase.LoginChecked,
	"thread/start":        cliphase.ThreadStarted,
	"thread/inject_items": cliphase.HistoryInjected,
}

func (p *protocol) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil || len(data) > maxWriteFrameBytes {
		return ErrProtocol
	}
	data = append(data, '\n')
	n, err := p.output.Write(data)
	if err != nil || n != len(data) {
		return ErrProtocol
	}
	return nil
}

// read returns the next frame. Nothing caps how many frames a turn may send:
// each reply token arrives as its own delta, so a frame count would cut long
// answers short, and the request and idle deadlines already bound a process
// that never stops. Every frame except a retry notice counts as activity for
// the idle deadline (see retryNotice).
//
// read 返回下一帧。一个回合可以发送多少帧不设上限：回复的每个 token 都单独作为一个增量
// 到达，按帧数设限会截断长回答，而请求期限与空闲期限已经约束了不停输出的进程。除重试通知
// 外的每一帧都计入空闲期限的活动（见 retryNotice）。
func (p *protocol) read() (frame, error) {
	if !p.scanner.Scan() {
		return frame{}, ErrProtocol
	}
	p.report.Events++
	data := p.scanner.Bytes()
	limit := maxFrameBytes
	if p.image {
		limit = maxImageFrameBytes
	}
	var msg frame
	if len(data) > limit || json.Unmarshal(data, &msg) != nil || (msg.Method == "" && len(msg.ID) == 0) {
		return frame{}, ErrProtocol
	}
	itemType := ""
	if msg.Method == "item/started" || msg.Method == "item/completed" {
		var n struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(msg.Params, &n) != nil {
			return frame{}, ErrProtocol
		}
		itemType = n.Item.Type
		switch {
		// Only items that run nothing on this machine are admitted;
		// functionCallOutput records a tool result and executes nothing.
		// 只放行不在本机执行任何操作的条目；functionCallOutput 记录工具结果，不执行任何操作。
		case itemType == "userMessage", itemType == "agentMessage", itemType == "reasoning", itemType == "plan",
			itemType == "dynamicToolCall", itemType == "contextCompaction", itemType == "functionCallOutput":
		case itemType == "imageGeneration" && p.image:
		default:
			return frame{}, ErrUnexpectedTool
		}
	}
	// Only a finished image may be large; every other frame keeps the text bound.
	// 只有已完成的图像可以很大，其余所有帧仍受文本上限约束。
	if len(data) > maxFrameBytes && (msg.Method != "item/completed" || itemType != "imageGeneration") {
		return frame{}, ErrProtocol
	}
	if p.activity != nil && !retryNotice(msg) {
		p.activity()
	}
	return msg, nil
}

// retryNotice reports whether msg is an "error" notification app-server sends
// while it retries the upstream itself (willRetry). Such notices are not
// progress: a reconnect loop that keeps announcing itself must still reach
// the idle deadline, so the Gateway can try another node instead of waiting
// for the request deadline.
//
// retryNotice 报告 msg 是否为 app-server 自行重试上游期间发出的 "error" 通知（willRetry）。
// 这类通知不算进展：不断自我报告的重连循环仍必须到达空闲期限，使 Gateway 能换节点，
// 而不是一直等到请求期限。
func retryNotice(msg frame) bool {
	if msg.Method != "error" {
		return false
	}
	var n struct {
		WillRetry bool `json:"willRetry"`
	}
	return json.Unmarshal(msg.Params, &n) == nil && n.WillRetry
}

func (p *protocol) request(id int, method string, params any, result any) error {
	p.pending = method
	if err := p.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		msg, err := p.read()
		if err != nil {
			return err
		}
		if msg.Method != "" {
			if len(msg.ID) > 0 {
				return ErrUnexpectedTool
			}
			continue
		}
		var got int
		if json.Unmarshal(msg.ID, &got) != nil || got != id {
			return ErrProtocol
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return ErrRPC
		}
		if len(msg.Result) == 0 || string(msg.Result) == "null" || json.Unmarshal(msg.Result, result) != nil {
			return ErrProtocol
		}
		p.pending = ""
		if phase, ok := replyPhase[method]; ok {
			p.phases.Mark(phase)
		}
		return nil
	}
}

func runProtocol(input io.Reader, output io.Writer, dir, model string, allowedInstructions []string) (Report, error) {
	p := &protocol{scanner: bufio.NewScanner(input), output: output}
	p.scanner.Buffer(make([]byte, 4096), maxFrameBytes+1)
	err := p.run(dir, model, allowedInstructions)
	return p.report, err
}

func (p *protocol) run(dir, model string, allowedInstructions []string) error {
	p.report.Stage = "initialize"
	if err := p.request(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "aisw-codex-probe", "version": "0.1.0"}, "capabilities": map[string]bool{"experimentalApi": true}}, new(map[string]any)); err != nil {
		return err
	}
	if err := p.write(map[string]string{"method": "initialized"}); err != nil {
		return err
	}
	var account struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	p.report.Stage = "account/read"
	if err := p.request(2, "account/read", map[string]bool{"refreshToken": false}, &account); err != nil {
		return err
	}
	if account.Account == nil || account.Account.Type != "chatgpt" {
		return ErrAuthentication
	}
	tool := map[string]any{
		"type": "function", "name": "aisw_probe_echo", "description": "Return a synthetic token from the client.",
		"inputSchema": map[string]any{
			"type": "object", "properties": map[string]any{"label": map[string]string{"type": "string"}},
			"required": []string{"label"}, "additionalProperties": false,
		},
	}
	params := map[string]any{
		"cwd": dir, "ephemeral": true, "sandbox": "read-only", "approvalPolicy": "never",
		"baseInstructions":      "You are a synthetic protocol test assistant. Only call the supplied aisw_probe_echo tool.",
		"developerInstructions": "Do not read files, execute commands, or call other tools.",
		"dynamicTools":          []any{tool},
	}
	if model != "" {
		params["model"] = model
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model              string   `json:"model"`
		InstructionSources []string `json:"instructionSources"`
	}
	p.report.Stage = "thread/start"
	if err := p.request(3, "thread/start", params, &started); err != nil {
		return err
	}
	p.report.Stage = "thread/validate"
	p.report.InstructionFiles = len(started.InstructionSources)
	if started.Thread.ID == "" || started.Model == "" || len(started.Model) > 256 {
		return ErrProtocol
	}
	p.report.Model = started.Model
	for _, source := range started.InstructionSources {
		allowed := false
		for _, expected := range allowedInstructions {
			if filepath.Clean(source) == expected {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrIsolation
		}
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	p.report.Stage = "turn/start"
	prompt := "Call aisw_probe_echo with label first. After receiving its result, call aisw_probe_echo with label second. Then output the second tool result verbatim and stop."
	if err := p.request(4, "turn/start", map[string]any{"threadId": started.Thread.ID, "input": []any{map[string]string{"type": "text", "text": prompt}}, "effort": "low"}, &turn); err != nil {
		return err
	}
	if turn.Turn.ID == "" {
		return ErrProtocol
	}
	var tokenBytes [16]byte
	rand.Read(tokenBytes[:])
	token := hex.EncodeToString(tokenBytes[:])
	seen := map[string]bool{}
	p.report.Stage = "tools"
	for {
		msg, err := p.read()
		if err != nil {
			return err
		}
		if len(msg.ID) > 0 {
			if msg.Method != "item/tool/call" {
				return ErrUnexpectedTool
			}
			if p.report.ToolCalls >= 2 || !validRequestID(msg.ID) {
				return ErrProtocol
			}
			var call struct {
				ThreadID  string          `json:"threadId"`
				TurnID    string          `json:"turnId"`
				CallID    string          `json:"callId"`
				Tool      string          `json:"tool"`
				Namespace *string         `json:"namespace"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(msg.Params, &call) != nil || call.ThreadID != started.Thread.ID || call.TurnID != turn.Turn.ID || call.Tool != "aisw_probe_echo" || call.Namespace != nil || call.CallID == "" || seen[call.CallID] {
				return ErrProtocol
			}
			var args struct {
				Label string `json:"label"`
			}
			dec := json.NewDecoder(bytes.NewReader(call.Arguments))
			dec.DisallowUnknownFields()
			want := "first"
			result := "first call acknowledged"
			if p.report.ToolCalls == 1 {
				want = "second"
				result = token
			}
			if dec.Decode(&args) != nil || dec.Decode(new(any)) != io.EOF || args.Label != want {
				return ErrProtocol
			}
			seen[call.CallID] = true
			p.report.ToolCalls++
			if err := p.write(map[string]any{"id": msg.ID, "result": map[string]any{"contentItems": []any{map[string]string{"type": "inputText", "text": result}}, "success": true}}); err != nil {
				return err
			}
			continue
		}
		switch msg.Method {
		case "item/completed":
			var item struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
				Item     struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"item"`
			}
			if json.Unmarshal(msg.Params, &item) != nil || item.ThreadID != started.Thread.ID || item.TurnID != turn.Turn.ID {
				return ErrProtocol
			}
			if item.Item.Type == "agentMessage" && strings.Contains(item.Item.Text, token) {
				p.report.ClientTokenSeen = true
			}
		case "turn/completed":
			var done struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID     string          `json:"id"`
					Status string          `json:"status"`
					Error  json.RawMessage `json:"error"`
				} `json:"turn"`
			}
			if json.Unmarshal(msg.Params, &done) != nil || done.ThreadID != started.Thread.ID || done.Turn.ID != turn.Turn.ID {
				return ErrProtocol
			}
			switch done.Turn.Status {
			case "completed", "failed", "interrupted":
				p.report.TurnStatus = done.Turn.Status
			default:
				return ErrProtocol
			}
			p.report.FailureCode = failureCode(done.Turn.Error)
			if done.Turn.Status != "completed" || (len(done.Turn.Error) > 0 && string(done.Turn.Error) != "null") || p.report.ToolCalls != 2 || !p.report.ClientTokenSeen {
				return ErrTurn
			}
			p.report.Passed = true
			p.report.Stage = "completed"
			return nil
		}
	}
}

func failureCode(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var data struct {
		Info json.RawMessage `json:"codexErrorInfo"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return "other"
	}
	var name string
	if json.Unmarshal(data.Info, &name) == nil {
		switch name {
		case "contextWindowExceeded", "sessionBudgetExceeded", "usageLimitExceeded", "rateLimitExceeded", "flexUnavailable", "serverOverloaded", "cyberPolicy", "misalignmentPolicyViolation", "tooManyDenials", "internalServerError", "unauthorized", "badRequest", "threadRollbackFailed", "sandboxError":
			return name
		}
	}
	var variants map[string]json.RawMessage
	if json.Unmarshal(data.Info, &variants) == nil {
		for _, key := range []string{"httpConnectionFailed", "responseStreamConnectionFailed", "responseStreamDisconnected", "responseTooManyFailedAttempts", "activeTurnNotSteerable"} {
			if _, ok := variants[key]; ok {
				return key
			}
		}
	}
	return "other"
}

func validRequestID(raw json.RawMessage) bool {
	if len(raw) > 128 {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != ""
	}
	var number int64
	return json.Unmarshal(raw, &number) == nil && string(raw) != "null"
}

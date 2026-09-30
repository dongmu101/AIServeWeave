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
)

const maxFrameBytes = 1 << 20

type frame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type protocol struct {
	scanner *bufio.Scanner
	output  io.Writer
	report  Report
}

func (p *protocol) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil || len(data) > maxFrameBytes {
		return ErrProtocol
	}
	data = append(data, '\n')
	n, err := p.output.Write(data)
	if err != nil || n != len(data) {
		return ErrProtocol
	}
	return nil
}

func (p *protocol) read() (frame, error) {
	if p.report.Events >= 10000 || !p.scanner.Scan() {
		return frame{}, ErrProtocol
	}
	p.report.Events++
	data := p.scanner.Bytes()
	var msg frame
	if len(data) > maxFrameBytes || json.Unmarshal(data, &msg) != nil || (msg.Method == "" && len(msg.ID) == 0) {
		return frame{}, ErrProtocol
	}
	if msg.Method == "item/started" || msg.Method == "item/completed" {
		var n struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(msg.Params, &n) != nil {
			return frame{}, ErrProtocol
		}
		switch n.Item.Type {
		case "userMessage", "agentMessage", "reasoning", "plan", "dynamicToolCall", "contextCompaction":
		default:
			return frame{}, ErrUnexpectedTool
		}
	}
	return msg, nil
}

func (p *protocol) request(id int, method string, params any, result any) error {
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

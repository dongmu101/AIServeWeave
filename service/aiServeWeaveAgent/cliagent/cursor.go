package cliagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"AIServeWeave/common/runtime"
)

// cursorPermissions denies every Cursor tool in the isolated workspace.
// cursorPermissions 在隔离工作区内拒绝 Cursor 的全部工具。
const cursorPermissions = `{"permissions":{"allow":[],"deny":["Shell(*)","Read(*)","Write(*)","WebFetch(*)"]}}`

func init() {
	register(&cliSpec{
		Kind:  runtime.KindCursor,
		Label: "Cursor",
		// cursor-agent first: "agent" is a generic name that an unrelated
		// program on PATH may also use.
		// cursor-agent 优先："agent" 是通用名称，PATH 上的无关程序也可能使用它。
		Executables: []string{"cursor-agent", "agent"},
		HomeBin:     ".local/bin",
		PassEnv:     []string{"CURSOR_API_KEY"},
		SignIn:      []string{"not logged in", "not authenticated", "authentication required", "please log in", "please login", "unauthorized"},
		Setup: func(dir string, env, _ []string) ([]string, error) {
			if err := os.Mkdir(filepath.Join(dir, ".cursor"), 0o700); err != nil {
				return nil, err
			}
			return env, os.WriteFile(filepath.Join(dir, ".cursor", "cli.json"), []byte(cursorPermissions), 0o600)
		},
		Discover: cursorDiscover,
		// dbx also passes --approve-mcps; it is left out so no MCP server is
		// ever approved.
		// dbx 还会传 --approve-mcps；这里不传，任何 MCP 服务都不会被批准。
		Turn: func(dir, _, prompt, model string) ([]string, []byte) {
			return []string{"-p", "--output-format", "stream-json", "--stream-partial-output", "--trust", "--workspace", dir, "--model", model}, []byte(prompt)
		},
		Parser: func() parser { return &cursorParser{} },
	})
}

// cursorDiscover parses `--list-models`, whose model lines look like
// "gpt-5 - GPT-5 (current)".
// cursorDiscover 解析 `--list-models`，模型行形如 "gpt-5 - GPT-5 (current)"。
func cursorDiscover(c *call) ([]string, error) {
	var ids []string
	total, signedOut := 0, false
	diag, err := c.run([]string{"--list-models"}, nil, func(line []byte) (bool, error) {
		if total += len(line); total > maxListBytes {
			return false, ErrProtocol
		}
		signedOut = signedOut || c.signedOut(line)
		text := strings.TrimSpace(ansi.ReplaceAllString(string(line), ""))
		id, _, found := strings.Cut(text, " - ")
		if !found || strings.HasPrefix(strings.ToLower(text), "available models") {
			return false, nil
		}
		if id = strings.TrimSpace(strings.TrimSuffix(id, " (current)")); id != "" && len(ids) < maxRawModels {
			ids = append(ids, id)
		}
		return false, nil
	})
	// An empty listing after a clean exit may be a sign-in message on stderr.
	// 正常退出却列表为空，可能是 stderr 上的未登录提示。
	if signedOut || ((errors.Is(err, errExit) || (err == nil && len(ids) == 0)) && c.signedOut(diag)) {
		return nil, ErrAuthentication
	}
	if errors.Is(err, errExit) {
		return nil, ErrRPC
	}
	return ids, err
}

// cursorParser reads Cursor's stream-json. With --stream-partial-output only
// assistant events carrying a timestamp are deltas; the untimestamped one
// repeats the whole message and is skipped.
//
// cursorParser 读取 Cursor 的 stream-json。使用 --stream-partial-output 时，
// 只有带时间戳的 assistant 事件是增量；不带时间戳的那条重复整条消息，被跳过。
type cursorParser struct {
	emitted bool
	usage   *runtime.Usage
	done    bool
}

func (p *cursorParser) line(event []byte, text func(string) error) (bool, error) {
	obj := object(event)
	switch str(obj, "type") {
	case "assistant":
		var message struct {
			Content []map[string]json.RawMessage `json:"content"`
		}
		if raw := obj["message"]; raw != nil && json.Unmarshal(raw, &message) != nil {
			return false, ErrProtocol
		}
		delta := lookup(obj, "timestamp_ms", "timestampMs") != nil
		for _, block := range message.Content {
			switch str(block, "type") {
			case "tool_use", "tool_call":
				return false, ErrUnexpectedTool
			case "text":
				if !delta {
					continue
				}
				s := str(block, "text")
				p.emitted = p.emitted || s != ""
				if err := text(s); err != nil {
					return false, err
				}
			}
		}
		return false, nil
	case "tool_call":
		return false, ErrUnexpectedTool
	case "result":
		var isError bool
		_ = json.Unmarshal(obj["is_error"], &isError)
		if str(obj, "subtype") != "success" || isError {
			return false, ErrTurn
		}
		if !p.emitted {
			if err := text(str(obj, "result")); err != nil {
				return false, err
			}
		}
		var err error
		p.usage, err = usageFrom(object(obj["usage"]),
			[][]string{{"inputTokens", "input_tokens"}, {"cacheReadTokens", "cache_read_input_tokens"}, {"cacheWriteTokens", "cache_creation_input_tokens"}},
			[][]string{{"outputTokens", "output_tokens"}})
		p.done = err == nil
		return p.done, err
	case "error":
		return false, ErrTurn
	}
	// system, user, thinking and future types carry no reply text.
	// system、user、thinking 及未来新增类型都不含回复文本。
	return false, nil
}

func (p *cursorParser) finish() (*runtime.Usage, error) {
	if !p.done {
		return nil, ErrProtocol
	}
	return p.usage, nil
}

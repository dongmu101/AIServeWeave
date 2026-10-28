package cliagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"AIServeWeave/common/runtime"
)

const (
	grokRPCID  = "aisw_model_discovery"
	grokConfig = "[cli]\nauto_update = false\nuse_leader = false\n"
)

// grokDisallowedTools lists every built-in Grok tool; the isolated config also
// declares no MCP server, so the model is offered no tool at all.
// grokDisallowedTools 列出全部 Grok 内建工具；隔离配置同样不声明任何 MCP 服务，
// 因此模型拿不到任何工具。
const grokDisallowedTools = "run_terminal_command,read_file,search_replace,list_dir,grep,kill_command_or_subagent,todo_write," +
	"get_command_or_subagent_output,spawn_subagent,scheduler_create,scheduler_delete,scheduler_list,monitor,search_tool,use_tool," +
	"workflow,enter_plan_mode,exit_plan_mode,ask_user_question,image_gen,image_edit,image_to_video,reference_to_video,write,bash,shell,edit,Glob,Grep"

func init() {
	register(&cliSpec{
		Kind:        runtime.KindGrok,
		Label:       "Grok",
		Executables: []string{"grok"},
		PromptFile:  true,
		HomeBin:     ".grok/bin",
		PassEnv:     []string{"XAI_API_KEY"},
		SignIn:      []string{"not signed in", "not authenticated", "not logged", "please login", "login required", "authentication required"},
		Setup:       grokSetup,
		Discover:    grokDiscover,
		// No --always-approve and no --allow rules: with every built-in tool
		// disallowed and no MCP server configured, nothing can be approved.
		// 不传 --always-approve 与 --allow 规则：全部内建工具被禁用且未配置
		// MCP 服务，没有任何东西可被批准。
		Turn: func(_, promptFile, _, model string) ([]string, []byte) {
			return []string{"--no-leader", "--prompt-file", promptFile, "--output-format", "streaming-json",
				"--disable-web-search", "--no-subagents", "--no-plan", "--disallowed-tools", grokDisallowedTools, "--verbatim", "--model", model}, nil
		},
		Parser: func() parser { return &grokParser{} },
	})
}

// grokSetup points HOME and GROK_HOME at the isolated directory while reusing
// the operator's sign-in file, so the user's own Grok config, MCP servers and
// project rules never load.
//
// grokSetup 把 HOME 与 GROK_HOME 指向隔离目录，同时复用运维账户的登录文件，
// 使用户自己的 Grok 配置、MCP 服务与项目规则都不会被加载。
func grokSetup(dir string, env, source []string) ([]string, error) {
	grokHome := filepath.Join(dir, ".grok")
	if err := os.Mkdir(grokHome, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(grokHome, "config.toml"), []byte(grokConfig), 0o600); err != nil {
		return nil, err
	}
	auth := envValue(source, "GROK_AUTH_PATH")
	if auth == "" {
		if home := envValue(source, "GROK_HOME"); home != "" {
			auth = filepath.Join(home, "auth.json")
		} else if home := envValue(source, "HOME"); home != "" {
			auth = filepath.Join(home, ".grok", "auth.json")
		}
	}
	if filepath.IsAbs(auth) {
		env = setEnv(env, "GROK_AUTH_PATH", auth)
	}
	return setEnv(setEnv(env, "HOME", dir), "GROK_HOME", grokHome), nil
}

// grokDiscover reads the catalogue from the ACP initialize response, falling
// back to the human-readable `grok models` listing. Neither path runs
// inference; there is no static fallback list.
//
// grokDiscover 从 ACP initialize 响应读取模型目录，失败时退回人类可读的
// `grok models` 列表。两条路径都不执行推理，不使用静态候选列表兜底。
func grokDiscover(c *call) ([]string, error) {
	ids, err := grokDiscoverACP(c)
	if err == nil || c.ctx.Err() != nil || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrProcess) {
		return ids, err
	}
	return grokDiscoverListing(c)
}

func grokDiscoverACP(c *call) ([]string, error) {
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": grokRPCID, "method": "initialize", "params": map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "AIServeWeave", "version": "1"},
	}})
	if err != nil {
		return nil, ErrProtocol
	}
	var ids []string
	answered := false
	diag, err := c.rpc([]string{"agent", "--no-leader", "stdio"}, append(request, '\n'), func(line []byte) (bool, error) {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &frame) != nil {
			return false, ErrProtocol
		}
		var id string
		if json.Unmarshal(frame.ID, &id) != nil || id != grokRPCID {
			return false, nil // notification or unrelated frame / 通知或无关帧
		}
		answered = true
		if len(frame.Error) > 0 && string(frame.Error) != "null" {
			if c.signedOut(frame.Error) {
				return true, ErrAuthentication
			}
			return true, ErrRPC
		}
		var parseErr error
		ids, parseErr = grokACPModels(frame.Result)
		return true, parseErr
	})
	if errors.Is(err, errExit) && c.signedOut(diag) {
		return nil, ErrAuthentication
	}
	if err == nil && !answered {
		if c.signedOut(diag) {
			return nil, ErrAuthentication
		}
		return nil, ErrProtocol
	}
	return ids, err
}

// grokACPModels reads modelState from result._meta, result.meta or result
// itself, accepting the compatibility spellings dbx observed in the wild.
// grokACPModels 从 result._meta、result.meta 或 result 本身读取 modelState，
// 接受 dbx 实际观察到的兼容写法。
func grokACPModels(result json.RawMessage) ([]string, error) {
	top := object(result)
	if top == nil {
		return nil, ErrProtocol
	}
	var state json.RawMessage
	for _, metaKey := range []string{"_meta", "meta"} {
		if meta := object(lookup(top, metaKey)); meta != nil {
			if state = lookup(meta, "modelState", "model_state"); state != nil {
				break
			}
		}
	}
	if state == nil {
		state = lookup(top, "modelState", "model_state")
	}
	stateObj := object(state)
	if stateObj == nil {
		return nil, ErrProtocol
	}
	var available []map[string]json.RawMessage
	if json.Unmarshal(lookup(stateObj, "availableModels", "available_models"), &available) != nil {
		return nil, ErrProtocol
	}
	ids := make([]string, 0, len(available))
	for _, model := range available {
		if id := str(model, "modelId", "model_id", "value", "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// grokDiscoverListing parses `grok models`, whose model lines look like "  * grok-4.5 (default)".
// grokDiscoverListing 解析 `grok models` 的输出，模型行形如 "  * grok-4.5 (default)"。
func grokDiscoverListing(c *call) ([]string, error) {
	var ids []string
	total, signedOut := 0, false
	diag, err := c.run([]string{"models"}, nil, func(line []byte) (bool, error) {
		if total += len(line); total > maxListBytes {
			return false, ErrProtocol
		}
		signedOut = signedOut || c.signedOut(line)
		rest, ok := strings.CutPrefix(string(bytes.TrimSpace(line)), "*")
		if !ok {
			return false, nil
		}
		if fields := strings.Fields(rest); len(fields) > 0 && len(ids) < maxRawModels {
			ids = append(ids, strings.Trim(fields[0], "(),"))
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

// grokParser reads `--output-format streaming-json`. A "thought" event carries
// one fragment of thinking text in "data", the same shape as "text", and is
// forwarded as thinking; a thought whose data is not a string is skipped
// rather than failing the turn, since it is never part of the reply. Any tool
// event aborts the turn. Grok exposes no thinking switch to this adapter, so
// ChatRequest.Reasoning is not mapped onto any flag.
//
// grokParser 读取 `--output-format streaming-json`。"thought" 事件在 "data" 中携带
// 一段思考文本，形状与 "text" 相同，作为思考转发；data 不是字符串的 thought 被跳过
// 而不让回合失败，因为它从不属于回复。任何工具事件都会中止回合。Grok 没有向本适配器
// 暴露思考开关，因此 ChatRequest.Reasoning 不映射到任何参数。
type grokParser struct{ usage *runtime.Usage }

func (p *grokParser) line(event []byte, out sink) (bool, error) {
	obj := object(event)
	switch str(obj, "type") {
	case "text":
		var s string
		if json.Unmarshal(obj["data"], &s) != nil {
			return false, ErrProtocol
		}
		return false, out.text(s)
	case "thought":
		var s string
		if json.Unmarshal(obj["data"], &s) != nil {
			return false, nil
		}
		return false, out.reasoning(s)
	case "tool_call", "tool_call_update":
		return false, ErrUnexpectedTool
	case "end":
		var err error
		p.usage, err = usageFrom(object(obj["usage"]), [][]string{{"input_tokens", "prompt_tokens", "inputTokens"}},
			[][]string{{"output_tokens", "completion_tokens", "outputTokens"}})
		return false, err
	case "error":
		return false, ErrTurn
	}
	// plan, usage, available_commands and future types carry neither reply nor thinking text.
	// plan、usage、available_commands 及未来新增类型既不含回复文本，也不含思考文本。
	return false, nil
}

func (p *grokParser) finish() (*runtime.Usage, error) { return p.usage, nil }

// usageFrom sums the input groups into prompt tokens and the output groups
// into completion tokens (see sumCounts); nil obj means no usage. Dialects
// that report cached input separately list it as its own input group, so
// cache hits still count toward quota.
// usageFrom 把各输入组相加为 prompt token，各输出组相加为 completion token
// （见 sumCounts）；obj 为 nil 表示没有用量。单独报告缓存输入的方言把它列为独立
// 的输入组，使缓存命中同样计入配额。
func usageFrom(obj map[string]json.RawMessage, input, output [][]string) (*runtime.Usage, error) {
	if obj == nil {
		return nil, nil
	}
	in, err := sumCounts(obj, input...)
	if err != nil {
		return nil, err
	}
	out, err := sumCounts(obj, output...)
	if err != nil {
		return nil, err
	}
	return tokenUsage(in, out), nil
}

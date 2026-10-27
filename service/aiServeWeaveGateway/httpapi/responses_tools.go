package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"AIServeWeave/common/runtime"
)

// This file is the tool-calling half of the Responses translation — what an
// agent client such as Codex CLI needs on top of plain text turns. Responses
// carries a tool loop as typed items in one array (function_call,
// function_call_output, custom_tool_call, local_shell_call, ...) where Chat
// Completions has assistant tool_calls plus role "tool" messages; the
// functions here map between the two so the backend below still sees only its
// own vocabulary.
//
// The three tool kinds a client can define are all reduced to a Chat function
// tool on the way in and rebuilt into their own item type on the way out:
//
//   - "function"    passes through unchanged.
//   - "custom"      (a freeform-text tool, e.g. Codex's apply_patch) becomes a
//     function with one string parameter, "input".
//   - "local_shell" becomes a function named "local_shell" with a command
//     array, and comes back as a local_shell_call item.
//
// A "namespace" tool groups function tools under a name; its members are
// flattened into Chat functions and rendered back with their namespace.
//
// Hosted tools (file_search, code_interpreter, mcp, ...) stay rejected: they are
// executed by OpenAI's own service, not by anything here. web_search alone is
// tolerated, for the reason on isOptionalHostedTool.
//
// 本文件是 Responses 转换中「工具调用」的那一半——Codex CLI 之类的 agent 客户端在纯
// 文本轮次之外所需要的部分。Responses 把工具循环表示成同一个数组里的一串带类型的项
// （function_call、function_call_output、custom_tool_call、local_shell_call……），
// 而 Chat Completions 用的是 assistant 的 tool_calls 加 role 为 "tool" 的消息；这里的
// 函数在两者之间互相映射，使下面的后端依旧只看到它自己的词汇。
//
// 客户端可以定义的三种工具，进来时都被还原成 Chat 的 function 工具，出去时再重建成各自
// 的项类型：
//
//   - "function"    原样透传。
//   - "custom"      （自由文本工具，例如 Codex 的 apply_patch）变成只有一个字符串
//     参数 "input" 的 function。
//   - "local_shell" 变成名为 "local_shell"、带 command 数组的 function，返回时重建成
//     local_shell_call 项。
//
// "namespace" 工具把若干 function 工具归在一个名字之下；其成员被展平成 Chat 的
// function，返回时再连同 namespace 一起渲染回去。
//
// 托管工具（file_search、code_interpreter、mcp……）仍然拒绝：它们由 OpenAI 自己的服务
// 执行，这里没有任何东西能执行它们。唯独 web_search 被容忍，理由见
// isOptionalHostedTool。

// toolKind is which Responses tool type a tool was declared as.
//
// toolKind 是一个工具当初以哪种 Responses 工具类型声明。
type toolKind int

const (
	toolKindFunction toolKind = iota
	toolKindCustom
	toolKindLocalShell
)

// localShellToolName is the Chat function name a "local_shell" tool is exposed
// to the backend under.
//
// localShellToolName 是 "local_shell" 工具暴露给后端时使用的 Chat 函数名。
const localShellToolName = "local_shell"

// customToolParameters is the JSON Schema a custom tool's freeform text input
// is wrapped in, since a Chat function can only take a JSON object.
//
// customToolParameters 是自由文本输入被包进去的 JSON Schema，因为 Chat 的 function
// 只能接收 JSON 对象。
var customToolParameters = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"The raw text input for this tool."}},"required":["input"],"additionalProperties":false}`)

// localShellParameters is the JSON Schema of the local_shell function.
//
// localShellParameters 是 local_shell 函数的 JSON Schema。
var localShellParameters = json.RawMessage(`{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"},"description":"The command to execute, as an argv array."},"timeout_ms":{"type":"integer","description":"Optional timeout in milliseconds."},"working_directory":{"type":"string","description":"Optional working directory."}},"required":["command"]}`)

// toolRef records how a backend-visible function name maps back to the tool a
// Responses request declared: its kind, and for a tool declared inside a
// namespace, the namespace and the tool's own name within it.
//
// toolRef 记录一个后端可见的函数名如何映射回 Responses 请求所声明的工具：它的类型；
// 对声明在某个 namespace 之内的工具，还有 namespace 与工具在其中的本名。
type toolRef struct {
	kind      toolKind
	namespace string
	name      string
}

// responsesToolset is the outcome of translating a request's tool list: the
// Chat function tools to hand the backend, the refs needed to render the
// model's calls back as the right item type, and the hosted tools that were
// left out.
//
// responsesToolset 是翻译请求工具列表的结果：交给后端的 Chat function 工具、把模型的
// 调用渲染回正确项类型所需的 refs，以及被略去的托管工具。
type responsesToolset struct {
	tools []runtime.Tool
	// refs holds an entry only for a tool that is not a plain top-level
	// function, keyed by the name the backend sees. A name absent from refs is
	// a plain function.
	//
	// refs 只为不是「普通顶层 function」的工具设条目，键是后端看到的名字。refs 里没有
	// 的名字就是普通 function。
	refs map[string]toolRef
	// ignored lists hosted tools that were accepted but not forwarded; see
	// isOptionalHostedTool.
	//
	// ignored 列出被接受但未转发的托管工具；见 isOptionalHostedTool。
	ignored []string
}

// chatToolName is the name a namespaced tool is exposed to the backend under.
// Two namespaces may define tools with the same name, so the namespace is part
// of the identity; trailing underscores are trimmed so a namespace that already
// ends in "__" (as MCP tool namespaces do) is not doubled.
//
// chatToolName 是 namespace 内的工具暴露给后端时使用的名字。两个 namespace 可以定义
// 同名工具，因此 namespace 属于标识的一部分；末尾的下划线会被裁掉，使本来就以 "__"
// 结尾的 namespace（MCP 工具的 namespace 就是如此）不会被重复拼接。
func chatToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return strings.TrimRight(namespace, "_") + "__" + name
}

// isOptionalHostedTool reports whether a hosted tool type is one an agent
// client advertises opportunistically. Codex CLI sends {"type":"web_search"}
// on every request unless the user turned it off, and a Gateway that refused it
// would refuse Codex outright. The model is simply never told the tool exists —
// the same outcome as a deployment without it — and the caller is told which
// tools were left out through the X-Gateway-Ignored-Tools response header
// rather than not at all. Every other hosted tool is still refused by name:
// a request that names file_search or code_interpreter is asking for a result
// only that tool can produce.
//
// isOptionalHostedTool 报告一个托管工具类型是否属于 agent 客户端顺带声明的那一类。
// Codex CLI 除非用户关掉，否则每个请求都会带 {"type":"web_search"}，拒绝它的 Gateway
// 等于直接拒绝了 Codex。模型只是从未被告知这个工具存在——与没有这项能力的部署结果相同
// ——而调用方会通过 X-Gateway-Ignored-Tools 响应头得知哪些工具被略去，而不是毫无提示。
// 其他所有托管工具仍按名字拒绝：指名 file_search 或 code_interpreter 的请求，要的是只有
// 该工具才能产出的结果。
func isOptionalHostedTool(toolType string) bool {
	return strings.HasPrefix(toolType, "web_search")
}

// ignoredToolsHeader names the response header that lists ignored hosted tools.
//
// ignoredToolsHeader 是列出被略去托管工具的响应头名称。
const ignoredToolsHeader = "X-Gateway-Ignored-Tools"

// responsesTools converts a request's tool list into Chat function tools.
//
// responsesTools 把请求的工具列表转换成 Chat 的 function 工具。
func responsesTools(in []responsesTool) (responsesToolset, error) {
	var set responsesToolset
	if err := set.add("", in); err != nil {
		return responsesToolset{}, err
	}
	return set, nil
}

// add converts tools declared under namespace ("" for the top level), recursing
// into namespace tools.
//
// add 转换声明在 namespace 之下的工具（顶层为 ""），并递归进入 namespace 工具。
func (set *responsesToolset) add(namespace string, in []responsesTool) error {
	remember := func(chatName string, ref toolRef) {
		if set.refs == nil {
			set.refs = map[string]toolRef{}
		}
		set.refs[chatName] = ref
	}
	for _, t := range in {
		switch t.Type {
		case "function":
			chatName := chatToolName(namespace, t.Name)
			if namespace != "" {
				remember(chatName, toolRef{kind: toolKindFunction, namespace: namespace, name: t.Name})
			}
			set.tools = append(set.tools, runtime.Tool{
				Type: "function",
				Function: runtime.FunctionDefinition{
					Name:        chatName,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		case "custom":
			if t.Name == "" {
				return fmt.Errorf(`a "custom" tool requires "name"`)
			}
			chatName := chatToolName(namespace, t.Name)
			remember(chatName, toolRef{kind: toolKindCustom, namespace: namespace, name: t.Name})
			set.tools = append(set.tools, runtime.Tool{
				Type: "function",
				Function: runtime.FunctionDefinition{
					Name:        chatName,
					Description: t.Description,
					Parameters:  customToolParameters,
				},
			})
		case "local_shell":
			remember(localShellToolName, toolRef{kind: toolKindLocalShell, name: localShellToolName})
			set.tools = append(set.tools, runtime.Tool{
				Type: "function",
				Function: runtime.FunctionDefinition{
					Name:        localShellToolName,
					Description: "Run a shell command on the user's machine and return its output.",
					Parameters:  localShellParameters,
				},
			})
		case "namespace":
			if namespace != "" {
				return fmt.Errorf("a namespace tool cannot be nested inside another namespace")
			}
			if t.Name == "" {
				return fmt.Errorf(`a "namespace" tool requires "name"`)
			}
			if err := set.add(t.Name, t.Tools); err != nil {
				return err
			}
		default:
			if isOptionalHostedTool(t.Type) {
				set.ignored = append(set.ignored, t.Type)
				continue
			}
			// Built-in tools (file_search, code_interpreter, mcp) are executed
			// by OpenAI's own service. This Gateway forwards to a model and
			// runs nothing, so accepting one would promise a capability that
			// does not exist here.
			//
			// 内置工具（file_search、code_interpreter、mcp）由 OpenAI 自己的服务执行。
			// 本 Gateway 只把请求转给模型、不运行任何东西，因此接受它等于承诺一项这里
			// 并不存在的能力。
			return fmt.Errorf("tool type %q is not supported by this gateway", t.Type)
		}
	}
	return nil
}

// responsesToolChoice converts Responses' tool_choice into the string
// runtime.ChatRequest.ToolChoice carries. A bare string ("auto", "none",
// "required") passes through; an object naming one tool is rewritten from the
// Responses shape {"type":"function","name":"x"} to Chat's nested
// {"type":"function","function":{"name":"x"}}, which is what a chat backend
// reads.
//
// responsesToolChoice 把 Responses 的 tool_choice 转换成 runtime.ChatRequest.ToolChoice
// 所携带的字符串。裸字符串（"auto"、"none"、"required"）原样透传；指名某个工具的对象
// 则从 Responses 的形状 {"type":"function","name":"x"} 改写成 Chat 的嵌套形状
// {"type":"function","function":{"name":"x"}}，那才是聊天后端读得懂的。
func responsesToolChoice(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true, nil
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", false, fmt.Errorf("tool_choice must be a string or an object")
	}
	name := obj.Name
	switch obj.Type {
	case "function", "custom":
	case "local_shell":
		name = localShellToolName
	default:
		return "", false, fmt.Errorf("tool_choice type %q is not supported by this gateway", obj.Type)
	}
	if name == "" {
		return "", false, fmt.Errorf("tool_choice of type %q requires a name", obj.Type)
	}
	encoded, err := json.Marshal(map[string]any{
		"type":     "function",
		"function": map[string]string{"name": name},
	})
	if err != nil {
		return "", false, err
	}
	return string(encoded), true, nil
}

// -----------------------------------------------------------------------
// Output: Chat tool calls → Responses items
// -----------------------------------------------------------------------

// itemPrefix is the id prefix of the item a call of this kind is rendered as.
//
// itemPrefix 是这种类型的调用所渲染成的项所用的 id 前缀。
func (k toolKind) itemPrefix() string {
	switch k {
	case toolKindCustom:
		return "ctc"
	case toolKindLocalShell:
		return "lsh"
	default:
		return "fc"
	}
}

// callItem renders one backend tool call as the Responses item its tool was
// declared as. status is "in_progress" while a streamed call is still being
// assembled and "completed" once its arguments are whole.
//
// callItem 把后端的一次工具调用渲染成其工具当初声明的那种 Responses 项。status 在流式
// 调用仍在拼装时为 "in_progress"，参数完整后为 "completed"。
func (set responsesToolset) callItem(itemID, callID, chatName, arguments, status string) responseOutputRaw {
	ref, known := set.refs[chatName]
	name := chatName
	if known {
		name = ref.name
	}
	switch ref.kind {
	case toolKindCustom:
		input := customToolInput(arguments)
		return responseOutputRaw{
			Type: "custom_tool_call", ID: itemID, Status: status,
			CallID: callID, Name: name, Namespace: ref.namespace, Input: &input,
		}
	case toolKindLocalShell:
		return responseOutputRaw{
			Type: "local_shell_call", ID: itemID, Status: status,
			CallID: callID, Action: localShellActionFor(arguments),
		}
	}
	if arguments == "" && status == "completed" {
		// A function that takes no parameters is reported with empty
		// arguments by some backends; Responses clients expect a JSON
		// document, and a later replay of "" into a chat template would not
		// be one.
		//
		// 某些后端对不带参数的函数上报空 arguments；Responses 客户端期望的是一份 JSON
		// 文档，而之后把 "" 重放进聊天模板时它并不是。
		arguments = "{}"
	}
	return responseOutputRaw{
		Type: "function_call", ID: itemID, Status: status,
		CallID: callID, Name: name, Namespace: ref.namespace, Arguments: &arguments,
	}
}

// itemPrefix is the id prefix of the item a call to chatName is rendered as.
//
// itemPrefix 是对 chatName 的调用所渲染成的项所用的 id 前缀。
func (set responsesToolset) itemPrefix(chatName string) string {
	switch set.refs[chatName].kind {
	case toolKindCustom:
		return "ctc"
	case toolKindLocalShell:
		return "lsh"
	default:
		return "fc"
	}
}

// wantsArgumentDeltas reports whether a call to chatName is streamed as
// function_call_arguments events. Only a plain function's arguments are the
// item's own arguments; a custom tool's or local_shell's item carries a
// value derived from them, which is only known once they are whole.
//
// wantsArgumentDeltas 报告对 chatName 的调用是否以 function_call_arguments 事件流式
// 输出。只有普通 function 的 arguments 就是该项自己的 arguments；custom 工具或
// local_shell 的项携带的是由它们派生出的值，只有在它们完整之后才知道。
func (set responsesToolset) wantsArgumentDeltas(chatName string) bool {
	return set.refs[chatName].kind == toolKindFunction
}

// customToolInput extracts the freeform text a model put in a custom tool's
// "input" argument. A model that ignored the schema and emitted something else
// gets its raw arguments passed through, so the client's own parser reports
// the malformed input back to it rather than this layer inventing a value.
//
// customToolInput 取出模型放进 custom 工具 "input" 参数里的自由文本。无视 schema、输出
// 了别的东西的模型，其原始参数会被原样透传，由客户端自己的解析器把格式错误反馈给它，而
// 不是由本层编造一个值。
func customToolInput(arguments string) string {
	var v struct {
		Input *string `json:"input"`
	}
	if err := json.Unmarshal([]byte(arguments), &v); err == nil && v.Input != nil {
		return *v.Input
	}
	return arguments
}

// localShellArgs is the argument object of the local_shell function.
//
// localShellArgs 是 local_shell 函数的参数对象。
type localShellArgs struct {
	Command          []string `json:"command"`
	TimeoutMS        *int64   `json:"timeout_ms,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
}

// localShellActionFor turns the function arguments a model produced into the
// "exec" action a local_shell_call item carries. A command given as one string
// rather than an argv array is run through bash, the reading a model that
// mis-shaped it almost always intends.
//
// localShellActionFor 把模型产出的函数参数转成 local_shell_call 项所携带的 "exec"
// 动作。以单个字符串而非 argv 数组给出的 command 交给 bash 运行，这几乎总是把它写错形状
// 的模型所想表达的意思。
func localShellActionFor(arguments string) map[string]any {
	var v struct {
		Command          json.RawMessage `json:"command"`
		TimeoutMS        *float64        `json:"timeout_ms"`
		WorkingDirectory string          `json:"working_directory"`
	}
	_ = json.Unmarshal([]byte(arguments), &v)

	command := []string{}
	var argv []string
	var line string
	if err := json.Unmarshal(v.Command, &argv); err == nil {
		command = argv
	} else if err := json.Unmarshal(v.Command, &line); err == nil && line != "" {
		command = []string{"bash", "-lc", line}
	}
	action := map[string]any{"type": "exec", "command": command}
	if v.TimeoutMS != nil {
		action["timeout_ms"] = int64(*v.TimeoutMS)
	}
	if v.WorkingDirectory != "" {
		action["working_directory"] = v.WorkingDirectory
	}
	return action
}

// -----------------------------------------------------------------------
// Input: Responses items → Chat messages
// -----------------------------------------------------------------------

// appendResponsesItem converts one element of a request's input array and
// appends the result to out. It is the single place an input item's type is
// interpreted, so a type this Gateway cannot represent is refused by name
// here rather than half-translated somewhere else.
//
// appendResponsesItem 转换请求 input 数组的一个元素，并把结果追加到 out。这里是唯一
// 解读输入项类型的地方，因此本 Gateway 无法表示的类型在此指名拒绝，而不是在别处被翻译
// 一半。
func appendResponsesItem(out []runtime.ChatMessage, item responsesInputItem) ([]runtime.ChatMessage, error) {
	switch item.Type {
	case "", "message":
		text, parts, err := inputItemContent(item.Content)
		if err != nil {
			return nil, err
		}
		role := item.Role
		switch role {
		case "":
			role = "user"
		case "developer":
			// "developer" is the Responses name for what chat backends know
			// as the system role, and few chat templates accept the former.
			//
			// "developer" 是 Responses 对聊天后端所称 system 角色的叫法，而很少有聊天
			// 模板认得前者。
			role = "system"
		}
		return append(out, runtime.ChatMessage{Role: role, Content: text, ContentParts: parts}), nil

	case "additional_tools":
		// Tool declarations are merged for the current generation by toRuntime.
		// 工具声明由 toRuntime 合并，用于当前这次生成。
		if item.Role != "developer" || len(item.Tools) == 0 {
			return nil, fmt.Errorf("additional_tools requires developer role and a nonempty tools array")
		}
		return out, nil

	case "reasoning":
		// A reasoning item is the model's own prior chain of thought, often
		// encrypted and meaningful only to the OpenAI model that wrote it. A
		// chat backend has no place to receive it, so it is not forwarded.
		//
		// reasoning 项是模型自己此前的思考链，往往已加密，只对写下它的那个 OpenAI 模型
		// 有意义。聊天后端没有地方接收它，因此不转发。
		return out, nil

	case "function_call":
		if item.CallID == "" || item.Name == "" {
			return nil, fmt.Errorf(`an input item of type "function_call" requires "call_id" and "name"`)
		}
		return appendAssistantToolCall(out, item.CallID, chatToolName(item.Namespace, item.Name), item.Arguments), nil

	case "custom_tool_call":
		if item.CallID == "" || item.Name == "" {
			return nil, fmt.Errorf(`an input item of type "custom_tool_call" requires "call_id" and "name"`)
		}
		encoded, err := json.Marshal(map[string]string{"input": item.Input})
		if err != nil {
			return nil, err
		}
		return appendAssistantToolCall(out, item.CallID, chatToolName(item.Namespace, item.Name), string(encoded)), nil

	case "local_shell_call":
		callID := item.CallID
		if callID == "" {
			callID = item.ID
		}
		if callID == "" {
			return nil, fmt.Errorf(`an input item of type "local_shell_call" requires "call_id"`)
		}
		var action struct {
			Command          []string `json:"command"`
			TimeoutMS        *int64   `json:"timeout_ms"`
			WorkingDirectory string   `json:"working_directory"`
		}
		if len(item.Action) > 0 {
			if err := json.Unmarshal(item.Action, &action); err != nil {
				return nil, fmt.Errorf(`an input item of type "local_shell_call" has an invalid "action"`)
			}
		}
		encoded, err := json.Marshal(localShellArgs{
			Command: action.Command, TimeoutMS: action.TimeoutMS, WorkingDirectory: action.WorkingDirectory,
		})
		if err != nil {
			return nil, err
		}
		return appendAssistantToolCall(out, callID, localShellToolName, string(encoded)), nil

	case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
		if item.CallID == "" {
			return nil, fmt.Errorf("an input item of type %q requires \"call_id\"", item.Type)
		}
		text, parts, err := toolOutputContent(item.Output)
		if err != nil {
			return nil, err
		}
		return append(out, runtime.ChatMessage{
			Role: "tool", ToolCallID: item.CallID, Content: text, ContentParts: parts,
		}), nil
	}

	// Anything else (hosted-tool calls such as web_search_call, compaction
	// items, ...) belongs to a part of this API that this endpoint does not
	// implement. Refusing by name beats translating half of one into a message
	// that means something else.
	//
	// 其余任何类型（web_search_call 这类托管工具调用、compaction 项……）属于本 API 中
	// 本端点并未实现的部分。指名拒绝，好过把其中一半翻译成一条意思已经变了的消息。
	return nil, fmt.Errorf("input item type %q is not supported", item.Type)
}

// appendAssistantToolCall adds a tool call to out. Consecutive calls, and a
// call directly after an assistant message, share one assistant message: that
// is how Chat Completions spells "the model said this and then called these",
// and a chat template expects the calls of one turn together.
//
// appendAssistantToolCall 把一次工具调用追加到 out。连续的调用，以及紧跟在 assistant
// 消息之后的调用，共用同一条 assistant 消息：Chat Completions 就是这样表达「模型说了
// 这些、然后调用了这些」的，聊天模板也期望同一轮的调用聚在一起。
func appendAssistantToolCall(out []runtime.ChatMessage, callID, name, arguments string) []runtime.ChatMessage {
	call := runtime.ToolCall{
		ID: callID, Type: "function",
		Function: runtime.FunctionCall{Name: name, Arguments: arguments},
	}
	if n := len(out); n > 0 && out[n-1].Role == "assistant" {
		out[n-1].ToolCalls = append(out[n-1].ToolCalls, call)
		return out
	}
	return append(out, runtime.ChatMessage{Role: "assistant", ToolCalls: []runtime.ToolCall{call}})
}

// toolOutputContent reads a tool-call output item's "output". Responses allows
// a bare string, an array of typed content parts (the form a tool that returns
// an image uses), and — from older clients — an object whose "content" holds
// either of those.
//
// toolOutputContent 读取工具调用输出项的 "output"。Responses 允许裸字符串、一组带类型的
// 内容部件（返回图片的工具所用的形式），以及——来自较旧客户端的——以 "content" 字段持有
// 前两者之一的对象。
func toolOutputContent(raw json.RawMessage) (string, []runtime.ContentPart, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil, nil
	}
	if raw[0] == '{' {
		var obj struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return "", nil, fmt.Errorf("a tool call output must be a string or an array of parts")
		}
		raw = bytes.TrimSpace(obj.Content)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return "", nil, nil
		}
	}
	return inputItemContent(raw)
}

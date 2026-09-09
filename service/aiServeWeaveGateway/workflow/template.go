package workflow

import (
	"encoding/json"
	"fmt"

	"AIServeWeave/common/workflowtemplate"
)

// InputType, the Input constants, Input, Output, Dependencies and their
// element types are aliases onto common/workflowtemplate rather than local
// definitions: a template's structural shape is a contract this package
// shares with the control plane (P03), not something the Gateway defines on
// its own. Aliasing keeps every existing literal and switch in this package
// and its callers working unchanged, since an alias is the same type, not a
// new one.
//
// InputType、Input 常量、Input、Output、Dependencies 及其成员类型都是指向
// common/workflowtemplate 的别名，而不是本地定义：一个模板的结构性形状是本包与
// 控制面共享的契约（P03），不是 Gateway 自行定义的东西。用别名是因为它就是同一个
// 类型而非新类型，本包及其调用方现有的字面量与 switch 都无需改动。
type (
	InputType    = workflowtemplate.InputType
	Input        = workflowtemplate.Input
	Output       = workflowtemplate.Output
	Dependencies = workflowtemplate.Dependencies
)

const (
	InputString  = workflowtemplate.InputString
	InputInteger = workflowtemplate.InputInteger
	InputNumber  = workflowtemplate.InputNumber
	InputBoolean = workflowtemplate.InputBoolean
	InputFile    = workflowtemplate.InputFile
)

// DefaultMaxStringLength bounds a string input that declares no MaxLength of
// its own. A prompt is the one field a caller can make arbitrarily large, and
// it travels the whole tunnel before ComfyUI ever sees it.
//
// DefaultMaxStringLength 限制未自行声明 MaxLength 的字符串输入。提示词是调用方唯一
// 能任意放大的字段，而它在 ComfyUI 看到之前要先走完整条隧道。
const DefaultMaxStringLength = workflowtemplate.DefaultMaxStringLength

// Template is one registered workflow: an API-format ComfyUI graph plus the
// inputs a caller is allowed to substitute into it, the outputs and
// dependencies its author declared, and — for a template synced from the
// control plane (P03) — the version and tenant visibility that publication
// carried.
//
// Template 是一个已注册的工作流：一张 API Format 的 ComfyUI 图，外加调用方被允许
// 替换进去的那些输入、其作者声明的输出与依赖，以及——对于从控制面同步而来的模板
// （P03）——那次发布携带的版本与租户可见范围。
type Template struct {
	ID           string
	Description  string
	Inputs       []Input
	Outputs      []Output
	Dependencies Dependencies
	Graph        json.RawMessage
	// Version is opaque and empty for a template loaded from a local file,
	// matching model.Job.WorkflowVersion's own "opaque, template-author-chosen
	// string" contract: a file-loaded template was never versioned, so a job
	// that ran it should not claim a version it does not have. A template
	// synced from the control plane carries its revision number as a string.
	//
	// Version 是不透明的，文件加载的模板留空，与 model.Job.WorkflowVersion 自身
	// "不透明的、由模板作者选择的字符串"这一约定对齐：文件加载的模板从未被版本化，
	// 用它跑出来的 job 不该声称一个自己没有的版本。从控制面同步来的模板携带其版本号
	// 的字符串形式。
	Version string
	// VisibleTenantIDs is empty for a file-loaded template, meaning every
	// tenant may see and run it — the pre-P03 behavior, unchanged. A template
	// synced from the control plane carries the allow list that publication
	// set.
	//
	// VisibleTenantIDs 对文件加载的模板留空，意味着所有租户都能看到并运行它——
	// P03 之前的行为，不变。从控制面同步来的模板携带那次发布设置的允许列表。
	VisibleTenantIDs []string
}

// VisibleTo reports whether tenantID may see and submit runs against this
// template. An empty VisibleTenantIDs means every tenant may.
//
// VisibleTo 报告 tenantID 是否可以看到并对本模板提交运行。VisibleTenantIDs 为空
// 意味着所有租户都可以。
func (t *Template) VisibleTo(tenantID string) bool {
	if len(t.VisibleTenantIDs) == 0 {
		return true
	}
	for _, id := range t.VisibleTenantIDs {
		if id == tenantID {
			return true
		}
	}
	return false
}

// Validate reports whether the template is internally consistent, delegating
// the actual structural check to workflowtemplate.Validate so the control
// plane and every Gateway replica reject the same templates for the same
// reason — see that function's doc comment.
//
// Validate 报告模板自身是否自洽，把实际的结构性检查委托给
// workflowtemplate.Validate，好让控制面与每个 Gateway 副本以同一个理由拒绝同一批
// 模板——见该函数的文档注释。
func (t *Template) Validate() error {
	return workflowtemplate.Validate(t.ID, workflowtemplate.Content{
		Description:  t.Description,
		Inputs:       t.Inputs,
		Outputs:      t.Outputs,
		Dependencies: t.Dependencies,
		Graph:        t.Graph,
	})
}

// graph is a decoded API-format workflow: every node's whole object, plus its
// inputs map decoded one level further. Both are kept because binding writes
// into the inputs and must hand back everything else — class_type, _meta —
// exactly as the template author wrote it.
//
// This duplicates workflowtemplate's own unexported graph parser rather than
// reusing it, because the two solve different problems: workflowtemplate only
// ever checks that a node and field exist, while Bind must produce a mutable
// copy it can write into and re-marshal — a concern that belongs to the
// Gateway's runtime, not to the shared structural contract.
//
// graph 是一张解码后的 API Format 工作流：每个节点的完整对象，外加再往下解一层的
// inputs 映射。两者都要保留，因为绑定写入的是 inputs，而其余部分——class_type、
// _meta——必须原样交还，与模板作者写下的一致。
//
// 这里重复了 workflowtemplate 自己那个不导出的图解析器，而不是复用它，因为两者
// 解决的是不同问题：workflowtemplate 只需要检查某个节点与字段是否存在，而 Bind
// 必须产出一份可写入、可重新编码的可变副本——这属于 Gateway 运行时的事，不属于
// 共享的结构性契约。
type graph struct {
	nodes  map[string]map[string]json.RawMessage
	inputs map[string]map[string]json.RawMessage
}

// parseGraph decodes an API-format graph. A node without an inputs object is
// rejected rather than tolerated: it is either not API format or not a node,
// and both are worth failing on at load time.
//
// parseGraph 解码一张 API Format 的图。没有 inputs 对象的节点会被拒绝而不是容忍：
// 它要么不是 API Format，要么根本不是节点，两种情况都值得在加载时就失败。
func parseGraph(raw json.RawMessage) (graph, error) {
	var nodes map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return graph{}, fmt.Errorf("graph is not a JSON object of nodes: %w", err)
	}
	g := graph{nodes: nodes, inputs: make(map[string]map[string]json.RawMessage, len(nodes))}
	for id, node := range nodes {
		raw, ok := node["inputs"]
		if !ok {
			return graph{}, fmt.Errorf("graph node %q has no inputs object", id)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return graph{}, fmt.Errorf("graph node %q has an inputs value that is not an object: %w", id, err)
		}
		g.inputs[id] = fields
	}
	return g, nil
}

// marshal renders the graph back to JSON, folding each node's inputs map back
// into its node object.
//
// marshal 把图渲染回 JSON，将每个节点的 inputs 映射折回它的节点对象。
func (g graph) marshal() (json.RawMessage, error) {
	for id, fields := range g.inputs {
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("re-encoding the inputs of node %q: %w", id, err)
		}
		g.nodes[id]["inputs"] = encoded
	}
	return json.Marshal(g.nodes)
}

// SetGraphField writes value into node/field of rawGraph and returns the
// re-encoded result. It exists for the one caller (httpapi.submitRun) that
// finishes what Bind leaves incomplete for a PendingFile input: Bind cannot
// know a file's InputRef before a node is chosen and the bytes are actually
// uploaded there (STATUS.md's P04), so it hands back the graph as far as it
// can go, and this fills in the rest once that upload has happened. It is
// exported — unlike the graph type it uses internally — because that
// resolution happens one layer up, in httpapi, after scheduler.UploadInput
// returns.
//
// SetGraphField 把 value 写入 rawGraph 的 node/field，并返回重新编码的结果。
// 它的存在，是为了完成 Bind 对一个 PendingFile 输入留下的未竟之事：Bind
// 在选定节点、字节真正上传过去（STATUS.md 的 P04）之前，不可能知道一个
// 文件的 InputRef，因此它只能把图交还到能力所及之处，而这个函数在那次
// 上传发生之后补上剩下的部分。之所以导出——不同于它内部所用的 graph
// 类型——是因为这一步解析发生在上一层，即 scheduler.UploadInput 返回之后
// 的 httpapi 里。
func SetGraphField(rawGraph json.RawMessage, node, field, value string) (json.RawMessage, error) {
	g, err := parseGraph(rawGraph)
	if err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}
	fields, ok := g.inputs[node]
	if !ok {
		return nil, fmt.Errorf("workflow: graph has no node %q", node)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	fields[field] = encoded
	return g.marshal()
}

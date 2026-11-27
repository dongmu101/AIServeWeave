// Package flowdef defines a Studio flow (编排): its JSON shape, the node types
// it may contain, the {{scope.field}} reference syntax, and Validate.
//
// It is the one place a flow's structure is judged. The control plane calls
// Validate before storing a flow version so a broken definition is never
// saved, and the Gateway calls it again before running a stored version.
// Neither service re-derives its own notion of "valid" — the same discipline
// common/workflowtemplate.Validate follows for ComfyUI templates.
//
// The checks are structural only. Whether a named model is still available to
// the caller is a run-time fact the Gateway's scheduler owns, not something a
// definition can know.
//
// Unlike workflowtemplate.Validate, which stops at the first problem, Validate
// collects every problem (up to MaxIssues) with the node it belongs to and a
// stable code, because the Studio editor shows them all at once on the canvas.
//
// Shape of a definition:
//
//   - exactly one input node: the start of the flow; it has no incoming edges.
//     The run-time variables it offers are declared in Definition.Inputs and
//     referenced anywhere as {{inputs.<name>}};
//   - exactly one output node: it has no outgoing edges. The result fields it
//     collects are declared in Definition.Outputs;
//   - llm, template, condition, image_generation and comfyui_template nodes
//     in between, each reachable from the input node. Edges out of a condition node carry branch "true" or
//     "false"; no other edge carries a branch;
//   - no cycles. A node may reference {{<node>.<field>}} only for a node
//     upstream of it, so the value always exists by the time it runs.
//   - the fields image_generation and comfyui_template nodes produce name
//     artifacts, not text. They may only be referenced as the whole of an
//     output's ref, so an artifact id is never fed to a model or compared.
//
// Flow contents — prompts, input values and outputs — are the same class of
// secret as a chat message. Errors from this package name identifiers (node
// ids, input and field names) and never quote a template, label or value.
//
// flowdef 包定义 Studio 的编排：JSON 形状、可包含的节点类型、{{作用域.字段}}
// 引用语法与 Validate。
//
// 这是唯一判断编排结构的地方。控制面在保存编排版本前调用 Validate，损坏的定义
// 永远不会被存下；Gateway 在运行已存版本前再调用一次。两边都不各自推导一套
// 「有效」标准——与 common/workflowtemplate.Validate 对 ComfyUI 模板的约定相同。
//
// 检查只涉及结构。引用的模型对调用方是否仍然可用，是 Gateway 调度器掌握的运行时
// 事实，不是定义本身能知道的。
//
// 与遇到第一个问题就返回的 workflowtemplate.Validate 不同，Validate 收集全部问题
// （最多 MaxIssues 条），每条带所属节点与稳定的错误码，因为 Studio 编辑器要在画布上
// 一次性标出它们。
//
// 定义的形状：
//
//   - 恰好一个输入节点：编排的起点，没有入边。它提供的运行时变量声明在
//     Definition.Inputs 中，任何地方都以 {{inputs.<名称>}} 引用；
//   - 恰好一个输出节点：没有出边。它收集的结果字段声明在 Definition.Outputs 中；
//   - 中间是 llm、template、condition、image_generation 与 comfyui_template
//     节点，每个都能从输入节点到达。条件节点的
//     出边带 branch「true」或「false」，其他边都不带 branch；
//   - 不能有环。节点只能以 {{<节点>.<字段>}} 引用它上游的节点，这样它运行时该值
//     一定已经存在。
//   - image_generation 与 comfyui_template 节点产出的字段指向产物而不是文本。它们
//     只能作为输出 ref 的全部内容被引用，因此产物 id 永远不会被送进模型或参与比较。
//
// 编排内容——提示词、输入值与输出——与对话消息属于同一类机密。本包的错误只写
// 标识符（节点 id、输入名与字段名），从不引用模板、标签或取值。
package flowdef

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Bounds on one definition. MaxNodes is the product limit from the Studio
// spec; the others keep a definition's size and validation work bounded.
//
// 单份定义的上限。MaxNodes 是 Studio 规格规定的产品上限；其余各项让定义的大小与
// 校验工作量保持有界。
const (
	MaxDefinitionBytes = 1 << 20
	MaxNodes           = 50
	MaxEdges           = 200
	MaxInputs          = 20
	MaxOutputs         = 20
	MaxJSONFields      = 20
	MaxIdentLength     = 64
	MaxLabelRunes      = 100
	MaxDescRunes       = 500
	MaxTemplateBytes   = 64 << 10
	MaxTextValueBytes  = 64 << 10
	MaxModelLength     = 256
	MaxIssues          = 50
	// MaxTemplateIDLength bounds a comfyui_template node's template id and
	// MaxTemplateInputs the inputs it binds; MaxTemplateInputNameLength
	// bounds each input's name, which the template's publisher chose.
	// MaxTemplateIDLength 限制 comfyui_template 节点的模板 id，MaxTemplateInputs
	// 限制它绑定的输入数；MaxTemplateInputNameLength 限制每个输入名（由模板发布者
	// 选定）的长度。
	MaxTemplateIDLength        = 256
	MaxTemplateInputs          = 50
	MaxTemplateInputNameLength = 128
	// MaxImageSide bounds each side of an image_generation node's size.
	// MaxImageSide 限制 image_generation 节点尺寸的每一边。
	MaxImageSide = 16384
)

// NodeType is the closed set of node kinds.
// / NodeType 是节点种类的封闭集合。
type NodeType string

// Node types. A definition that names a type this package does not know is
// rejected rather than stored unrunnable.
//
// 节点类型。定义中出现本包不认识的类型时直接拒绝，而不是存下一份无法运行的定义。
const (
	NodeInput     NodeType = "input"
	NodeLLM       NodeType = "llm"
	NodeTemplate  NodeType = "template"
	NodeCondition NodeType = "condition"
	NodeImage     NodeType = "image_generation"
	NodeComfyUI   NodeType = "comfyui_template"
	NodeOutput    NodeType = "output"
)

// ValueType is the type of a run-time input or of a structured LLM output
// field.
// / ValueType 是运行时输入或 LLM 结构化输出字段的类型。
type ValueType string

// Value types. / 取值类型。
const (
	TypeText    ValueType = "text"
	TypeNumber  ValueType = "number"
	TypeBoolean ValueType = "boolean"
)

// Branch labels an edge leaving a condition node.
// / Branch 标记从条件节点出发的边。
type Branch string

// Branches. / 分支。
const (
	BranchTrue  Branch = "true"
	BranchFalse Branch = "false"
)

// Operator is a condition node's comparison.
// / Operator 是条件节点的比较方式。
type Operator string

// Operators. Empty and NotEmpty take no right operand; the numeric ones parse
// both sides as numbers at run time.
//
// 比较方式。Empty 与 NotEmpty 不需要右操作数；数值比较在运行时把两侧都解析为数字。
const (
	OpEquals      Operator = "equals"
	OpNotEquals   Operator = "not_equals"
	OpContains    Operator = "contains"
	OpNotContains Operator = "not_contains"
	OpGT          Operator = "gt"
	OpGTE         Operator = "gte"
	OpLT          Operator = "lt"
	OpLTE         Operator = "lte"
	OpEmpty       Operator = "empty"
	OpNotEmpty    Operator = "not_empty"
)

// Output field names nodes expose to {{<node>.<field>}}.
// / 节点向 {{<节点>.<字段>}} 暴露的输出字段名。
const (
	// FieldText is the generated or rendered text of an llm or template node.
	// FieldText 是 llm 或 template 节点生成或渲染出的文本。
	FieldText = "text"
	// FieldResult is a condition node's outcome, "true" or "false".
	// FieldResult 是条件节点的结果，「true」或「false」。
	FieldResult = "result"
	// FieldImage is the artifact id of the image an image_generation or
	// comfyui_template node produced; for the latter, its first image, or ""
	// when it produced none.
	// FieldImage 是 image_generation 或 comfyui_template 节点产出的图片的产物 id；
	// 对后者是第一张图片，没有图片时为空字符串。
	FieldImage = "image"
	// FieldArtifacts lists every artifact a comfyui_template node produced,
	// as a JSON array of {"artifact_id", "filename", "kind"} objects.
	// FieldArtifacts 列出 comfyui_template 节点产出的全部产物，形式为
	// {"artifact_id", "filename", "kind"} 对象组成的 JSON 数组。
	FieldArtifacts = "artifacts"
)

// IsArtifactField reports whether field of a node of type t names artifacts
// rather than text.
//
// IsArtifactField 报告 t 类型节点的 field 字段指向的是产物而不是文本。
func IsArtifactField(t NodeType, field string) bool {
	switch t {
	case NodeImage:
		return field == FieldImage
	case NodeComfyUI:
		return field == FieldImage || field == FieldArtifacts
	}
	return false
}

// Definition is one flow version's content, without its identity and
// publication metadata (id, name, version), which the control plane owns.
//
// Definition 是一个编排版本的内容，不含身份与发布元数据（id、名称、版本号），
// 那些由控制面管理。
type Definition struct {
	Inputs  []Input  `json:"inputs"`
	Nodes   []Node   `json:"nodes"`
	Edges   []Edge   `json:"edges"`
	Outputs []Output `json:"outputs"`
}

// Input declares one run-time variable. Default, when present, must be a
// value of Type; JSON null counts as absent.
//
// Input 声明一个运行时变量。Default 存在时必须是 Type 类型的取值；JSON null 视为
// 未设置。
type Input struct {
	Name     string          `json:"name"`
	Label    string          `json:"label,omitempty"`
	Type     ValueType       `json:"type"`
	Required bool            `json:"required"`
	Default  json.RawMessage `json:"default,omitempty"`
}

// Node is one step on the canvas. Config's shape depends on Type; read it
// through LLM, Template or Condition. Input and output nodes take no config.
//
// Node 是画布上的一个步骤。Config 的形状取决于 Type，经 LLM、Template 或
// Condition 读取。输入与输出节点没有 config。
type Node struct {
	ID       string          `json:"id"`
	Type     NodeType        `json:"type"`
	Label    string          `json:"label,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
	Position Position        `json:"position"`
}

// Position is the node's place on the canvas; it does not affect execution.
// / Position 是节点在画布上的位置，不影响执行。
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Edge runs from one node to the next. Branch is set only on edges leaving a
// condition node.
//
// Edge 从一个节点指向下一个节点。只有从条件节点出发的边才设置 Branch。
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Branch Branch `json:"branch,omitempty"`
}

// Output declares one result field of a run. Ref is a template — usually a
// single reference such as "{{summary.text}}" — evaluated against nodes
// upstream of the output node.
//
// Output 声明一次运行的一个结果字段。Ref 是一个模板——通常是单个引用，如
// 「{{summary.text}}」——基于输出节点上游的节点求值。
type Output struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

// LLMConfig is an llm node's config. System and Prompt are templates. A
// non-empty JSONFields asks for structured output: each field is then
// referenceable as {{<node>.<field>}} alongside {{<node>.text}}.
//
// LLMConfig 是 llm 节点的配置。System 与 Prompt 是模板。JSONFields 非空表示要求
// 结构化输出：每个字段都可以与 {{<节点>.text}} 一样以 {{<节点>.<字段>}} 引用。
type LLMConfig struct {
	Model       string      `json:"model"`
	System      string      `json:"system,omitempty"`
	Prompt      string      `json:"prompt"`
	Temperature *float64    `json:"temperature,omitempty"`
	MaxTokens   *int        `json:"max_tokens,omitempty"`
	JSONFields  []JSONField `json:"json_fields,omitempty"`
}

// JSONField declares one field of an llm node's structured output.
// Description is passed to the model as guidance.
//
// JSONField 声明 llm 节点结构化输出的一个字段。Description 作为提示传给模型。
type JSONField struct {
	Name        string    `json:"name"`
	Type        ValueType `json:"type"`
	Description string    `json:"description,omitempty"`
}

// TemplateConfig is a template node's config: one template whose rendering
// becomes {{<node>.text}}.
//
// TemplateConfig 是 template 节点的配置：一个模板，渲染结果即 {{<节点>.text}}。
type TemplateConfig struct {
	Template string `json:"template"`
}

// ConditionConfig is a condition node's config. Left and Right are templates;
// Right is unused by Empty and NotEmpty.
//
// ConditionConfig 是条件节点的配置。Left 与 Right 是模板；Empty 与 NotEmpty 不使用
// Right。
type ConditionConfig struct {
	Left  string   `json:"left"`
	Op    Operator `json:"op"`
	Right string   `json:"right,omitempty"`
}

// ImageConfig is an image_generation node's config. Prompt is a template.
// Model is a fixed image model name, or empty for the deployment's own image
// generation workflow; Size, when set, is "<width>x<height>".
//
// ImageConfig 是 image_generation 节点的配置。Prompt 是模板。Model 是固定的图像
// 模型名，为空时使用部署自己的生图工作流；Size 设置时形如「<宽>x<高>」。
type ImageConfig struct {
	Model  string `json:"model,omitempty"`
	Prompt string `json:"prompt"`
	Size   string `json:"size,omitempty"`
}

// ComfyUIConfig is a comfyui_template node's config: the published workflow
// template to run, and a template for each of its inputs the node sets. An
// input left out takes the workflow template's own default.
//
// ComfyUIConfig 是 comfyui_template 节点的配置：要运行的已发布工作流模板，以及该
// 节点为其设置的每个输入的模板。未列出的输入取工作流模板自己的默认值。
type ComfyUIConfig struct {
	Template string            `json:"template"`
	Inputs   map[string]string `json:"inputs,omitempty"`
}

// LLM decodes an llm node's config strictly: unknown fields are an error.
// / LLM 严格解码 llm 节点的配置：未知字段报错。
func (n Node) LLM() (LLMConfig, error) {
	var c LLMConfig
	return c, n.decodeConfig(NodeLLM, &c)
}

// Template decodes a template node's config strictly.
// / Template 严格解码 template 节点的配置。
func (n Node) Template() (TemplateConfig, error) {
	var c TemplateConfig
	return c, n.decodeConfig(NodeTemplate, &c)
}

// Condition decodes a condition node's config strictly.
// / Condition 严格解码条件节点的配置。
func (n Node) Condition() (ConditionConfig, error) {
	var c ConditionConfig
	return c, n.decodeConfig(NodeCondition, &c)
}

// Image decodes an image_generation node's config strictly.
// / Image 严格解码 image_generation 节点的配置。
func (n Node) Image() (ImageConfig, error) {
	var c ImageConfig
	return c, n.decodeConfig(NodeImage, &c)
}

// ComfyUI decodes a comfyui_template node's config strictly.
// / ComfyUI 严格解码 comfyui_template 节点的配置。
func (n Node) ComfyUI() (ComfyUIConfig, error) {
	var c ComfyUIConfig
	return c, n.decodeConfig(NodeComfyUI, &c)
}

func (n Node) decodeConfig(want NodeType, v any) error {
	if n.Type != want {
		return fmt.Errorf("node %q is a %s node, not %s", n.ID, n.Type, want)
	}
	if isNull(n.Config) {
		return errors.New("config is required")
	}
	return decodeStrict(n.Config, v)
}

// Decode parses raw as a Definition, rejecting oversized input, unknown
// fields and trailing data. A failure is a *ValidationError, so callers
// handle one error shape from Decode and Validate alike. It does not call
// Validate.
//
// Decode 把 raw 解析为 Definition，拒绝超大输入、未知字段与多余的尾部数据。失败时
// 返回 *ValidationError，调用方对 Decode 与 Validate 只需处理一种错误形状。它不调用
// Validate。
func Decode(raw []byte) (Definition, error) {
	if len(raw) > MaxDefinitionBytes {
		return Definition{}, single(CodeTooLarge, "", fmt.Sprintf("definition exceeds %d bytes", MaxDefinitionBytes))
	}
	var def Definition
	if err := decodeStrict(raw, &def); err != nil {
		return Definition{}, single(CodeInvalidJSON, "", "definition is not valid: "+err.Error())
	}
	return def, nil
}

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null"
}

// IssueCode is a stable, machine-readable problem kind; Studio maps it to its
// own text.
// / IssueCode 是稳定的、机器可读的问题种类；Studio 把它映射为自己的文案。
type IssueCode string

// Issue codes. / 问题码。
const (
	CodeTooLarge         IssueCode = "too_large"
	CodeInvalidJSON      IssueCode = "invalid_json"
	CodeTooMany          IssueCode = "too_many"
	CodeInvalidName      IssueCode = "invalid_name"
	CodeDuplicateName    IssueCode = "duplicate_name"
	CodeInvalidLabel     IssueCode = "invalid_label"
	CodeInvalidType      IssueCode = "invalid_type"
	CodeInvalidDefault   IssueCode = "invalid_default"
	CodeInvalidConfig    IssueCode = "invalid_config"
	CodeRequired         IssueCode = "required"
	CodeOutOfRange       IssueCode = "out_of_range"
	CodeInvalidTemplate  IssueCode = "invalid_template"
	CodeUnknownReference IssueCode = "unknown_reference"
	CodeNotUpstream      IssueCode = "reference_not_upstream"
	CodeMissingNode      IssueCode = "missing_node"
	CodeMultipleNodes    IssueCode = "multiple_nodes"
	CodeInvalidEdge      IssueCode = "invalid_edge"
	CodeCycle            IssueCode = "cycle"
	CodeUnreachable      IssueCode = "unreachable"
	// CodeArtifactReference is a reference to an artifact field anywhere but
	// as the whole of an output's ref.
	// CodeArtifactReference 表示在输出 ref 的全部内容之外的地方引用了产物字段。
	CodeArtifactReference IssueCode = "artifact_reference"

	// Run-input codes, from BindInputs. / 运行输入的问题码，来自 BindInputs。
	CodeUnknownInput IssueCode = "unknown_input"
	CodeInvalidValue IssueCode = "invalid_value"
)

// Issue is one problem. Node is the id of the node it belongs to: problems in
// Definition.Inputs belong to the input node and those in Definition.Outputs
// to the output node, since that is where the editor shows them. Node is
// empty for a problem with the definition as a whole, or when the node it
// would belong to has no usable id. Field is a dotted path inside the node or
// definition, e.g. "config.prompt" or "inputs.topic.default".
//
// Issue 是一个问题。Node 是其所属节点的 id：Definition.Inputs 中的问题属于输入
// 节点，Definition.Outputs 中的问题属于输出节点，因为编辑器在那里显示它们。问题
// 属于整份定义、或所属节点没有可用 id 时 Node 为空。Field 是节点或定义内部以点
// 分隔的路径，如「config.prompt」或「inputs.topic.default」。
type Issue struct {
	Node    string    `json:"node,omitempty"`
	Field   string    `json:"field,omitempty"`
	Code    IssueCode `json:"code"`
	Message string    `json:"message"`
}

// ValidationError carries every problem found, at most MaxIssues of them;
// Truncated reports that more were dropped.
//
// ValidationError 携带找到的全部问题，最多 MaxIssues 条；Truncated 表示还有更多被
// 丢弃。
type ValidationError struct {
	Issues    []Issue
	Truncated bool
}

func (e *ValidationError) Error() string {
	const show = 3
	msgs := make([]string, 0, show)
	for i, is := range e.Issues {
		if i == show {
			break
		}
		msg := is.Message
		if is.Node != "" {
			msg = fmt.Sprintf("node %q: %s", is.Node, msg)
		}
		msgs = append(msgs, msg)
	}
	s := "flowdef: invalid definition: " + strings.Join(msgs, "; ")
	if more := len(e.Issues) - show; more > 0 || e.Truncated {
		s += " (and more)"
	}
	return s
}

func single(code IssueCode, field, msg string) *ValidationError {
	return &ValidationError{Issues: []Issue{{Field: field, Code: code, Message: msg}}}
}

// Validate reports whether def is a well-formed, runnable flow. It returns nil
// or a *ValidationError listing every problem found.
//
// Validate 报告 def 是否是一份结构完整、可以运行的编排。返回 nil，或一个列出全部
// 问题的 *ValidationError。
func Validate(def Definition) error {
	v := &validator{def: def}
	v.run()
	if len(v.issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: v.issues, Truncated: v.truncated}
}

// templateUse is one template found in a node's config or an output, kept so
// its references can be checked once the whole graph is known. owner is the
// node whose upstream the references must lie in, and the node a problem is
// reported on; "" when that node has no usable id.
type templateUse struct {
	owner string
	field string
	tmpl  Template
	// whole marks an output ref that is a single reference and nothing else,
	// the one place an artifact field may be referenced.
	whole bool
}

type validator struct {
	def       Definition
	issues    []Issue
	truncated bool

	inputs    map[string]bool            // declared, well-named inputs
	nodes     map[string]Node            // well-named, unique nodes
	fields    map[string]map[string]bool // node id → referenceable fields
	nodeTypes map[string]NodeType        // node id → type, for artifact fields
	uses      []templateUse
	inputID   string // the single input node, if exactly one
	outID     string // the single output node, if exactly one
}

func (v *validator) add(node, field string, code IssueCode, format string, args ...any) {
	if len(v.issues) >= MaxIssues {
		v.truncated = true
		return
	}
	v.issues = append(v.issues, Issue{Node: node, Field: field, Code: code, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) run() {
	d := v.def
	for _, c := range []struct {
		what   string
		n, max int
	}{
		{"nodes", len(d.Nodes), MaxNodes},
		{"edges", len(d.Edges), MaxEdges},
		{"inputs", len(d.Inputs), MaxInputs},
		{"outputs", len(d.Outputs), MaxOutputs},
	} {
		if c.n > c.max {
			v.add("", c.what, CodeTooMany, "at most %d %s are allowed", c.max, c.what)
		}
	}
	if len(v.issues) > 0 {
		return
	}

	v.checkNodes()
	v.checkInputs()
	v.checkOutputs()
	children, parents := v.checkEdges()
	order, acyclic := v.checkCycles(children, parents)
	var upstream map[string]map[string]bool
	if acyclic {
		upstream = ancestors(order, parents)
		if v.inputID != "" {
			for _, id := range order {
				if id != v.inputID && !upstream[id][v.inputID] {
					v.add(id, "", CodeUnreachable, "node is not reachable from the input node")
				}
			}
		}
	}
	v.checkReferences(upstream)
}

func (v *validator) checkInputs() {
	at := v.inputID
	v.inputs = make(map[string]bool, len(v.def.Inputs))
	for i, in := range v.def.Inputs {
		path := fmt.Sprintf("inputs[%d]", i)
		switch {
		case !isIdent(in.Name):
			v.add(at, path+".name", CodeInvalidName, "input name must be an identifier of letters, digits and underscores")
		case v.inputs[in.Name]:
			v.add(at, "inputs."+in.Name, CodeDuplicateName, "duplicate input %q", in.Name)
		default:
			v.inputs[in.Name] = true
			path = "inputs." + in.Name
		}
		v.checkLabel(at, path+".label", in.Label)
		if !validValueType(in.Type) {
			v.add(at, path+".type", CodeInvalidType, "input type must be text, number or boolean")
			continue
		}
		if !isNull(in.Default) {
			if _, err := valueText(in.Type, in.Default); err != nil {
				v.add(at, path+".default", CodeInvalidDefault, "default %s", err)
			}
		}
	}
}

func (v *validator) checkNodes() {
	v.nodes = make(map[string]Node, len(v.def.Nodes))
	v.fields = make(map[string]map[string]bool, len(v.def.Nodes))
	v.nodeTypes = make(map[string]NodeType, len(v.def.Nodes))
	var inputs, outputs []string
	for i, n := range v.def.Nodes {
		id := n.ID
		switch {
		case !isIdent(id):
			v.add("", fmt.Sprintf("nodes[%d].id", i), CodeInvalidName, "node id must be an identifier of letters, digits and underscores")
			id = ""
		case id == InputsScope:
			v.add(id, "id", CodeInvalidName, "node id %q is reserved", InputsScope)
			id = ""
		default:
			if _, dup := v.nodes[id]; dup {
				v.add(id, "id", CodeDuplicateName, "duplicate node id %q", id)
				id = ""
			} else {
				v.nodes[id] = n
				v.nodeTypes[id] = n.Type
			}
		}
		v.checkLabel(id, "label", n.Label)

		switch n.Type {
		case NodeInput:
			inputs = append(inputs, id)
			v.checkNoConfig(id, n)
		case NodeOutput:
			outputs = append(outputs, id)
			v.checkNoConfig(id, n)
		case NodeLLM:
			v.checkLLM(id, n)
		case NodeTemplate:
			v.checkTemplateNode(id, n)
		case NodeCondition:
			v.checkCondition(id, n)
		case NodeImage:
			v.checkImage(id, n)
		case NodeComfyUI:
			v.checkComfyUI(id, n)
		default:
			v.add(id, "type", CodeInvalidType, "unknown node type")
		}
	}
	v.inputID = v.anchor(inputs, NodeInput)
	v.outID = v.anchor(outputs, NodeOutput)
}

// anchor checks that exactly one node of an anchor type exists and returns its
// id, or "" when there is none, several, or the one has an invalid id.
func (v *validator) anchor(ids []string, t NodeType) string {
	switch len(ids) {
	case 0:
		v.add("", "nodes", CodeMissingNode, "the flow needs exactly one %s node", t)
		return ""
	case 1:
		return ids[0]
	default:
		for _, id := range ids[1:] {
			v.add(id, "type", CodeMultipleNodes, "the flow needs exactly one %s node", t)
		}
		return ""
	}
}

func (v *validator) checkLabel(node, field, label string) {
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > MaxLabelRunes {
		v.add(node, field, CodeInvalidLabel, "label must be valid text of at most %d characters", MaxLabelRunes)
	}
}

func (v *validator) checkNoConfig(id string, n Node) {
	if isNull(n.Config) {
		return
	}
	var empty struct{}
	if decodeStrict(n.Config, &empty) != nil {
		v.add(id, "config", CodeInvalidConfig, "%s nodes take no config", n.Type)
	}
}

// setFields records the fields a node exposes. A node whose id was rejected
// exposes nothing, since nothing can address it.
func (v *validator) setFields(id string, names ...string) {
	if id == "" {
		return
	}
	f := make(map[string]bool, len(names))
	for _, n := range names {
		f[n] = true
	}
	v.fields[id] = f
}

func (v *validator) checkLLM(id string, n Node) {
	c, err := n.LLM()
	if err != nil {
		v.add(id, "config", CodeInvalidConfig, "llm config is invalid: %s", err)
		return
	}
	switch m := strings.TrimSpace(c.Model); {
	case m == "":
		v.add(id, "config.model", CodeRequired, "model is required")
	case m != c.Model || len(c.Model) > MaxModelLength || strings.Contains(c.Model, "{{"):
		// The model is fixed when the flow is saved so the editor can flag
		// one that is no longer available; it is never a template.
		// 模型在保存时就是固定的，编辑器才能标出已不可用的模型；它永远不是模板。
		v.add(id, "config.model", CodeInvalidConfig, "model must be a model name of at most %d bytes, not a template", MaxModelLength)
	}
	v.template(id, "config.prompt", c.Prompt, true)
	v.template(id, "config.system", c.System, false)
	if t := c.Temperature; t != nil && (*t < 0 || *t > 2) {
		v.add(id, "config.temperature", CodeOutOfRange, "temperature must be between 0 and 2")
	}
	if m := c.MaxTokens; m != nil && *m < 1 {
		v.add(id, "config.max_tokens", CodeOutOfRange, "max_tokens must be at least 1")
	}
	if len(c.JSONFields) > MaxJSONFields {
		v.add(id, "config.json_fields", CodeTooMany, "at most %d json fields are allowed", MaxJSONFields)
		return
	}
	names := []string{FieldText}
	seen := map[string]bool{}
	for i, f := range c.JSONFields {
		path := fmt.Sprintf("config.json_fields[%d]", i)
		switch {
		case !isIdent(f.Name):
			v.add(id, path+".name", CodeInvalidName, "json field name must be an identifier of letters, digits and underscores")
		case f.Name == FieldText:
			v.add(id, path+".name", CodeInvalidName, "json field name %q is reserved for the reply text", FieldText)
		case seen[f.Name]:
			v.add(id, path+".name", CodeDuplicateName, "duplicate json field %q", f.Name)
		default:
			seen[f.Name] = true
			names = append(names, f.Name)
		}
		if !validValueType(f.Type) {
			v.add(id, path+".type", CodeInvalidType, "json field type must be text, number or boolean")
		}
		if !utf8.ValidString(f.Description) || utf8.RuneCountInString(f.Description) > MaxDescRunes {
			v.add(id, path+".description", CodeInvalidLabel, "description must be valid text of at most %d characters", MaxDescRunes)
		}
	}
	v.setFields(id, names...)
}

func (v *validator) checkTemplateNode(id string, n Node) {
	c, err := n.Template()
	if err != nil {
		v.add(id, "config", CodeInvalidConfig, "template config is invalid: %s", err)
		return
	}
	v.template(id, "config.template", c.Template, true)
	v.setFields(id, FieldText)
}

func (v *validator) checkCondition(id string, n Node) {
	c, err := n.Condition()
	if err != nil {
		v.add(id, "config", CodeInvalidConfig, "condition config is invalid: %s", err)
		return
	}
	left, leftOK := v.template(id, "config.left", c.Left, true)
	v.setFields(id, FieldResult)
	switch c.Op {
	case OpEmpty, OpNotEmpty:
		if c.Right != "" {
			v.add(id, "config.right", CodeInvalidConfig, "operator %s takes no right operand", c.Op)
		}
		return
	case OpEquals, OpNotEquals, OpContains, OpNotContains, OpGT, OpGTE, OpLT, OpLTE:
	default:
		v.add(id, "config.op", CodeInvalidConfig, "unknown operator")
		return
	}
	right, rightOK := v.template(id, "config.right", c.Right, true)
	switch c.Op {
	case OpGT, OpGTE, OpLT, OpLTE:
		v.numericOperand(id, "config.left", c.Op, c.Left, left, leftOK)
		v.numericOperand(id, "config.right", c.Op, c.Right, right, rightOK)
	}
}

func (v *validator) checkImage(id string, n Node) {
	c, err := n.Image()
	if err != nil {
		v.add(id, "config", CodeInvalidConfig, "image_generation config is invalid: %s", err)
		return
	}
	if c.Model != "" && (strings.TrimSpace(c.Model) != c.Model || len(c.Model) > MaxModelLength || strings.Contains(c.Model, "{{")) {
		v.add(id, "config.model", CodeInvalidConfig, "model must be a model name of at most %d bytes, not a template", MaxModelLength)
	}
	v.template(id, "config.prompt", c.Prompt, true)
	if c.Size != "" && !validImageSize(c.Size) {
		v.add(id, "config.size", CodeInvalidConfig, "size must be <width>x<height>, each between 1 and %d", MaxImageSide)
	}
	v.setFields(id, FieldImage)
}

// validImageSize reports whether s is "<width>x<height>" with both sides
// decimal integers between 1 and MaxImageSide, without sign or leading zero.
func validImageSize(s string) bool {
	w, h, ok := strings.Cut(s, "x")
	return ok && validSide(w) && validSide(h)
}

func validSide(s string) bool {
	if s == "" || s[0] == '0' || len(s) > 5 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	n, _ := strconv.Atoi(s)
	return n <= MaxImageSide
}

func (v *validator) checkComfyUI(id string, n Node) {
	c, err := n.ComfyUI()
	if err != nil {
		v.add(id, "config", CodeInvalidConfig, "comfyui_template config is invalid: %s", err)
		return
	}
	v.setFields(id, FieldImage, FieldArtifacts)
	switch t := strings.TrimSpace(c.Template); {
	case t == "":
		v.add(id, "config.template", CodeRequired, "template is required")
	case t != c.Template || len(c.Template) > MaxTemplateIDLength || !utf8.ValidString(c.Template) || strings.Contains(c.Template, "{{"):
		// Like an llm node's model, the template is fixed when the flow is
		// saved so the editor can flag one that is no longer published.
		// 与 llm 节点的模型一样，模板在保存时就是固定的，编辑器才能标出已不再发布的模板。
		v.add(id, "config.template", CodeInvalidConfig, "template must be a workflow template id of at most %d bytes, not a template", MaxTemplateIDLength)
	}
	if len(c.Inputs) > MaxTemplateInputs {
		v.add(id, "config.inputs", CodeTooMany, "at most %d template inputs are allowed", MaxTemplateInputs)
		return
	}
	names := make([]string, 0, len(c.Inputs))
	for name := range c.Inputs {
		names = append(names, name)
	}
	slices.Sort(names) // stable issue order across map iteration
	for _, name := range names {
		// The name was chosen by the template's publisher and is checked
		// against the template at run time; here it only has to be a
		// printable label that is safe to show and to key a field path by.
		// 输入名由模板发布者选定，运行时再与模板核对；这里只要求它是可以安全显示、
		// 也可以用作字段路径的可打印标签。
		if !validTemplateInputName(name) {
			v.add(id, "config.inputs", CodeInvalidName, "template input names must be printable text of at most %d bytes", MaxTemplateInputNameLength)
			continue
		}
		v.template(id, "config.inputs."+name, c.Inputs[name], false)
	}
}

func validTemplateInputName(s string) bool {
	if s == "" || len(s) > MaxTemplateInputNameLength || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '.' {
			return false
		}
	}
	return true
}

// numericOperand rejects a numeric comparison's operand that has no
// references and is not a number: it is a constant, so "gt abc" is caught at
// save time rather than on every run.
//
// numericOperand 拒绝数值比较中不含引用、又不是数字的操作数：它是常量，因此
// 「gt abc」在保存时就被发现，而不是每次运行时才失败。
func (v *validator) numericOperand(id, field string, op Operator, src string, t Template, ok bool) {
	if !ok || len(t.Refs()) > 0 {
		return
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(src), 64); err != nil {
		v.add(id, field, CodeInvalidConfig, "operator %s compares numbers, and this operand is not one", op)
	}
}

// template parses one template field and records it for reference checks,
// owned by node. It reports whether the field holds a usable template; an
// empty optional field is usable and contributes nothing.
func (v *validator) template(node, field, src string, required bool) (Template, bool) {
	if strings.TrimSpace(src) == "" {
		if required {
			v.add(node, field, CodeRequired, "%s is required", lastSegment(field))
			return Template{}, false
		}
		return Template{}, true
	}
	if len(src) > MaxTemplateBytes {
		v.add(node, field, CodeInvalidTemplate, "template exceeds %d bytes", MaxTemplateBytes)
		return Template{}, false
	}
	t, err := ParseTemplate(src)
	if err != nil {
		v.add(node, field, CodeInvalidTemplate, "%s", err)
		return Template{}, false
	}
	v.uses = append(v.uses, templateUse{owner: node, field: field, tmpl: t})
	return t, true
}

func lastSegment(path string) string {
	return path[strings.LastIndexByte(path, '.')+1:]
}

// checkEdges validates every edge and returns the adjacency of the valid ones.
func (v *validator) checkEdges() (children, parents map[string][]string) {
	children = make(map[string][]string, len(v.nodes))
	parents = make(map[string][]string, len(v.nodes))
	type key struct {
		from, to string
		branch   Branch
	}
	seen := make(map[key]bool, len(v.def.Edges))
	for _, e := range v.def.Edges {
		from, fromOK := v.nodes[e.From]
		to, toOK := v.nodes[e.To]
		at := e.From
		if !fromOK {
			at = e.To
			if !toOK {
				at = ""
			}
		}
		bad := func(format string, args ...any) { v.add(at, "edges", CodeInvalidEdge, format, args...) }
		switch {
		case !fromOK || !toOK:
			bad("edge %s → %s names a node that does not exist", quoteID(e.From), quoteID(e.To))
		case e.From == e.To:
			bad("edge from %q to itself", e.From)
		case to.Type == NodeInput:
			bad("edge %q → %q enters the input node, which must be the start of the flow", e.From, e.To)
		case from.Type == NodeOutput:
			bad("edge %q → %q leaves the output node, which must be the end of the flow", e.From, e.To)
		case from.Type == NodeCondition && e.Branch != BranchTrue && e.Branch != BranchFalse:
			bad("edge %q → %q leaves a condition node and needs branch \"true\" or \"false\"", e.From, e.To)
		case from.Type != NodeCondition && e.Branch != "":
			bad("edge %q → %q has a branch but does not leave a condition node", e.From, e.To)
		case seen[key{e.From, e.To, e.Branch}]:
			bad("duplicate edge %q → %q", e.From, e.To)
		default:
			seen[key{e.From, e.To, e.Branch}] = true
			children[e.From] = append(children[e.From], e.To)
			parents[e.To] = append(parents[e.To], e.From)
		}
	}
	return children, parents
}

// quoteID quotes an edge endpoint for an error message. An endpoint that is
// not an identifier is author input that matched nothing, so it is described
// rather than echoed.
func quoteID(id string) string {
	if isIdent(id) {
		return strconv.Quote(id)
	}
	return "(invalid id)"
}

// checkCycles orders the valid nodes topologically. When the graph has a
// cycle, it reports each node on one — the nodes left after peeling off those
// with no parents and then those with no children — and returns false.
func (v *validator) checkCycles(children, parents map[string][]string) ([]string, bool) {
	indeg := make(map[string]int, len(v.nodes))
	for id := range v.nodes {
		indeg[id] = len(parents[id])
	}
	var queue, order []string
	for _, n := range v.def.Nodes { // definition order keeps the result stable
		if _, ok := v.nodes[n.ID]; ok && indeg[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, c := range children[id] {
			if indeg[c]--; indeg[c] == 0 {
				queue = append(queue, c)
			}
		}
	}
	if len(order) == len(v.nodes) {
		return order, true
	}

	left := make(map[string]bool, len(v.nodes)-len(order))
	for id := range v.nodes {
		if !slices.Contains(order, id) {
			left[id] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for id := range left {
			if !slices.ContainsFunc(children[id], func(c string) bool { return left[c] }) {
				delete(left, id)
				changed = true
			}
		}
	}
	for _, n := range v.def.Nodes {
		if left[n.ID] {
			v.add(n.ID, "", CodeCycle, "node is part of a cycle")
		}
	}
	return nil, false
}

// ancestors returns, for each node, the set of nodes upstream of it.
func ancestors(order []string, parents map[string][]string) map[string]map[string]bool {
	up := make(map[string]map[string]bool, len(order))
	for _, id := range order {
		set := map[string]bool{}
		for _, p := range parents[id] {
			set[p] = true
			for a := range up[p] {
				set[a] = true
			}
		}
		up[id] = set
	}
	return up
}

// checkOutputs checks the declared result fields. Their references resolve
// against the output node's upstream, so the output node owns them.
//
// checkOutputs 检查声明的结果字段。它们的引用基于输出节点的上游求值，因此归输出
// 节点所有。
func (v *validator) checkOutputs() {
	at := v.outID
	seen := make(map[string]bool, len(v.def.Outputs))
	for i, o := range v.def.Outputs {
		path := fmt.Sprintf("outputs[%d]", i)
		switch {
		case !isIdent(o.Name):
			v.add(at, path+".name", CodeInvalidName, "output name must be an identifier of letters, digits and underscores")
		case seen[o.Name]:
			v.add(at, "outputs."+o.Name, CodeDuplicateName, "duplicate output %q", o.Name)
		default:
			seen[o.Name] = true
			path = "outputs." + o.Name
		}
		if t, ok := v.template(at, path+".ref", o.Ref, true); ok {
			_, v.uses[len(v.uses)-1].whole = t.SoleRef()
		}
	}
}

// checkReferences checks every recorded template's references. upstream is
// nil when the graph has a cycle, in which case only existence is checked.
func (v *validator) checkReferences(upstream map[string]map[string]bool) {
	type key struct {
		node, field string
		ref         Ref
	}
	reported := map[key]bool{}
	for _, u := range v.uses {
		for _, r := range u.tmpl.Refs() {
			k := key{u.owner, u.field, r}
			if reported[k] {
				continue
			}
			if code, msg := v.checkRef(u.owner, r, u.whole, upstream); code != "" {
				reported[k] = true
				v.add(u.owner, u.field, code, "{{%s}} %s", r, msg)
			}
		}
	}
}

func (v *validator) checkRef(owner string, r Ref, whole bool, upstream map[string]map[string]bool) (IssueCode, string) {
	if r.Scope == InputsScope {
		if !v.inputs[r.Field] {
			return CodeUnknownReference, "names an input that is not declared"
		}
		return "", ""
	}
	if _, ok := v.nodes[r.Scope]; !ok {
		return CodeUnknownReference, "names a node that does not exist"
	}
	if !v.fields[r.Scope][r.Field] {
		return CodeUnknownReference, "names a field that node does not produce"
	}
	if !whole && IsArtifactField(v.nodeTypes[r.Scope], r.Field) {
		return CodeArtifactReference, "names an artifact, which only an output can reference, as its whole ref"
	}
	if upstream != nil && owner != "" && !upstream[owner][r.Scope] {
		return CodeNotUpstream, "names a node that is not upstream of this one"
	}
	return "", ""
}

func validValueType(t ValueType) bool {
	return t == TypeText || t == TypeNumber || t == TypeBoolean
}

// valueText checks that raw is a JSON value of type t and returns it as the
// text a template substitutes: a string as is, a number in its shortest
// decimal form, a boolean as "true" or "false".
//
// valueText 检查 raw 是否是 t 类型的 JSON 值，并返回模板替换时使用的文本：字符串原样、
// 数字取最短的十进制形式、布尔值为「true」或「false」。
func valueText(t ValueType, raw json.RawMessage) (string, error) {
	switch t {
	case TypeText:
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", errors.New("must be a string")
		}
		if len(s) > MaxTextValueBytes {
			return "", fmt.Errorf("must be at most %d bytes", MaxTextValueBytes)
		}
		return s, nil
	case TypeNumber:
		var f float64
		if json.Unmarshal(raw, &f) != nil {
			return "", errors.New("must be a number")
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	case TypeBoolean:
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return "", errors.New("must be a boolean")
		}
		return strconv.FormatBool(b), nil
	default:
		return "", fmt.Errorf("has unknown type")
	}
}

// BindInputs checks one run's supplied values against the declared inputs
// and returns every declared input's value as text, keyed by name. A value
// that is absent or JSON null takes the input's default, or "" when there is
// none; a required input must end up with a non-blank value. Supplying a name
// that is not declared is an error, so a typo in a run request is not
// silently ignored. A failure is a *ValidationError whose issues have no
// node and a field of "inputs.<name>".
//
// inputs must come from a definition that passed Validate.
//
// BindInputs 按声明的输入检查一次运行提供的取值，并以名称为键、以文本形式返回每个
// 已声明输入的取值。未提供或为 JSON null 的取值使用该输入的默认值，没有默认值时为
// 空字符串；必填输入最终必须得到非空白的取值。提供未声明的名称会报错，这样运行请求
// 中的拼写错误不会被静默忽略。失败时返回 *ValidationError，其问题不带节点，字段为
// 「inputs.<名称>」。
//
// inputs 必须来自一份通过 Validate 的定义。
func BindInputs(inputs []Input, supplied map[string]json.RawMessage) (map[string]string, error) {
	v := &validator{}
	declared := make(map[string]bool, len(inputs))
	values := make(map[string]string, len(inputs))
	for _, in := range inputs {
		declared[in.Name] = true
		path := "inputs." + in.Name
		raw, from := supplied[in.Name], "value"
		if isNull(raw) {
			raw, from = in.Default, "default"
		}
		var text string
		if !isNull(raw) {
			var err error
			if text, err = valueText(in.Type, raw); err != nil {
				v.add("", path, CodeInvalidValue, "%s %s", from, err)
				continue
			}
		}
		if in.Required && strings.TrimSpace(text) == "" {
			v.add("", path, CodeRequired, "input %q is required", in.Name)
			continue
		}
		values[in.Name] = text
	}
	names := make([]string, 0, len(supplied))
	for name := range supplied {
		if !declared[name] {
			names = append(names, name)
		}
	}
	slices.Sort(names) // stable issue order across map iteration
	for _, name := range names {
		v.add("", "inputs", CodeUnknownInput, "input %s is not declared", quoteID(name))
	}
	if len(v.issues) > 0 {
		return nil, &ValidationError{Issues: v.issues, Truncated: v.truncated}
	}
	return values, nil
}

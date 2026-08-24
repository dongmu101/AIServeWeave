package workflow

import (
	"encoding/json"
	"fmt"
)

// InputType is the JSON type a declared input accepts. The set is closed:
// an input is a scalar a caller may substitute into one node field, never a
// nested structure that could smuggle in a different graph.
//
// InputType 是某个已声明输入所接受的 JSON 类型。集合是封闭的：输入是调用方可以替换
// 进某个节点字段的标量，而不是能夹带另一张图进来的嵌套结构。
type InputType string

const (
	InputString  InputType = "string"
	InputInteger InputType = "integer"
	InputNumber  InputType = "number"
	InputBoolean InputType = "boolean"
)

// Input declares one substitutable value: which node field it writes, what
// type it accepts, and the bounds it must fall within.
//
// Input 声明一个可替换的取值：它写入哪个节点字段、接受什么类型、必须落在什么范围内。
type Input struct {
	// Name is what the caller uses in the request body.
	//
	// Name 是调用方在请求体里使用的名字。
	Name string `json:"name"`
	// Node and Field address the graph position this input writes. Both must
	// already exist in Graph — an input never creates a field, it only
	// overwrites one the template author already put there.
	//
	// Node 与 Field 指出本输入写入的图位置。两者都必须已存在于 Graph 中——输入从不
	// 创建字段，只覆盖模板作者已经放在那里的字段。
	Node  string    `json:"node"`
	Field string    `json:"field"`
	Type  InputType `json:"type"`
	// Required rejects a request that omits this input and declares no
	// Default.
	//
	// Required 让缺少本输入、且没有 Default 的请求被拒绝。
	Required bool `json:"required"`
	// Default is used when the caller omits this input. Absent Default and
	// absent value leaves the graph's own value in place.
	//
	// Default 在调用方未给本输入时使用。既无 Default 又无取值时，图里原本的值保持不变。
	Default json.RawMessage `json:"default,omitempty"`
	// MaxLength bounds a string input, in bytes. Zero means the package
	// default, DefaultMaxStringLength, applies.
	//
	// MaxLength 以字节为单位限制字符串输入的长度。为零时采用本包默认值
	// DefaultMaxStringLength。
	MaxLength int `json:"max_length,omitempty"`
	// Min and Max bound a numeric input, inclusive. Nil means unbounded on
	// that side.
	//
	// Min 与 Max 是数值输入的闭区间边界。为 nil 表示该侧无界。
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

// Template is one registered workflow: an API-format ComfyUI graph plus the
// inputs a caller is allowed to substitute into it.
//
// Template 是一个已注册的工作流：一张 API Format 的 ComfyUI 图，外加调用方被允许
// 替换进去的那些输入。
type Template struct {
	ID          string          `json:"id"`
	Description string          `json:"description,omitempty"`
	Inputs      []Input         `json:"inputs"`
	Graph       json.RawMessage `json:"graph"`
}

// DefaultMaxStringLength bounds a string input that declares no MaxLength of
// its own. A prompt is the one field a caller can make arbitrarily large, and
// it travels the whole tunnel before ComfyUI ever sees it.
//
// DefaultMaxStringLength 限制未自行声明 MaxLength 的字符串输入。提示词是调用方唯一
// 能任意放大的字段，而它在 ComfyUI 看到之前要先走完整条隧道。
const DefaultMaxStringLength = 8192

// Validate reports whether the template is internally consistent: it has an
// id, its graph is an API-format object of nodes with input maps, and every
// declared input addresses a node field that graph actually has.
//
// Validate 报告模板自身是否自洽：有 id、图是一个由带 inputs 映射的节点组成的
// API Format 对象、且每个已声明输入都指向该图确实拥有的节点字段。
func (t *Template) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("workflow: template id must not be empty")
	}
	g, err := parseGraph(t.Graph)
	if err != nil {
		return fmt.Errorf("workflow: template %q: %w", t.ID, err)
	}

	seen := make(map[string]struct{}, len(t.Inputs))
	for _, in := range t.Inputs {
		if in.Name == "" {
			return fmt.Errorf("workflow: template %q: an input has an empty name", t.ID)
		}
		if _, dup := seen[in.Name]; dup {
			return fmt.Errorf("workflow: template %q: duplicate input name %q", t.ID, in.Name)
		}
		seen[in.Name] = struct{}{}

		switch in.Type {
		case InputString, InputInteger, InputNumber, InputBoolean:
		default:
			return fmt.Errorf("workflow: template %q: input %q has unknown type %q", t.ID, in.Name, in.Type)
		}

		fields, ok := g.inputs[in.Node]
		if !ok {
			return fmt.Errorf("workflow: template %q: input %q binds to node %q, which the graph does not have", t.ID, in.Name, in.Node)
		}
		if _, ok := fields[in.Field]; !ok {
			return fmt.Errorf("workflow: template %q: input %q binds to field %q of node %q, which the graph does not have",
				t.ID, in.Name, in.Field, in.Node)
		}
		if len(in.Default) > 0 {
			if _, err := in.coerce(in.Default); err != nil {
				return fmt.Errorf("workflow: template %q: input %q has a default that %w", t.ID, in.Name, err)
			}
		}
	}
	return nil
}

// graph is a decoded API-format workflow: every node's whole object, plus its
// inputs map decoded one level further. Both are kept because binding writes
// into the inputs and must hand back everything else — class_type, _meta —
// exactly as the template author wrote it.
//
// graph 是一张解码后的 API Format 工作流：每个节点的完整对象，外加再往下解一层的
// inputs 映射。两者都要保留，因为绑定写入的是 inputs，而其余部分——class_type、
// _meta——必须原样交还，与模板作者写下的一致。
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

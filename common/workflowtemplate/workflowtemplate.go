// Package workflowtemplate defines the shared workflow-template publication
// contract between the control plane and the Gateway: what one versioned
// template revision contains, how it is validated, and how it is digested.
//
// It sits beside common/modelroute rather than inside it, for the same reason
// modelroute is its own package and not part of common/workflowview: this is
// a publication contract two write-capable services must agree on bit for
// bit, not a read-only view either of them renders for a caller. Unlike
// modelroute's single platform-wide table, a template is one of many
// independently versioned documents, so a revision is always addressed by
// (TemplateID, Revision) rather than by a lone counter.
//
// The Graph carried here is the one piece of this contract that must never
// reach a tenant or an operator catalogue listing: the repository's security
// rule puts a full workflow JSON in the same class as an API key and a
// prompt (see common/workflowview.Template). It is legitimate here only
// because this package's callers are the control plane's own storage layer
// and the Gateway's internal sync path — the two places already trusted to
// hold it.
//
// workflowtemplate 包定义控制面与 Gateway 之间共享的工作流模板发布契约：一个已
// 版本化的模板版本包含什么、如何校验、如何取摘要。
//
// 它与 common/modelroute 并列而不在其内部，理由与 modelroute 本身不属于
// common/workflowview 相同：这是两个具备写能力的服务必须逐位对齐的发布契约，
// 不是其中一方为调用方渲染的只读视图。与 modelroute 全平台单一表不同，模板是
// 多份各自独立版本化的文档之一，因此一个版本永远由 (TemplateID, Revision) 二元
// 组寻址，而不是单一计数器。
//
// 这里携带的 Graph 是本契约中唯一绝不能到达租户或运维目录列表的部分：仓库的安全
// 规则把完整的工作流 JSON 与 API Key、提示词归为同一类（见
// common/workflowview.Template）。它在这里是正当的，仅仅因为本包的调用方是控制面
// 自己的存储层与 Gateway 的内部同步路径——两处本就被信任持有它的地方。
package workflowtemplate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MaxGraphBytes bounds one template's API-format ComfyUI graph. An API-format
// graph is a few hundred kilobytes at the outside; anything larger is a
// mistake worth rejecting at publish time.
//
// MaxGraphBytes 限制单个模板的 API Format ComfyUI 图。一张 API Format 的图撑死
// 几百 KB，更大的是发布时就值得拒绝的错误。
const MaxGraphBytes = 4 << 20

// MaxContentBytes bounds the whole publication document — graph plus
// description, inputs, outputs and dependencies — leaving headroom over
// MaxGraphBytes for that metadata.
//
// MaxContentBytes 限制整份发布文档——图加上描述、输入、输出与依赖——在
// MaxGraphBytes 之外为这些元数据留出空间。
const MaxContentBytes = MaxGraphBytes + 256*1024

// MaxInputs caps declared inputs per template. / MaxInputs 限制每个模板的已声明输入数。
const MaxInputs = 100

// MaxOutputs caps declared outputs per template. / MaxOutputs 限制每个模板的已声明输出数。
const MaxOutputs = 20

// MaxCustomNodeDeps caps declared custom-node dependencies. / MaxCustomNodeDeps 限制已声明的自定义节点依赖数。
const MaxCustomNodeDeps = 100

// MaxModelDeps caps declared model dependencies. / MaxModelDeps 限制已声明的模型依赖数。
const MaxModelDeps = 100

// MaxVisibleTenants caps the per-template tenant allow list. / MaxVisibleTenants 限制单个模板的租户允许列表长度。
const MaxVisibleTenants = 1000

// MaxTemplates caps distinct template ids the platform may publish. / MaxTemplates 限制平台可发布的不同模板 id 数量。
const MaxTemplates = 500

// MaxRevisionsPerTemplate caps the immutable history retained per template,
// mirroring modelroute.MaxRevisions for the same reason: unbounded history is
// unbounded storage.
//
// MaxRevisionsPerTemplate 限制每个模板保留的不可变历史数量，理由与
// modelroute.MaxRevisions 相同：不设上限的历史就是不设上限的存储。
const MaxRevisionsPerTemplate = 1000

// InputType is the JSON type a declared input accepts. The set is closed: an
// input is a scalar a caller may substitute into one node field, never a
// nested structure that could smuggle in a different graph.
//
// InputType 是某个已声明输入所接受的 JSON 类型。集合是封闭的：输入是调用方可以
// 替换进某个节点字段的标量，而不是能夹带另一张图进来的嵌套结构。
type InputType string

// The closed set of input types. InputFile is the one exception to the type
// doc comment's "a scalar, never a nested structure" rule, and only in the
// sense that its value is not known until upload time: a caller supplies raw
// bytes out of band (STATUS.md's P04), not a JSON value, so it can never
// smuggle in graph structure any more than a string can. See Input.Default's
// doc comment for why an InputFile input may not declare one.
//
// 输入类型的封闭集合。InputFile 是类型文档注释「是一个标量，绝不是能夹带
// 结构的嵌套体」这条规则唯一的例外，且仅仅是在「它的取值直到上传那一刻才
// 确定」这个意义上：调用方带外提供的是原始字节（STATUS.md 的 P04），而不是
// 一个 JSON 值，因此它和字符串一样，不可能夹带图结构。为什么 InputFile 输入
// 不能声明 Default，见 Input.Default 的文档注释。
const (
	InputString  InputType = "string"
	InputInteger InputType = "integer"
	InputNumber  InputType = "number"
	InputBoolean InputType = "boolean"
	InputFile    InputType = "file"
)

// DefaultMaxStringLength bounds a string input that declares no MaxLength of
// its own.
//
// DefaultMaxStringLength 限制未自行声明 MaxLength 的字符串输入。
const DefaultMaxStringLength = 8192

// Input declares one substitutable value: which node field it writes, what
// type it accepts, and the bounds it must fall within.
//
// Default is rejected for an InputFile input (Validate, via coerce): a
// default would be a static file reference nothing has uploaded, and there
// is no meaningful way to substitute one without an actual upload. An
// InputFile input that goes unsupplied and is not Required is instead left
// untouched in the graph — whatever the template author's own Graph JSON
// already has at that node field, unchanged.
//
// Input 声明一个可替换的取值：它写入哪个节点字段、接受什么类型、必须落在什么
// 范围内。
//
// InputFile 类型的输入不允许声明 Default（由 Validate 经 coerce 拒绝）：
// 默认值会是一个没有任何上传与之对应的静态文件引用，没有实际上传就没有
// 办法有意义地替换出一个来。一个未被提供、也非 Required 的 InputFile 输入，
// 则在图里保持原样不动——该节点字段在模板作者自己的 Graph JSON 里本来是
// 什么，就还是什么。
type Input struct {
	Name      string          `json:"name"`
	Node      string          `json:"node"`
	Field     string          `json:"field"`
	Type      InputType       `json:"type"`
	Required  bool            `json:"required"`
	Default   json.RawMessage `json:"default,omitempty"`
	MaxLength int             `json:"max_length,omitempty"`
	Min       *float64        `json:"min,omitempty"`
	Max       *float64        `json:"max,omitempty"`
}

// Output declares one produced artifact, structurally: the node that
// produces it and a caller-facing kind. Unlike Input, Output never writes
// anything — a template author names a node whose whole purpose is to save a
// result — so it carries no Field. Validation only checks that Node exists in
// the graph; whether that node actually emits an artifact of the declared
// Type at run time is not checked here, the same way declared dependencies
// are not checked against what any node actually has installed.
//
// Output 结构性地声明一个产出：产出它的节点，以及一个面向调用方的种类。与 Input
// 不同，Output 从不写入任何东西——模板作者点名的是一个整个用途就是保存结果的
// 节点——因此它没有 Field。校验只检查 Node 是否存在于图中；该节点运行时是否真的
// 产出了声明 Type 的产物，本处不做核对，与已声明依赖不与任何节点实际安装的东西
// 核对是同一种克制。
type Output struct {
	Name        string `json:"name"`
	Node        string `json:"node"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// NodeDependency names one ComfyUI custom node package a template's graph
// relies on. / NodeDependency 点名模板的图所依赖的一个 ComfyUI 自定义节点包。
type NodeDependency struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ModelDependency names one model checkpoint a template's graph relies on.
// / ModelDependency 点名模板的图所依赖的一个模型 checkpoint。
type ModelDependency struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Dependencies is a template author's own declared list of what a node must
// already have installed to run this template. It is checked only for
// structural sanity — non-empty names, no duplicates, bounded counts — never
// cross-referenced against what any connected node actually reports, because
// no node currently reports such a thing. An installed-but-undeclared
// mismatch is an operational fact for whoever runs the fleet, not something
// this contract can see.
//
// Dependencies 是模板作者自己声明的、一个节点要运行本模板必须已经安装的清单。
// 校验只检查结构性的合理——名字非空、不重复、数量有界——从不与任何已连接节点实际
// 上报的东西做交叉核对，因为目前没有节点上报这类信息。已安装但未声明（或反之）的
// 不一致，是机群运维者才看得见的运维事实，不是本契约能看见的东西。
type Dependencies struct {
	CustomNodes []NodeDependency  `json:"custom_nodes,omitempty"`
	Models      []ModelDependency `json:"models,omitempty"`
}

// Content is everything about one template version except its identity
// (TemplateID) and its publication metadata (Revision, Digest, CreatedAt,
// ActorID) — the part that changes when an author edits the template rather
// than when the platform re-publishes it.
//
// Content 是一个模板版本除了身份（TemplateID）和发布元数据（Revision、Digest、
// CreatedAt、ActorID）之外的一切——作者编辑模板时会变的那部分，而不是平台重新
// 发布时才变的那部分。
type Content struct {
	Description  string          `json:"description,omitempty"`
	Inputs       []Input         `json:"inputs"`
	Outputs      []Output        `json:"outputs,omitempty"`
	Dependencies Dependencies    `json:"dependencies,omitempty"`
	Graph        json.RawMessage `json:"graph"`
}

// Validate reports whether id and content are internally consistent: id is
// non-empty, the graph is an API-format object of nodes with input maps,
// every declared input addresses a node field the graph actually has, every
// declared output addresses a node the graph actually has, and dependencies
// and counts stay within bounds.
//
// This is the one place either service checks a template's structure — the
// control plane calls it at publish time so a broken template is rejected
// before it is ever stored, and the Gateway calls it when building a
// registry from a synced bundle so a corrupted or truncated pull is rejected
// before activation. Neither side re-derives its own notion of "valid".
//
// Validate 报告 id 与 content 是否自洽：id 非空、图是一个由带 inputs 映射的节点
// 组成的 API Format 对象、每个已声明输入都指向图确实拥有的节点字段、每个已声明
// 输出都指向图确实拥有的节点，且依赖与各项计数都在上限内。
//
// 这是两边服务唯一检查模板结构的地方——控制面在发布时调用它，让一个损坏的模板
// 在被存储之前就被拒绝；Gateway 在从同步来的整包构建目录时调用它，让一次损坏或
// 截断的拉取在启用之前就被拒绝。两边都不各自推导一套自己的"有效"标准。
func Validate(id string, content Content) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("workflowtemplate: template id must not be empty")
	}
	if len(content.Inputs) > MaxInputs {
		return fmt.Errorf("workflowtemplate: template %q has too many inputs", id)
	}
	if len(content.Outputs) > MaxOutputs {
		return fmt.Errorf("workflowtemplate: template %q has too many outputs", id)
	}
	if len(content.Dependencies.CustomNodes) > MaxCustomNodeDeps || len(content.Dependencies.Models) > MaxModelDeps {
		return fmt.Errorf("workflowtemplate: template %q has too many declared dependencies", id)
	}
	if len(content.Graph) > MaxGraphBytes {
		return fmt.Errorf("workflowtemplate: template %q graph exceeds %d bytes", id, MaxGraphBytes)
	}

	g, err := parseGraph(content.Graph)
	if err != nil {
		return fmt.Errorf("workflowtemplate: template %q: %w", id, err)
	}

	seenInputs := make(map[string]struct{}, len(content.Inputs))
	for _, in := range content.Inputs {
		if in.Name == "" {
			return fmt.Errorf("workflowtemplate: template %q: an input has an empty name", id)
		}
		if _, dup := seenInputs[in.Name]; dup {
			return fmt.Errorf("workflowtemplate: template %q: duplicate input name %q", id, in.Name)
		}
		seenInputs[in.Name] = struct{}{}

		switch in.Type {
		case InputString, InputInteger, InputNumber, InputBoolean, InputFile:
		default:
			return fmt.Errorf("workflowtemplate: template %q: input %q has unknown type %q", id, in.Name, in.Type)
		}

		fields, ok := g.inputs[in.Node]
		if !ok {
			return fmt.Errorf("workflowtemplate: template %q: input %q binds to node %q, which the graph does not have", id, in.Name, in.Node)
		}
		if _, ok := fields[in.Field]; !ok {
			return fmt.Errorf("workflowtemplate: template %q: input %q binds to field %q of node %q, which the graph does not have",
				id, in.Name, in.Field, in.Node)
		}
		if len(in.Default) > 0 {
			if _, err := coerce(in, in.Default); err != nil {
				return fmt.Errorf("workflowtemplate: template %q: input %q has a default that %w", id, in.Name, err)
			}
		}
	}

	seenOutputs := make(map[string]struct{}, len(content.Outputs))
	for _, out := range content.Outputs {
		if out.Name == "" {
			return fmt.Errorf("workflowtemplate: template %q: an output has an empty name", id)
		}
		if _, dup := seenOutputs[out.Name]; dup {
			return fmt.Errorf("workflowtemplate: template %q: duplicate output name %q", id, out.Name)
		}
		seenOutputs[out.Name] = struct{}{}
		if out.Type == "" {
			return fmt.Errorf("workflowtemplate: template %q: output %q has an empty type", id, out.Name)
		}
		if _, ok := g.nodes[out.Node]; !ok {
			return fmt.Errorf("workflowtemplate: template %q: output %q names node %q, which the graph does not have", id, out.Name, out.Node)
		}
	}

	if err := validateDeps("custom node", content.Dependencies.CustomNodes, func(d NodeDependency) string { return d.Name }); err != nil {
		return fmt.Errorf("workflowtemplate: template %q: %w", id, err)
	}
	if err := validateModelDeps(content.Dependencies.Models); err != nil {
		return fmt.Errorf("workflowtemplate: template %q: %w", id, err)
	}
	return nil
}

func validateDeps(kind string, deps []NodeDependency, name func(NodeDependency) string) error {
	seen := make(map[string]struct{}, len(deps))
	for _, d := range deps {
		if strings.TrimSpace(name(d)) == "" {
			return fmt.Errorf("a declared %s dependency has an empty name", kind)
		}
		if _, dup := seen[name(d)]; dup {
			return fmt.Errorf("duplicate declared %s dependency %q", kind, name(d))
		}
		seen[name(d)] = struct{}{}
	}
	return nil
}

func validateModelDeps(deps []ModelDependency) error {
	seen := make(map[string]struct{}, len(deps))
	for _, d := range deps {
		if strings.TrimSpace(d.Name) == "" {
			return errors.New("a declared model dependency has an empty name")
		}
		if _, dup := seen[d.Name]; dup {
			return fmt.Errorf("duplicate declared model dependency %q", d.Name)
		}
		seen[d.Name] = struct{}{}
	}
	return nil
}

// coerce checks raw against the input's declared type and bounds. It exists
// here, duplicating the Gateway's own workflow.coerce, only to let Validate
// check a declared Default without depending on the Gateway's package — the
// two packages may not import each other. It is never used to bind a real
// request's values; that stays the Gateway's exclusive job.
//
// coerce 按输入声明的类型与边界检查 raw。它在这里重复了 Gateway 自己的
// workflow.coerce，仅仅是为了让 Validate 能在不依赖 Gateway 包的前提下检查一个
// 已声明的 Default——两个包不能互相 import。它从不用于绑定一次真实请求的取值；
// 那始终是 Gateway 独占的事。
func coerce(in Input, raw json.RawMessage) (json.RawMessage, error) {
	switch in.Type {
	case InputString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("must be a string")
		}
		limit := in.MaxLength
		if limit <= 0 {
			limit = DefaultMaxStringLength
		}
		if len(s) > limit {
			return nil, fmt.Errorf("must be at most %d bytes", limit)
		}
		return json.Marshal(s)
	case InputInteger:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		return json.Marshal(n)
	case InputNumber:
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, fmt.Errorf("must be a number")
		}
		return json.Marshal(n)
	case InputBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("must be a boolean")
		}
		return json.Marshal(b)
	case InputFile:
		// Reached only when a template declares a Default for a file input,
		// which this rejects unconditionally — see Input's doc comment.
		// Binding a real request's file inputs never calls this: it is the
		// Gateway's own workflow.Bind's job, and it does not go through
		// coerce for InputFile at all.
		//
		// 只在模板为一个文件输入声明了 Default 时才会走到这里，本分支无条件
		// 拒绝——理由见 Input 的文档注释。绑定一次真实请求的文件输入从不
		// 调用这里：那是 Gateway 自己 workflow.Bind 的事，而它对 InputFile
		// 根本不会走 coerce。
		return nil, fmt.Errorf("file inputs must not declare a default")
	default:
		return nil, fmt.Errorf("has unknown type %q", in.Type)
	}
}

// graph is a decoded API-format workflow, parsed only far enough to check
// structure: every node id and its inputs field names. It is not kept around
// to write into — that mutable copy is the Gateway's own concern.
//
// graph 是一张解码后的 API Format 工作流，只解到足够检查结构的深度：每个节点 id
// 及其 inputs 字段名。它不会被留着用于写入——那份可变副本是 Gateway 自己的事。
type graph struct {
	nodes  map[string]json.RawMessage
	inputs map[string]map[string]json.RawMessage
}

// parseGraph decodes an API-format graph. A node without an inputs object is
// rejected rather than tolerated: it is either not API format or not a node,
// and both are worth failing on at publish time.
//
// parseGraph 解码一张 API Format 的图。没有 inputs 对象的节点会被拒绝而不是容忍：
// 它要么不是 API Format，要么根本不是节点，两种情况都值得在发布时就失败。
func parseGraph(raw json.RawMessage) (graph, error) {
	var nodes map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return graph{}, fmt.Errorf("graph is not a JSON object of nodes: %w", err)
	}
	g := graph{nodes: nodes, inputs: make(map[string]map[string]json.RawMessage, len(nodes))}
	for id, node := range nodes {
		var withInputs struct {
			Inputs map[string]json.RawMessage `json:"inputs"`
		}
		if err := json.Unmarshal(node, &withInputs); err != nil || withInputs.Inputs == nil {
			return graph{}, fmt.Errorf("graph node %q has no inputs object", id)
		}
		g.inputs[id] = withInputs.Inputs
	}
	return g, nil
}

// Canonical returns deterministic JSON for content plus its visibility list,
// used both to compute Digest and to detect a no-op republish.
//
// Canonical 为 content 及其可见范围返回确定性 JSON，用于计算 Digest 及检测一次
// 无实质变化的重复发布。
func Canonical(id string, content Content, visibleTenantIDs []string) ([]byte, error) {
	if err := Validate(id, content); err != nil {
		return nil, err
	}
	if len(visibleTenantIDs) > MaxVisibleTenants {
		return nil, fmt.Errorf("workflowtemplate: template %q has too many visible tenants", id)
	}
	tenants := make([]string, len(visibleTenantIDs))
	copy(tenants, visibleTenantIDs)
	sort.Strings(tenants)
	doc := struct {
		Content
		VisibleTenantIDs []string `json:"visible_tenant_ids,omitempty"`
	}{Content: content, VisibleTenantIDs: tenants}
	body, err := json.Marshal(doc)
	if err != nil || len(body) > MaxContentBytes {
		return nil, fmt.Errorf("workflowtemplate: template %q content exceeds limit", id)
	}
	return body, nil
}

// Digest identifies content and visibility independently of map iteration
// order or tenant list ordering.
//
// Digest 标识内容与可见范围，不受映射遍历顺序或租户列表排序影响。
func Digest(id string, content Content, visibleTenantIDs []string) (string, error) {
	body, err := Canonical(id, content, visibleTenantIDs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// RevisionInfo is immutable publication metadata for one template revision.
// / RevisionInfo 是一个模板版本的不可变发布元数据。
type RevisionInfo struct {
	TemplateID string    `json:"template_id"`
	Revision   int64     `json:"revision"`
	Digest     string    `json:"digest"`
	CreatedAt  time.Time `json:"created_at"`
	ActorID    string    `json:"actor_id"`
	RollbackOf int64     `json:"rollback_of,omitempty"`
}

// Snapshot carries one complete published template revision.
// / Snapshot 携带一个完整已发布的模板版本。
type Snapshot struct {
	RevisionInfo
	Content
	// VisibleTenantIDs is the tenant allow list captured at this revision. An
	// empty list means every tenant may see and run this template; rolling
	// back to an earlier revision restores that revision's own list along
	// with its content, not just its graph.
	//
	// VisibleTenantIDs 是本版本捕获的租户允许列表。空列表意味着所有租户都能看到并
	// 运行本模板；回滚到较早版本时，恢复的是那个版本自己的列表，与其内容一并恢复，
	// 而不只是它的图。
	VisibleTenantIDs []string `json:"visible_tenant_ids,omitempty"`
}

// Applied is one Gateway replica's locally activated bundle, never an
// inferred fleet acknowledgement. Unlike modelroute.Applied there is no
// single revision to report — a replica holds many independently versioned
// templates — so BundleDigest fingerprints the whole set instead.
//
// Applied 是一个 Gateway 副本本地已启用的整包，从不推断机群确认。与
// modelroute.Applied 不同，这里没有单一版本号可报告——一个副本持有多个各自独立
// 版本化的模板——因此 BundleDigest 对整个集合取指纹。
type Applied struct {
	Mode          string    `json:"mode"`
	TemplateCount int       `json:"template_count"`
	BundleDigest  string    `json:"bundle_digest"`
	AppliedAt     time.Time `json:"applied_at"`
	CheckedAt     time.Time `json:"checked_at"`
	Error         string    `json:"error,omitempty"`
}

// ReplicaStatus is one Gateway's current template-sync observation.
// / ReplicaStatus 是单个 Gateway 当前的模板同步观测。
type ReplicaStatus struct {
	Applied
	ReplicaID   string    `json:"replica_id"`
	GeneratedAt time.Time `json:"generated_at"`
}

// BundleDigest fingerprints a set of revisions independently of their order,
// so two replicas holding the same templates always report the same value
// regardless of pull order or map iteration.
//
// BundleDigest 为一组版本取指纹，不受其顺序影响，因此两个持有相同模板集合的副本，
// 无论拉取顺序或映射遍历顺序如何，永远报告相同的值。
func BundleDigest(revisions []RevisionInfo) (string, error) {
	sorted := make([]RevisionInfo, len(revisions))
	copy(sorted, revisions)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TemplateID < sorted[j].TemplateID })
	type entry struct {
		TemplateID string `json:"template_id"`
		Revision   int64  `json:"revision"`
		Digest     string `json:"digest"`
	}
	entries := make([]entry, len(sorted))
	for i, r := range sorted {
		entries[i] = entry{TemplateID: r.TemplateID, Revision: r.Revision, Digest: r.Digest}
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

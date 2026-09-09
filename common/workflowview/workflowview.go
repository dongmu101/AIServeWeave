// Package workflowview is the contract for two things a Gateway replica knows
// about workflows: the templates it has registered, and the runs currently in
// its job table.
//
// It is a second contract package alongside common/nodeview rather than an
// extension of it, because the two answer to different audiences. A node
// inventory is infrastructure and belongs to no tenant; a job belongs to
// exactly one, and every read of one is scoped by tenant id at the point the
// question is asked.
//
// What a job view does not carry is where it ran. The node id, the runtime id
// and the resolved model are all deliberately absent: nodes are an operator
// view by the same reasoning that keeps them off the tenant Admin API, and
// scheduler.Candidate.Model is documented as something the client never
// learns — it is what a routing alias resolved to, and telling a caller would
// undo the alias. A job answers "what happened to my run", not "which machine
// served it".
//
// workflowview 包是 Gateway 副本就工作流所知的两件事的契约：它已注册的模板，以及它
// 的 job 表中当前的运行。
//
// 它是与 common/nodeview 并列的第二个契约包，而不是对后者的扩展，因为两者面向不同的
// 受众。节点清单是基础设施，不属于任何租户；而一个 job 恰好属于一个租户，对它的每一次
// 读取都在提问的那一刻就按租户 id 限定了范围。
//
// job 视图不携带的东西是「它在哪里运行」。节点 id、运行时 id 与解析后的模型都刻意缺席：
// 让节点成为运维视图的那套理由，同样适用于这里；而 scheduler.Candidate.Model 的文档写明
// 客户端从不得知它——那是路由别名解析出来的结果，告诉调用方就等于取消了别名的意义。job
// 回答的是「我那次运行怎么样了」，而不是「哪台机器服务了它」。
package workflowview

import (
	"encoding/json"
	"time"

	"AIServeWeave/common/workflowtemplate"
)

// Dependencies, NodeDependency and ModelDependency are aliases onto
// common/workflowtemplate: a template author's declared dependency list
// carries no graph position and is not the security-sensitive part of a
// template (see Template's own doc comment on Graph), so the same shape
// serves both the publication contract and this read-only catalogue view.
//
// Dependencies、NodeDependency 与 ModelDependency 是指向
// common/workflowtemplate 的别名：模板作者声明的依赖清单不携带图位置，也不是
// 模板中安全敏感的那部分（见 Template 自己关于 Graph 的文档注释），因此发布契约
// 与这份只读目录视图可以共用同一个形状。
type (
	Dependencies    = workflowtemplate.Dependencies
	NodeDependency  = workflowtemplate.NodeDependency
	ModelDependency = workflowtemplate.ModelDependency
)

// TemplateCatalog is one replica's registered workflow templates.
//
// TemplateCatalog 是一个副本已注册的工作流模板。
type TemplateCatalog struct {
	ReplicaID   string     `json:"replica_id"`
	GeneratedAt time.Time  `json:"generated_at"`
	Templates   []Template `json:"templates"`
}

// Template is one registered workflow, as far as a caller needs to know it.
//
// The ComfyUI graph is not here and must never be: the repository's security
// rule puts a full workflow JSON in the same class as an API key and a
// prompt. What a caller needs is the menu — which workflows exist and what
// they accept — and that is what this carries.
//
// Template 是一个已注册的工作流，只到调用方需要知道的程度。
//
// ComfyUI 图不在这里，也绝不能在这里：仓库的安全规则把完整的工作流 JSON 与 API key、
// 提示词归为同一类。调用方需要的是菜单——有哪些工作流、它们接受什么——而这正是本结构
// 所携带的。
type Template struct {
	ID          string  `json:"id"`
	Description string  `json:"description,omitempty"`
	Inputs      []Input `json:"inputs"`
	// Outputs are the artifacts a template's author declared it produces.
	// Like Dependencies, this is a P03 addition: structurally checked
	// declarations, not something the Gateway verifies a run actually
	// produced.
	//
	// Outputs 是模板作者声明本模板产出的产物。与 Dependencies 一样，这是 P03 新增
	// 的字段：经结构性校验的声明，而不是 Gateway 核实过某次运行确实产出了的东西。
	Outputs []Output `json:"outputs,omitempty"`
	// Dependencies are the custom nodes and model checkpoints the template's
	// author declared it needs (P03). They are shown for the same reason
	// Inputs are: a caller deciding whether a template is usable needs to
	// know what it depends on, even though nothing here confirms any
	// connected node actually has it installed.
	//
	// Dependencies 是模板作者声明本模板所需的自定义节点与模型 checkpoint
	// （P03）。展示它们的理由与展示 Inputs 相同：调用方在判断一个模板是否可用时，
	// 需要知道它依赖什么，尽管这里的任何内容都不确认某个已连接节点确实装了它。
	Dependencies Dependencies `json:"dependencies,omitempty"`
	// Version is opaque and empty for a template this Gateway loaded from a
	// local file. A template published through the control plane (P03)
	// carries its revision number as a string here — the same value a job
	// submitted against it records as its own WorkflowVersion.
	//
	// Version 是不透明的，本 Gateway 从本地文件加载的模板留空。经控制面发布
	// （P03）的模板在这里携带其版本号的字符串形式——与针对它提交的 job 记录为自身
	// WorkflowVersion 的值相同。
	Version string `json:"version,omitempty"`
	// VisibleTenantIDs is the tenant allow list a control-plane publication
	// (P03) set for this template; empty means every tenant may see and run
	// it, which is also what a file-loaded template always reports. This
	// field exists for an aggregator or an operator view to reason about
	// visibility — a tenant-facing menu is expected to have already been
	// filtered by it before this Template ever reaches a tenant's response.
	//
	// VisibleTenantIDs 是控制面发布（P03）为本模板设置的租户允许列表；为空意味着
	// 所有租户都能看到并运行它，文件加载的模板也总是报告这个状态。这个字段是为了
	// 让聚合器或运维视图能推理可见范围——面向租户的菜单预期在本 Template 抵达
	// 租户的响应之前，就已经按它过滤过了。
	VisibleTenantIDs []string `json:"visible_tenant_ids,omitempty"`
	// Valid reports whether the template passed its own validation when the
	// replica loaded it. A replica refuses to start on an invalid template,
	// so this is true in practice — it is carried anyway so an operator page
	// showing a catalogue is showing a checked one, not an assumed one.
	//
	// Valid 报告该模板在副本加载它时是否通过了自身的校验。副本遇到无效模板会拒绝启动，
	// 因此实践中它为 true——之所以仍然携带，是为了让展示目录的运维页面展示的是一份
	// 经过检查的目录，而不是一份被假定为如此的目录。
	Valid bool `json:"valid"`
	// ValidationError is empty when Valid. It is the template's own error
	// text, which names ids and fields — never graph contents.
	//
	// ValidationError 在 Valid 时为空。它是模板自身的错误文本，会点出 id 与字段名
	// ——绝不含图的内容。
	ValidationError string `json:"validation_error,omitempty"`
}

// Input declares one substitutable value, as a caller sees it.
//
// The graph position an input writes — its node and field — is not here. A
// caller substitutes by name; where that name lands is the template author's
// business, and it is one more piece of graph structure with no reason to
// leave the Gateway.
//
// Input 声明一个可替换的取值，以调用方看到的样子。
//
// 输入所写入的图位置——它的节点与字段——不在这里。调用方按名字替换；那个名字落在哪里
// 是模板作者的事，而它是又一块没有理由离开 Gateway 的图结构。
type Input struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	// Default is the value used when the caller omits this input. It is the
	// template author's own literal, and it is shown because a caller
	// deciding whether to send a value needs to know what they get if they
	// do not.
	//
	// Default 是调用方省略本输入时所用的取值。它是模板作者自己写下的字面量，展示它
	// 是因为调用方在决定要不要传值时，需要知道不传会得到什么。
	Default   json.RawMessage `json:"default,omitempty"`
	MaxLength int             `json:"max_length,omitempty"`
	Min       *float64        `json:"min,omitempty"`
	Max       *float64        `json:"max,omitempty"`
}

// Output declares one produced artifact, as a caller sees it (P03).
//
// The node that produces it is not here, for the same reason an Input's node
// and field are not: it is graph structure, and it is the template author's
// business, not the caller's.
//
// Output 声明一个产出，以调用方看到的样子（P03）。
//
// 产出它的节点不在这里，理由与 Input 的节点和字段不在这里相同：它是图结构，是模板
// 作者的事，不是调用方的事。
type Output struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// JobPage is one replica's view of one tenant's runs.
//
// It is one tenant's because the request that produced it named a tenant: a
// replica never renders a job table across tenants, so a bug in an aggregator
// cannot turn into one tenant reading another's runs.
//
// JobPage 是一个副本眼中某一个租户的运行。
//
// 它属于某一个租户，因为产生它的那次请求点名了一个租户：副本从不渲染跨租户的 job 表，
// 因此聚合器里的一个缺陷，无法演变成一个租户读到另一个租户的运行记录。
type JobPage struct {
	ReplicaID   string    `json:"replica_id"`
	GeneratedAt time.Time `json:"generated_at"`
	TenantID    string    `json:"tenant_id"`
	Jobs        []Job     `json:"jobs"`
	// Truncated reports that the replica's job table dropped older runs to
	// stay within its bound. It is what stops a short list from reading as a
	// quiet week.
	//
	// Truncated 报告该副本的 job 表为了守住上限而丢弃了较早的运行。正是它让一份很短
	// 的列表不至于被读成「这一周很清闲」。
	Truncated bool `json:"truncated"`
}

// Job is one submitted workflow run.
//
// Job 是一次已提交的工作流运行。
type Job struct {
	ID         string `json:"id"`
	WorkflowID string `json:"workflow_id"`
	// State is the last state this Gateway observed, not the run's state now.
	//
	// A Gateway learns that a run advanced only when somebody asks about it:
	// the submitter polling GET /v1/jobs/{id}, or holding its event stream
	// open. Nothing reconciles in the background. So a submitter that stopped
	// asking leaves its run recorded as pending or running for as long as the
	// entry survives, however long ago the backend actually finished.
	//
	// UpdatedAt is therefore not decoration either: it is when this state was
	// observed, and it is the only thing that separates "still running" from
	// "nobody has looked since". A consumer that renders State without it is
	// asserting something this Gateway never claimed.
	//
	// State 是本 Gateway 最后一次观测到的状态，不是这次运行此刻的状态。
	//
	// Gateway 只有在有人来问的时候才知道一次运行有了进展：提交方轮询
	// GET /v1/jobs/{id}，或者持续挂着它的事件流。没有任何东西在后台做对账。因此一个不再
	// 询问的提交方，会让它的运行在条目存活期间一直被记录为 pending 或 running，无论后端
	// 实际上早在多久之前就已完成。
	//
	// 因此 UpdatedAt 同样不是装饰：它是这个状态被观测到的时刻，也是区分「仍在运行」与
	// 「从那以后没人看过」的唯一依据。一个脱离它去渲染 State 的消费方，是在断言一件本
	// Gateway 从未声称过的事。
	State string `json:"state"`
	// QueuePosition is the backend's own queue depth for this run, when it
	// reported one.
	//
	// QueuePosition 是后端就这次运行报告的队列深度（如果它报告过）。
	QueuePosition int `json:"queue_position,omitempty"`
	// ErrorSummary is the sanitized failure text. runtime.HealthReport's rule
	// applies to it: no credentials, no headers, no payloads.
	//
	// ErrorSummary 是已脱敏的失败文本。runtime.HealthReport 的规则对它同样适用：
	// 不含凭据、请求头或负载。
	ErrorSummary string    `json:"error_summary,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	// UpdatedAt is when State was last observed — see State.
	//
	// UpdatedAt 是 State 最后一次被观测到的时刻——见 State。
	UpdatedAt time.Time `json:"updated_at"`
	// ArtifactIDs are the public ids minted for this run's outputs. They are
	// ids only: an artifact is fetched from the Gateway's data plane with the
	// tenant's own API key, which no console holds.
	//
	// ArtifactIDs 是为这次运行的产出铸造的公开 id。它们只是 id：产物要用租户自己的
	// API Key 从 Gateway 数据面获取，而任何控制台都不持有那个 key。
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
}

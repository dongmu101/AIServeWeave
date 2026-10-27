# P2：图像生成映射到 ComfyUI + Responses 持久会话 + 多模态输入边界设计

本文档交付 STATUS.md「P2：后续扩展」的复合条目：「OpenAI-compatible 图像生成映射到受控 ComfyUI 模板，以及 Responses 持久会话和更多多模态输入」。三个子功能规模与风险差异极大，本文档先核实三者在代码里的现状，再各自划定边界；其中**图像生成子项本轮已实际落地**（见下方「二、图像生成：已交付」），Responses 持久会话与多模态输入本轮**只做边界设计，不含新代码**，与 [A01](2026-09-14-a01-scope-reliability-design.md)、[A04](2026-09-15-a04-direct-mode-boundary-design.md)、[A05](2026-09-15-a05-scheduling-extension-breakdown-design.md)、[P2 API 兼容边界](2026-09-16-p2-api-compat-boundary-design.md) 同一先例。

## 一、核实结果

### 1.1 图像生成：设计意图已明确写在 README，代码里此前完全空白

README.md:392（本轮已更新为指向本功能，见下）此前已写明设计意图：「对于简单的文生图场景，可以把 OpenAI-compatible `POST /v1/images/generations` 映射到管理员指定的 ComfyUI 工作流模板。复杂工作流仍使用 AIServeWeave Workflow API」。核实前代码里对此零实现：`chat.go`/`responses.go`/`models.go` 均未提及图像或 DALL-E。依赖机制均已齐备：

- ComfyUI 适配器（`common/runtime/workflow/comfyui/runtime.go`）的 `Submit`/`Status`/`Artifacts`/`OpenArtifact` 分别对应提交、轮询、列举产物与流式打开产物字节，`Status` 读取的是后端 `GET /history`/`GET /queue`，是权威源；`Subscribe` 明确按文档是尽力而为。
- `common/workflowtemplate` 的 `Input`/`Output` 是纯粹的通用键值绑定：没有"图像生成"这种预置模板类型，`Output.Type` 是自由文本，校验只检查其 `Node` 存在，不核对运行期是否真的产出该类型。
- `service/aiServeWeaveGateway/workflow` 的 `Template.Bind` 是既有的、模板无关的输入绑定实现，复用它即可。
- `runtime.ArtifactRef`（`common/runtime/types.go`）只有 `RunID`/`Filename`/`Subfolder`/`Type` 四个字段，**没有 `Node` 字段**——这是本轮实现遇到的核心结构性缺口，见下节。

### 1.2 Responses 持久会话：完全没有持久化层

`httpapi/responses.go` 的 `unsupported()`（320-337 行）显式拒绝 `previous_response_id`/`store`/`background` 三个字段，注释明确写着"responses are not stored and each request is scheduled independently"。全仓库对 `PreviousResponseID`/`previous_response_id` 的引用只出现在 `responses.go`/`responses_handler.go` 自身。`common/` 与 `service/aiServeWeaveControlPlane/` 都没有任何按 `response_id` 建键的存储、内存表或数据库表；唯一相邻的持久化是 `request_logs`（P09/C28），但那是脱敏审计记录（`request_id`/`tenant_id`/`endpoint`/`status_code`/`duration_ms`），不含请求体或响应体，无法充当会话存储。

可复用的既有模式是 Job 持久化（J01-J08）：控制面拥有表 + 内部 CRUD API（`internal/logic/jobs.go`、`internal/handler`）、Gateway 侧 `controlplaneclient.JobsClient` 类型化 HTTP 客户端、一个小适配器（`GatewayPersister`）把富类型接口收窄成 `httpapi` 自己声明的 `JobPersistClient`/`JobRecoveryClient` 接口以打破导入环。一个持久化 Responses 会话功能理应遵循同一形状。

### 1.3 多模态输入：`ChatMessage.Content` 是纯字符串，Responses 端已对图片硬拒绝

`common/runtime/types.go:183-189` 的 `ChatMessage` 只有 `Content string`，Chat/Responses/隧道 wire 格式全部共用这一个类型。`httpapi/chat.go` 的 `chatMessageJSON.Content` 直接解码为字符串，遇到 OpenAI 视觉请求的数组分片形式会直接 JSON 解码失败。`httpapi/responses.go` 的 `inputItemText` 已经能解析数组分片，但对 `image_url` 等非文本分片显式返回错误（"needs a canonical representation this repository does not have yet"）。

`runtime.Capability` 枚举里已经有 `CapabilityVision`，但只在 Ollama 的模型能力发现里出现（标注某模型"支持视觉"），请求路径完全没有消费它——因为没有图像内容可送。P04 的 `OPERATION_INPUT_UPLOAD` 上传机制结构性地只属于 `WorkflowRuntime`（`common/runtime/runtime.go`），不属于 `InferenceRuntime`，且唯一调用方是工作流提交端点，与 Chat/Responses 完全不相交。三个后端适配器（Ollama/vLLM/SGLang）共用的 `oaibase.Base.Chat` 把 `Content` 当纯字符串透传给后端，没有任何一条路径能真正把图像字节送到一个视觉模型。

设计文档 `2026-09-16-p2-api-compat-boundary-design.md` §9.3 已经把"`ChatMessage.Content` 结构化改造"列为 Anthropic API 兼容边界的后续任务 3，并明确建议"依赖任务 1（Anthropic Messages v1）先验证 v1 范围是否已经足够，不应在此之前抢先排期"——这是因为该改造影响 Chat/Responses/Anthropic 三个前门共用的核心类型，以及三个后端适配器的请求序列化，是全仓库里改动面最大、最容易引入跨前门不一致的一类变更。本文档采纳并引用该结论，不重复决策。

## 二、图像生成：已交付

### 2.1 管理员配置：约定优于配置

新增 Gateway CLI flag `-images-workflow-id`（默认空，禁用该端点）与 `-images-generation-timeout`（默认 120s）。**不新增输入名映射 flag**：被配置的模板必须按固定约定声明名为 `prompt` 的必填字符串输入，可选声明 `width`/`height` 整数输入（用于承接调用方的 `size` 参数）。理由：`-workflow-source controlplane` 模式下模板可被控制面热替换（P03），独立于 Gateway 重启；一张静态 CLI 级映射表会在模板重新发布后悄悄与其 `Inputs` 失步，而固定、写进文档的命名约定让两者从结构上保持一致。

**启动期校验，非请求期**：`main.go` 在 `workflowHandle` 就绪后立即调用 `validateImagesWorkflow`（`workflows.go`）——若 `-images-workflow-id` 非空但模板不存在、缺少必填 `prompt` 输入、或没有至少一个 `Type == "image"` 的 `Output`，进程直接启动失败，与 `configureWorkflows` 对一条坏 `-workflow-templates` 路径的既有 fail-fast 纪律一致。这是**一次性静态检查**，不会在控制面热替换目录时重新运行——已知缺口，见第 2.5 节。

### 2.2 请求/响应映射与同步执行模型

`POST /v1/images/generations` 的处理器（`httpapi/images.go`、`httpapi/images_handler.go`）：

1. 解码 `{prompt, n, size, response_format, model, quality, style}`；`n>1`、`quality`、`style`、未声明 `size` 均按名字拒绝（400），沿用 `responsesRequest.unsupported()` 已确立的"具名拒绝、不默默丢弃"纪律。
2. `tpl.Bind(inputs, nil)` 把 `prompt`（及可选 `width`/`height`）代入图，复用既有的通用绑定实现，不新增任何模板相关代码。
3. `h.sched.SubmitWorkflow(ctx, ...)`——与 `submitRun` 完全相同的调度入口，让本端点与普通 `/v1/workflows/{id}/runs` Job 在同一个（通常 `Exclusive` 的）ComfyUI 节点上正确排队、竞争（P2 有界排队天然生效），而不是绕开调度另开一条路径。
4. 提交后立刻把 job 记入 `h.jobs`（在轮询之前，与 `submitRun` 相同顺序），使 `GET /v1/jobs/{job_id}` 与 J06 的路由绑定恢复在这次同步等待超时时依然可用。
5. `pollWorkflowToTerminal` 用注入的 `runtime.Clock` 驱动的有界轮询循环反复调用 `WorkflowStatus`（不是 `Subscribe`——History 是权威源，`Subscribe` 按文档是尽力而为，且轮询能用假 Clock 确定性测试），固定间隔 500ms（包内常量，不是运维旋钮），直到终态或 `-images-generation-timeout` 超时。
6. 超时答 504，运行本身在服务端继续，不被取消。
7. 终态非成功：`WorkflowFailed` 且 `OutOfMemory` 答一个独立的 `out_of_memory` 错误码；否则 `generation_failed`；`WorkflowCancelled` 答 `cancelled`。
8. 成功后 `WorkflowArtifacts` 列举产物，`selectImageArtifacts` 筛选出 `Type == "output"` 且扩展名已知（`.png .jpg .jpeg .webp .gif .bmp`）的产物——见 2.4 节的结构性理由。零个合格产物答 500 `no_image_produced`。
9. `response_format=b64_json`（默认）：`OpenArtifact` 流式读取，`io.LimitReader` 有界读取（`MaxImageResponseBytes = 32 MiB`，刻意远小于 ComfyUI 适配器自身 `MaxArtifactBytes` 的 512 MiB——把一个足尺寸产物 base64 膨胀进一个 JSON 响应体不是同步处理器该做的事，这是 AGENTS.md「任何一跳都不得无界缓冲」允许的、刻意且有界的例外），base64 编码入响应。
10. `response_format=url`：把产物记入 `h.jobs.recordArtifacts` 并提醒既有的后台 `jobPersister`，直接返回既有的 `GET /v1/artifacts/{artifact_id}` 路径——不需要新的同步持久化路径，因为 `downloadArtifact` 本就有「优先读持久副本、失败回退实时节点拉取」的逻辑，覆盖异步持久化器还没赶上的窗口。

`n>1`（批量生成）首版明确排除：要么在一个已经有界的同步超时内串行重提交、放大最坏延迟与节点争用，要么依赖一个本端点不假定存在的模板专属批量约定；按仓库「不做投机性灵活性」的一贯做法，先以 `n=1` 交付。

### 2.3 错误映射

`SubmitWorkflow`/`WorkflowStatus`/`WorkflowArtifacts`/`OpenArtifact` 的 `*runtime.RuntimeError`/`scheduler.ErrNoCapableNode` 一律走既有 `handleDispatchError`，与其余端点共用同一套分类，不新增 `classify` 分支。

### 2.4 一处真实发现：`runtime.ArtifactRef` 无法与模板声明的 `Output.Node` 关联

设计阶段发现：`runtime.ArtifactRef` 只携带 `{RunID, Filename, Subfolder, Type}`，**没有 `Node` 字段**，因此运行期无法把 `WorkflowArtifacts()` 返回的一个产物，与模板声明的某个具体 `Output.Node`/`Output.Type == "image"` 做结构性关联——`Output.Type` 本就只在模板发布/加载时被校验非空，从未与任何节点运行期产出的东西核对过（`common/workflowtemplate.Validate` 的文档注释本身也承认这一点）。因此实现只能做"启动期声明校验（模板必须声明至少一个 `image` 类型输出）+ 运行期扩展名约定（`output` 分区 + 已知图片扩展名）"的折衷，而不是真正的运行期结构化查找。一个非图像的 `output` 分区产物（例如模板作者自己 Save 节点存下的调试 JSON）会被静默跳过而不是报错，这是更有用的默认行为，但也是一处已知限制。彻底修复（给 `ArtifactRef` 加 `Node` 字段，或让模板声明"主图像输出节点"）不在本轮范围内。

### 2.5 附带修复：`runtime.WorkflowStatus.OutOfMemory` 从未真正跨隧道传输

为本端点的 `out_of_memory` 错误码编写集成测试时，发现一个此前已存在、与本功能无关但直接阻塞该错误码工作的真实缺陷：`common/tunnelwire/wire.go` 的 `WorkflowStatusToProto`/`WorkflowStatusFromProto`，以及 `api/proto/tunnel/v1/tunnel.proto` 的 `WorkflowStatus` 消息，此前都不包含 `out_of_memory` 字段——Agent 端 ComfyUI 适配器算出的 `OutOfMemory` 判断从未真正越过隧道传到 Gateway，意味着 A06 交付的 `gateway_workflow_job_oom_total` 指标在生产环境里**从未被真正观测到过一次 true**（此前的 A06 默认测试套件都是直接构造 `runtime.WorkflowStatus{OutOfMemory: true}` 在内存里传给 `jobStore.update`，没有一个测试真正走过隧道 proto 序列化这一跳，因此没有被发现）。本轮已修复：`tunnel.proto` 新增 `bool out_of_memory = 6`，`go generate ./api/...` 重新生成，`WorkflowStatusToProto`/`FromProto` 补上该字段的双向转换，并在 `common/tunnelwire/wire_test.go` 新增一个显式覆盖该字段的往返测试用例，防止再次回归。

### 2.6 文件改动清单

- `service/aiServeWeaveGateway/httpapi/images.go`、`images_handler.go`（新增）
- `service/aiServeWeaveGateway/httpapi/httpapi.go`（`Config` 新增两个字段、挂载新路由）
- `service/aiServeWeaveGateway/httpapi/metrics.go`（新增 `EndpointImagesGenerations` 标签值）
- `service/aiServeWeaveGateway/main.go`（新增两个 flag、调用启动期校验、写入 `httpCfg`）
- `service/aiServeWeaveGateway/workflows.go`（新增 `validateImagesWorkflow`）
- `api/proto/tunnel/v1/tunnel.proto`、`tunnel.pb.go`（生成）、`common/tunnelwire/wire.go`（`OutOfMemory` 修复）
- 测试：`httpapi/images_test.go`（纯函数单元测试）、`httpapi/images_handler_test.go`（端到端集成测试）、`workflows_test.go`（启动期校验）、`common/tunnelwire/wire_test.go`（新增往返用例）
- 未改动：`scheduler/workflow.go`、`workflow/binder.go`、`common/workflowtemplate` ——全部复用现状

### 2.7 已知缺口（不阻塞交付，如实记录）

- 产物-节点运行期关联缺失（2.4 节），依赖 `ArtifactRef` 结构性扩展才能根治。
- 启动期校验是一次性的，控制面热替换模板（P03）剥离所需字段不会被立即发现，只会在下一次请求时表现为 400/500，不是启动失败。
- `n=1` only；`quality`/`style`/负向提示词等参数未实现。
- 无真实 ComfyUI/GPU 环境验证——与 A06 同一先例，默认测试套件（假节点）作为交付依据，真实后端验证留作后续。

## 三、Responses 持久会话：后续任务拆分（本轮不实现）

沿用 Job 持久化的既有形状：

1. 控制面新表（候选名 `response_turns`）：按 `response_id`（主键）、`tenant_id`、`previous_response_id`（可空，指向上一轮）建索引；保存渲染出的规范化消息列表（而非原始请求体），供续接时重建对话历史。
2. 控制面内部 CRUD API（仿 `internal/logic/jobs.go` 与 `/internal/v1/jobs*` 路由）：创建一轮、按 `response_id` 读取一轮及其祖先链。
3. Gateway 侧 `controlplaneclient.ResponsesClient` + 一个 `GatewayPersister` 风格的小适配器，满足 `httpapi` 自己声明的窄接口（打破导入环，同 Job 持久化）。
4. `responses.go` 的 `unsupported()` 对 `store`/`previous_response_id` 的拒绝分支改为：`store=true` 时在终态后异步落库（与 Job 持久化"应答不等待控制面确认"的既有取舍一致）；`previous_response_id` 非空时先从控制面读出该轮及其祖先链，重建 `messages` 前缀，再把当前轮的 `input` 追加在后面。

未决问题（留给后续任务实现阶段拆分）：

- 续接一轮会话时，是否需要把"同一个最终用户的多轮对话"与调度节点亲和绑定？README 已有的"会话亲和"是 A05 未交付项，[A05 设计文档](2026-09-15-a05-scheduling-extension-breakdown-design.md) 第 1.6/八.5 节已经记录过这处缺口，需要互相引用而不是在这里重复设计——多轮对话续接本身不强制要求同节点（每轮仍是无状态的 Chat 请求，只是消息前缀更长），但若未来要做"同一会话优先回到同一节点"式的缓存亲和优化，两份文档需要协同。
- `store=true` 但从不 `previous_response_id` 续接的场景（纯审计式留存）是否要单独降级处理，还是与"会续接"的场景共用同一张表——待实现阶段决定。

## 四、多模态输入：不建议在本轮排期

不新增设计方案：本文档核实确认，`ChatMessage.Content` 结构化改造是三个前门（Chat/Responses/未来的 Anthropic）与三个后端适配器共用的核心类型变更，`2026-09-16-p2-api-compat-boundary-design.md` §9.3 已经给出"应先验证 Anthropic v1 范围是否已经足够"的排期建议，本文档采纳该结论，不重复设计,也不建议抢先排期。一个风险更小的候选（只在 Responses 端为具备 `CapabilityVision` 的模型透传 `image_url` 分片，不生成图像）本身仍需要 `ChatMessage.Content` 至少局部结构化，无法完全绕开核心类型变更，因此同样归入该后续任务，不单独拆分。

## 五、已知缺口清单

- Responses 持久会话与多模态输入均只有边界设计，不含代码；具体实现任务留待独立排期。
- 图像生成的产物-节点关联缺失（2.4 节）、启动期校验非持续（2.5 节小节的姊妹缺口——2.1 节末段）均已如实记录，不阻塞图像生成子项的交付判定。
- `OutOfMemory` 隧道传输修复（2.5 节）范围已限定为最小改动（补一个 proto 字段 + 两个转换函数 + 一个回归测试），未评估是否存在其他类似"Go 类型有字段、wire 格式缺失"的隐藏缺口——那需要一次独立的全字段审计，不在本轮范围内。
- `POST /v1/images/generations` **未接入 P09/C28 的请求检索**（`httpapi/requestlog.go` 的 `requestLogEndpoint` 是绑定 `request_logs.endpoint` 数据库列的封闭枚举）：新增一个枚举值需要评估是否要给这张已稳定的表加迁移，超出本轮范围，因此这次生成请求暂不出现在 `/console/requests`/`/operator/requests` 的检索结果里——功能本身不受影响，只是这一类请求的可检索历史留空，记作已知缺口。

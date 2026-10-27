# P2-3 Anthropic Messages、Ollama 原生 API、音频转录/翻译与 rerank 的能力与兼容边界

本文档交付根 STATUS.md「P2：后续扩展」第一条：Anthropic Messages、Ollama 原生 API、音频转录/翻译和 rerank，逐项定义能力与兼容边界。M4 里程碑对 P2 全部条目的统一要求是「各项先明确场景、依赖与验收，再拆分实施」（STATUS.md:19），本文档据此执行。

范围边界：**本项以设计文档为主，不含新代码或新测试**，与 [A01](2026-09-14-a01-scope-reliability-design.md)、[A04](2026-09-15-a04-direct-mode-boundary-design.md)、[A05](2026-09-15-a05-scheduling-extension-breakdown-design.md)、[P2-1 GPU 资源感知调度设计](2026-09-15-p2-resource-aware-scheduling-design.md) 同一先例。A01 设计文档第 104 行已经做过一次核实，结论是这四项（连同 OpenAI-compatible 图像生成）「都出现在 README 的规划范围里，但没有任何强制执行的代码，属于 STATUS.md 的 P2 范畴」——本文档不重新核实这一点，而是在其基础上继续往下：**四项各自需要多大改动、能复用哪些既有架构、边界卡在哪里**，这是 A01 未展开的部分。凡属阅读代码得出的结论标注文件:行号；仅在文档散文中出现、找不到代码依据的计入第十节缺口清单。

## 一、现状盘点

### 1.1 四项能力今天在代码里完全不存在

对 `transcri`/`whisper`/`rerank`/`audio` 四个关键词做全仓库不分大小写检索，`*.go` 文件零命中。仅有的命中全部是文档散文或不相关的同名词：

- `README.md:203-209`「规划中的扩展协议范围」列出 `Anthropic POST /v1/messages`、`Ollama 原生 API`、`音频转录和翻译`、`rerank`、`OpenAI-compatible 图像生成`——这是路线图，不是已实现范围。
- `README.md:220-235` 的 `DeploymentCapability` ASCII 树包含 `audio`/`video`/`image_generation`，但**没有 `rerank` 条目**——与它上方 209 行的散文列表自相矛盾。[A01 设计文档](2026-09-14-a01-scope-reliability-design.md):80 已经指出这棵树是「尚未落地的伪代码草案」，本文档因此不采信树的内容为现状依据，只记录这处不一致本身（第十节）。
- `common/runtime/README.md:1233`「后续演进」一节把「OpenAI Responses、Completions、音频和 rerank 等更多能力」列为「首期稳定后再独立规划」——这是刻意延后，不是遗漏，与 A01 的核实结论一致。
- `service/aiServeWeaveGateway/httpapi/uploadformat.go:16,47`、`service/aiServeWeaveGateway/README.md:122` 提到的 "audio" 是工作流输入文件的 MIME 类型嗅探（`.mp3`/`.wav`/`.ogg`/`.flac` → `audio/*`），服务于 ComfyUI 工作流输入上传（STATUS.md 的 P04），与转录能力无关。
- `common/runtime/workflow/comfyui/runtime.go:709` 的 "images, gifs, audio, video" 是 ComfyUI 节点输出的分类标签，同样与转录能力无关。

### 1.2 OpenAI 前门的架构已经把「wire 格式转换」与「调度」彻底分离

`service/aiServeWeaveGateway/httpapi/chat.go:79-140` 定义纯 wire 层结构体（`chatCompletionRequest`/`chatMessageJSON`/`toolJSON` 等），`chatCompletionRequest.toRuntime()`（`chat.go:93-140`）转换成 `runtime.ChatRequest`；handler（`chat.go:236-296`）解析、校验后直接调用 `h.sched.Chat(...)`/`h.sched.ChatStream(...)`，**没有任何 OpenAI 特有的东西流入 `Scheduler`**。`embeddings.go:44-58` 是同一模式。`Scheduler.Chat`/`Embed`/`ChatStream`（`scheduler/scheduler.go:214,239,270`）只接受 `runtime.ChatRequest`/`runtime.EmbeddingRequest`/`runtime.Capability`，对 wire 格式一无所知。

**这意味着第二个 wire 前门（Anthropic Messages、Ollama 原生 API）理论上可以只在 `httpapi` 层新增一组 wire 结构体 + 转换函数，复用同一条 `Scheduler.Chat`/`ChatStream`/`Embed` 调用路径，不需要改动调度逻辑**——前提是对方的协议语义能装进今天的 `runtime.ChatRequest`/`ChatResponse`/`ChatEvent` 形状（第 1.5 节列出这套形状今天长什么样，缺口留给第四、五节）。

### 1.3 Ollama 原生 API：今天的 Ollama 适配器走的是 OpenAI 兼容协议，不是 Ollama 自己的协议

`common/runtime/ollama/runtime.go` 包文档明确写着：「身份、健康和模型元数据来自 Ollama 的原生 `/api` 端点，推理复用 `runtime/openai` 里共享的 OpenAI 兼容客户端——Ollama 的原生 generate 协议被刻意地没有实现」。原生端点只用于身份/发现：`/api/version`（`runtime.go:21,270-280`）、`/api/tags`（`runtime.go:22,282-288`）、`/api/show`（`runtime.go:23,346-352`）。推理路径 `Chat`/`ChatStream`/`Embed`（`runtime.go:110-127`）全部委托给 `common/runtime/openai`，真正打到的是 `POST /v1/chat/completions`（`openai/chat.go:204`）、`POST /v1/embeddings`（`openai/embedding.go:37`）——全仓库没有一处调用 `/api/chat`、`/api/generate` 或 Ollama 原生的 `/api/embeddings`。

**这解决了 STATUS.md 措辞的一处歧义**：Ollama 适配器的现状回答的是「Agent 怎么连一个本地/远程 Ollama 后端」（刻意选择 OpenAI 兼容协议，不该被本项动摇），STATUS.md 这一条说的是另一件事——**Gateway 对外暴露第二套协议前门**，让说 Ollama 自己协议（而不是 OpenAI 协议）的调用方也能打到 Gateway，与 README.md:213「vLLM/Ollama 已提供 OpenAI 兼容能力，因此第一版以 OpenAI 协议作为主要对外协议」这句话描述的是同一枚硬币的两面：Agent 对后端选 OpenAI 协议是「减少适配成本」的既定选择，本项要做的是在 Gateway 对客户端的一侧**再开一扇门**，不影响 Agent 侧那个选择。

### 1.4 音频转录/rerank 需要给 `InferenceRuntime` 新增方法——这是破坏性接口变更

`common/runtime/capability.go:11-25` 列出全部 15 个 `Capability` 常量（`CapabilityChat`、`ChatStream`、`Completions`、`Embeddings`、`Responses`、`Vision`、`Tools`、`ParallelToolCalls`、`StructuredOutput`、`Reasoning`、`WorkflowExecution`、`WorkflowEvents`、`WorkflowCancel`、`ArtifactRead`、`InputWrite`），没有一个与音频或 rerank 相关。`InferenceRuntime`（`common/runtime/runtime.go:16-22`）只有 `ListModels`/`Chat`/`ChatStream`/`Embed` 四个方法——每种能力对应接口上一个专用类型方法，没有「任意能力」的通用调用路径（`oaibase.Base.Chat` 通过 `BeginRequest` + `CapabilitySet.Require` 做能力门禁，`oaibase.go:139-146`）。新增 rerank 或转录能力**必须**在这个接口上加新方法，意味着每一个实现者（`ollama`、`vllm`、`sglang` 等基于 `oaibase` 的适配器，以及测试用的 `runtimetest.Runtime`）都要跟进，即使只是返回「不支持」。

`WorkflowRuntime.UploadInput`（`runtime.go:40-52`）的文档注释把它的用途锁定在「为后续 `Submit` 的 `Template` 暂存字节」（STATUS.md 的 P04），`InputUploadResult.InputRef` 只对产生它的那个 ComfyUI 适配器有意义（`types.go:378-390`）——它是 `WorkflowRuntime` 专属机制，服务的是异步 Job 提交场景，不是同步推理调用；音频转录如果要接收音频体，需要 `InferenceRuntime` 自己长出一个携带 `io.Reader` 的方法，而不是挪用 `UploadInput`。

### 1.5 `runtime.ChatRequest`/`ChatMessage` 今天的形状是纯 OpenAI 风格

以下是 `common/runtime/types.go` 里今天实际存在的字段（不涉及跨协议映射规则，只陈述现状，为第四节的差距分析打底）：

- `ChatRequest`（106-134）：`Model`、`Messages []ChatMessage`、`Temperature`/`TopP *float64`、`MaxTokens *int`、`Stop []string`、`Seed *int64`、`Tools []Tool`、`ToolChoice string`、`ResponseFormat *ResponseFormat`、`Extra map[string]json.RawMessage`（与已建模字段冲突时拒绝，129-133）。没有专门的 system 字段——system 提示只是 `Messages` 里一条 `Role: "system"` 的消息。
- `ChatMessage`（172-178）：`Role string`、**`Content string`**（单一字符串，不是内容块列表）、`Name`、`ToolCallID`、`ToolCalls []ToolCall`。
- `Tool`/`FunctionDefinition`（139-154）：只有 OpenAI 的 "function" 一种工具类型。
- `ToolCall`/`FunctionCall`（221-230）：`Function.Arguments` 是 JSON 字符串，OpenAI 风格。
- `ResponseFormat`/`JSONSchemaFormat`（158-170）：`Type` 取值 "text"/"json_object"/"json_schema"，OpenAI 结构化输出形状。
- `ChatResponse`（180-187）：单一 `Message`，没有多候选（choices）概念。
- `ChatEvent`/`ChatMessageDelta`/`ToolCallDelta`（189-213）：OpenAI SSE 增量分片形状，工具调用增量按 `Index` 索引。
- `Usage`（215-219）：`PromptTokens`/`CompletionTokens`/`TotalTokens`，没有缓存读写 token 字段。

## 二、场景与目标定义

把 STATUS.md 一句话拆成四个颗粒度、依赖都不同的独立方向，对齐第一节的核实结果：

1. **Anthropic Messages 兼容**：新 wire 前门问题，理论上可复用第 1.2 节的 `Scheduler.Chat`/`ChatStream` 路径，不需要动 `common/runtime` 核心类型——前提是 v1 范围不追求与 Anthropic 协议的完整功能对等（第四节展开）。四项里改动面可能最小。
2. **Ollama 原生 API**：同样是新 wire 前门问题，与 Agent 连接后端 Ollama 的既定选择（第 1.3 节）完全无关，纯粹是 Gateway 对客户端多开一套协议。
3. **rerank**：破坏性接口变更（第 1.4 节），但请求/响应形状简单——纯文本 query + 文档列表，没有二进制流问题。
4. **音频转录/翻译**：同样是破坏性接口变更，且多一层音频体的流式传输设计（不能复用 `UploadInput`，需要 `InferenceRuntime` 自己的机制），四项里工作量最大。

这四者互相独立，可以分别排期、独立交付，不需要等其他三项完成。

## 三、依赖与设计原则

1. **分层职责不因新前门而改变。** AGENTS.md：「隧道层不做能力判断、不做模型路由、不做重试——这三件事分别属于 `runtime` 的能力门禁和 Gateway 的调度器」。Anthropic/Ollama 原生前门只在 `httpapi` 层做一次 wire 转换，能力门禁仍然是 `CapabilitySet.Require`，模型路由仍然是 `Scheduler`/`routing.Table`，这条边界不需要重新设计，只需要在实现时不越界。
2. **破坏性接口变更必须配能力门禁的优雅降级，不能让不支持的适配器编译失败。** rerank/转录新增到 `InferenceRuntime` 后，`oaibase.Base` 这类共享基座可以给出一个默认「不支持」实现（走 `CapabilitySet.Require` 返回 `ErrCapabilityUnsupported`），具体适配器（`ollama`/`vllm`/`sglang`）不需要每个都手写拒绝逻辑，除非它们确实支持。这与 `Chat`/`Embed` 今天的门禁模式（`oaibase.go:139-146`）一致，不是新发明。
3. **音频体的传输必须遵守既有的流式纪律。** AGENTS.md 安全红线「任何一跳都不得无界缓冲」——`OpenArtifact`/`UploadInput` 已经确立「绝不整体缓冲」的先例（`types.go:44-45,341`），转录请求的音频体应该边读边转发给后端，不应该在 Gateway 或 Agent 任何一跳被完整缓冲进内存。
4. **不假设某个具体后端支持某项能力。** README.md:213 提到「vLLM 还可能暴露 Responses、音频、rerank 等端点」，但 `common/runtime/README.md:528` 已经明确警告「首期不因端点出现在新版文档中就自动上报」——新增的能力常量必须通过 `Discover` 的能力探测流程暴露，不能硬编码「vLLM 都支持音频」这类假设。

## 四、Anthropic Messages 兼容边界设计草案

1. **架构复用**：新增一个 `httpapi/anthropic.go`（或类似命名），仿照 `chat.go` 的模式——wire 结构体 + `toRuntime()`/`fromRuntime()` 转换，调用现成的 `h.sched.Chat`/`h.sched.ChatStream`，不改 `Scheduler` 或 `common/runtime`。
2. **v1（纯文本对话）可以零核心类型改动交付**：如果第一版不支持工具调用、不支持图片/文档内容块，Anthropic 的 `messages` 数组可以直接映射进 `[]ChatMessage`（`Content` 都是纯文本），`system` 字段映射成一条 `Role: "system"` 的消息——今天的类型已经够用。
3. **完整功能对等需要跨越的具体形状鸿沟**（只陈述缺口，不预设解法）：
   - `ChatMessage.Content` 是单一字符串（第 1.5 节），Anthropic 的 `content` 是块列表（text/image/tool_use/tool_result 混合）。要无损承载多块内容或图片块，今天的类型做不到，需要评估是否将 `Content` 扩展为结构化块类型——这是一处**跨 OpenAI 与 Anthropic 两个前门共用的核心类型改动**，影响所有现有 OpenAI 前门的调用方和测试，必须与 OpenAI 前门团队共同评估，不能只从 Anthropic 一侧的需求单方面决定。
   - `Tool` 只建模了 OpenAI 的 "function" 一种类型；Anthropic 的工具交互模式是把 `tool_use`/`tool_result` 表达成消息内容的一部分而非独立字段，今天的 `ToolCall`/`ToolCallID` 字段能否表达这种模式需要具体验证，本文档不代为决定。
   - `Usage` 没有缓存 token 字段，Anthropic 的 prompt caching 计数在今天的类型下无法透传，只能丢弃或放进 `Extra`（需要先确认 `Extra` 是否允许非冲突性附加数据，而不仅是"跟已建模字段冲突时拒绝"这一种语义，`types.go:129-133`）。
4. **结论**：一个「纯文本、无工具、无图片」的 Anthropic Messages v1 是四项里最容易交付的一个，可以独立最先排期；追求功能对等则牵出一处需要跨前门评估的核心类型改动，不应该和 v1 绑在一起排期。

## 五、Ollama 原生 API 兼容边界设计草案

1. **定位澄清（重申第 1.3 节）**：这是 Gateway 对客户端的第二套协议前门，与 Agent 连接后端 Ollama 的方式无关，不应该因为本项而重新评估 `common/runtime/ollama` 现有的 OpenAI 兼容选择。
2. **架构复用**：与 Anthropic 一样，新增一层 wire 转换（`/api/chat`、`/api/generate`、`/api/embeddings` 的请求/响应结构体），映射进 `ChatRequest`/`EmbeddingRequest`，复用同一条 `Scheduler` 路径。
3. **明确排除模型管理类端点**：Ollama 原生 API 还包含 `/api/pull`、`/api/create`、`/api/delete`、`/api/show` 等模型生命周期管理端点，这些在本平台的语义完全不同——Gateway 不管理任何节点上的模型文件，那是节点自身与其后端的事（README 现有架构从未赋予 Gateway 这个角色）。**这条边界必须在实现前明确声明并在 API 文档中标注「仅做推理转发，不代理模型管理」**，否则调用方会误以为 `POST /api/pull` 能让 Gateway 帮忙拉模型，实际上没有任何代码路径能做到这件事。
4. **`keep_alive` 等 Ollama 专有参数**：Ollama 原生协议里一些参数（如 `keep_alive` 控制模型在内存里的驻留时间）在今天的调度模型下没有对应语义（Gateway 不管理单个后端进程的内存驻留），需要在实现阶段决定是静默忽略还是显式拒绝，本文档不代为决定。

## 六、音频转录/翻译能力边界设计草案

1. **需要新增的最小集合**：`CapabilityAudioTranscription`（可能还需要 `CapabilityAudioTranslation`，取决于是否把翻译当成独立能力）、`TranscriptionRequest{Audio io.Reader; Filename string; Language *string; ...}`/`TranscriptionResponse{Text string; ...}` 类型、`InferenceRuntime` 新增 `Transcribe(ctx, req) (TranscriptionResponse, error)` 方法（或流式变体，取决于是否要支持流式转录结果）。
2. **二进制体的流式传输是本项独有的复杂度**：第三节第 3 条已经定下原则——不能整体缓冲。具体机制需要新设计（不是复用 `UploadInput`，见第 1.4 节），且要决定这层流式传输在隧道协议上如何表达（是否需要 `tunnel.proto` 新增 Operation，类似 `OPERATION_INPUT_UPLOAD` 的先例），这触及 AGENTS.md「契约唯一源」流程，需要走 proto 改动 + 重新生成。
3. **后端支持现状是空白，不能假设。** 全仓库没有任何适配器连接到语音后端（如 Whisper 兼容服务）；vLLM/SGLang 是否支持音频转录取决于具体部署的模型，必须通过 `Discover` 能力探测暴露（第三节第 4 条），不能硬编码假设。
4. **工作量评估**：四项里最大，同时涉及新能力类型、破坏性接口变更、二进制流式传输设计、可能的 proto 改动，建议放在 rerank（同样是破坏性接口变更但没有流式传输复杂度）验证过「新增 `InferenceRuntime` 方法 + 能力门禁降级」这套模式之后再排期。

## 七、rerank 能力边界设计草案

1. **需要新增**：`CapabilityRerank`、`RerankRequest{Query string; Documents []string; TopN *int}`/`RerankResponse{Results []RerankResult{Index int; Score float64}}` 类型、`InferenceRuntime` 新增 `Rerank(ctx, req) (RerankResponse, error)` 方法。
2. **相比音频转录，没有二进制流问题**——请求体是纯文本 JSON，可以直接走今天 `Chat`/`Embed` 同款的「wire 解析 → runtime 类型 → `Scheduler` 新增 `Rerank` 方法 → 适配器」路径，是四项里除 Anthropic v1 外改动量最可控的一项，且不涉及任何跨前门的核心类型冲突（`RerankRequest`/`Response` 是全新类型，不与 `ChatRequest` 共享字段，没有第四节那种「哪个字段该长什么样」的争议）。
3. **建议作为验证「新增 `InferenceRuntime` 方法」这套破坏性变更模式的第一个试点**：先在 rerank 上走一遍「新增能力常量 → 新增类型 → 接口新增方法 → `oaibase` 默认降级 → 至少一个适配器给出真实实现」的完整流程，验证过程中发现的接口设计问题（比如默认降级的具体写法）可以直接复用到后续的音频转录任务上，避免两个破坏性接口变更并行摸索、互相踩坑。

## 八、验收目标（供未来实现该功能时使用；本项不实现）

1. Anthropic Messages v1（纯文本、无工具、无图片）端到端可用，复用 `Scheduler.Chat`/`ChatStream`，无需改动 `common/runtime` 核心类型；有测试覆盖 wire 转换与流式事件映射，以及 v1 范围之外的输入（工具调用、图片块）被明确拒绝而不是静默丢弃。
2. Ollama 原生 API 的 `/api/chat`、`/api/generate`、`/api/embeddings` 端到端可用；`/api/pull` 等模型管理端点返回明确的「不支持」而不是 404 或误导性的成功响应。
3. `InferenceRuntime` 新增 `Rerank`/`Transcribe` 方法后，全部现有适配器与测试桩（`runtimetest.Runtime` 等）编译通过；不支持某能力的适配器通过 `CapabilitySet.Require` 返回明确的能力不支持错误，而不是编译失败或运行时 panic。
4. 音频转录的二进制体传输有测试证明整个链路不整体缓冲（比照 `OpenArtifact`/`UploadInput` 现有的验证方法）。
5. README.md「规划中的扩展协议范围」与 `DeploymentCapability` ASCII 树之间的不一致（rerank 在树中缺失，第 1.1 节）得到修正，或者明确标注该树是过时草案、不代表实际能力矩阵。

## 九、后续实现任务的拆分建议（不在本文档范围内交付）

1. **Anthropic Messages v1（纯文本）**：独立、改动面最小，可最先排期，不依赖其他三项。
2. **Ollama 原生 API（纯推理端点）**：独立，与任务 1 规模相当，可与任务 1 并行推进。
3. **`ChatMessage.Content` 结构化改造（可选）**：为 Anthropic/OpenAI 完整功能对等服务，影响面最大（跨两个前门的共用核心类型），依赖任务 1 先验证「v1 范围是否已经够用」，不应在验证之前排期。
4. **rerank**：独立的破坏性接口变更，建议作为验证「新增 `InferenceRuntime` 方法」模式的试点（第七节第 3 条），改动集中在 `common/runtime` + 至少一个适配器实现。
5. **音频转录/翻译**：同样是破坏性接口变更，但多一层二进制流式传输设计（可能触及 proto 改动），工作量最大，建议在任务 4 验证完接口扩张模式之后再排期。

任务 1、2 相互独立且可并行；任务 4 是任务 5 的经验前置（不是硬性代码依赖，是"先摸清模式再复用"的顺序建议）；任务 3 依赖任务 1 的验证结果。不建议把四项合并成一次实现，与 P2-1 GPU 设计文档、A05 的先例一致。

## 十、已知缺口清单

1. **具体某个后端（vLLM/SGLang 某版本）是否真的支持音频转录/rerank，需要维护者对照实际部署的模型能力核实**——本文档没有做这类假设，第三节第 4 条已经把「不能硬编码假设」定为设计原则。
2. **Anthropic Messages 协议的工具调用/图片内容块精确映射规则未设计**（第四节第 3 条）——留给任务 3 的实现阶段，可能需要与 OpenAI 前门团队共同评估对 `ChatMessage.Content` 的改动。
3. **Ollama 原生 API 的错误码/HTTP 状态码映射规则未设计**，`keep_alive` 等专有参数的处理方式未定（第五节第 4 条）。
4. **音频转录是否需要 `tunnel.proto` 新增 Operation 尚未设计**（第六节第 2 条）——如果需要，将触及 AGENTS.md「契约唯一源」的 proto 演进流程，是本文档未展开的一处实现细节。
5. **README 的 `DeploymentCapability` ASCII 树内部不一致（rerank 缺失）已发现但未修复**（第 1.1 节，验收目标第 5 条）——本文档只记录，不在本项范围内改 README。

## 十一、与 STATUS.md 的关系

本文档完成 P2 该条要求的「先明确场景、依赖与验收，再拆分实施」：第一节核实四项能力今天完全不存在、确认 OpenAI 前门的 wire/调度分离架构可被新前门复用、澄清 Ollama 原生 API 与 Agent 侧 OpenAI 兼容选择无关、指出音频/rerank 需要破坏性接口变更；第二节把一句话拆成四个独立颗粒度的场景；第三节给出跨四项通用的设计原则；第四至七节分别给出四项的兼容边界设计草案；第八节给出验收目标；第九节拆出五项可独立排期的后续实现任务。本项**不实现 Anthropic Messages、Ollama 原生 API、音频转录/翻译或 rerank 本身**，不新增任何 Go 代码或测试，第十节缺口不阻塞勾选，与 A01/A04/A05/P2-1 同一先例。STATUS.md 该条据此更新为链接本文档，措辞改为「场景与拆分已完成，五项后续实现任务待独立排期」，不改变「这些能力尚未交付」这一事实。

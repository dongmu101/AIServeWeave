# Codex CLI 图像生成经 Agent 接入 Gateway：设计方案

日期：2026-10-16。

状态（2026-10-16）：已实现。阶段 0 实测门槛通过（20/20 命中，见第五节），代码按本文落地；实现时偏离原稿的几处记在第九节。

## 一、目标与非目标

**目标**：调用方用 OpenAI 兼容的 `POST /v1/images/generations` 请求，由 Agent 本机已登录的 Codex CLI（ChatGPT 账户）生成一张图片，字节经隧道回到调用方。凭据仍只留在 Agent。

```text
调用方 ── POST /v1/images/generations {model, prompt}
   ↓
Gateway：鉴权、配额、模型路由 → 选中声明 image_generation 能力的 codex 运行时
   ↓  OPERATION_IMAGE_GENERATE（新增，语义隧道，不是 HTTP 代理）
Agent codexcli：独立临时进程 + ephemeral thread，只开 image_generation 这一项托管工具
   ↓  stdio JSON-RPC
codex app-server → ChatGPT 后端的图像模型
```

**完成标准**：两个租户并发各生成一张图，图片字节、修订后的提示词、用量互不串线；取消、超时、账户额度耗尽都能以固定错误类别返回，Agent 本机不残留图片文件。

**第一版非目标**（按名字拒绝，不静默忽略）：

- `n > 1`、`response_format=url`、图片编辑（`/v1/images/edits`）和变体。
- Responses API 里的 `tools: [{"type": "image_generation"}]` 托管工具。Codex CLI 客户端走这条路，但它要求在 Chat 流里夹带图片，是另一份设计，见第七节。

**只走 Codex 登录，对外模型 ID 由能否选中模型决定**（维护者 2026-10-16 决定）：

- 不引入 OpenAI API Key 后端。按 API 文档，`gpt-image-2.5-flare` 和 `gpt-image-2.5-sunburst` 要用 API Key 认证，在 Images API 的 `model` 字段，或 Responses `image_generation` 工具的 `model` 字段里指定。Codex 用 ChatGPT 登录时，这个工具调用由 Codex 内部生成，目前没找到能指定 `model` 的地方。
- 默认对外只暴露一个 ID `codex-image`，不标具体型号，实际用哪个图像模型由 ChatGPT 后端决定。
- 只有阶段 0 第 6 项找到办法、能让 Codex 按指定模型生图，才额外暴露 `gpt-image-2.5-flare` / `gpt-image-2.5-sunburst`，并按 ID 精确路由。不同 ID 必须真的对应不同的上游模型，不能只是换个名字。

## 二、已核实的事实与缺口

2026-10-16 用本机 `codex-cli 0.162.1` 执行 `codex app-server generate-json-schema --experimental` 并检查 `codex features list`，结论如下。**这些只是 schema 层面的事实，字段的实际取值、大小和时序必须靠阶段 0 实测确认。**

| 项目 | 事实 | 对设计的影响 |
| --- | --- | --- |
| 生图结果 | `ThreadItem` 新增变体 `imageGeneration`：`id`、`status`、`result`（string，必填）、`revisedPrompt`、`savedPath`（绝对路径，可空）、`transparentBackground`、`failure` | 图片要么从 `result` 解码，要么从 `savedPath` 读取，只能二选一，见三.3 |
| 失败类型 | `ImageGenerationFailure` 目前只有 `usageLimitExceeded`（带 `limitId`、`resetsAt`） | 映射为 `runtime.ErrorRateLimited`，可提示重试时间 |
| 能力查询 | `modelProvider/capabilities/read` 返回 `{imageGeneration, webSearch}` 两个布尔值 | Probe/Discover 用它决定是否声明能力，不调用推理 |
| 增量事件 | `item/*` 通知里没有图片的部分进度事件，结果只出现在 `item/completed` | 生图期间流上可能长时间静默，现有空闲超时（默认 60s）会误杀 |
| 参数 | `thread/start`、`turn/start` 都没有 size/quality/n/图像模型字段；`thread/start.config` 可以覆盖任意配置项，但二进制里没找到图像模型相关的配置键 | 尺寸、质量等无法结构化传递，第一版按名字拒绝 |
| 模型目录 | 2026-10-16 只读查询本机账户：`model/list`（含 `includeHidden`）返回 9 个对话模型（`gpt-6.1-sol`、`gpt-5.6-terra` 等），**没有** `gpt-image-*`，所有条目的 `modelSpecialty` 都是 null；`capabilities/read` 返回 `imageGeneration: true` | 账户有生图权限，但 `gpt-image-2.5-flare`/`sunburst` 怎么选中目前没有证据 |
| 二进制内的模型名 | 只在内置 imagegen 技能文档里出现 `gpt-image-2`、`gpt-image-1.5`、`gpt-image-1`、`gpt-image-1-mini`，那是走 OpenAI API Key 的 CLI 后备路径，不是 ChatGPT 登录下的内置工具；没有 `flare`/`sunburst` 字样 | 不能根据这些字符串推断出选模型的机制 |
| API 文档 | GPT Image 2.5 有 `gpt-image-2.5-flare`（偏速度）和 `gpt-image-2.5-sunburst`（偏质量）两个 ID。Images API 用 `model` 选择；Responses API 在 `image_generation` 工具对象里用 `model` 选择。参数还有 size（16 的倍数，边长 ≤ 3840）、`quality`（low/medium/high/xhigh/max/auto）、`background`、`output_format`、`partial_images`。都要 API Key | Codex 登录路径能否透传工具的 `model`，是阶段 0 第 6 项 |
| 媒体省略开关 | 二进制里有 `omit_app_server_notification_media` 配置键 | 可能导致通知里的 `result` 被省略，阶段 0 需确认默认值 |
| Feature | `image_generation` 为 stable，默认开启 | 当前适配器在 [probe.go:186](../../../service/aiServeWeaveAgent/codexcli/probe.go) 显式 `--disable` |
| 协议读取 | [protocol.go](../../../service/aiServeWeaveAgent/codexcli/protocol.go) 的 `read` 只放行 6 种 item 类型，`imageGeneration` 会被判为 `ErrUnexpectedTool` | 需要按运行模式放行，Chat 路径保持原样拒绝 |
| 帧上限 | `maxFrameBytes = 1 MiB`；一张 1024² PNG 的 base64 通常在 1.5–4 MiB | 如果图片在 `result` 里，`item/completed` 这一帧必然超限 |

Gateway 侧现状：

- [images_handler.go](../../../service/aiServeWeaveGateway/httpapi/images_handler.go) 只走 ComfyUI 工作流（`SubmitWorkflow` → 轮询 → `OpenArtifact`），解析了 `model` 字段但从不使用。
- `runtime.InferenceRuntime` 没有生图方法。`runtime.MessagesRuntime` 是"可选接口 + dispatch 时类型断言"的先例（[dispatch.go](../../../service/aiServeWeaveAgent/tunnel/dispatch.go) 的 `OPERATION_MESSAGES` 分支），新能力可以照这个模式加，不必让所有后端实现空方法。

## 三、方案

### 3.1 选型：新增可选运行时接口与隧道操作

考虑过三条路：

| 方案 | 做法 | 结论 |
| --- | --- | --- |
| A. 新可选接口 `ImageRuntime` + `OPERATION_IMAGE_GENERATE` | 一次请求生成一张图，字节分块回传 | **采用** |
| B. 把 codex 包装成 `WorkflowRuntime` | 复用现有 images handler 的 Submit/Status/Artifacts 流程 | 否决：Codex 每请求一个进程、无持久 run，要伪造 run ID 和跨请求产物存储，Agent 因此多出状态 |
| C. 图片塞进 `ChatEvent` | 复用 Chat 隧道 | 否决：Chat 事件是有界小帧，夹带几 MB 图片会破坏"事件逐条小帧转发"的约定，且 Chat 调用方并不期待图片 |

### 3.2 共享契约（`common/runtime`、`api/proto`、`common/tunnelwire`）

```go
// common/runtime/image.go（新文件）
type ImageRequest struct {
    Model    string // 图像模型 ID（默认 codex-image）；必须命中 Agent 发布的图像模型目录
    Prompt   string // 有长度上限，不写日志
    TenantID string // 归属标识，不是凭据
    KeyID    string
}

type ImageMeta struct {
    ContentType   string // image/png 等，Agent 按魔数判定，不信任扩展名
    Size          int64  // 随后所有 Data 的精确总字节数
    RevisedPrompt string
    Usage         *Usage
}

// 第一个事件只带 Meta，之后每个事件只带一段 Data（≤ MaxImageChunkBytes）
type ImageEvent struct {
    Meta *ImageMeta
    Data []byte
}

type ImageRuntime interface {
    Runtime
    GenerateImage(ctx context.Context, req ImageRequest) (Stream[ImageEvent], error)
}
```

- 新增 `CapabilityImageGeneration Capability = "image_generation"`，同步更新 `common/runtime/README.md`。
- `tunnel.proto` 新增 `OPERATION_IMAGE_GENERATE = 15`、`ImageGenerateRequest{model, prompt, tenant_id, key_id}`、`ImageGenerateMeta{content_type, size, revised_prompt, usage}`。
- 响应是 `ShapeStream`：每个 `DataChunk` 是一个 `ImageGenerateEvent{oneof meta | data}`，第一个是 `ImageGenerateMeta`，之后是字节分块（每块 ≤ 256 KiB），最后 `ResponseEnd`。这样 Agent 和 Gateway 可以直接复用 `forwardStream` / `newResponseStream`。不复用 `ResponseHeaders`，因为它的注释写明只给 `ARTIFACT_OPEN` 用，而这里还要带修订提示词和用量。
- proto 与 runtime 类型的转换只在 `tunnelwire` 里写（安全红线）。请求里没有 URL、路径、Header 或凭据字段，从结构上做不到"让 Agent 读任意文件"。

**槽位**：生图占用 `SLOT_CLASS_INFERENCE`，字节也在同一条流上回传，不另开 BULK 槽。理由是单张图有硬上限（32 MiB，与现有 `MaxImageResponseBytes` 一致），两步式（先返回引用、再走 BULK 下载）会要求 Agent 在进程退出后继续保管图片文件并做 TTL 清理，平添状态。代价是一次生图会在几 MB 回传期间多占一个推理槽，实测若成为瓶颈再改两步式。

### 3.3 Agent：`codexcli` 实现 `GenerateImage`

**进程参数**：新增 `imageServerArgs(dir)`，和 `appServerArgs` 只差一处：不 `--disable image_generation`。其余 shell、MCP、插件、`view_image`、web search 一律保持关闭。Chat 路径的参数**不变**，Chat 请求仍然生不了图。

**回合驱动**（新函数 `runImage`，与 `runChat` 并列）：

1. `initialize` → `account/read`（仍只接受 ChatGPT 登录）→ `modelProvider/capabilities/read`，`imageGeneration` 为 false 就直接返回 `ErrorCapability`，不发起推理。
2. `thread/start`：`dynamicTools` 为空；`baseInstructions` 固定为"用图像生成工具按下面的描述生成恰好一张图，不提问，不调用其他工具"；调用方 prompt 作为 `turn/start` 的唯一用户输入，不拼进系统指令。
3. 读帧：放行 `imageGeneration` item；收到第一个 `status` 为完成的 `imageGeneration` 就取图并立即结束进程组，不等 `turn/completed`，从结构上排除"一次请求生成多张"。
4. 回合结束仍没有图 → `ErrorUpstream`（固定文案"no image produced"，不回显模型文本）。`failure.type == usageLimitExceeded` → `ErrorRateLimited`。
5. 出现动态工具回调或审批请求，和现在一样视为 `ErrUnexpectedTool`。

**取图**，二选一由阶段 0 实测决定：

- **`savedPath` 优先**：仅当 `filepath.Clean` 后位于本请求 0700 临时目录内（或阶段 0 确认的固定生成目录内）、`Lstat` 为普通文件且非符号链接、大小 ≤ 32 MiB 时才打开，按魔数判定类型，读完删除。路径不满足条件一律报 `ErrProtocol`，不尝试读取。
- **`result` 兜底**：只在生图模式把单帧上限提到 48 MiB（足以容纳 32 MiB 的 base64），Chat 模式仍为 1 MiB。这一帧会整帧进内存，是有界的例外，和 Gateway 侧 `MaxImageResponseBytes` 同一性质；解码后按块写入，不再复制一份完整字节。

**期限**：生图期间可能长时间没有帧，`GenerateImage` 不用 `StreamIdleTimeout`，只受 `RequestTimeout`（默认 5 分钟）和 Gateway 下发的 deadline 约束，全部走注入的 `runtime.Clock`。

**并发**：第一版与 Chat 共用 `runtime.Limiter`。生图占用时间比文本长，若阶段 0 显示单次超过 60 秒，再考虑增加单独的 `max_image_concurrent`。

**图像模型目录**：图像模型在模型目录里是独立条目，只带 `CapabilityImageGeneration` 一项能力，不带 Chat 系能力。对话模型也不带生图能力。这样 Chat 路由永远不会选中图像模型，生图路由也不会选中对话模型。

- **默认**：`inspect` 在 `capabilities/read` 返回 `imageGeneration: true` 时发布一个条目 `codex-image`，返回 false 时不发布。这个 ID 是固定常量，不来自本地 YAML。
- **可选**：阶段 0 第 6 项如果找到选模型的机制，再发布 `gpt-image-2.5-flare` / `gpt-image-2.5-sunburst`，并保留 `codex-image` 作为"不指定型号"的入口。候选机制按优先级排：
  1. `model/list` 返回带图像专长（`modelSpecialty`）的条目，`thread/start` 的 `model` 填这个 ID 就能选中；
  2. `thread/start.config` 里有某个配置键，能设置内置 `image_generation` 工具的 `model`；
  3. 需要更新版本的 Codex CLI。这种情况下 Agent 按最低版本号做门禁，版本不够就只发布 `codex-image`。

发布型号 ID 的前提是 app-server 自己报告这些 ID 可用，不允许在本地 YAML 里凭空声明。Probe/Health 仍然不调用推理。

**负责调用工具的对话模型**：内置生图工具由某个对话模型发起调用。第一版用本机默认的对话模型，调用方不能指定，避免 `model` 一个字段同时承担两种含义。

**分发**：[dispatch.go](../../../service/aiServeWeaveAgent/tunnel/dispatch.go) 新增 `OPERATION_IMAGE_GENERATE` 分支，`rt.(runtime.ImageRuntime)` 断言失败返回 `ErrorCapability`，与 `OPERATION_MESSAGES` 写法一致。`runtime_id` 白名单校验不变。

### 3.4 Gateway：前门路由

`imagesGenerations` 在现有校验之前先做一次分流：

| 请求 | 走向 |
| --- | --- |
| `model` 精确等于某个节点发布的图像模型 ID（默认只有 `codex-image`），或经 `modelroute` 别名解析到这样的 ID | 新的 Codex 路径，只在发布了**该 ID** 的节点之间调度 |
| `model` 为空，或不是任何已发布的图像模型，且配置了 `-images-workflow-id` | 现有 ComfyUI 路径，行为逐字节不变 |
| 两者都不满足 | 保持现有 404 `endpoint_not_configured` |

在默认情况下，调用方发 `model: "gpt-image-2.5-flare"` 不会命中 Codex：这个 ID 没有被任何节点发布，请求走上表第二、三行。运维也**不应该**在 `modelroute` 里把 `gpt-image-2.5-*` 设成指向 `codex-image` 的别名，否则就是用错误的型号名称标注实际输出，README 里要写明这一点。阶段 0 如果找到了选模型的机制，型号 ID 之间同样互不替代，不会悄悄换成另一个模型。

现有调用方（OpenAI SDK 通常会带 `model: "dall-e-3"` 之类）不受影响，因为这些名字不在图像模型目录里。`/v1/models` 会列出 `codex-image`，调用方可以据此发现。

Codex 路径的参数规则：

- `prompt` 必填，长度上限沿用 `MaxRunRequestBytes` 的请求体约束。
- `n` 只接受 1；`response_format` 只接受空或 `b64_json`，`url` 按名字拒绝；`size`/`quality`/`style`/`background` 一律按名字拒绝。
- 新增 `scheduler.GenerateImage`，候选筛选复用现有能力门禁，排队与背压语义和 Chat 一致。
- 读取隧道响应时边读边做 base64 编码写进 JSON 响应体，总字节超过 `MaxImageResponseBytes` 就中止并返回 `ErrorResponseTooLarge`。
- 用量：计一次请求，`Usage` 有值时按现有 token 配额扣减。新增 `/v1/images/generations` 的 backend 维度指标标签（`comfyui`/`codex`）。
- 响应里 `data[0].revised_prompt` 填 Codex 返回的修订提示词（OpenAI 响应本来就有这个字段）。

## 四、安全与资源边界

- prompt、修订提示词和图片字节都不写日志、不进错误文本，统一走 `runtime.Redact`。
- 凭据边界不变：Gateway 只传模型名和 prompt，Agent 不读取、不转发 `auth.json`。
- Agent 不新增任何监听端口；`savedPath` 只在校验通过后打开，请求里不可能出现路径。
- 不留盘：临时目录随进程回收删除；如果阶段 0 发现 Codex 把图存到 `CODEX_HOME` 下的固定目录，`GenerateImage` 读完必须删除该文件，并补一个测试覆盖"取消或失败时也删除"。否则多个租户的图片会在 Agent 主机上不断累积。
- 所有缓冲都有上限：单帧 48 MiB（仅生图模式）、单图 32 MiB、分块 256 KiB。
- **账户与额度（已确认，2026-10-16）**：图像生成消耗的是 Agent 所在机器 ChatGPT 账户的生图额度。维护者确认该账户为公司采购、供公司内多个部门共同使用，这种共享方式合规。仍需注意的是生图额度比文本更紧，所有租户共用同一份额度，耗尽时统一返回 429。

## 五、实施阶段

**阶段 0：实测门槛**（会消耗本机账户额度，单独放 `experiments/codex-image-generation/`，不进默认测试）

记录以下事实，写进 `RESULTS.md`：

1. `result` 的编码方式（base64 还是其他）和实际字节数。
2. `savedPath` 是否有值、落在哪个目录、是否受 `cwd` 和 `sandbox: read-only` 影响。
3. `item/started` 到 `item/completed` 的耗时，期间有没有其他帧。
4. 只给文本指令时，模型调用生图工具的稳定性（至少 20 次，统计命中率）。
5. 触发失败的形状：内容策略拒绝、额度耗尽（如果能安全复现的话）。
6. **（可选）选中 `gpt-image-2.5-flare` / `gpt-image-2.5-sunburst` 的机制**：按 3.3 列的三个候选逐一核对。确认方法是分别用两个 ID 各生成一次，看返回里有没有能区分上游模型的证据，例如 `imagegen_request_id`、生成元数据或可观察到的输出差异。只看到"请求没报错"不算证据。

**放弃条件**：第 4 项命中率明显不稳定（比如低于 90%），或图片既不在 `result` 也不在可校验的 `savedPath` 里。任意一条成立就停在这里，向维护者汇报，不继续编码。第 6 项不成立不阻塞：只发布 `codex-image`，型号 ID 留到 Codex CLI 提供选择机制以后再做。

**阶段 1：契约**：`common/runtime` 新类型和能力、`tunnel.proto` 新操作（`go generate ./api/...`）、`tunnelwire` 编解码及往返测试。

**阶段 2：Agent**：`codexcli.GenerateImage`、`imageServerArgs`、按模式放行 item 和调整帧上限、`savedPath` 校验、`dispatch.go` 分支。

**阶段 3：Gateway**：`scheduler.GenerateImage`、`imagesGenerations` 分流、指标、配额。

**阶段 4：联调与文档**：`e2e` 假节点联调，加 `AISW_CODEX_LIVE=1` 真实用例；同步更新 Agent、codexcli、Gateway、`common/runtime` 四份 README 和根 README 的协议兼容说明。

## 六、测试计划

默认测试全部用假 app-server 子进程，不访问网络。

| 层 | 用例（表驱动） |
| --- | --- |
| `tunnelwire` | 请求/Meta 往返；空字段；超长 prompt；未知 operation |
| `codexcli` | 正常出图（`result` 与 `savedPath` 两种来源）；回合结束无图；连续两张图只取第一张并回收进程；`usageLimitExceeded`；`capabilities/read` 为 false；超大帧和超大图；非法 base64；`savedPath` 在根目录外、是符号链接或不存在；中途出现工具回调或审批请求；取消；注入时钟触发总期限；限流器满额；读完以及失败时都删除文件 |
| `dispatch` | 非 `ImageRuntime` 返回 `ErrorCapability`；白名单外的 `runtime_id`；Meta 先于字节；分块大小 |
| `httpapi` | 分流三种情况；`codex-image` 只调度到发布了该 ID 的节点；未发布的 `gpt-image-2.5-*` 不会命中 Codex；别名解析到图像模型 ID；图像模型不出现在 Chat 候选里；`n`、`url`、`size` 等按名字拒绝；响应超限；`revised_prompt` 回填；ComfyUI 路径回归保持原样 |
| 并发 | 两个租户同时生图，字节与修订提示词不串线，用 `-race` 跑 |

协程泄漏沿用各包 `main_test.go` 的 `TestMain` 统一断言。

## 七、后续（不在本方案范围）

- **Responses 托管工具 `image_generation`**：让原版 Codex CLI 客户端经 Gateway 调用时也能生图。需要在 `ChatEvent` / Responses SSE 里表达 `image_generation_call` 输出项（schema 中已有 `ImageGenerationCallResponseItem`），涉及流式大帧，单独设计。
- `response_format=url`：把图片写入控制面产物存储后返回链接，依赖产物持久化扩展到非工作流来源。
- 尺寸、质量控制：等 app-server 暴露结构化参数再做，不用自然语言提示去模拟。
- 图片编辑与图片输入：需要先放开 Codex 的图片输入（现在被明确拒绝）。

## 八、待维护者确认

1. `size`/`quality` 第一版按名字拒绝，还是作为自然语言提示附加到 prompt（结果不保证）。本方案默认拒绝。
2. 第一版是否需要 `response_format=url`。本方案默认不做。
3. ~~生图后端与模型 ID~~：已定（2026-10-16）。只走 Codex 登录，不引入 API Key 后端；默认 ID 为 `codex-image`，型号 ID 视阶段 0 第 6 项的结果而定。`codex-image` 这个名字如需调整，请在实现前告知。
4. ~~第四节的合规风险是否可以接受~~：已定（2026-10-16）。账户为公司采购、供多部门共用，合规。

## 九、阶段 0 实测结果与实现偏差

### 9.1 阶段 0（2026-10-16，macOS，`codex-cli 0.162.1`，对话模型 `gpt-6.1-sol`）

探测脚本模拟 Agent 的隔离参数（只放开 `image_generation`），共做了 21 次真实生成：1 次摸清帧形状，20 次统计命中率。每次取证后立即删除 `generated_images` 下由探测生成的文件。用户原有的 37 个生成目录没有动，测试前后数量一致。

| 项 | 结果 |
| --- | --- |
| 1. `result` | 标准 base64 编码的 PNG。base64 长 1.36–5.60 MB，解码后 1.02–4.20 MB。帧本身比 base64 只多几百字节 |
| 2. `savedPath` | 每次都有值，指向 `CODEX_HOME/generated_images/<thread_id>/exec-<uuid>.png`，与 `result` 解码后的字节完全一致。不受 `cwd` 和 `sandbox: read-only` 影响，进程退出后**仍保留在磁盘上** |
| 3. 时序 | 约 16–23 秒后出现 `item/started`（模型先推理），到 `item/completed` 之间静默 15.7–45.4 秒（中位数 26.1），期间没有任何帧。端到端 34.8–72.1 秒（中位数 47.4）。图片完成后模型还会再写一段 `agentMessage` |
| 4. 命中率 | **20/20**，每次恰好一张图，没有出现工具回调、审批请求或错误通知。提示词包括只有一个词 `cat` 的情况和抽象描述 |
| 5. 失败形状 | 未能安全复现额度耗尽或内容策略拒绝，按 schema 实现映射（见 3.3） |
| 6. 选模型机制 | **不成立**。`model/list`（含 `includeHidden`）没有 `gpt-image-*`，`modelSpecialty` 全为 null；用"未识别配置会触发 `configWarning`"这一点逐个试探 `image_generation.model`、`tools.image_generation.model`、`image_generation_model`、`imagegen.model`、`image_model`，全部被判为无法识别（只做 `initialize`，不消耗额度）。按设计只发布 `codex-image` |
| `omit_app_server_notification_media` | 同样被判为无法识别。默认情况下 `result` 有内容 |

放弃条件都不成立，进入编码。完成后又用真实 Codex 跑了一次正式实现（`codexcli.GenerateImage`）：得到 1.65 MB 的 PNG，分 7 块回传，`revised_prompt` 252 字节，`generated_images` 目录数量前后一致。

### 9.2 与原稿不同的实现决定

- **取图只用 `result`**。阶段 0 证明它每次都有内容，而且与 `savedPath` 的文件逐字节相同。所以 Agent 不打开 app-server 报回的任何路径，`savedPath` 的校验逻辑也不需要了。清理时，用 `thread/start` 返回并校验过的线程 ID（只允许 `[A-Za-z0-9_-]`，≤ 128）拼出 `CODEX_HOME/generated_images/<thread_id>`，在进程回收之后整个删除。成功、失败、取消都走这一步。
- **大帧只对 `imageGeneration` 的 `item/completed` 放开**，上限 48 MiB；生图进程里的其他帧仍是 1 MiB。
- **图像模型显式否定聊天能力**。`runtime.Intersect` 会用运行时级别的能力补齐模型没提到的项，所以 `codex-image` 把各项聊天能力标为 `unsupported`，对话模型把 `image_generation` 标为 `unsupported`。只"不带"是不够的，联调时正是因此发现 Chat 请求会被派发给 `codex-image`。
- **`tenant_id`/`key_id` 可选**。Agent 不使用这两个字段，所以没有配置 API Key 的部署也能生图。两端只限制长度。
- **`size`/`quality`/`background` 接受 `auto`**。它等同于不传，不构成"静默丢弃"；其余取值仍按名字拒绝。另外拒绝 `output_format`、`output_compression`、`moderation`、`partial_images`、`stream`。
- **Gateway 不边读边做 base64**。先把字节读进有界缓冲（32 MiB，必须与元数据的 `size` 一致），再整体编码，与 ComfyUI 路径的 `renderImageData` 做法相同。这样流中途失败时可以返回明确的错误状态码，而不是一个已经发出 200 却被截断的 JSON。
- **调度重试**：读到元数据之前的可重试失败换下一个候选；读到元数据之后不再重试，因为已经消耗了生图额度。
- **未做**：指标的 backend 维度标签（端点级指标仍由现有中间件按 `/v1/images/generations` 记录），以及按 token 扣减配额（生图在拿到 `thread/tokenUsage/updated` 之前就结束了，`Usage` 恒为空）。生图仍按一次请求计入现有的请求级配额。
- **阶段 0 结果直接写在本节**，没有另建 `experiments/codex-image-generation/`。探测用的是一次性脚本，不进仓库。


# Studio 编排「视频生成」节点（火山方舟）

状态：已实现（2026-11-28 与用户确认方案并交付代码；未与真实火山方舟联调，交付范围与已知限制见根 STATUS.md）

## Context

Studio 编排目前有对话、图像生成和 ComfyUI 模板节点，还不能生成视频。这次接入火山方舟的视频生成任务接口（创建、查询、列表、取消/删除），用 `github.com/volcengine/ark-runtime-go` SDK 调用，新增节点类型 `video_generation`。

火山的 API Key 有两种来源：
- 运维在运维平台配一个平台 Key；
- 租户 owner/admin 在 Console 配自己的 Key。

已确认的决定：
- **Key 选用**：租户配了自己的 Key 就用自己的；没配时用平台 Key，所有租户都能用。
- **计费**：本期接入计费账本。用平台 Key 时，按 Ark 返回的 `usage.completion_tokens` 和模型单价扣费；用租户自己的 Key 时记录用量，金额为 0。
- **节点输入**：文生视频，加上首帧、尾帧和参考图。图片引用上游的 image 输入，或者生图节点、模板节点的 `image`。
- **列表接口的用途**：做成 Console 的视频任务页（租户查看自己 Key 下的任务）和运维平台的任务页（运维查看平台 Key 下的任务），两处都能取消或删除任务。

调研结论（会影响设计）：
- SDK `ark-runtime-go` 最新为 v0.24.0，要求 Go 1.20（仓库是 1.27），会间接引入 `volcengine-go-sdk`。
  - 调用方法：`CreateContentGenerationTask` / `GetContentGenerationTask` / `ListContentGenerationTasks` / `DeleteContentGenerationTask`。
  - 任务状态多一个 `expired`。
  - **SDK 默认会对 5xx/429 重试 2 次，创建任务重试可能重复建单**，所以创建这一步要关掉重试。
- `video_url` 只有 24 小时有效，必须下载后转存到对象存储。
- 引擎的单节点超时默认 5 分钟（`flow/flow.go:78-83`），视频任务会超时；整个运行的超时是 30 分钟。
- 计费只按 `node_id` 决定免单，没有表示「租户自有 Key」的标志。`UsageRecord.Attributes` 已有这个字段，但还没有任何地方写入它。
- 没有定价的模型按 0 计，标为 `price_source=none`。
- Ark 只给 Key 不能限定租户，用平台 Key 列任务会看到所有租户的任务。所以 Console 的任务页只在租户配了自己的 Key 时显示。

## 方案

### 0. 规格
- 写 `.scratch/ark-video-node/spec.md`，内容是本方案的定稿，`Status: ready-for-agent`。
- 实施按下面的第 1–7 节在同一轮完成，未另拆 issue 文件。

### 1. `common/arkvideo`（新包，控制面和 Gateway 共用）
- 封装 SDK，类型是 `Client{Create, Get, List, Cancel}`，用 `arkruntime.NewClientWithApiKey` 加上 `WithBaseUrl` / `WithHTTPClient` 构造。
  - 创建用一个单独的客户端，设 `WithRetryTimes(0)`；查询、列表、删除保留重试。
- 只暴露本仓库自己的类型：
  - `TaskRequest{Model, Prompt, Images[]{Role, DataURL}, Resolution, Ratio, Duration, GenerateAudio, Watermark, ReturnLastFrame, ExpiresAfter}`
  - `Task{ID, Model, Status, VideoURL, LastFrameURL, CompletionTokens, TotalTokens, ErrorCode, CreatedAt, UpdatedAt, ...}`
  - SDK 的 `Opt*` 类型不外泄。
- 错误分类，不转述上游的原文（遵守 SECURITY.md）：
  - `*model.APIError` 按状态码映射为 `auth`、`invalid_request`、`rate_limited`、`content_rejected`、`not_found`、`upstream`；
  - `*model.RequestError` 映射为 `network`。
- `Probe(ctx)`：用 `List(page_size=1)` 检查 Key 是否可用，保存配置前调用。
- 下载结果 `Fetch(ctx, url)`：
  - 只接受 https，拨号时复用 `common/objectstore` 里的内网地址限制拨号器；
  - 不设大小上限，按流读取（见 memory：稳定性不靠加限制）。

### 2. 控制面：火山方舟凭据（照 tenant_storage 的模式）
- **迁移**：base `0023_ark_credentials.sql`（MySQL 与 PostgreSQL 各一份，列带中文注释）。
  - 列：`scope VARCHAR(40) PK`（取值为租户 id，或 `model.PlatformScope`）、`api_key_ciphertext TEXT`、`key_last4`、`base_url`（只有平台这一行能设，租户固定用默认的北京地址）、`models TEXT`（JSON 数组）、`version`、`updated_by`、`created_at`、`updated_at`。
  - 同步修改 `schemacheck.go` 里的 models 列表，并重新生成 `sql/init.sql`。
- **配置**：新增 `Ark{MasterKey, MasterKeyFile}`，环境变量 `AISW_ARK_MASTER_KEY`，留空即关闭。
  - 复用 `ProviderSecrets` 的 `sealWith` / `openWith`（`logic/nodeproviders.go`），AAD 为 `"aisw-ark-credential/v1\x00"+scope`。
  - 接线照 `svc/servicecontext.go:531-545` 写。
- **逻辑** `logic/arkcredentials.go`，参照 `logic/tenantstorage.go`：
  - 权限：租户侧要求 owner/admin（`canManageKeys`），平台侧要求 `requirePlatformActor`。
  - 保存：`api_key` 只写不读，省略表示保留原值；保存前调用 `arkvideo.Probe`，失败时返回 `ark_unreachable` 及原因。
  - 审计新增 `ark.credential.update` / `ark.credential.delete`，详情只记 host、last4 和模型数。
  - `InternalArkCredential(tenantID)`：租户有自己的就返回它，否则返回平台的，带上 `source: tenant|platform`；密钥解不开时返回 503，不回退。
  - 任务列表、取消/删除：控制面自己拿 Key 调用 `arkvideo.List` / `Cancel`。只能操作本 scope 的 Key；租户没有自己的 Key 时返回 404。
- **路由** `handler/routes.go`：
  - 租户：`GET/PUT/DELETE /admin/v1/ark`、`GET /admin/v1/ark/tasks`（参数 `page_num`、`page_size`、`status`、`model`）、`DELETE /admin/v1/ark/tasks/:task_id`。
  - 运维：`/operator/v1/ark` 下同样一套，挂在 `requirePlatformSession` 后。
  - 内部：`GET /internal/v1/tenants/:tenant_id/ark`，挂在 `requireSharedSecret` 后。
- **计费** `logic/usagepricing.go` 的 `chargeUsage`：记录带 `Attributes["credential"]="tenant"` 时，`CostMicro=0`，并把 `doc.OwnKey` 设为 true，处理方式同自有节点。
  - 实施时先确认 `CreateUsageRecords` 对 attributes 的校验和落库方式，必要时放行这个键。
  - `OperatorEffectivePrices` 的「未定价别名」把平台 Ark 配置里的模型也算进去，没定价的会出现在 Console 的提示条里。

### 3. `common/flowdef`：节点类型 `video_generation`
- `VideoConfig{Model, Prompt, FirstFrame?, LastFrame?, ReferenceImages?[], Resolution?, Ratio?, Duration?, GenerateAudio?, Watermark?}`，严格解码。
  - 需要改的地方：`NodeVideo` 常量、`Video()` 访问器、`checkNodes` 的分派、新增 `checkVideo`、`setFields(id, "video", "last_frame")`。
- 产物字段：
  - `IsArtifactField(NodeVideo, "video" | "last_frame")` 为 true；
  - `RefersToFile` 把 `last_frame` 和 `video` 当作单个文件处理，这样 comfy 模板输入、输出节点和下一个视频节点的首帧都能引用。
- 图片槽位（first_frame、last_frame、reference_images 的每一项）必须**整个**引用单个图片：image 输入、`image` 或 `last_frame`。这里复用现有的 `slotTemplateInput` 思路，新增 `slotImage`。
  - 引用到其他东西时报新问题码 `image_reference_required`；
  - prompt 里引用产物，仍然报 `artifact_reference`。
- 参数只做格式和枚举检查，其余交给 Ark：
  - resolution ∈ {480p, 720p, 1080p, 4k}；
  - ratio ∈ {16:9, 4:3, 1:1, 3:4, 9:16, 21:9, adaptive}；
  - duration 取 -1 或正整数；
  - model 的长度和 `{{` 规则同 `checkImage`。
- 更新包注释（`flowdef.go:18-67`）中的节点列表。

### 4. Gateway
- **凭据来源**：`controlplaneclient/ark.go` 调用内部端点，404 视为未配置。`httpapi` 里加一层缓存，照 `tenantstorage.go` 的做法：30 秒 TTL，拿不到时最多用 10 分钟内的旧值。
- **引擎** `flow/`：
  - `Dispatcher` 新增 `GenerateVideo(ctx, VideoCall) (MediaReply, error)`；
  - `planNode` 新增 `video` 槽位，`compile` / `start` 新增 `startVideo`，`callOutputs` 输出 `{video, last_frame}`，没有视频时报 `no_video_produced`；
  - `Config.VideoNodeTimeout` 默认 60 分钟，视频节点不受 5 分钟单节点超时约束；
  - 含视频节点的计划，运行超时提高到 `VideoRunTimeout`（默认 2 小时）。
- **派发** `httpapi/studio_video.go`（新文件，结构参照 `studio_media.go` 的 `RunTemplate` / `awaitJob`）：
  1. 取凭据。没有时报 `video_generation_unavailable`；用平台 Key 而 model 不在平台模型列表里时报 `model_unavailable`。
  2. 图片槽位经现有的 `openFiles`（`studio_template_files.go`）读出，覆盖输入文件、`sart_` 图和 job 产物，转成 base64 data URL。
  3. 在 `withQuota` 里创建任务。`ExpiresAfter` 设为 3600 秒（Ark 允许的最小值），和节点超时对齐，减少无人认领的任务。
  4. 每 5 秒查询一次；连续失败 5 次才放弃，404 立即放弃。
  5. ctx 结束时，用 `context.WithoutCancel` 加 10 秒超时调用 `Cancel`。任务已在 running 时 Ark 不支持取消，只记日志。
  6. 成功后，把 `video_url` 按流写进对象存储：`studioArtifactKey`，`fileKindVideo="video"`（新增），文件名 `video.mp4`。`last_frame_url` 按 image 转存。
  7. 记一条账：`ledgerUsage`，其中 `Endpoint="video_generation"`（新增到 `usageledger.go` 的端点集合），`Model`=Ark 模型 id，`CompletionTokens` / `TotalTokens` 取 Ark 的 usage，`Attributes["credential"]` 取 source，请求 id 每次调用新生成。
  8. 失败码：`video_generation_failed`、`content_rejected`、`video_task_expired`、`upstream_error`，以及 `artifact_storage_unavailable`（未配置对象存储时）。
- **启动前检查**：`studio_flows.go` 的 `unavailableModels` 新增 `NodeVideo` 分支，问题码同上，在保存运行之前返回 400。
- **目录**：新增 `GET /studio/v1/video-models`，返回 `{data:[{id}], available, source}`。用租户自己的 Key 时允许自由填写模型。
- **下载**：`runArtifact` 接受 `video` / `last_frame` 字段，`sniffedMediaType` 已经支持 video。
- 更新 `openapi.yaml`：`FlowNode` 枚举、`FlowVideoConfig`、`StudioRunStep.node_type`、问题码、错误码和新端点。

### 5. Studio 前端
- `lib/studio/flows.ts`：`NodeType`、`VideoConfig`、`NODE_TYPES`，以及 ISSUE / FIELD / RUN_ERROR 文案。
- `lib/studio/flow-graph.ts`：`EDITABLE_TYPES`、`defaultConfig`、`nodeFields`、`artifactReferenceIssues`、`serialize`，并新增 `RefSlot` 取值 `image`。
- `lib/studio/artifacts.ts`：`artifactFieldShape` 覆盖 `video` / `last_frame`。
- `lib/studio/flow-catalog.ts`、`lib/server/studio.ts`：加载 video-models；`upstream-routes.ts` 放行 `GET video-models`。
- `components/studio/flows/`：
  - `flow-node.tsx`：`NODE_KIND`、`nodeSummary`；
  - `node-inspector.tsx` 新增 `VideoForm`：模型选择或自由填写、提示词、首帧/尾帧/参考图的「选择图片来源」下拉、分辨率、比例、时长、音频、水印；
  - `run-view.tsx`：「正在生成」的占位提示加上视频节点；
  - `artifact-view.tsx`：视频用内联 `<video controls>` 播放，同时保留下载。

### 6. Console（租户与运维）
- **租户** `/console/settings`：新增「火山方舟」卡片 `components/console/ark-credential-panel.tsx`，写法照 `tenant-storage-panel.tsx`。
  - Key 只写，显示 last4；可以编辑模型列表；未配置时提示「当前使用平台 Key」。
  - 权限用 `canManageStorage` 同款的 owner/admin 判断。
- **租户任务页** `/console/video-tasks`：
  - 列表展示状态、模型、创建时间、时长和分辨率，可按状态筛选、分页；
  - 排队中的任务可以取消，已结束的可以删除记录，操作前用 `ConfirmDialog` 确认；
  - 只在租户配了自己的 Key 时显示。
- **运维** `/operator/ark`：平台 Key、base_url 和模型列表，加上平台 Key 下的任务列表；在 `operator-shell.tsx` 的导航里加入口。
- 契约放在 `lib/console/ark.ts`，并配 parser 和测试。`upstream-routes.ts` 的 admin 和 operator 白名单要加新路由；`app/api/admin/[...path]/route.ts` 为保存 PUT 放宽超时（探测最多 20 秒）。
- i18n 文案覆盖 8 个语言，放在 `tenant.ts` 和 `operator.ts`。

### 7. 部署与文档
- 部署配置：
  - `deploy/controlplane.example.yaml` 新增 `Ark` 块；
  - `deploy/.env.example` 和 `docker-compose.yaml` 新增 `AISW_ARK_MASTER_KEY`，留空即关闭；
  - `deploy/README.md` 说明 Key 在界面里填写。
- STATUS：根 `STATUS.md`，以及 ControlPlane、Gateway、Studio、Console 各自的 `STATUS.md`。写清交付范围、升级顺序（先控制面，再 Gateway，最后 Studio 和 Console），并注明「未与真实火山方舟联调」。
- 其他文档：`GLOSSARY.md` 加「火山方舟凭据」，`docs/billing-token-pricing.md` 补充「租户自有 Key 不扣费」。
- **已知限制**（写进 STATUS）：
  - Gateway 在轮询中途重启时，Ark 任务继续执行，但不会记账，产物也会丢失；
  - running 状态的任务不能取消；
  - 用平台 Key 列任务时，无法区分是哪个租户的任务。

## 验证
- **默认测试**：flowdef、flow、Gateway、控制面，以及 Console 与 Studio 的 `pnpm lint` / `typecheck` / `test` / `build`。
- **临时 `_test.go`**（结束前删除，并用 `git status` 确认无残留），用 `httptest` 起一个假 Ark 服务，通过 `WithBaseUrl` 指过去，覆盖以下场景：
  - 文生视频、首尾帧和参考图转 base64、轮询到成功后转存、生成 last_frame；
  - 失败、expired、内容拒绝、401、429、网络错误；
  - 节点停止时调用取消，以及对 running 任务取消失败；
  - 节点超时、运行超时；
  - 创建不重试；
  - 平台 Key 下的模型白名单，租户 Key 记账金额为 0，平台 Key 按 output 单价扣费；
  - 凭据缓存的旧值与 503；
  - 控制面凭据的增删改查、权限、密钥保留规则、探测失败、主密钥更换后打不开；
  - 任务列表和取消的 scope 隔离；
  - 并发保存（`-race` 只跑相关包）；
  - MySQL 与 PostgreSQL 临时容器上的迁移和 live 用例。
- **手工验收**：如果你能提供一个可用的火山 Key，在本机按「控制面 → Gateway → Studio」的顺序起服务，跑一次「生图 → 视频（首帧）→ 输出」，确认视频能播放和下载、扣费金额正确、Console 任务页能看到这条任务。

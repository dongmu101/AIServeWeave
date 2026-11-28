# Studio 编排「视频生成」节点（火山方舟，2026-11-28）

Studio 编排新增节点类型 `video_generation`，经 `github.com/volcengine/ark-runtime-go` v0.24.0 调用火山方舟的视频生成任务接口：文生视频，可选首帧、尾帧与最多 10 张参考图。火山方舟 API Key 分两级：运维在运维平台保存平台 Key 并列出开放的模型；租户 owner/admin 可以在 Console 保存本租户自己的 Key。租户有自己的 Key 时优先使用，否则用平台 Key。代码与默认测试已通过。**未与真实火山方舟联调，Console 与 Studio 的新界面也未在浏览器里走通。**设计见 `.scratch/ark-video-node/spec.md`。

- **`common/arkvideo`**（新包）：封装 SDK 的创建、查询、列表、取消/删除与 Key 检查，只暴露本仓库自己的类型。创建任务关闭 SDK 的自动重试，避免重复建单；错误按类别归类，不转述方舟的原文；`Fetch` 只接受 https 结果地址，经拒绝内网地址的客户端流式下载，不限大小。
- **`common/flowdef`**：节点 `video_generation`：`{model, prompt, first_frame?, last_frame?, reference_images?, resolution?, ratio?, duration?, generate_audio?, watermark?}`。输出 `video` 与 `last_frame`（产物 id）。图片槽位必须整个引用一张图片（image 输入、上游的 `image` 或 `last_frame`），否则报新问题码 `image_reference_required`；`video` 只能作为输出字段或 ComfyUI 模板文件型输入的全部内容。
- **控制面**：新表 `ark_credentials`（base `0023`），Key 以 AES-256-GCM 加密保存，接口从不返回明文；`/admin/v1/ark`、`/operator/v1/ark` 的读写删与任务列表、取消/删除，以及供 Gateway 使用的 `GET /internal/v1/tenants/{tenant_id}/ark`。保存前先向方舟检查 Key。新配置 `Ark.MasterKey`/`MasterKeyFile`。计费：用量记录带 `credential=tenant` 时费用为 0 并标记 `own_key`。见 [ControlPlane STATUS](service/aiServeWeaveControlPlane/STATUS.md)。
- **Gateway**：引擎为视频节点单独设 60 分钟的节点超时，含视频节点的运行期限延长到 2 小时。派发器读取 Key（30 秒缓存，读不到时最多沿用 10 分钟内的旧值），把图片槽位转成 data URL，在租户限流内创建任务，每 5 秒查询一次，成功后把视频与尾帧流式转存到对象存储并记账（端点 `video_generation`）。节点被停止或超时会取消方舟任务。新端点 `GET /studio/v1/video-models`；启动运行前检查 Key 与模型。见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md)。
- **Studio**：节点面板、配置表单（模型、提示词、首帧/尾帧/参考图来源、分辨率、比例、时长、音频、水印）、运行中提示，以及视频的内联播放与下载。见 [Studio STATUS](service/aiServeWeaveStudio/STATUS.md)。
- **Console**：设置页的「火山方舟」卡片、`/console/video-tasks` 视频任务页（仅租户有自己的 Key 时列出任务），以及运维 `/operator/ark` 页。见 [Console STATUS](service/aiServeWeaveConsole/STATUS.md)。
- **部署**：`deploy/controlplane.example.yaml` 新增 `Ark` 块；`.env.example` 与 `docker-compose.yaml` 新增 `AISW_ARK_MASTER_KEY`（留空即关闭，安装脚本不自动生成）。见 [deploy README「火山方舟视频生成」](deploy/README.md#火山方舟视频生成)。

升级须知：
- 先迁移并升级控制面，再升级 Gateway，最后升级 Studio 与 Console。控制面要用新的 `flowdef` 才能保存含视频节点的编排；Gateway 把旧控制面的 404 当作「未配置」。
- 要开启：在 `deploy/.env` 填 `AISW_ARK_MASTER_KEY`（`openssl rand -base64 32`），并把示例中的 `Ark` 块加进本机 `controlplane.yaml`。主密钥丢失或更换后 Key 需要重新填写；主密钥变了导致租户自己的 Key 解不开时，Gateway 不会改用平台 Key（返回 503），避免租户被平台计费。
- 视频结果要转存，需要部署存储或租户自有存储；都没有时节点以 `artifact_storage_unavailable` 失败，且不会创建方舟任务。
- 平台 Key 列出的模型会出现在计费页的「未定价别名」提示里，运维需要为它们定价（按 `output` 单价乘方舟返回的 `completion_tokens`），否则按 0 计价。

已知限制：
- Gateway 在轮询中途重启时，方舟任务会继续执行，但不会记账，结果也会丢失（方舟最多保留 1 小时后过期）。
- 方舟不支持取消执行中（running）的任务：节点停止时只能取消排队中的任务，执行中的会跑完并在方舟侧计费，平台侧不记账。
- 用平台 Key 列任务时，方舟不区分租户，因此只向运维列出；租户只能看到自己 Key 下的任务。
- 结果下载经 BFF 流式转发、不支持 Range，Safari 可能无法内联播放，可以下载后观看。

已通过的检查：
- 全仓 `go build`、`go vet` 与 `go test ./...`。临时用例（已删除）覆盖：假方舟服务下的请求体、创建不重试、查询重试、错误归类与不泄露原文、列表两种返回形态、结果下载；flowdef 视频节点 31 个校验场景与产物字段规则；引擎的输出、失败码、视频节点超时与运行期限、取消与并发；Gateway 端到端的成功转存与下载、租户 Key 记账为 0、启动前拒绝、16 种节点失败、取消与超时时撤销方舟任务、模型目录端点、Key 缓存的 TTL/旧值/上限；控制面的增删改查、权限、Key 保留规则、检查失败、主密钥更换、任务列表的 scope 隔离、计费与 handler 状态码。并发相关包在 `-race` 下稳定。
- 在临时 MySQL 9.7 容器上跑了迁移、列注释与 `ark_credentials` 的 live 用例（含并发首次保存）；PostgreSQL 18 上 0023 迁移单独执行正常，整体 `Migrate` 仍受下文既有的 `tenant_nodes` 索引误报影响。
- Studio 的 lint、typecheck、147 项单元测试与生产构建通过；Console 的 lint、typecheck、486 项单元测试与生产构建通过。

# 文件登记表 files（2026-11-27）

Gateway 写进对象存储的每个文件（job 产物、Studio 运行输入、推理生图；部署存储与租户存储）都在控制面的新表 `files` 登记一行，记下所在存储、key、所属业务对象（`biz_type` + `biz_id`）、类别、大小与 sha256。保留期清理改为按这张表进行，不再按前缀列举对象存储；保留期规则不变。`files` 按月分区。代码与默认测试已通过，在临时 MySQL/PostgreSQL 容器与 Cloudflare R2 上做了进程内验证。**未在运行中的容器里验收。**设计见 `.scratch/file-registry/spec.md`。

- **控制面**：表 `files`（base `0022`），`job_artifacts.file_id`（jobs `0008`），四个 `/internal/v1/files` 内部接口。见 [ControlPlane STATUS](service/aiServeWeaveControlPlane/STATUS.md#文件登记表-files2026-11-27)。
- **Gateway**：写入顺序为登记 `pending` → 写入 → 登记 `ready`；登记不可达时不写入。新的 `fileCleaner` 替代 Studio 与租户存储的两个前缀扫描清理器。没有新 flag。见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#文件登记表-files2026-11-27)。

升级须知：
- 先迁移并升级控制面，再升级 Gateway。Gateway 先升级时，写入照常但不登记，清理暂停，直到控制面升级。
- 升级前写入、没有记录的 Studio 文件与租户存储文件不再被自动清理（这两项功能在 v3.3.1 之后加入，尚未发布）。部署存储里的旧 job 产物仍按原规则清理。

# 租户自有对象存储（2026-11-27）

租户的 owner 或 admin 可以在 Console 为本租户配置自己的 S3 或 WebDAV，配置后该租户的 job 产物、Studio 运行输入文件与推理生图都写进租户的存储，保留期由租户自己设（0 表示平台从不删除）。部署默认关闭，填了主密钥才开启。代码与默认测试已通过。objectstore、Gateway 的写入/下载/清理与控制面的保存检查已在 Cloudflare R2 上用进程内用例验证，并修正了两个 S3 写入缺陷（大小未知时 411、body 过长时截断写入），见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#租户自有对象存储2026-11-27)。**尚未在运行中的容器里验收，Console 也未与真实控制面联调、未在浏览器里走通**。设计见 `.scratch/tenant-object-storage/spec.md`。

- **`common/objectstore`**：对象存储包从 Gateway 移到 `common/`，供控制面与 Gateway 共用。新增租户后端的构建、内网地址限制（拨号时检查，覆盖 DNS 重绑定与跳转，不用环境代理）、错误分类与连通性检查。见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#租户自有对象存储2026-11-27)。
- **控制面**：新表 `tenant_storage`（base `0021`）；`GET`/`PUT`/`DELETE /admin/v1/storage`（owner/admin），保存前做连通性检查；凭据以 AES-256-GCM 加密保存，接口从不返回密钥；两个供 Gateway 使用的内部接口。新配置 `TenantStorage.MasterKey`/`MasterKeyFile`/`AllowPrivateEndpoints`。见 [ControlPlane STATUS](service/aiServeWeaveControlPlane/STATUS.md#租户自有对象存储2026-11-27)。
- **Gateway**：写入只写租户当前的存储，读不到租户设置时写入失败而不回退到部署存储；读取依次尝试租户存储、部署存储，job 产物再从节点实时拉取。每小时按租户保留期清理。没有新 flag，设置了 `-control-plane-addr` 即生效。
- **Console**：`/console/settings` 新增「对象存储」卡片。见 [Console STATUS](service/aiServeWeaveConsole/STATUS.md)。
- **部署**：
  - `deploy/controlplane.example.yaml` 新增 `TenantStorage` 块；`deploy/.env.example` 与 `docker-compose.yaml` 新增 `AISW_TENANT_STORAGE_MASTER_KEY`（留空即关闭，安装脚本不自动生成）。
  - 部署存储也可以在 `.env` 里开启：`AISW_ARTIFACT_STORAGE=s3|webdav` 加对应参数与凭据，留空即关闭。凭据经环境变量传给 Gateway，不出现在进程参数里。见 [deploy README「对象存储」](deploy/README.md#对象存储)。

升级须知：
- 先迁移并升级控制面，再升级 Gateway 与 Console。顺序反过来不会出错：Gateway 把旧控制面的 404 当作「未配置」。
- 要开启：在 `deploy/.env` 填 `AISW_TENANT_STORAGE_MASTER_KEY`（`openssl rand -base64 32`），并把示例中的 `TenantStorage` 块加进本机 `controlplane.yaml`。主密钥丢失或更换后，租户需要重新填写凭据，请备份 `deploy/.env`。
- `AllowPrivateEndpoints` 放行内网地址与明文 http，只在每个租户都可信时打开。
- 更换或删除配置不迁移旧文件：旧文件留在原来的存储里，平台不再读取。

已通过的检查：
- objectstore、控制面、Gateway、flowdef 与 flow 的默认测试。临时用例（已删除）覆盖三层的正常、边界、异常与并发，相关包在 `-race` 下稳定。
- 在进程内的真实 WebDAV 服务器上走通了探测。
- 在临时 PostgreSQL 18 与 MySQL 9.7 容器上跑了 tenant_storage 的 live 用例；MySQL 上全部既有 live 用例通过。
- 由此修正了 S3 重试丢失错误原因、列类型、`updated_at` 被覆盖、MySQL 并发首次保存死锁四处问题。
- Console 的 lint、类型检查、473 项单元测试与生产构建通过。

已知问题（与本次无关，未修）：在干净的 PostgreSQL 上，控制面 `Migrate` 在迁移后的 schema 检查处误报 `tenant_nodes` 的索引不兼容并返回错误，既有的 `TestLiveRoutes`、`TestLiveSchemaComments` 因此失败（HEAD 上同样复现）。控制面放在 PostgreSQL 上的部署可能受影响；默认部署的控制面用 MySQL。详见 ControlPlane STATUS。

# Studio 编排的文件输入、保留期清理与 OpenAPI（2026-11-27）

编排运行可以带图片与文件，ComfyUI 模板的文件型输入可以从编排运行；Studio 写入对象存储的文件有了保留期；`openapi.yaml` 收录了 `/studio/v1`。代码与默认测试已通过，**未接真实 ComfyUI、S3/WebDAV 与 nginx/Cloudflare 联调，也未在浏览器里走通新界面**。

- **`common/flowdef`**：
  - 输入新增类型 `image` 与 `file`，取值为 `{artifact_id, filename}`，不能有默认值。
  - 引用规则放宽一处：comfyui_template 的模板输入可以整个引用**单个**文件（image/file 输入或节点的 `image`，不含 `artifacts`）。文件类引用出现在其余位置仍报 `artifact_reference`，输出 ref 照旧可以引用任何产物。
- **Gateway**（见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#studio-编排的文件输入保留期清理与-openapi2026-11-27)）：
  - `POST /studio/v1/flows/{id}/runs` 另接受 multipart：`request` 字段放原来的 JSON，文件按输入名分片，上限 200 MiB（与工作流上传相同）。重跑可以用 `{run_id, artifact_id}` 复用上次的文件。
  - 文件存入 `-artifact-storage`（`studio/<租户>/<运行>/<id>`），未配置对象存储时 400；运行输入中的文件可以从运行的产物下载端点取回。
  - 模板节点把文件以流式 multipart 交给工作流运行，原来的 `unsupported_input` 已删除。
  - 新 flag `-studio-artifact-retention`（默认 30 天）：每小时扫描 `studio/` 前缀，删除过期的推理生图与上传的运行输入。为此 objectstore 后端新增 `List`。
  - `openapi.yaml` 收录全部 `/studio/v1` 端点。
- **Studio**：输入节点可声明图片与文件输入，运行表单有文件选择器，重跑预填上次的文件；ComfyUI 文件型输入改为「选择文件来源」下拉；运行视图展示输入文件，文件过期时有提示。启动运行改走流式转发的专门 BFF 路由，不缓冲、不另设上限。见 [Studio STATUS](service/aiServeWeaveStudio/STATUS.md)。
- **控制面**：无代码改动，保存时用新的 `flowdef` 校验。
- **部署**：`deploy/nginx.example.conf` 的 Studio 站点为启动运行的路径单独放宽到 `client_max_body_size 200m`，并关闭请求体缓冲。经 Cloudflare 代理时，其套餐的上传上限（免费版 100 MB）先生效。

升级须知：先升级控制面（它用新的 `flowdef` 才能保存含文件输入的编排），再升级 Gateway 与 Studio。已部署 nginx 的 Studio 站点需要照示例加上启动运行的 location，否则超过 10 MB 的上传被 nginx 拒绝。清理在升级后的第一个小时开始运行，此前写入的推理生图超过 30 天的会在首轮被删除。

已通过的检查：flowdef、flow、objectstore、Gateway 与控制面的默认测试；临时用例（已删除）覆盖三种存储后端的列举、清理器、上传与复用的成功与 18 种拒绝、模板节点经假 ComfyUI 上传文件等，相关包在 `-race` 下稳定。Studio 的 lint、typecheck、147 项单元测试与生产构建通过，并用桩上游验证了 250 MiB 上传的流式转发。

# Studio 编排第二期节点（2026-11-27）

编排新增「图像生成」与「ComfyUI 模板」两类节点，从定义、执行到下载都已交付（规格用户故事 51、52、59、68）。代码与默认测试已通过，**未接真实 ComfyUI、推理生图模型与对象存储联调，也未在浏览器里走通新界面**。

- **`common/flowdef`**：
  - `image_generation`：`{model?, prompt, size?}`。`model` 是固定的图像模型名，留空表示部署自己的生图工作流（`-images-workflow-id`）；`size` 形如 `宽x高`。输出字段 `image`。
  - `comfyui_template`：`{template, inputs?}`。`template` 是已发布工作流模板的 id，`inputs` 按模板输入名给出模板文本，未列出的取模板默认值。输出字段 `image`（第一张图片）与 `artifacts`（全部产物的 JSON 数组，每项 `artifact_id`、`filename`、`kind`）。
  - 这些字段保存的是产物 id，只能作为输出 ref 的全部内容被引用，其他位置报新问题码 `artifact_reference`，产物 id 因此不会被送进模型或参与比较。
- **Gateway**（`flow/`、`httpapi/studio_media.go`）：
  - 引擎的派发器新增 `GenerateImage` 与 `RunTemplate`，两者与模型调用共用并发上限和单节点超时。
  - 两类节点都在进程内走 API Key 前门的同一段代码：部署默认生图走 `/v1/images/generations`（url 模式），模板走 `/v1/workflows/{id}/runs` 并跟踪 job 到结束，节点被停止时撤回 job。调度、共享工作流队列、job 记录、产物持久化与租户限流照常生效。
  - 推理生图模型只返回字节，Gateway 把它们流式写入对象存储（key 由租户、运行与产物 id 推导），未配置 `-artifact-storage` 时节点以 `artifact_storage_unavailable` 失败。
  - 新端点：`GET /studio/v1/image-models`、`GET /studio/v1/workflow-templates`（只列本租户可见的模板，不含图），以及 `GET /studio/v1/runs/{id}/artifacts/{artifact_id}`。下载只接受该成员自己的运行中某个步骤产出的产物，并带 `nosniff` 与 `Content-Security-Policy: sandbox`。
  - 启动运行时的事前检查扩展到这两类节点：图像模型不可用、部署没有默认生图、模板不存在或对本租户不可见时，在存下运行之前就回 400，并附节点定位的问题。
- **Studio**：新节点的配置面板（图像模型与模板来自上述目录）、产物字段只在输出节点的变量菜单中出现、即时 `artifact_reference` 检查、运行视图中图片与文件的展示与下载，以及产物下载的流式 BFF 路由。见 [Studio STATUS](service/aiServeWeaveStudio/STATUS.md)。
- **控制面**：无改动。保存时的校验自动接受新节点类型，`node_type` 列宽 32 已够。

升级须知：先升级控制面（它用新的 `flowdef` 才能保存含新节点的编排），再升级 Gateway 与 Studio。无新开关；推理生图节点需要 Gateway 配置 `-artifact-storage`。

已通过的检查：Gateway 与控制面全部默认测试；`flow` 与新用例的 `-race`；临时用例（已删除）覆盖 flowdef 新节点的校验与产物引用规则、引擎的产物输出与失败码、超时、取消与并发上限，以及 Gateway 经假 ComfyUI 节点与假推理生图节点走通的模板运行、默认生图、推理生图写入对象存储、下载的归属与响应头、各类失败码、模板可见范围、取消和事前拒绝。Studio 的 lint、typecheck、121 项单元测试与生产构建通过，并用桩上游做了下载与页面渲染的 HTTP 级检查。

当时尚未交付的编排文件输入、推理生图的保留期清理与 `openapi.yaml` 中的 `/studio/v1`，已在上一节交付。

# CLI 订阅额度调度优化（2026-11-26）

Gateway 调度 Claude Code / Codex 订阅账户时，从「只看已用比例」改为同时看距重置的时间，并让 Agent 的额度快照更快赶上实际用量。代码已交付，现有默认测试全部通过；新逻辑用一次性测试验证（未提交），默认测试套件尚未覆盖；**未在真实账户接近耗尽或临近重置的场景下验收**。

- **Gateway**（`scheduler/quota.go`、`scheduler/quotarefusal.go`）：
  - 新增「快到期」档（新 flag `-quota-expiring-percent`，默认 10%）：即将重置、还有用不完额度的账户在节点有空闲槽时先用；进行中的对话留在钉选账户上，不为此迁走。
  - 「紧张」档只降级按均匀速度会在重置前用完的账户；紧张账户之间先留在会话钉选的账户上，其次优先剩余额度多的。
  - CLI 账户答复 `rate_limited` 时，按快照推断拒绝它的窗口，账户级窗口一并冷却同账户的其他模型。
  - 新增三个 `gateway_scheduler_quota_*` 指标。
- **Agent**（`cliusage/`、`claudecode/`、`codexcli/`）：暂时性查询失败保留上一轮读数；有窗口达到 80% 时每分钟刷新（已用完的账户除外）；CLI 警告接近上限或拒绝时立即刷新。
  - 已知账户用完（读数显示适用窗口到 100%，或 CLI 报告带重置时间的拒绝）时，回合在 Agent 本地答复 `rate_limited`，不启动 CLI、不联系服务商，直到重置或新读数显示恢复。
- **Gateway** 另外：CLI 账户连续不带重置时间被拒时，限流冷却从 30s 逐次翻倍到至多 1h。

升级须知：两侧各自生效，升级顺序不限；只升级 Gateway 时快照仍按 5 分钟刷新。新 flag 不设置即用默认值，`-quota-expiring-percent=-1` 可关闭快到期档。详见 [Gateway STATUS「按订阅额度主动降级」](service/aiServeWeaveGateway/STATUS.md#按订阅额度主动降级) 与 [Agent README「本地 CLI 账户额度」](service/aiServeWeaveAgent/README.md#本地-cli-账户额度)。

# Studio 编排（2026-11-26）

编排第一期从前到后都已交付：在 Studio 画布上编辑并保存（带校验与版本）→ 在 Gateway 上执行（真实调度与计费路径）→ 实时事件流 → 步骤与结果落库 → 运行视图与运行记录。

- **`common/flowdef`**：编排的 JSON 结构、第一期节点类型（输入、LLM、文本模板、条件、输出）、`{{inputs.x}}` / `{{节点.字段}}` 引用语法、`Decode`、`Validate` 与运行输入绑定 `BindInputs`。`Validate` 一次列出全部问题，每条带节点 id 和稳定的错误码。
- **Gateway**：执行引擎 `flow`（并行分支、条件跳过与汇合、变量替换、单节点超时、整体期限、取消、首个失败即停、结构化输出），以及 `/studio/v1/flows*`、`/studio/v1/runs*` 端点。运行在接收请求的副本上于后台执行，每次模型调用都在进程内走 `/v1/chat/completions` 的同一路径，并**各自**经过租户限流、各自计入用量账本。SSE 事件流以快照开头，断线重连不丢进度。见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#studio-编排与运行2026-11-26)。
- **Studio**：编排列表、画布编辑器（`@xyflow/react`）、运行表单、运行视图（实时节点状态与逐段文字、停止、重跑）、运行记录，以及运行事件流的 BFF 转发。见 [Studio STATUS](service/aiServeWeaveStudio/STATUS.md)。
- **控制面**：四张新表与 12 个内部接口；保存时用 `flowdef` 校验；运行凭租约由副本持有，租约过期即读作 `interrupted` 并拒绝该副本的后续写入。见 [ControlPlane STATUS](service/aiServeWeaveControlPlane/STATUS.md#studio-编排存储2026-11-25)。

契约、执行规则与分期见 `.scratch/studio-chat-flows/spec.md` 的「编排（Flow）」一节，术语见 [GLOSSARY.md](GLOSSARY.md)。

升级须知：先迁移并升级控制面（`studio/0004`–`0007` 只新增表，`sql/init.sql` 已同步），再升级 Gateway。Gateway 以 `-studio` 启动时自动挂载编排端点，无新开关。

已通过的检查：flowdef 与 flow 的 `go test -race`（覆盖率 98% / 93.5%）；Gateway 与控制面全部默认测试；Gateway httpapi 新用例经进程内聊天路径与假节点走通执行、事件流、持久化、取消、失去运行与事前拒绝，`-race` 下重复 20 次稳定；控制面 e2e 用 Gateway 真实客户端对真实控制面走通全部内部接口。Studio 的 lint、typecheck、76 项单元测试与生产构建通过，并用桩上游在无头 Chrome 中走通从新建、编辑、保存出错定位、运行、实时观察到停止与重跑的全流程，包括手机宽度与深色主题。

尚未验收：**gormstore live 用例未运行**（本机没有 Docker 与数据库），新表在真实 MySQL 与 PostgreSQL 上的迁移、结构自检与契约测试待补跑；Studio、Gateway、控制面与真实模型的端到端联调未做。尚未交付：运行表分区与保留期、编排指标、请求检索中的 `studio_flow` 来源（第二期节点、文件类输入与 `openapi.yaml` 中的 `/studio/v1` 已于 2026-11-27 交付，见上文）。

# 购买与赠送点数、注册赠送入库（2026-11-24）

租户余额分为**购买点数**与**赠送点数**，注册赠送点数改由运维在 Console 配置并存入数据库（默认 0）。规则见[按 token 计价「购买点数与赠送点数」](docs/billing-token-pricing.md#购买点数与赠送点数)与 [ControlPlane STATUS](service/aiServeWeaveControlPlane/STATUS.md)。

- **控制面**：
  - 新增 `tenant_billing.gift_balance_micro`、`usage_acc_gift_micro`，`credit_ledger.gift_amount_micro`、`gift_balance_after_micro`，以及单行表 `billing_settings`。
  - 用量先扣赠送点数；退款或争议扣回超过购买点数时由赠送点数抵扣。赠送余额与总余额一样形成余额链。
  - 「只退未用点数」的退款上限只看购买点数。
  - 新增 `GET`/`PUT /operator/v1/billing/settings`；运维调整余额新增 `credit_type`（`gift` 默认，或 `purchased`），扣减的赠送点数多于现有时 `400`。
  - 余额概览、运维商户列表、账目响应带上购买与赠送两部分。
- **Console**：运维计费页新增「设置」标签页与调整类型选项；租户余额卡片、总览与账目显示点数类型。见 [Console STATUS](service/aiServeWeaveConsole/STATUS.md)。

升级须知：

- **顺序**：先迁移并升级控制面，再升级 Console。新 Console 能读取旧控制面的响应（缺少字段时按全部是购买点数显示），但旧控制面没有 `/operator/v1/billing/settings`，「设置」页会显示「计费未开启」。
- **迁移**：`billing/0016`，四个 `BIGINT NOT NULL DEFAULT 0` 列（MySQL `ALGORITHM=INSTANT`，PostgreSQL 11+ 加带常量默认值的列不重写表）与一张新表。`sql/init.sql` 已同步。
- **行为变更**：升级前的余额全部算作购买点数，升级前写入的注册赠送不追溯为赠送点数；退款上限与升级前相同，直到产生新的赠送点数。
- **配置**：`Billing.SignupGrantCredits` 仍然有效，但只作尚未在 Console 保存时的默认值；保存过一次后以数据库为准（保存 0 即关闭赠送）。
- **回退**：数据库记录了 `billing/0016` 后，旧版控制面会以「unknown database migration」拒绝启动，回退须恢复迁移前的备份。

已通过的检查：

- 控制面 `go vet` 与默认测试；共享余额链校验 `storetest.CheckLedgerChain` 加入赠送余额链与 `0 ≤ 赠送 ≤ max(余额, 0)`，全部既有的账目场景（含并发与时钟倒退）在此校验下通过。
- 临时用例覆盖：规则函数（收敛、入账、先扣赠送、封账）；memstore 上的完整场景，并发扣费与入账带 `-race`；logic 的注册赠送来源（默认、配置文件、已保存、保存为 0）、设置的权限、校验与审计、调整类型、退款上限只看购买点数；HTTP handler 的设置接口、调整、概览、商户列表与账目字段。临时用例已删除。
- Console lint、类型检查、445 项单元测试、生产构建；Playwright 相关 276 项通过。

尚未验收：

- **gormstore 实库**：临时 PostgreSQL 与 MySQL 上的 live 用例未运行（本机 Docker 未启动），包括 `0016` 迁移的执行、`billing_settings` upsert 与压缩命令累加赠送部分。
- 真实控制面与 Console 的端到端联调。

# Studio 对话（2026-11-16）

[Studio](service/aiServeWeaveStudio/README.md) 可以发送消息了：新建对话、流式回答、思考过程、重新生成、停止、历史、重命名与删除。规格见 `.scratch/studio-chat-flows/spec.md`。

- **控制面**：新增 `studio_conversations`、`studio_messages` 两张表（迁移命名空间 `studio`，两种方言），以及 8 个内部接口 `/internal/v1/studio/conversations/*`（凭 `InternalToken`）。用量记录新增可选 `user_id`，只在没有 `key_id` 时采用，用于 Studio 的无 key 请求。
- **Gateway**：`-studio` 下新增对话 CRUD 与 `POST /studio/v1/conversations/{id}/messages`（SSE）。发送在进程内走与 `/v1/chat/completions` 相同的调度路径，回答由 Gateway 写回控制面。见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#studio-api-与会话鉴权2026-11-16)。
- **Studio**：引入 Vercel AI SDK（`ai` 7、`@ai-sdk/react` 4）。BFF 把 Gateway 帧翻译成 UI 消息流，自身不存储。

升级须知：先迁移并升级控制面（`sql/init.sql` 已同步），再升级 Gateway。Gateway 以 `-studio` 启动时，须用 `-control-plane-token` 或 `AISW_CONTROL_PLANE_TOKEN` 提供内部令牌。

已通过的检查：

- Gateway 与控制面的默认测试，Gateway Studio 用例带 `-race`。控制面共享 store 契约覆盖归属隔离、分页、搜索、重新生成、幂等与过期回答、上限、删除。e2e 用真实 Gateway 客户端走通对话全流程。
- 临时 MySQL 9.7 与 PostgreSQL 16 上的 live 用例。
- Studio 的 lint、typecheck、33 项单元测试和生产构建；用桩上游在无头 Chromium 中走通发送、Markdown、思考过程、刷新后历史、重新生成、停止后「已中断」、拒绝后恢复输入、重命名、删除当前对话。

尚未交付：附件发送、编辑后重发、服务端停止接口、异步生成标题、对话表分区与保留期、删除用户或租户时清理对话。尚未验收：接真实模型的全链路联调。

# 按 token 计价：官方价 × 倍率（2026-11-16）

点数计费改为参照 [sub2api](https://github.com/Wei-Shaw/sub2api) 的计价方式：**售价 = 基础价 × 模型倍率 × 方案系数**，1 点 = 1 美元。规则、运维设置与升级步骤见[按 token 计价](docs/billing-token-pricing.md)，设计与取舍见 `.scratch/token-pricing/`。

- **计量项**：用量按 `input`、`input.cache_read`、`input.cache_write`（含 `.1h`）、`output`、`output.reasoning` 分别计价；子项没有单价时按父项计费，不会少收。支持长上下文档位。新增计量项不必改协议，也不必改表。
- **价目表**：内置 LiteLLM 价目表快照，按配置定时同步；新条目自动生效，大幅改价、降为 0 与删除等运维审核。
- **定价配置与价格方案**：按别名设置参照模型、覆盖价、模型倍率；默认价；价格方案（默认系数、按别名的系数或固定售价），租户属于一个方案。每次修改追加版本，**按请求时间计价**。
- **每笔费用明细**：用量记录保存原价、实扣、倍率、价格来源与各计量项的单价和费用。
- **扣费防重**改由按月分区的 `usage_records` 承担，`usage_charges` 不再写入（它不分区也不清理，会无限增长）。
- **余额账目**：扣费累计在租户计费行上，按 `Billing.LedgerTimezone` 每天封成一条用量账目，遇到其他资金事件先封账；账目只追加、不修改。
- **上报链路**：Anthropic / OpenAI adapter、隧道协议、Agent 的 Claude 风格 / Cursor / Codex CLI runtime 上报缓存与推理明细；Gateway 推送前过滤。**修正既有漏计**：Gateway 原生 Messages 路径（Claude CLI 桥接）原本漏计缓存读取与写入。
- **查询接口**：用量汇总新增缓存与费用列、逐笔明细、租户价格、运维有效价格与未定价提示；公开目录改为默认方案下的售价。旧的 `/operator/v1/billing/model-prices` 已移除。
- **Console**：运维的模型定价、价目表审核、价格方案；租户的用量费用与逐笔明细、模型价格、账目「进行中」与时间段，见 [Console STATUS](service/aiServeWeaveConsole/STATUS.md)。

升级须知：

- **顺序**：先控制面与 Console（一起），再 Gateway，最后 Agent。用量推送新增 `details`、`attributes` 字段，旧控制面会拒绝整批。
- **迁移**：`usage_records/0005`（即时加列）、`usage_records/0006`（索引 `(tenant_id, created_at, id)`：**PostgreSQL 建索引期间阻塞用量写入**，MySQL 在线执行；大表须先在恢复副本上计时）、`billing/0012`–`0014`。`sql/init.sql` 已同步。
- **行为变更**：名称与价目表条目完全一致、原本落到 `*` 默认价的别名，升级后按官方价收费；1 点 = 1 美元成为定价基准，请重新核对充值包；已有 `model_prices` 行转换为覆盖价，价格不变；**Claude CLI 桥接的用量与费用会明显增加**（缓存 token 开始计入），升级前请通知相关商户。
- **不能直接回退**到旧版控制面（旧版只查 `usage_charges` 防重，重发的记录会被重复扣费），回退须恢复备份。
- **可选维护命令**：`-migrate backfill-usage-cost`（把升级前费用写回 `usage_records`）、`-migrate compact-usage-ledger`（按天合并升级前的用量账目，会改写财务记录，须先备份）。
- **新配置**：`Billing.PriceCatalog`（`URL`、`RefreshInterval`、`AutoApplyMaxChangePercent`）、`Billing.LedgerTimezone`（默认 UTC，台湾或大陆市场建议 `Asia/Taipei` / `Asia/Shanghai`）。

已通过的检查：

- 全仓 `go vet` 与默认测试；`common/pricing`、控制面 store、logic、handler、config、e2e 与 Gateway `httpapi` / `controlplaneclient` 的竞态检查。
- 临时 PostgreSQL 16 与 MySQL 9.7 上 gormstore 全部 live 用例，新增：价目表（含按完整快照导入）、定价配置、扣费防重（含跨月分区重发与并发推送）、余额链（含同一微秒、时钟倒退、扣费 / 入账 / 封账并发、升级前的旧租户）、回填与压缩命令、用量查询；分区升级用例同步了新列。
- 计价引擎单元测试含规格的计算示例；上游 LiteLLM 原始文件（5993 个条目）解析与过滤。
- Console 409 项单元测试、类型检查、lint 与生产构建；Playwright 新增运维定价与租户用量用例，与计费相关用例合跑 181 项通过。`billing.spec.ts:382` 失败：断言的文案在 HEAD 中就不存在，与本次无关。

尚未验收：

- 真实控制面、Gateway、Agent 与 Console 的端到端计价联调。
- 真实 Claude Code、Codex CLI 上报的缓存字段（尤其是 1 小时缓存）。
- 从真实网络同步上游价目表。
- 生产规模数据上 `usage_records/0006` 的建索引耗时。

# Studio 首页与会话鉴权（2026-11-16）

新服务 [Studio](service/aiServeWeaveStudio/README.md)：面向租户成员的 AI 工作台，规格见 `.scratch/studio-chat-flows/spec.md`。本次交付首页与它所需的后端：

- **控制面**：新增内部接口 `POST /internal/v1/sessions/verify`，校验 Console 会话并返回与 API Key 同形的租户授权，外加用户与角色。租户授权的计算抽成 `VerifyTenant`，由 key 校验与会话校验共用。
- **Gateway**：新增开关 `-studio`，挂载 `/studio/v1`（`me`、`models`）。会话令牌与 API Key 互不通用。会话校验结果最多缓存 5 秒，见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#studio-api-与会话鉴权2026-11-16)。
- **Studio**：登录与登出、首页（侧边栏、问候、输入框、「+」菜单上传文件、模型选择）。**发送消息尚未接入**。

升级须知：先升级控制面，再以 `-studio` 启动 Gateway。不开启 `-studio` 时行为不变。

已通过的检查：

- Gateway 与控制面的默认测试。新增用例：
  - Gateway Studio 前门的鉴权矩阵、两扇前门凭据隔离、未开启时不挂载、模型授权过滤；
  - 控制面 e2e：真实登录 → Gateway 客户端校验 → 登出后失效，以及平台运维、伪造令牌与 API Key 被拒。
- Studio 的 lint、typecheck、18 项单元测试和生产构建。
- 用桩上游在无头 Chromium 中走通登录、首页、附件、模型选择与退出流程。

尚未验收：真实控制面、Gateway 与 Studio 的联调。

# 用量按用户与 API Key 归属（2026-11-15）

用量账本新增 key 与用户维度，可看每个用户、每个 key、每个模型的输入与输出 token：

- **Gateway**：用量记录附发起请求的 `key_id`。
- **控制面**：`usage_records/0003` 新增 `key_id`、`user_id` 两列；入账时按 key 查创建者写入 `user_id`，之后删除 key 或用户不影响归属。`/admin/v1/usage/summary` 与 `/operator/v1/usage/summary` 新增 `group_by=key`，按 `(tenant, user, key, model)` 分组。
- **Console**：租户「用量」页改为：时间范围预设（全部 / 近 7 天 / 近 30 天 / 自定义）、四项合计卡片（输入、输出、总 token、请求数），以及「按模型 / 按用户 / 按 Key」三个标签页；名称取自用户与 key 列表首页（200 条），找不到的显示 id。`/api/admin/*` 与 `/api/operator/*` 的用量汇总白名单放行 `group_by`（首次上线漏放，按 Key 统计实际拿到的是按模型数据）。
- **Console**：「API Key」页与「用户」页新增「Token 用量」列，按模型列出该 key / 该用户的全部输入与输出 token（不限时间窗口）。

升级须知：**先升级控制面，再升级 Gateway**（旧控制面会拒绝带 `key_id` 的批次并丢弃）。迁移只加两列带默认值的列；升级前的历史用量显示为「未归属」。`sql/init.sql` 已同步。

已通过的检查：控制面与 Gateway 默认测试；临时 MySQL 9.7 与 PostgreSQL 16 上 gormstore 全部 live 用例（含升级前数据保留与按 key 分组查询）；Console typecheck、lint 与单元测试。

尚未验收：真实环境端到端（Gateway 推送 → Console 显示）。

# 增长表按时间分区（2026-11-14）

控制面五张持续增长的表改为按时间 RANGE 分区，MySQL 9.7 与 PostgreSQL 都做，两边表结构一致：

| 表 | 分区 | 保留期（默认） |
| --- | --- | --- |
| `request_logs` | 按天，`created_at` | 30 天 |
| `usage_records` | 按月，`created_at` | 400 天 |
| `metrics_history_points` | 按天，`bucket_at` | 90 天（启用指标历史时） |
| `response_turns` | 按天，`created_at` | 30 天（新增 `ResponseTurnRetention`） |
| `audit_logs` | 按月，`created_at` | 400 天（新增 `AuditLogRetention`） |

- **清理（至少保留 N 天）**：每小时运行，只整段删除过期分区，截止时间所在的分区整段保留（最多多留一个分区周期：按天的表 1 天，按月的表约 1 个月）。稳态下没有逐行删除；零散迟到行分批删除，每批 5000 行。
- **维护**：控制面启动时及之后每小时预建未来分区（按天 7 天、按月 2 个月），多副本之间用迁移锁串行；每张表有兜底分区，维护落后时写入不会失败。兜底分区积压超过 20 万行时，定时维护不搬移，交给 `-migrate up` 在低峰期完成。
- **维护账号**：新增可选的 `Database.MaintenanceDSN`，生产运行账号没有 DDL 权限时，分区 DDL 走这个账号。分区 DDL 失败时，保留期清理退回分批逐行删除，不会停摆。
- **配置**：新增 `ResponseTurnRetention`、`AuditLogRetention`、`Database.MigrationTimeout`、`Database.MaintenanceDSN`；按天分区的表保留期上限 3650 天。
- **不分区**：`usage_charges` 等计费表，扣费去重依赖 `request_id` 全局唯一。

MySQL 9.7 对分区没有实质性加强：主键和唯一键须包含分区列、分区表不能有外键，这些限制仍在，本次按此设计。

升级须知（不兼容）：

- 五张表的主键改为 `(id, 分区列)`；`audit_logs.created_at` 改为 `NOT NULL`，NULL 回填为迁移时间。
- 保留期改为「至少保留 N 天」，数据可能多留最多一个分区周期。
- 升级后约 1 小时的第一轮清理，会删除超过 30 天的对话轮次和超过 400 天的审计记录；需要保留更久的部署，先调大配置再升级。
- 生产运行账号没有 DDL 权限，须配置 `Database.MaintenanceDSN`（一般用迁移账号）。
- 迁移原地转换已有数据，整表复制期间不可写；大表须先在恢复副本上计时，并设置 `Database.MigrationTimeout`。
- 没有反向迁移，回退只能恢复升级前的备份。只需升级控制面，Gateway 与 Agent 无需改动。

说明见 [数据库升级与恢复「按时间分区」](deploy/database-recovery.md#按时间分区base-0017--request_logs-0004--usage_records-0002--metrics_history-0002--response_turns-0002)，设计与规格见 `openspec/changes/partition-time-series-tables/`。

已通过的检查：

- 控制面默认测试与 gormstore、logic、retention、partitionmaint 的竞态检查；Gateway `httpapi` 默认测试与竞态检查（新增「用量重试保持原 `created_at`」契约测试）。
- 临时 MySQL 9.7.4 与 PostgreSQL 16 上 gormstore 全部 live 用例，新增分区用例：
  - 升级保留数据与索引、自增计数不回退；
  - PostgreSQL 转换失败整体回滚、MySQL 已转换后显式 resume；
  - 迁移超时拒绝启动并可恢复；
  - 复合主键下用量去重、扣费不重复、对话轮次重试；
  - 多副本维护、兜底分区搬移；
  - 「至少保留」的清理（截止日分区整段保留、迟到行逐行删除）与多副本并发清理；
  - 没有 DDL 权限时退回分批逐行删除、受限运行账号配合维护账号、积压超过上限交给迁移命令、MySQL 会话锁等待恢复；
  - 查询回归与审计跨月分页；
  - schema 检查按表名拒绝。
- 带分区表的原生备份与恢复演练（mysqldump / pg_dump）。
- `EXPLAIN` 核对：租户倒序分页走跨分区索引扫描，无全量排序；单日范围查询只扫一个分区。
- 本机 Docker 上转换 100 万行 `request_logs`：MySQL 22.4 秒、PostgreSQL 16 为 5.6 秒。删除 16 万行，MySQL `DELETE` 1.81 秒，`DROP PARTITION` 0.085 秒。

既有问题（与本次无关，已在未改动的 HEAD 上复现）：

- PostgreSQL 18 上 gormstore live 用例因 `tenant_nodes` 索引检查失败。
- `TestLiveServiceStartupRelaysRecoveredRevocations` 失败。

尚未验收：

- 生产规模数据的转换耗时。
- 真实多副本部署下的维护与清理。
- 长期运行后分区数与清理节奏的观察。

# Gateway Redis 队列与 Agent 整机准入（2026-11-14）

Gateway 的等待改为全集群：

- **工作流与图像生成**：JSON 工作流提交与 ComfyUI 图像生成进入 Redis + asynq 持久队列（`jobqueue/`），提交立即答 202 `queued`，任一副本的 worker 在节点空闲时派发。
  - 投递至少一次，但提交至多一次：意图标记保证结果未知时不重新提交。
  - 任一副本都能查询、取消排队中的 job，也能订阅它的 `queue_changed` 事件。
  - 图像生成在原有的超时预算内同步等待派发。
- **对话**：对话请求的等待改为 Redis 跨副本 FIFO（`waitqueue/`，带头部窗口与停滞逃逸），派发仍在持有连接的副本上。
- **开启条件**：两个队列默认关闭，开启都必须配 `-redis-addr`，否则拒绝启动。进程内排队已删除，`-*-queue-max-waiters` 改为全集群上限。

Agent 新增实时 CPU/内存采集（不引入 gopsutil），经心跳新字段 `Heartbeat.load` 上报，并提供可选的整机准入：

- **资源闸门**：CPU 或内存越过阈值时拒绝新的重任务，带迟滞。
- **本机信号量**：`cli` 与 `general` 两类，满了先有界等待，超时返回可重试背压。
- **Gateway 与 Console**：Gateway 把资源紧张节点在所有策略中排到最后（不跳过）；Console 机群页显示「实时负载」与「资源紧张」。

升级顺序：

- 负载上报与 Agent 准入不限顺序。
- 队列须等所有 Gateway 副本都升级后再开启。
- 新依赖 `github.com/hibiken/asynq` v0.25.1 只用于 Gateway。

说明见 [Gateway STATUS「工作流 Job 的持久队列」](service/aiServeWeaveGateway/STATUS.md#工作流-job-的持久队列asynq2026-11-14)、[对话请求的全集群等待队列](service/aiServeWeaveGateway/STATUS.md#对话请求的全集群等待队列redis2026-11-14) 与 [Agent README「整机准入与负载上报」](service/aiServeWeaveAgent/README.md#整机准入与负载上报)。

已通过的检查：

- Gateway、Agent、common 与控制面 fleet 的默认测试及竞态检查。
- 新增测试：
  - 队列状态机：意图未确认不重提、持有后交还、期限超时、提交途中取消；
  - 两副本共享内存队列的 HTTP 路径：跨副本派发与查询、取消、队列满 429、Redis 不可用时退回同步、租户隔离、排队事件流、图像排队超时；
  - 对话等待：头部窗口、停滞逃逸、租约过期、Redis 故障；
  - Agent：准入迟滞与信号量、派发分类与豁免、心跳负载、配置往返；
  - Gateway：压力排序。
- 在专用 Redis 7 容器上通过 Lua 脚本与 asynq 端到端投递的 live 用例（`AISW_REDIS_ADDR=… go test -p 1 ./service/aiServeWeaveGateway/jobqueue/... ./service/aiServeWeaveGateway/waitqueue/... ./service/aiServeWeaveGateway/ratelimit/...`；`-p 1` 是因为 ratelimit 的 live 用例会 FLUSHDB）。
- Agent 交叉编译：windows/linux/darwin，amd64/arm64。
- 实际采样：Linux arm64 容器在 256 MiB cgroup 限额下读到 256 MiB 总量，macOS 本机读到估算 CPU。
- Console：378 项单元测试、lint（仅 1 个既有警告）、TS7 类型检查与生产构建。

复审修复（2026-11-14，三路独立代码审查之后）：

- **工作流提交不再重复**：
  - 工作流提交只在确定后端没收到时重试：新增跨隧道哨兵 `ErrNotDelivered`，同步派发也适用；
  - 提交途中的取消不再丢失；
  - 写入意图对同一次投递幂等；
  - 最终写入失败会交还任务；
  - 等待超过时限的孤儿记录在读取时以 `queue_timeout` 收尾；
  - 入队不受客户端断开影响；
  - Redis 读不到时答 503 而不是 404；
  - 图像等待能容忍短暂读失败，放弃时撤回排队任务；
  - 排队事件流加保活；
  - worker 持有时长缩短到 10 秒、默认并发 8，关停等待（35 秒）大于单次提交上限（30 秒）。
- **对话等待**：
  - 按模型分通道，修复跨模型的队头阻塞；
  - 获得资格的票在派发期间被领走，繁忙时按原序号放回；
  - 每次 Redis 调用有短超时，共享客户端改为 1 秒超时、重试 1 次；
  - 新增 `-chat-queue-name`，等待数与轮询间隔设了上下限。
- **Agent**：
  - 迟滞退出线始终可达，并有配置校验；
  - 指标持续缺失时 fail-open；
  - 请求期限到期不再被当作可重试背压；
  - gauge 写入不再乱序；
  - 采样失败后不再上报陈旧数据；
  - cgroup 内存按 MemAvailable 口径计算，并支持嵌套 cgroup；
  - 压力原因变化也会补发心跳。
- **Gateway 节点视图**：
  - 越界时间戳视为未上报，修复一个异常 Agent 能让机群视图变成空 200 的问题；
  - 节点断线时压力指标复位，重连时清除旧负载；
  - 非 least_load 策略的压力排序改为无分配。

调度热路径基准与改动前一致（`PickCandidates` 7 allocs/op）。

尚未验收：

- 多副本真实部署与真实 ComfyUI 下的排队和崩溃恢复。
- 真实负载下的阈值校准。
- Windows 实机采样。
- 孤儿运行（节点已接单、绑定未写入时 worker 崩溃）的对账。

# 商户订单删除（2026-11-13）

商户计费页「订单」新增删除与确认框，owner/admin 可删除任意状态的本商户订单；member 只读。删除持久化为 `checkout_orders.tenant_deleted_at`，仅商户列表隐藏，运维历史、付款恢复、去重、发货、退款与库存占用继续使用原订单。新接口为 `DELETE /admin/v1/billing/orders/:id`，成功 `204`，跨商户、未知与已删除统一 `404`，审计动作 `billing.order.delete`。Console 白名单、八语言提示、失败保留确认框与末页回退已接入。迁移为 PostgreSQL/MySQL `billing/0010_order_deletion.sql`，`sql/init.sql` 已同步；控制面与 Console 须一起更新。行为与升级说明见[商户订单删除](docs/billing-orders.md)。

验证：控制面默认测试及 logic/handler/store 竞态检查、Console 375 项单元测试、lint（仅既有 alerts 页警告）、类型检查与生产构建、Chrome 计费回归 29 项通过。新增共享 store 场景覆盖所有订单状态、租户隔离、分页前过滤、删除后的迟到付款与重放去重；新增 HTTP/logic 用例覆盖授权与审计。真实 PostgreSQL/MySQL 迁移、真实付款与生产部署尚未验收。

# 易支付（EZFP）人民币收款（2026-11-12）

新增易支付（彩虹易支付 V2）作为 Dodo 之外的第二个法币收款通道，租户可选支付宝或微信支付，用于充值包、账号包月与账号预售三条结账流程。只有币种为 CNY 的商品可以走易支付，金额就是现有标价，不另设价格；CNY 商品可以不填 Dodo 产品 ID，此时只能经易支付购买。易支付可与 Dodo 并存（结账时由租户选择），也可单独开启计费。到账只认经平台公钥验签的异步通知或控制面主动查询，两者都走 Dodo 已有的入账事务与去重；Console 回跳只触发一次查询，订单列表另有「再次确认」，后台每分钟补查 2 小时内的待付款订单。退款以退款申请 id 为 `out_refund_no` 同步结算；答复丢失时以退款查询恢复，不自动重发。配置为控制面 `Billing.Ezfp`，密钥经 `AISW_EZFP_PRIVATE_KEY` / `AISW_EZFP_PUBLIC_KEY` 注入，反向代理须暴露 `/billing/v1/ezfp/notify`（已更新 `deploy/` 下的配置示例、`.env.example`、Compose 与 nginx 示例）。迁移为 `billing/0009_ezfp.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则、开通步骤与验证边界见[易支付说明](docs/billing-ezfp.md)。

已通过的检查：控制面默认测试，billing、logic、handler、store、config、e2e 的竞态检查；签名内容与算法与 OpenSSL 生成的固定向量一致；e2e 以真实客户端经 HTTPS 连模拟易支付，跑完「只配易支付 → 建 CNY 充值包 → 结账 → 签名 GET 通知入账 → 重放不重复 → 运维退款」；临时 PostgreSQL 16 与 MySQL 8.0 上的 gormstore live 用例（含 0009 迁移与 `storetest.EzfpOrders`）。Console 375 项单元测试、lint（仅 1 个既有警告）、TS7 类型检查与生产构建通过，本机 Chrome 计费专项 26 项通过；全套浏览器用例中 `console-header.spec.ts` 两项、`console.spec.ts` 三项（320px 的 `/operator/routes`、`/operator/workflow-templates` 与导航 `aria-current`）失败，已在未含本次改动的 HEAD 上复现同样 5 项，与本次无关。未用真实商户号联调（手机微信 jump、`out_refund_no` 防重复、通知重投是否重新签名均未实测），`clientip` 是 Console 的地址而不是付款人真实 IP，尚未部署。

# 公开首页与法律条款（2026-11-12）

为配合 Antom MOR 审核（要求网站公开展示产品、价格、T&C、隐私条款与退款条款），Console 新增公开页面：`/` 未登录时改为产品介绍首页（已登录照旧进入概览），新增 `/terms`、`/privacy`、`/refund`（英文、简中、繁中，以英文为准，其余语言显示英文），首页、价格、登录、注册页加统一页脚，注册页加同意声明，价格页加分区说明，计费页加条款链接。退款条款按现有规则撰写（点数在 `RefundWindowDays` 内退未用部分，经邮件申请；已开通包月不退；预售未交付全额退；支付宝额度不可撤销）。营运方名称、地址、邮箱、准据法与退款天数来自 Console 环境变量 `AISW_CONSOLE_LEGAL_*`（已接入 Compose 与 `.env.example`），未设置时显示方括号占位符。只需更新 Console，控制面、Gateway 与 Agent 无需升级。说明见 [Console README](service/aiServeWeaveConsole/README.md#公开首页与法律条款)。

同时修复 `app/globals.css` 中 `--font-sans` 引用自身的问题（原本整个 Console 实际显示为浏览器默认的 Times 字体，现恢复为已载入的 Geist），这会影响所有页面的西文字形。

已通过：Console 362 项单元测试（含新增 8 项法律页测试与八语言词典检查）、lint、TS7 类型检查与生产构建；本机生产模式下以模拟目录检查 7 个公开页 × 4 种宽度（1440/768/390/320）× 4 种语言（繁中、英、日、德），均无页面横向卷动或元素超出视窗，页首、正文、页脚左右对齐，页脚链接点击高度不小于 24px。条款正文未经法律审阅，营运方资讯仍为占位符，尚未部署，也未提交 Antom 审核。

# 支付宝（一人付）点数兑换（2026-11-11）

新增支付宝付款方式：商户在支付宝购买次数或积分套餐，额度留在支付宝，在 Console 计费页「支付宝」分区兑换为点数。控制面先保存兑换（编号即一人付幂等键），只有一人付确认核销成功且编号、数量、单位一致时，才在同一事务里记入 `alipay_topup` 点数；结果未知时以原编号查询或原参数重发，后台每分钟恢复，绝不换号再扣。购买入口、回跳和扫码都不入账。Dodo 改为可选，只配支付宝也能开启点数计费（充值包、结账、门户与退款随 Dodo 关闭）。API Key 只从控制面环境变量 `opcpay_api_key` 读取。迁移为 `billing/0007_alipay.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则、开通步骤与验证边界见[支付宝说明](docs/billing-alipay.md)。

已通过的检查：控制面默认测试与 opcpay 客户端、logic、handler、store 的竞态检查；临时 PostgreSQL 16 与 MySQL 8.0 上的 gormstore live 用例（迁移、schema 检查、并发结算只入账一次、并发创建只留一笔待确认）。Console 347 项单元测试、lint、TS7 类型检查与生产构建通过，本机 Chrome 计费专项 17 项通过。未配置真实 `opcpay_api_key`，没有只读查询真实能力与套餐，也没有真实支付宝购买、核销和部署验收；只支持次数/积分套餐，一人付通知与退款未接入。

# 账号预售（2026-11-11）

新增账号预售：运维只定义类型（Claude Code / Codex）、法币月价、Dodo 产品 ID 和名额上限，不绑定具体账号。商户经 Dodo 付款后，订单进入「已付款，待发货」，此时不开通账号，也不切换计费模式。运维在 `/operator/billing` 的「账号预售 → 待发货订单」为每笔订单分配一个同类型的空闲库存账号，分配时开通一个自然月并切换为账号包月；无法交付时可以在同一列表全额退款。账号商品新增 `kind`（创建时必填），下架商品可以不标价，仅供预售分配。迁移为 `billing/0006_account_presale.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。规则与契约见[账号预售](docs/billing-accounts.md#账号预售)。

**支付宝额度支付（2026-11-11 新增）**：运维可在预售上设置「支付宝额度数量」（0 或留空表示不走支付宝），商户在 Console「账号购买」用支付宝额度支付一个月。控制面先保存待支付订单与待确认核销（`action_id` 幂等），只有一人付确认核销成功才把订单标为已付款并进入「待发货」；确定失败则订单失败并释放名额；结果未知保持待确认并由原恢复流程续办。支付宝订单**系统内不可退款**（一人付没有核销撤销），由运维线下处理。次数包每次核销只能扣 1，数量大于 1 的预售需要积分包。迁移 `billing/0008_alipay_presale.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。未用真实 `opcpay_api_key` 验收，gormstore 的预售支付宝路径只由共享规则与默认测试覆盖，未在临时 PostgreSQL/MySQL 上跑 live 用例。

已通过的检查：控制面默认测试，store、logic、handler 与 e2e 的竞态检查；在临时 PostgreSQL 16 与 MySQL 8.0 上运行的 gormstore live 用例（迁移、升级保留数据、账号结账、预售）。Console 侧通过 338 项单元测试、lint、TS7 类型检查与生产构建，本机 Chrome 计费专项 15 项全部通过。`console.spec.ts` 中有 1 项与本次改动无关的导航用例失败。真实 Dodo 付款、本地与生产部署、真实 CLI 账号的发货联调尚未验收。

# 账号包月改为法币标价（2026-11-11）

账号包月商品改为以法币标价（币种 + 月价 + Dodo 产品 ID），商户经 Dodo 在线结账购买或续费一个月，不再使用点数余额；点数购买路由已移除。付款 webhook 验签后才开通期限，待付款结账为本商户保留账号 30 分钟，超时后被他人买走的付款记为「未开通，待退款」。迁移 `billing/0005_account_fiat.sql` 会把现有商品下架并清零价格（已售期限保留），运维须重新定价上架。购买需要 `Billing.Enabled` 并配置 Dodo，见[账号包月说明](docs/billing-accounts.md)。

控制面默认测试与竞态检查、Console 328 项单元测试、lint、TS7 类型检查、生产构建和 Chrome 计费专项 12 项通过；临时 MySQL 8.0 实测迁移（含旧数据升级）与结账/付款事务通过。PostgreSQL live、真实 Dodo 付款、本地与生产部署尚未验收。

# Agent 默认不共享与运维控制（2026-11-09）

机群页现可直接对每个平台注册节点确认开启或停止共享（2026-11-10）。新增 Agent 接入/审批共享选项与共享设置，默认不共享；未配置的 Agent 也不进入普通租户的模型目录和调度池。有效的独享账号购买权益与专属租户绑定保留。基础迁移 `0015_node_sharing.sql`、授权载荷和 Gateway 检查已实现，使用及升级顺序见[Agent 共享设置](docs/agent-sharing.md)。

控制面与 Gateway 相关默认/竞态测试、326 项 Console 单元测试、共享操作与运维宽窄屏 Chrome 检查通过；隔离 MySQL 的迁移、模式持久化与账号购买回归通过。本地 Gateway、控制面和 Console 已备份后重建，真实运维/租户登录验证：1 台 Agent 已接入、无显式共享，租户模型目录不包含其服务；共享设置 API 返回 200，租户写入被拒绝。PostgreSQL live、生产升级和真实 CLI 账号联调尚未验收。


## Agent 账号独享包月（2026-11-09）

已交付 Claude Code 与 Codex 账号商品、点数余额购买/续费、独享授权与 Console 入口。同一个 Agent 中的两种账号可分别售给不同商户。账号以 Agent ID + 运行时 ID 定位，按 UTC 自然月延长期限；购买后切换为账号包月模式，仅可调用自己已购且未到期的账号。接口、迁移和升级顺序见 [账号包月说明](docs/billing-accounts.md)。

账号包月现支持独立开关 `AccountSales.Enabled`，不要求配置 Dodo。新增表单改为选择 Agent 与实际上报的 Claude Code / Codex 账号，排除重复商品，并支持刷新清单与失败重试。本地 Docker 的控制面和 Console 已重建、启用独立开关，真实运维登录后账号 API 返回 200；Agent 接入后的默认共享准入见上节，真实 CLI 账号售卖联调尚未验收。

两种账号同 Agent 分别售卖的 logic 测试和真实隧道加模拟后端的 scheduler 测试（Claude 原生 Messages / Codex Chat）已通过竞态检查；两种真实上游登录的售卖联调仍待验收。

默认测试、相关竞态检查、Console 类型检查与生产构建通过；Chrome 计费专项 11 项和隔离 MySQL 9.7 账号事务实测通过。PostgreSQL live 测试未运行，生产升级、真实付款及实际 CLI 账号联调尚未验收。

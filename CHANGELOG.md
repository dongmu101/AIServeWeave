# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/) — with one caveat while it stays on
`0.x`: **minor version bumps may still contain breaking changes** (protocol,
config, database schema, CLI flags). Cross-version rolling upgrades and
protocol/config compatibility are not yet a supported goal — see the `A03`
entry in [STATUS.md](STATUS.md). Read a release's own entry for what changed
before upgrading; do not assume a `0.x` bump is safe by default.

本文件记录所有值得关注的变更。格式遵循 [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)，
版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)——但在 `0.x` 阶段有一条例外：
**minor 版本升级仍可能包含破坏性变更**（协议、配置、数据库 schema、CLI flag）。跨版本
滚动升级与协议/配置兼容性尚未作为已支持的目标，见 [STATUS.md](STATUS.md) 的 `A03` 条目。
升级前请读清楚目标版本自己的条目，不要默认 `0.x` 的版本升级是安全的。

## [Unreleased]

### Added

- 文件登记表：Gateway 写进对象存储的每个文件都在控制面新表 `files`（base 迁移 `0022`，按月分区）登记，记下存储、key、所属业务对象（`biz_type` + `biz_id`）、大小与 sha256；`job_artifacts` 新增 `file_id`（jobs 迁移 `0008`）。新增内部接口 `/internal/v1/files`。升级顺序：先控制面，再 Gateway。见 [STATUS.md](STATUS.md#文件登记表-files2026-11-27)。

- 租户自有对象存储：租户 owner/admin 可在 Console 为本租户配置 S3 或 WebDAV，job 产物与 Studio 文件随后写进租户的存储，保留期由租户设置。控制面新增 `GET`/`PUT`/`DELETE /admin/v1/storage`、表 `tenant_storage`（base 迁移 `0021`）与配置 `TenantStorage`（`MasterKey`/`MasterKeyFile`/`AllowPrivateEndpoints`，环境变量 `AISW_TENANT_STORAGE_MASTER_KEY`，默认关闭）；`objectstore` 包移到 `common/objectstore`。部署存储（Gateway `-artifact-storage`）可在 `deploy/.env` 用 `AISW_ARTIFACT_STORAGE*` 开启，凭据可从环境变量读取。升级顺序：先控制面，再 Gateway 与 Console。见 [STATUS.md](STATUS.md#租户自有对象存储2026-11-27)。

- Studio 编排新增「图像生成」（`image_generation`）与「ComfyUI 模板」（`comfyui_template`）节点，运行产出的图片与文件可在运行视图中查看与下载。Gateway 新增 `GET /studio/v1/image-models`、`GET /studio/v1/workflow-templates`、`GET /studio/v1/runs/{id}/artifacts/{artifact_id}`。产物字段只能作为输出 ref 的全部内容被引用（新问题码 `artifact_reference`）。推理生图节点需要 Gateway 配置 `-artifact-storage`。升级顺序：先控制面，再 Gateway 与 Studio。见 [STATUS.md](STATUS.md#studio-编排第二期节点2026-11-27)。

- 跨地域隧道优化（Gateway + Agent，升级顺序不限，任一侧为旧版时对应项不生效）：
  - 大于 16 KiB 的请求 payload 由 Gateway 以 zstd 压缩后发往 Agent（`Ready.payload_encodings` / `RequestHeaders.payload_encoding`，均为新增字段；只对声明支持的槽压缩，流式响应不压缩）。见 [tunnel README「payload 压缩」](service/aiServeWeaveAgent/tunnel/README.md#payload-压缩)。
  - Gateway 每 15s 经 Control 流 Ping 测量各节点链路 RTT（新指标 `tunnel_server_node_rtt_seconds`，`tunnelserver.Config.PingInterval`）；调度器在负载完全相同的候选之间优先选 RTT 档位更低的节点。
  - Agent 的 bulk 槽（产物上传下载、音频）改用到同一副本的第二条 TCP 连接，丢包重传不再阻塞推理流；每个 Agent 对每个副本多一条连接。
  - 部署：Gateway 容器设置 `net.ipv4.tcp_slow_start_after_idle=0`；[deploy/README「跨地域隧道网络调优」](deploy/README.md#跨地域隧道网络调优) 说明宿主机 BBR、缓冲区与 netem 基线测量。

- CLI 订阅额度调度（Gateway + Agent）：
  - Gateway 排序新增「快到期」档：窗口进入最后 `-quota-expiring-percent`（新 flag，默认 10%；负值关闭）的时间、按均匀速度还会剩下额度的账户，在节点有空闲槽、其他窗口没有明显超速时排在最前，先用掉重置时会作废的额度；进行中对话的钉选账户（额度充足、有空闲槽、无资源紧张）仍排在它之前，对话不会因此迁走。
  - Gateway 在 CLI 账户答复 `rate_limited` 时，依据快照推断拒绝它的窗口，把该窗口覆盖的同账户其他模型一并冷却到同一时刻（按系列的窗口只覆盖同系列）；之后的快照显示未用完时立即解除。
  - Gateway 新增指标 `gateway_scheduler_quota_first_choice_total{tier}`、`gateway_scheduler_quota_rate_limited_total{tier}`、`gateway_scheduler_quota_cooldown_spread_total`。
  - Agent 额度监视在有窗口用到 80% 时每分钟刷新；Claude Code 的 `rate_limit_event`（`allowed_warning`/`rejected`）与 Codex 的额度失败触发立即刷新（受每分钟一批约束）。`runtime.Dependencies` 新增可选的 `Quota`（`runtime.QuotaGuard` 接口：`Notice`、`Refused`、`Hold`）。覆盖所有模型的窗口已用完的账户不按每分钟刷新。
  - Agent 已知账户用完时在本地作答：Claude Code 的 Messages、Codex 的对话与生图在回合开始前询问额度监视器，用量读数显示适用窗口到 100%（重置尚未到来），或 CLI 报告了带重置时间的拒绝时，直接答复可重试的 `rate_limited`（`retry_after` 到重置为止），不启动 CLI、不向服务商发出只会被拒绝的请求；拒绝后至少 2 分钟的读数显示未用完时提前解除，最多搁置 8 天。开启额外用量的 Claude 账户到 100% 后同样被搁置。
  - Agent 对没有可用重置时间的额度拒绝（未给出或已过去；Codex 补读窗口失败、生图额度用完；Claude 无 `rejected` 事件的上游 429）也在本地按退避搁置：1 分钟起，1 小时内再被拒即翻倍，至多 1 小时，读数不提前解除。此前这类拒绝不搁置，后续回合仍会发往服务商。Claude 的这种 429 改报可重试的 `rate_limited`（此前为 `upstream`）。`runtime.QuotaGuard.Refused` 的约定随之改为「零或已过的 `resetsAt` 表示重置时间未知，由守卫按退避搁置」。
  - Gateway：CLI 订阅账户连续不带重置时间被拒时，限流冷却逐次翻倍（30s 起，至多 `-max-rate-limit-cooldown`），一次成功后复位；其他后端仍为固定时长。
  - 升级顺序不限，两侧各自生效。见 [Gateway STATUS「按订阅额度主动降级」](service/aiServeWeaveGateway/STATUS.md#按订阅额度主动降级)。

- Agent 本机 CLI 回合分段计时：
  - Claude 原生回合与 Codex 对话回合在结束日志中记录进程启动、CLI 就绪、模型请求发出、上游响应头、首个事件、结束、退出等阶段的耗时，以及上游连接等待、进程 CPU 与峰值内存。
  - Codex 新增 `Codex round finished` 日志；`/metrics` 新增 `agent_cli_phase_seconds`、`agent_cli_upstream_connect_seconds`、`agent_cli_process_cpu_seconds`、`agent_cli_process_max_rss_bytes`。
  - 新增离线基准 `TestCLIStartupBenchmark`（`AISW_CLI_BENCH=1`），不联网、不消耗额度。
  - 只需升级 Agent。见 [Agent README](service/aiServeWeaveAgent/README.md#本机-cli-回合分段计时)。
- Gateway 工作流持久队列（`jobqueue/`，Redis + `github.com/hibiken/asynq` v0.25.1）：`-workflow-queue-max-wait > 0`（必须配 `-redis-addr`）后，JSON 工作流提交与 ComfyUI 图像生成进入全集群队列，任一副本的 worker 在节点空闲时派发。新增 flag `-workflow-queue-concurrency`（默认 8）与 `-workflow-queue-name`（默认 `aisw-workflow`）；`-workflow-queue-max-waiters` 改为全集群长度上限（默认 64），`-workflow-queue-retry-interval` 默认 1s。提交答 202 `queued`，`durability` 新增 `shared_queue`，Job 视图新增 `error_code`（`queue_timeout`、`submit_outcome_unknown`、`node_not_allowed`、`cancelled`、`internal_error`）；队列满答 429 `queue_full`（`Retry-After: 5`），入队结果无法确认答 503 `queue_unavailable`。投递至少一次、提交至多一次（意图标记，结果未知不重提）。事件流对排队 job 推送 `queue_changed`。新增指标 `gateway_workflow_queue_*`，移除 `gateway_scheduler_queue_*`。
- Gateway 对话等待改为全集群 Redis FIFO（`waitqueue/`，按请求模型分通道）：`-chat-queue-max-wait > 0`（必须配 `-redis-addr`）后，候选全忙的新 Chat/Responses/Messages 请求在共享队列中按头部窗口（新增 `-chat-queue-head-window`，默认 8）与停滞逃逸重试，派发仍在原副本；新增 `-chat-queue-name`（默认 `aisw-chat`），`-chat-queue-retry-interval` 最小 50ms、`-chat-queue-max-waiters` 最大 1024。`-chat-queue-max-waiters` 改为全集群上限。Redis 故障时立即 429。新增指标 `gateway_scheduler_chat_queue_store_errors_total`，`gateway_scheduler_chat_queue_depth` 改为本副本等待数。
- Agent 实时负载与整机准入（`admission/`）：采集实时 CPU、内存、负载均值（Linux `/proc` + cgroup v2、macOS sysctl 估算 CPU、Windows kernel32，不引入 gopsutil），经心跳新字段 `Heartbeat.load`（`NodeLoad`，隧道 proto 字段 4，只增不改）上报；可选资源闸门 `-admission-cpu-high`/`-admission-memory-low`（带迟滞）与本机分类信号量 `-admission-cli-max`/`-admission-general-max`（满时有界等待 `-admission-queue-max-wait` 默认 5s、`-admission-queue-max-waiters` 默认 16，之后可重试背压），YAML `admission:` 区块，新增 `agent_admission_*` 与 `agent_host_*` 指标。默认全部关闭。Gateway 把资源紧张节点在所有调度策略中排到最后（不跳过），`/internal/v1/nodes` 输出 `load`，新增 `tunnel_server_node_pressure`；Console 机群页显示「实时负载」与「资源紧张」。升级顺序不限：旧 Gateway 忽略字段 4，旧 Agent 显示为「未上报」。

- 易支付（EZFP，彩虹易支付 V2）人民币收款：作为 Dodo 之外的第二个法币通道，服务充值包、账号包月与账号预售，租户可选支付宝或微信支付；只有币种为 CNY 的商品可走易支付，CNY 商品可以不填 Dodo 产品 ID。控制面新增 `Billing.Ezfp`（`Enabled`、`PID`、`PrivateKey`、`PublicKey`、`Types`、`NotifyURL`、`ReturnURL`、`BaseURL`），密钥经 `${AISW_EZFP_PRIVATE_KEY}` / `${AISW_EZFP_PUBLIC_KEY}` 引用；新增公开的 `GET`/`POST /billing/v1/ezfp/notify`（纯文本 `success`/`fail`）与 `POST /admin/v1/billing/orders/:id/refresh`；三个结账请求体新增可选的 `provider`（`"ezfp"`）与 `pay_type`；`payment_methods` 新增 `ezfp`、`ezfp_types`；租户视图的充值包与账号新增 `dodo_available`/`ezfp_available`，预售新增 `ezfp_available`；订单新增 `provider`、`pay_type`。到账只认验签的通知或主动查询，与 Dodo 共用入账事务与去重；后台每分钟补查待付款订单与处理中的退款。迁移 `billing/0009_ezfp.sql`（PostgreSQL / MySQL）。**行为变化**：`Billing.Enabled` 现在需要 Dodo、支付宝或易支付之一；只配易支付时充值包、三个结账与退款路由照常挂载，Dodo webhook 与客户门户不挂载；公开目录的预售 `available` 改为「任一已配置法币通道可售且有名额」。控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。详见[易支付说明](docs/billing-ezfp.md)。

- 账号预售支持支付宝额度支付：预售新增 `alipay_amount`（运维「支付宝额度数量」，0 表示不走支付宝），新增 `POST /admin/v1/billing/presales/:id/alipay`（租户 owner/admin，`{action_id, amount}`，成功或失败 `200`，待确认 `202`）。先保存待支付订单与待确认核销，只有确认核销成功才标记已付款并进入「待发货」，确定失败订单失败并释放名额，未知结果由原恢复流程续办。支付宝订单不可在系统内退款。迁移 `billing/0008_alipay_presale.sql`；控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。详见[支付宝说明](docs/billing-alipay.md)。
- 支付宝（一人付 opcpay）点数兑换：商户在支付宝购买次数或积分套餐，在 Console 计费页「支付宝」分区把额度兑换为点数。控制面新增 `Billing.Alipay`（`Enabled`、`CreditsPerUnitMicro`、`ReturnURL`、`BaseURL`），API Key 只从环境变量 `opcpay_api_key` 读取；新增 `GET /admin/v1/billing/alipay`、`POST /admin/v1/billing/alipay/purchases`、`GET/POST /admin/v1/billing/alipay/redemptions`、`POST /admin/v1/billing/alipay/redemptions/:id/refresh`，账目类型 `alipay_topup`，`GET /admin/v1/billing` 增加 `payment_methods`。兑换先落库再核销，只有确认成功且编号、数量、单位一致时才在同一事务入账；结果未知时以原编号恢复，后台每分钟重试。迁移 `billing/0007_alipay.sql`（PostgreSQL / MySQL）。**行为变化**：`Billing.Dodo` 改为可选，`Billing.Enabled` 只需 Dodo 或支付宝之一；未配置 Dodo 时充值包、结账、客户门户、webhook 与退款路由不挂载，公开目录只返回模型单价。控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。详见[支付宝说明](docs/billing-alipay.md)。

- 账号预售：运维按类型（Claude Code / Codex）、法币月价、Dodo 产品 ID 与名额上限创建预售，商户经 Dodo 结账预购一个月；付款确认后订单进入「已付款，待发货」，不开通、不切换计费模式。运维在 `/operator/billing` 的「账号预售 → 待发货订单」为订单分配同类型的空闲库存账号，分配时开通一个自然月并切换 `account_monthly`；无法交付时可在同一列表全额退款。新增 `GET /admin/v1/billing/presales`、`POST /admin/v1/billing/presales/:id/checkout`（仅 `Billing.Enabled`）、`GET/POST /operator/v1/billing/presales`、`PUT /operator/v1/billing/presales/:id`、`POST /operator/v1/billing/orders/:id/assign`，平台订单列表新增 `fulfillment=awaiting` 筛选；订单增加 `presale_id`，账号增加 `kind`，公开目录增加 `presales`。迁移 `billing/0006_account_presale.sql`（PostgreSQL / MySQL）。**行为变化**：创建账号商品必须带 `kind`（Console 取自 Agent 上报）；下架的账号商品可以不标价，仅供预售分配。控制面与 Console 须一起更新，Gateway 与 Agent 无需升级。详见[账号包月说明](docs/billing-accounts.md#账号预售)。

- 未带 `safeguards` 的 Claude 原生 Messages 直通。新增能力 `anthropic_messages_native`（Claude 运行时默认声明）与追加隧道字段 `MessagesRequest.native`（7）。Gateway 对至少有一个声明该能力节点的模型，把未带 `safeguards` 的 `/v1/messages` 原样、无状态地交给 Agent，`Scheduler.MessagesStart` 只派给声明能力的节点；Agent 以与安全检查路径相同的方式经本机已登录 CLI 交给上游，不再把 adaptive 思考改成固定预算，不再去掉 `cache_control`、`context_management`，也不改写延迟工具、对话中途 system 或工具 ID。每轮结束停止 CLI，不需要 Redis 续接记录，可跨副本重试。Office、视频与文本文件附件在发送前转换为原生内容块。旧版 Agent、答复桥接工具调用（`toolu_aisw_`）的回合及 Chat/Responses 继续走兼容桥接。**行为变化**：新版 Agent 与 Gateway 都部署后，Claude Code 等客户端的 `/v1/messages` 默认改走原生直通，工具 ID 改为上游原始 ID，每个工具回合新开一次 CLI 进程。须先升级 Agent，再升级 Gateway。

- 请求侧开启思考与 Anthropic 签名往返。隧道契约追加 `ChatRequest.reasoning`（12，`ReasoningConfig`）、`ChatMessage.reasoning_blocks`（8）与 `ChatMessageDelta.reasoning_block`（5，`ReasoningBlock`），新旧版本混跑时缺省为空，旧 Agent 忽略开关。Gateway 接受 Chat `reasoning_effort`、Responses `reasoning`、Anthropic `thinking`、Ollama `think`，格式错误返回 400 并点名参数；合法值是提示，模型明确不支持思考时被去掉，不会因此拒绝请求。适配器按种类映射：vLLM/SGLang/TRT-LLM/TokenSpeed 用 `chat_template_kwargs`，Ollama 与 OpenAI 系用 `reasoning_effort`，通义千问用 `enable_thinking`/`thinking_budget`，智谱用 `thinking`，Anthropic 用 `thinking.budget_tokens`。Anthropic 后端签过名的 `thinking`/`redacted_thinking` 块经 Anthropic 前门原样输出（流式 `signature_delta`），客户端回传后回放给上游，工具循环里的扩展思考因此可用。Agent 运行时新增可选的 `reasoning_parser`（`think_tags` / `think_tags_prefilled`），把内联在回答里的 `<think>…</think>` 切成思考与回答，只适用于 OpenAI 兼容种类；设置页面保存时保留这个文件专有字段。**行为变化**：Chat 请求里的 `reasoning_effort` 此前被静默忽略（Claude CLI 模型则直接拒绝），现在转发给后端或换算为 Claude CLI 的思考预算，非法值返回 400。租户 API 参考同步：chat-completions（含 nostream）、messages、ollama-chat、ollama-generate 种子 revision 2，responses revision 3，8 种语言译文同步。

- 推理内容透传：模型的思考过程不再在链路上被丢弃。隧道契约新增 `ChatMessage.reasoning_content`（7）、`ChatMessageDelta.reasoning_content`（4）、`Usage.reasoning_tokens`（4），均为追加字段，新旧 Agent/Gateway 混跑时缺省为空。OpenAI 兼容后端的 `reasoning_content` 与 `reasoning` 两种字段名都会解析，Anthropic 后端解析 `thinking` 块。Gateway 输出：Chat Completions 为 `reasoning_content`，Responses 为 `reasoning` 输出项（`reasoning_text` 部件，流式 `response.reasoning_text.delta`），Anthropic Messages 为打头的 `thinking` 块（签名为空），Ollama 原生 API 为 `thinking` 字段；后端报告时输出 `reasoning_tokens`。客户端回传的思考一律不转发给后端，Anthropic 前门不再拒绝 assistant 回合里的 `thinking`/`redacted_thinking` 块。**行为变化**：Anthropic 流式响应的内容块改为随内容到达再打开，工具调用之后的文本会开新的 `text` 块；没有思考时块的下标与类型与此前一致。

- 请求检索（C28）覆盖全部推理前门：Gateway 的请求日志除 chat/responses/embeddings/models 外，新增记录 `images`、`messages`（Anthropic Messages，经 `x-api-key` 认证的请求同样带 key 展示形式）、`transcriptions`、`translations`、`rerank`、`ollama_chat`、`ollama_generate`、`ollama_embed`。取值不超过 `request_logs.endpoint` 的 16 字符，无需数据库迁移；Job 路由与 Ollama 模型管理路由仍不记录。Gateway 指标的 `endpoint` 标签新增 `audio_transcriptions`、`audio_translations`、`rerank`，三者此前落在 `other`。
- Claude CLI 模型支持 `POST /v1/responses`（JSON 与 SSE）。Gateway 先按普通 Responses 的规则还原 `input`，再复用原生 Chat 转换与 Messages 会话协调；续接沿用身份、完整历史与原节点绑定校验，Agent 无需升级。同一回合被拆开的 assistant 文本项与调用项会合并回一条；`function`/`custom`/`local_shell`/`namespace` 工具按原类型还原。`store: true`、`previous_response_id`、`top_p`、非 `text` 的 `text.format`、非 `auto` 的 `tool_choice`、`strict: true` 工具和图片/文件内容明确返回 400。用量与指标记为 `responses`。租户 API 参考 `responses` 种子新增「Claude 系列模型的参数限制」一节（revision 2），8 种语言译文同步。内部重构：`nativeMessagesOutput` 的输出适配改为 `nativeOutput` 接口，Chat 行为不变。
- 公开 Job 响应（提交、状态、取消）新增 J01 设计的 `durability` 字段：`memory_only` 表示只在受理副本的内存里，`persisted` 表示控制面已确认落库，只会单向变化。租户 API 参考文档（`workflow-runs`、`job-status`、`job-cancel` 三个种子，revision 递增）与 8 种语言的译文同步更新。

- Agent 新增 TensorRT-LLM（`kind: trtllm`，`trtllm-serve`）与 TokenSpeed（`kind: tokenspeed`，`tokenspeed serve`，前端为 SMG）两种本地推理后端，均走 OpenAI 兼容 HTTP。TensorRT-LLM 以 `/v1/models` 的 `owned_by="tensorrt_llm"` 核实身份，`/server_info` 判定是否为主 LLM 角色；TokenSpeed 以 SMG 的 `/readiness` 探测与健康检查，身份不强验证（`IdentityVerified=false`）。`-auto-discover` 在 8000 端口依次探测 vLLM、TensorRT-LLM、TokenSpeed，三者探测互斥；vLLM 的 Probe 现在会拒绝 `owned_by` 为 `tensorrt_llm` 的服务。**行为变化**：本机自动发现改为按地址去重，一个地址已以任何种类注册（含手工声明）时不再注册其他种类。本地设置页的后端下拉框同步新增两项。
- Gateway 新增 `-scheduler-policy`（`least_load` 默认 / `power_of_two` / `prefix_hash`）与 `-scheduler-prefix-bytes`：只改变同一路由 target 内的候选顺序。`prefix_hash` 按「直到第一条 user 消息」的提示前缀做 rendezvous 哈希，让同一对话落到同一节点以复用提示缓存；工作流始终用 `least_load`。
- 工作流模板可视化编辑（P03 后续）：Console `/operator/workflow-templates` 以 `wujie` 同源挂载官方 ComfyUI_frontend 1.55.17，可在画布上编辑模板图、切换 App 模式并导出回草稿；控制面新增 `internal/comfyeditor` 只编辑桩后端（`ComfyEditor.DistDir` 启用，编译内置 678 个 ComfyUI 核心节点，自定义节点 JS 永不提供，执行与上传一律 403）与 `scripts/fetch-comfyui-frontend.sh`（固定版本、SHA-256 校验，GPL-3.0 发布包不进仓库）。`common/workflowtemplate.Content` 新增可选 UI 工作流 `workflow`（迁移 0003 追加 `workflow_json` 列），`MaxContentBytes` 增至 8 MiB + 256 KiB。**升级须先 Gateway 后控制面**：旧 Gateway 不认识该字段，会拒绝含布局的整包并继续使用上一份缓存。
- Agent 本机自动发现新增 SGLang：`-auto-discover` 除 Ollama（11434）、vLLM（8000）外，还探测 `127.0.0.1:30000`（SGLang `launch_server` 默认端口），应答即按 `sglang` 注册。SGLang 无法从 OpenAI 兼容响应核实身份，快照中 `IdentityVerified=false`；Gateway 本机清单不受影响，仍只探测显式配置的 Ollama/vLLM 地址。
- Codex CLI 接入：Gateway 的 `POST /v1/responses` 现在完整承载工具循环——流式输出 `function_call` 项与参数事件，接受 `function_call`/`function_call_output`/`custom_tool_call`/`local_shell_call` 及其输出、`reasoning`（丢弃）作为输入项，接受 `custom`、`local_shell`、`namespace` 工具并按各自类型还原，`developer` 角色映射为 `system`，`tool_choice` 对象改写成 Chat 形状；`web_search` 工具被略去并在 `X-Gateway-Ignored-Tools` 响应头中列出；截断的流式回复以 `response.incomplete` 收尾。同时修复了 message 项的 `output_item.added` 因空 `content` 被省略、导致 Codex 报 `OutputTextDelta without active item` 的旧缺陷。已用 codex-cli 0.156.1 对真实 Gateway 联调；配置见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md) 的「工具循环与 Codex CLI 接入」。 新增 `scripts/codex-model-catalog.sh`：基于已安装 Codex 自带目录为自建模型生成 `model_catalog_json`，消除 `Model metadata … not found` 警告并让 Codex 按真实上下文窗口压缩。
- P09b 告警（C29）：控制面新增 `alert_rules`/`alert_instances` 两张表（PostgreSQL/MySQL 双支持，固定版本 SQL 迁移），新增 `internal/alertengine` 后台评估循环——随服务启动无条件运行，默认每 60 秒一轮，把 `metrics_history` 的原始 Prometheus 计数器/直方图汇总行折算成 `request_rate`/`success_rate`/`latency_p95`/`token_usage`/`capacity` 五个派生指标（`success_rate`/`latency_p95` 是把此前只存在于 Console 前端 `lib/console/metrics-charts.ts` 的近似算法移植成 Go 实现），在规则的连续桶窗口内比较阈值，按去抖动规则创建/维持/自动解除告警实例；新增 Webhook 发送器，固定 3 次尝试、指数退避、无持久投递队列；控制面新增 5 个规则 CRUD 端点（`/operator/v1/alert-rules`）与 2 个实例端点（`GET /operator/v1/alerts`、`POST /operator/v1/alerts/:id/acknowledge`）；Console 新增 `/operator/alert-rules`、`/operator/alerts` 两个页面。
- P09a 请求与错误检索（C28）：控制面新增 `request_logs` 表（PostgreSQL/MySQL 双支持，固定版本 SQL 迁移，默认保留 30 天，独立协程每日清理），Gateway 新增请求日志中间件与有界后台推送器——对已鉴权的 chat/responses/embeddings/models 四个前门端点按状态码分类进封闭 outcome 枚举，异步批量上报，缓冲满时丢弃并计入 `gateway_request_log_dropped_total`；控制面新增 `POST /internal/v1/requestlogs`（按主键幂等）、`GET /admin/v1/requests`（租户）、`GET /operator/v1/requests`（运维，跨租户）；Console 新增 `/console/requests`、`/operator/requests` 两个只读检索页。不记录请求体、响应体、模型名、node_id、Prompt 或鉴权头。
- P08 指标、追踪与历史监控：Registry 与控制面接入 `common/metrics`（新增 `-metrics-addr`、指标目录与标签基数测试）；控制面新增 `internal/metricshistory` 定时抓取 Gateway/Registry 的 Prometheus 文本、聚合后写入固定版本 SQL 落地的 `metrics_history_points` 表（复用现有关系库，不引入独立时序数据库）与保留期清理；`GET /operator/v1/metrics/history` 与 Console `/operator/metrics`（C27，ECharts `dataZoom`）；新增共享 `common/reqid` 包，把 Gateway 前门的 request_id 带到 Scheduler/Tunnel/Agent 的结构化日志，实现最轻量的跨服务请求关联。
- P07 数据库升级与恢复：双引擎固定版本 SQL、锁与校验和/dirty 账本、独立 up/status/resume 命令、已有数据升级及原生备份恢复测试；事务 outbox 跨崩溃补发 Key 吊销。
- P05 用户与会话生命周期：Redis 权威可吊销会话、租户用户与平台运维改密/重置/禁用/启用/角色及会话管理，以及对应 Console 页面。
- P06 Key 吊销通知：ControlPlane 以 Redis generation 长轮询唤醒 Gateway；健康链路 1 秒内清缓存，通知异常时 Gateway 立即停用正向缓存并逐请求校验。

### Changed

- Claude 运行时的回合进程默认关闭 CLI 遥测：设置 `DISABLE_TELEMETRY=1` 与 `CLAUDE_CODE_GB_DISK_CACHE_WHEN_TELEMETRY_OFF=1`，CLI 不再在模型请求前等待功能开关拉取，改读磁盘缓存的功能开关；账户额度查询照常开着遥测，每 5 分钟刷新该缓存。Mac mini 实测模型请求发出从约 0.6–1.1 s 提前到约 0.18 s，上游收到的模型请求除每进程 ID 外逐字段一致。install.sh 新装与升级的节点都生效；按运行时写本地字段 `cli_telemetry: true` 恢复开启，Gateway 下发不能改动。只需升级 Agent。见 [Agent README「CLI 遥测（默认关闭）」](service/aiServeWeaveAgent/README.md#cli-遥测默认关闭)。

- Claude 运行时在各回合之间缓存 CLI 发模型请求前必须等待的启动请求答复（`policy_limits` 与功能开关求值）：命中时立即答复，同时后台刷新，超过 10 分钟未刷新成功的答复不用；组织策略或功能开关的变更通常在下一回合生效，最迟 10 分钟。每回合省掉两次串行上游往返。新增本地字段 `cache_startup_requests: false` 按运行时关闭（Gateway 下发不能改动），回合结束日志新增 `startup_cache_hits` 与 `startup_cache_misses`（未命中原因，只含名称不含值）；OAuth 账户改用 `/api/eval-authed/` 时同样缓存。只需升级 Agent。见 [Agent README「启动请求缓存」](service/aiServeWeaveAgent/README.md#启动请求缓存)。

- Agent 隧道改用固定的大 HTTP/2 流控窗口（每流 `MaxTunnelMessageBytes`，连接为其两倍），超过 64 KiB 的请求 payload 不再每 64 KiB 等一个 `WINDOW_UPDATE` 往返。跨地域节点（往返约 350 ms）上，64 KiB–2 MB 的 Messages 请求从派发到 Agent 开始处理的时间预计从约 0.9 s 降到约 0.4 s，更大的请求降得更多。只需升级 Agent。见 [tunnel README「HTTP/2 流控窗口」](service/aiServeWeaveAgent/tunnel/README.md#http2-流控窗口)。
- Gateway 的 Studio 与租户存储保留期清理改为按控制面 `files` 表进行，不再按前缀列举对象存储；保留期规则不变。`file_id` 非空的 job 产物随文件一起清理，原有 artifactcleanup 只处理 `file_id` 为空的产物。登记不可达时 Gateway 不写入文件（jobPersister 下轮重试，Studio 上传回 503）。

- CLI 订阅额度调度行为变化：
  - Gateway 的「紧张」档改为同时看距重置的时间：已用超过 `-quota-low-percent`，但剩余额度足以按均匀速度撑到重置的窗口不再降级（例如五小时窗口 92%、3 分钟后重置）。多个紧张账户之间，有空闲槽的优先，其次会话的钉选账户，再次已用比例低的优先（所有账户都紧张时，对话不会随各账户已用比例交叉而来回切换）。
  - Agent 额度查询暂时失败时保留上一轮读数与查询时间（此前清空，Gateway 会把该账户当作额度充足）；未登录、未提供额度仍清空。
- 减轻前端服务端的单次请求开销，为流量增长做准备：
  - Console 不再把当前语言的整份词典（约 250 KB）内联进每个 HTML 页面。`I18nProvider` 只接收语言，浏览器以带哈希、可长期缓存的静态 chunk 加载词典，服务端仍输出完整翻译的 HTML。`/login` 从 267 KB 降到约 20 KB。新增 `catalogue()`，每种语言共用同一个 promise。
  - Studio 的流式回答改为服务端原样转发 Gateway 的 SSE 字节，由浏览器里的 `StudioChatTransport` 翻译成 AI SDK UI 消息流，服务端不再逐个 token 解析和重新编码。截断、停止、拒绝的表现不变。
  - 公开定价页的目录在每个 Console 进程内缓存 30 秒（`createTtlCache`），失败不缓存；并发访问共用一次在途请求。价格变更最多延迟 30 秒出现在 `/pricing`。
- Go 服务镜像不再在 Docker 里编译。新增 `scripts/build-go.sh`，在宿主机交叉编译 Registry、ControlPlane、Gateway 及 Gateway 内置的 Agent，产物放进 `.image-build/`。根目录 Dockerfile 只拷贝目标平台的二进制，构建上下文也只剩这个目录，不再下载模块，也不需要 Go 工具链。`make release` 与 `release.yml` 用 `PREBUILT=dist`，对照 `SHA256SUMS.txt` 校验后直接取 `build-release.sh` 的产物，镜像和 GitHub Release 是同一批字节。**行为变化**：
  - `make publish` 的 `AGENT_DIST` 改为 `PREBUILT`。
  - 删除 `make release-agent-dist`、`check-docker-mem`、`BUILD_PARALLELISM`。
  - Dockerfile 不再接受 `VERSION`、`GOPROXY`、`AGENT_PLATFORMS` 构建参数，改由 `build-go.sh` 的环境变量提供。
  - `deploy/upgrade.sh --build` 与源码构建的 `install.sh` 需要宿主机装有 Go。
  - 直接 `docker compose build` 前须先执行 `scripts/build-go.sh`。
- Console 与 Studio 镜像的基础镜像由 `node:26-bookworm-slim` 改为 `node:26-alpine`，本地解压后约小 116 MB。compose 健康检查改用镜像自带的 busybox `wget --spider`，每次探测不再另起一个 node 进程，间隔从 60 秒恢复为 30 秒。产物是纯 JavaScript，`build-web.sh` 会拒绝原生模块，不受 musl 影响。
- Console 与 Studio 设置 `compress: false`，压缩改由 Nginx 完成。`deploy/nginx.example.conf` 在这两个 server 块开启 gzip，Gateway API 不受影响。压缩类型包括 HTML、JS、CSS、JSON、RSC（`text/x-component`）、SVG，刻意不含 `text/event-stream`，Studio 的流式回答仍逐段到达。**部署须知**：服务器上的 Nginx 配置要同步加上这段 gzip。否则源站到 Cloudflare 这一段不再压缩，Cloudflare 发给访客时仍会压缩。
- 所有标识符改为 UUIDv7（新增 `common/idgen`，基于 Go 1.27 标准库 `uuid` 包的 `uuid.NewV7`，不引入第三方依赖）。新 ID 的前 48 位是毫秒时间戳，按铸造顺序排序，B-tree 主键的新行追加在索引右端，不再随机分裂页。涉及控制面实体 ID（`model.NewID`）、Gateway 的请求 ID（`X-Request-Id` 与 `request_logs.id`），以及在它前面加前缀的 Job、产物、response 与 item ID、隧道帧 request_id、节点 ID（`node_`、`tnode.`），还有排队票据、租约、工作流意图、ComfyUI client id、CLI 工具调用 ID 等临时句柄。请求 ID 与节点 ID 仍是 32 位十六进制，长度不变。API key、会话、令牌、安装码与 nonce 仍直接使用 `crypto/rand`。**数据库 schema 变更**：控制面实体 ID 由前缀加 28 位十六进制（32 字符）变为前缀加 32 位十六进制（36 字符），所有保存这类 ID 的列由 `VARCHAR(32)` 加宽到 `VARCHAR(40)`（`model.IDSize`）。迁移为 `base/0018`、`billing/0011`、`request_logs/0005`、`usage_records/0004`、`response_turns/0003`、`alerting/0003`、`routes/0003`、`workflowtemplates/0004`、`jobs/0006`，只加宽列、不改数据，旧 ID 继续有效；必须先执行 `-migrate up` 再启动新控制面。Gateway 与 Agent 不依赖 ID 的格式，可以按任意顺序升级。详见[数据库升级与恢复](deploy/database-recovery.md#id-列加宽base-0018--billing-0011--request_logs-0005--usage_records-0004--response_turns-0003--alerting-0003--routes-0003--workflowtemplates-0004--jobs-0006)。
- **行为变化**：工作流提交（同步与队列）只在能确定后端没收到请求时才换节点重试：背压、限流、`runtime_closed`，或带新隧道哨兵原因 `ErrNotDelivered` 的连接失败（隧道请求在 `RequestEnd` 之前失败、Agent 到 ComfyUI 拨号未连通）。slot 在 Agent 收到请求后断开、ComfyUI 在 prompt 发出后断线，不再换节点重提，以免生成两次。Agent 未升级时，ComfyUI 拨号失败不带该标记，于是不再换节点。Gateway 的共享 Redis 客户端改为读写超时 1 秒、只重试 1 次。

- **行为变化**：Gateway 的进程内工作流与对话排队已删除（`scheduler.Config` 的 `QueueMaxWait`/`QueueRetryInterval`/`QueueMaxWaiters`/`ChatQueue*` 字段移除，改为注入 `scheduler.WaitQueue`；`SubmitWorkflow` 只派发一轮）。之前设置了 `-workflow-queue-max-wait` 或 `-chat-queue-max-wait` 但没有 `-redis-addr` 的部署现在会拒绝启动；`-*-queue-max-waiters` 从每副本上限改为全集群上限。所有 Gateway 副本升级后再开启队列。

- 账号包月改为法币标价、经 Dodo 在线结账购买，不再使用点数余额。账号商品新增币种、月价（货币最小单位）与 Dodo「按意愿付费」产品 ID，新端点 `POST /admin/v1/billing/accounts/:id/checkout` 仅在 `Billing.Enabled` 时挂载；付款 webhook 验签后才开通或续费一个月。待付款结账为本商户保留账号 30 分钟；超时后被他人买走的付款记为已付款未开通，由运维退款。订单返回 `account_id`、`account_expires_at`。**破坏性变更**：移除 `POST /admin/v1/billing/accounts/:id/purchase` 与点数月价 `price_micro`；迁移 `billing/0005_account_fiat.sql` 把现有账号商品全部下架并清零价格（已售期限保留），须重新定价上架；控制面与 Console 须一起升级。详见 [docs/billing-accounts.md](docs/billing-accounts.md)。

- Redis 成为 ControlPlane 必需依赖，Compose 使用 AOF `appendfsync always`；无 `sid` 的旧 JWT 升级后失效。
- 禁用租户用户会在同一数据库事务中吊销其创建的 active API Key；ControlPlane Key 缓存改用 generation 防止并发旧读取回填。
- Gateway 的 `-key-cache-ttl` 保留为纵深防御；提交后通知前崩溃由 P07 outbox 重试，漏通知、断线重连和控制面副本切换由持久 generation 补偿。

- Compose 部署适配小内存主机（2 核 3 GiB）。新增 `AISW_IMAGE_TAG`：留空时照旧源码构建，镜像改名为 `ghcr.io/dongmu101/aiserveweave-<服务>:local`；设为已发布的 `vX.Y.Z` 时，`install.sh` / `upgrade.sh` / `enable-agent-install.sh` 改为从 GHCR 拉取该版本镜像并以 `--no-build` 启动，`install.sh` 与 `upgrade.sh` 新增 `--image-tag`。各常驻服务新增内存上限，Go 服务配套 `GOMEMLIMIT`，Console 限制 V8 堆，均可在 `.env` 覆盖；容器日志改为轮转（每个容器 3 × 10 MB）；Console 健康检查改为每 60 秒一次。Dockerfile 的 Go 编译加 BuildKit 缓存挂载，`.dockerignore` 排除 Console 与 `.claude`。**行为变化**：MySQL 关闭 performance_schema 与 binlog，buffer pool 256M、最大连接数 60，升级时 mysql 容器会重建一次；`upgrade.sh` 改为先构建或拉取镜像、再备份；`--prune` 额外删除旧的 `deploy-<服务>` 镜像，构建缓存保留量从 5 GB 降到 2 GB。详见 [deploy/README.md](deploy/README.md)「小内存主机」。
- 新增根目录 `Makefile`：`make images` 在开发机为服务器平台构建镜像，`make push SERVER=...` 经 ssh 载入服务器，`make deploy` 再在服务器执行新增的 `deploy/upgrade.sh --no-build`（沿用本机已有镜像，缺少时中止）。Dockerfile 的 Go 构建阶段改为在构建机平台交叉编译（`--platform=$BUILDPLATFORM`），跨架构构建不再经过 QEMU；`.dockerignore` 排除 `dist`。`make publish` 构建 amd64 + arm64 镜像并以 `git describe` 版本推送到 Docker Hub（`docker.io/dongmu101/aiserveweave-<服务>`），服务器设置 `AISW_IMAGE_REPO=docker.io/dongmu101` 后用 `upgrade.sh --image-tag <版本>` 拉取；`check_image_tag` 能从 `<tag>-<n>-g<提交>` 识别出提交并核对 compose 文件。新增跟随模式（`AISW_IMAGE_FOLLOW`，`upgrade.sh --follow` / `install.sh --follow`）：每次升级以新提交的 `git describe` 为 tag 拉取 `make publish` 推送的镜像，直接运行 `deploy/upgrade.sh` 即可；`--image-tag` 固定版本并关闭跟随。**行为变化**：`git describe` 的哈希固定为 12 位，二进制 `-version` 与 Agent 制品文件名随之变长；`upgrade.sh` 拉取代码时一并拉取 tag。
- **行为变化**：Compose 部署的 MySQL 重新开启 binlog（`sync_binlog=1`），新增 `MYSQL_BINLOG_EXPIRE_SECONDS`（默认 604800，即 7 天）控制保留时长，可用 mysqldump 加其后的 binlog 做按时间点恢复，见 [database-recovery.md](deploy/database-recovery.md#按时间点恢复mysql-binlog)。每次提交多一次 fsync，数据卷按写入量多占 binlog 空间。升级时 mysql 容器会重建一次。同时修正 deploy/README.md 与 `.env.example` 中 MySQL 内存上限、buffer pool 与最大连接数的默认值，使其与 compose 一致（1024m、512M、256）。

### Fixed

- Gateway 写 S3（部署存储与租户存储）时：大小未知的产物（节点未给 Content-Length）不再被 R2/AWS 以 411 `MissingContentLength` 拒绝，改为不足 8 MiB 一次写入、否则分片上传；body 比声明长度长时写入失败，不再以截断的内容写成功。见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md#租户自有对象存储2026-11-27)。

- Agent Codex 运行时误伤正常请求：
  - 长回答在约一万 token 处被截断：原因是每请求最多 10000 个 RPC 帧，现已去掉。
  - 长会话被拒为 400：注入历史的上限从 1 MiB 提高到 32 MiB，并取消 2048 条消息、128 个工具的上限。
  - app-server 的功能开关改用 `-c features.*`：`--disable` 遇到新版 CLI 删掉的功能名会让每次启动失败。
  - 断网时请求挂到总期限：关闭默认开启的无限重连 `unbounded_connection_retries`，`willRetry` 通知不再重置空闲期限。
  - 上下文过长等请求错误改为 400 `invalid_config`，不再以 502 计入节点熔断；`serverOverloaded` 改为可重试的 `backpressure`。
  - 只需升级 Agent。见 [Codex 包说明](service/aiServeWeaveAgent/codexcli/README.md#失败分类)。
- Agent Claude 运行时：
  - CLI 报告「另一个进程正在刷新登录」时，改按可重试的 `backpressure` 报告，Gateway 换下一个候选；以前以 502 应答并计入熔断。
  - 每个 CLI 进程都带 `DISABLE_AUTOUPDATER=1` 与 `CLAUDE_CODE_OAUTH_401_WAIT_MS=30000`。后者未写入官方文档，真实并发刷新尚未验收。
  - 只需升级 Agent。见 [故障排查](service/aiServeWeaveAgent/claudecode/README.md#终止原因与故障排查)。
- Agent CLI 运行时的未知事件、终止与诊断：
  - Claude 原生回合跳过新版 CLI 新增的顶层事件，并原样转发不认识的流事件、增量与停止原因；兼容桥接保持严格。
  - Codex 的协议流与隔离失败从 400 改为 502，与 Claude 一致；Codex 健康检查不再每 10 秒读取模型目录；另外关闭新版默认开启的宿主工具。
  - 两种 CLI 的推理请求被取消时，先 SIGTERM，2 秒后 SIGKILL。
  - 失败时只记录 stderr 的固定分类与 Claude 的 `startup_failure_reason`。
  - Claude 进程新增 `CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1` 与 `CLAUDE_CODE_STARTUP_FAILURE_RESULTS=1`。
  - 只需升级 Agent。
- Claude 原生直通遇到未声明工具时整轮失败：模型调用本轮 `tools` 未声明的工具（例如尚未载入 schema 的延迟工具）时，Agent 以 `unsafe_tools` 中止回合，客户端重试同一轮反复得到 400。原生回合（含 `safeguards`）现原样转发这类调用，由调用方答复「没有这个工具」，与直连 API 一致；名称为本机 CLI 自有工具（`EndConversation`、`mcp__` 前缀）时仍拒绝，兼容桥接保持严格。只需升级 Agent。见[诊断记录](docs/acceptance/claude-unsafe-tools-2026-11-11/README.md#根因确认与修复同日稍后)。

- Claude CLI 契约加固：原生进程显式拒绝所有本机工具，保留调用方定义与工具身份；兼容合法认证/工具进度通知及成功结果后的额度通知。本机协议、工具隔离、设置与启动失败改为后端错误（正文前 HTTP 502），真实上游 400/429/529 和恢复分类保持原有透传。新增动态工具名、ToolSearch、通知顺序及真实 CLI 隔离回归，见[验证记录](docs/acceptance/claude-cli-contract-2026-11-11/README.md)。现有节点需更新并重启 Agent；平台分发安装包需刷新 Gateway 镜像内置的 Agent 制品。原生产 `unsafe_tools` 的具体分支仍未复现。

- Claude 原生链路加固：父代理 CONNECT 握手可随取消立即退出；按完整 SSE 事件捕获多行错误，保留 429/529 与重试信息；gzip 编码名按大小写无关识别；Agent 会话寿命或生成空闲到期按 `timeout` 报告，避免误报为通用协议 400。复现与默认离线回归见[验证记录](docs/acceptance/claude-native-robustness-2026-11-09/README.md)。现有节点需更新并重启 Agent，平台安装包需刷新 Gateway 镜像内的 Agent 制品；生产验收仍待执行。

- 原生 Claude 长任务在历史增大后快速返回通用 400：Agent 的完整请求设置文件会撞上 Claude Code `2.1.319` 的 2 MiB 限制。现在按最终设置字节数切换输入方式，大文档经每进程回环代理补入同一次主 API 调用，保留原始历史、工具结果、safeguards、认证头和 beta 选择器，平台请求上限仍为 32 MiB；本地缺少结果或进程异常退出按后端错误返回，回合日志新增 `request_bytes`。默认回归及真实 CLI 的 31 MiB 历史、4 MiB safeguards、转义密集请求契约通过。已有节点更新并重启 Agent；平台一键安装用户需要刷新 Gateway 镜像内的新版 Agent 制品。真实生产长任务尚待更新后验收，见 [验收记录](docs/acceptance/claude-settings-limit-2026-11-09/README.md)。

- Claude Code 长工具链的续接 400：Agent 按本轮已公开的工具 ID 集合校验结果，已投递但尚待迟到 MCP 回调取走的旧邮箱不再触发 `continuation_tool_count_mismatch`。`anthropic-beta` 的顺序、空格及重复项按功能集合比较；实际增删 beta 时，在完整归属、历史、工具结果及其他配置均匹配后，使用新 CLI 从已执行结果恢复。修复已进入源码（`ca16c33`），默认 CI 覆盖连续工具回合、迟到回调、beta 变化和跨两个 Gateway 的 mTLS 回归。**升级范围**：现有节点需升级并重启 Agent；平台通过 `/agent/*` 分发安装包时，需重新构建并部署包含新版 Agent 制品的 Gateway 镜像，已有节点仍需各自升级。真实 Opus 长任务仍待线上验收，详见 [Agent 升级说明](deploy/agent/README.md#claude-code-长工具链-400-修复)。

- `common/metrics.Registry` 的 `Counter`/`Gauge`/`Histogram` 此前对 nil 接收者 panic——一个未初始化的 `*Registry` 字段被装进 `runtime.Metrics` 接口后不再是 nil 接口，调用方常见的 `== nil` 判断拦不住它；现在与其余记录点一致，返回丢弃仪器。

## [0.1.0] - 2026-09-07

首个可安装、可部署的版本：三个数据面/身份服务加 Console 均有版本化二进制与容器镜像；
适用范围以 [STATUS.md](STATUS.md) 当前已完成条目为准，以下是这份发布可用能力的摘要，
不是完整清单。

First installable, deployable release: the three data-plane/identity services
and the Console all get versioned binaries and container images. The scope
below summarizes what this release can do; [STATUS.md](STATUS.md)'s current
"已完成" section is authoritative, not this list.

### Added

- Registry：节点证书签发与续期、`GatewayDirectory` 副本名册、`node_id` 唯一性检测
  （S01）、受控注册令牌管理（S02）、Gateway 加入认证与节点禁用/证书吊销（S03）。
- Agent：主动出站 mTLS 隧道、多副本连接、槽池；Ollama/vLLM/SGLang/ComfyUI 运行时适配；
  本机 Ollama/vLLM 自动发现。
- Gateway：OpenAI 兼容的 Chat Completions/Responses/Embeddings/Models 前门（含流式）；
  模型别名、标签、优先级与权重路由，健康过滤、熔断与受限重试；工作流模板加载、Job
  提交/状态/SSE/取消/产物；租户请求数、token 数与并发限流（内存与 Redis 两种实现）；
  受持久化保护的 Job 可靠性闭环（重启恢复、跨副本访问，详见控制面「Job 持久化契约」）。
- 控制面：租户/用户/API Key/审计的 Admin API（go-zero + gorm + Redis），配额读写，
  游标分页与服务端筛选；持久化 Job 历史查询（`/admin/v1/jobs/history*`）。
- Console：登录与服务端会话、用户/Key/审计/配额页面、只读节点与模型清单、工作流目录
  与实时 Job 视图、Job 历史与取消/产物预览。
- Agent/Gateway Prometheus 指标导出；`deploy/docker-compose.yaml` 部署配置（含 Console）。
- 开源治理文件（LICENSE：Apache-2.0、CONTRIBUTING.md、SECURITY.md）与 GitHub Actions
  CI（Go 质量门禁、Console 门禁、真实 MySQL 9.7 集成测试）。
- 版本化二进制与容器镜像、校验和、发布流水线（本条目本身，R03）。

### Known limitations

- Job 的权威路由绑定以单个 Gateway 副本的有界内存为主，一个还没来得及持久化就被逐出的
  job 仍会永久丢失；详见 [STATUS.md](STATUS.md#当前主要边界)。
- 节点、模型、工作流模板的管理写路径（P01–P03）与产物持久存储（P04）尚未交付，只有
  只读清单。
- 跨版本滚动升级与配置/协议兼容性未验证（A03）；本版本假定全新部署或整体停机升级。

[Unreleased]: https://github.com/dongmu101/AIServeWeave/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/dongmu101/AIServeWeave/releases/tag/v0.1.0

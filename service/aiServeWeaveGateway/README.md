# aiserveweave-gateway

数据面。对外终结 OpenAI 兼容 API、Anthropic Messages v1（纯文本）、Ollama 原生推理 API（纯推理端点）、音频转录/翻译端点与工作流 Job API，对内通过隧道把请求派给节点。

**当前进度：隧道服务端、调度器、OpenAI 前门、ComfyUI 工作流的提交与状态查询、Registry 名册订阅、指标导出与只读的运维清单端点均已落地。** 这个二进制现在能接住 Agent、知道每个节点能服务什么、把 HTTP 请求路由过去，自己的副本身份会同步给 Registry 维护的名册，并在 `-metrics-addr` 上导出 Prometheus 文本格式的指标。

| 目录 | 状态 | 内容 |
| --- | --- | --- |
| `tunnelserver/` | 已实现 | 隧道终结：mTLS 认证、节点表、槽池、十二个 Operation 的分发、`NodeRuntime` |
| `routing/` | 已实现 | 逻辑模型到部署的映射：别名、节点选择器、优先级与权重；共享 `common/modelroute` 契约，调度器按不可变快照热切换 |
| `routesync/` | 已实现 | 控制面版本的有界拉取、校验、持久化最近有效快照与生效状态（P02） |
| `scheduler/` | 已实现 | 按模型与能力从节点表选节点，处理背压与重试语义，读 Agent 上报的健康状态并维护每候选的熔断器；工作流按 runtime 层能力选节点，见 `workflow.go` |
| `httpapi/` | 已实现 | `GET /v1/models`、`POST /v1/chat/completions`（含 SSE）、`POST /v1/embeddings`、`POST /v1/responses`（含 SSE）、`POST /v1/images/generations`（P2，见「图像生成」）、`POST /v1/messages`（P2，Anthropic Messages v1，见「Anthropic Messages」）、`POST /api/chat`/`POST /api/generate`/`POST /api/embeddings`（P2，Ollama 原生 API，纯推理端点，见「Ollama 原生 API」）、`POST /v1/audio/transcriptions`/`POST /v1/audio/translations`（P2，见「音频转录与翻译」）、`POST /v1/rerank`（P2，见「Rerank」）、`POST /v1/workflows/{workflow_id}/runs`、`GET /v1/jobs/{job_id}`、`GET /v1/jobs/{job_id}/events`（SSE）、`POST /v1/jobs/{job_id}/cancel`、`GET /v1/jobs/{job_id}/artifacts`、`GET /v1/artifacts/{artifact_id}`；鉴权见下面「API Key 鉴权」，工作流见「工作流 Job」 |
| `workflow/` | 已实现 | 管理员注册的 ComfyUI 工作流模板目录：文件或控制面来源（P03）、声明式输入/输出/依赖、绑定与校验；`Handle` 原子持有当前生效目录 |
| `workflowsync/` | 已实现 | 控制面版本的有界拉取、逐模板校验、持久化最近有效整包与生效状态（P03） |
| `ratelimit/` | 已实现 | 租户配额执行：连续补充的令牌桶，`Memory`（副本内）与 `Redis`（集群级）两个实现 |
| `registryclient/` | 已实现 | 向 Registry 的 `GatewayDirectory` 报到，把收到的名册转发给 `tunnelserver.Server.SetRoster` |
| `controlplaneclient/` | 已实现 | `Verifier` 对着控制面校验 API Key，进程内缓存，发出的是哈希而不是调用方的 key；`JobsClient`（STATUS.md 的 J04）是控制面 Job 持久化内部 API 的客户端，与 `Verifier` 刻意分开——它不缓存、不重试；`GatewayPersister` 把它同时适配成 `httpapi.JobPersistClient`（J05，写入）与 `httpapi.JobRecoveryClient`（J06，重启后按路由绑定读回非终态 job），分别接入 `httpapi/jobpersist.go` 与 `httpapi/jobrecover.go` 的两个后台循环 |
| `e2e/` | 已实现 | 真实 TCP + mTLS 下三副本与真实 Agent 的联调测试 |
| `main.go` | 已实现 | 装配隧道监听、HTTP 监听、Registry 名册订阅、`/metrics` 监听 |

隧道协议本身定义在 [../aiServeWeaveAgent/tunnel/README.md](../aiServeWeaveAgent/tunnel/README.md)，改这里的代码前先读那份。两端共用 `common/runtime`（类型与接口）与 `common/tunnelwire`（proto 编解码），不允许任一侧另写一份等价转换。

## tunnelserver 的四条约束

这四条不是实现细节，是设计约束，改动时不能绕过：

1. **证书是身份的唯一来源。** `node_id` 只从 TLS 栈**验证过的**证书链里读（`VerifiedChains`，不是 `PeerCertificates`），流上声明的 `node_id` 必须与之相符，不符就断流。没开客户端校验的副本认不出任何人，而不是认可所有人。这个判断逻辑收在 `common/nodeid.FromPeer` 里，Registry 的 `RenewCertificate` 也复用同一份，避免两处各写一份而漂移。
2. **不排队。** 没有空闲槽时 `Dispatch` 立刻返回 `ErrorBackpressure`（`Retryable: true`），由调度器换节点。槽是预先 park 好的，所以"这个节点满了"是微秒级的答案。
3. **不缓冲。** 响应帧一帧一交给调用方，调用方不读就阻塞，背压顺着 gRPC 流控传回 Agent。没有队列可以涨，也就没有队列需要限长。
4. **不转发。** 每个副本只服务连到自己身上的节点。请求路径上没有副本间跳转，这是多副本设计的前提，不是优化。

## 名册来源

`Server.SetRoster` 是名册的唯一注入点，`registryclient.Run` 是它现在的调用方：启动时向 `-registry-addr` 指定的 Registry 发起 `GatewayDirectory.Join`，上报 `-replica-id`（默认取 hostname）和 `-tunnel-advertise-addr`（未设置则退回 `-tunnel-addr`，NAT/负载均衡场景下必须显式设置成 Agent 真正能拨通的地址），把收到的每一份名册转发给 `SetRoster`；连接断开按全抖动指数退避重连；收到关闭信号时先发一条 `DRAINING` 状态再断开，让还没连上这个副本的 Agent 提前知道不用再连。`-registry-addr` 留空则完全跳过订阅，等价于旧行为（名册需要调用方手工调用 `SetRoster` 注入），方便本地单副本调试不必起 Registry。

## 运维清单监听器

`-admin-addr` 打开一组**只读**端点，供控制面聚合；不给地址就不启用。

```bash
AISW_GATEWAY_ADMIN_TOKEN=$(openssl rand -base64 32) \
go run ./service/aiServeWeaveGateway -admin-addr 127.0.0.1:8091 ...
```

几个端点，Bearer token 均来自 `AISW_GATEWAY_ADMIN_TOKEN`（常数时间比较），**都不接受写操作**：

| 端点 | 内容 |
| --- | --- |
| `GET /internal/v1/nodes` | 连到本副本的节点、运行时与能力 |
| `GET /internal/v1/routes` | 本副本生效的路由状态：来源、版本、摘要（P02） |
| `GET /internal/v1/workflows` | 本副本注册的工作流模板：id、描述、输入/输出声明、依赖、版本与可见范围（P03）、校验状态 |
| `GET /internal/v1/workflows/status` | 本副本生效的工作流模板整包状态：来源、数量、整包摘要（P03） |
| `GET /internal/v1/jobs?tenant_id=…` | 本副本 job 表中**某一个租户**的运行 |

- `tenant_id` 在 job 端点上是**必填**：这张表持有每个租户的运行，一个能返回全部的端点会让控制面的过滤成为横在两个租户之间的唯一一道东西。缺失时返回 400，而不是「乐于助人」地返回全部。
- 模板目录**不含图**。仓库把完整的工作流 JSON 与 API key 归为同一类，图从不离开本进程；输入所写入的节点与字段同样不外传（那也是图结构，调用方按名字替换）。渲染是压根没取用图，而不是事后剥掉——后者距离被打破只差一行被遗忘的代码。
- job 视图**不含运行位置**：节点 id、运行时 id 与解析后的模型都刻意缺席。节点属于运维视图，而 `scheduler.Candidate.Model` 的文档写明客户端从不得知它——那是别名解析的结果，告知调用方等于取消了别名的意义。
- job 表有条数上限且跨租户共享，逐出过内容时 `truncated` 为 true。一份短列表若没有这个标志，会被读成「这段时间很清闲」，而那恰恰是这张表最无法支撑的结论。
- **`state` 是最后观测状态，不是此刻的状态。** 前台轮询、SSE 与有界后台同步器（`httpapi/jobsync.go`）都会更新观测；调用方停止查询后，后台仍会推进非终态 Job。节点不可达时保留最后状态并退避，不编造终态。消费方应同时展示 `updated_at`，同步频率与失败边界见下方「工作流 Job」第九条。
- 它与推理监听器分处不同端口：公开监听器面对持有租户 API Key 的调用方，这一个面对持有部署密钥的控制面。放同一个端口，就意味着距离「某个租户读到整个机群」只差一条配错的路由。
- 没有 token 时**拒绝启动**而不是以未认证方式提供：一份谁连上端口就能读的机群清单不是值得保留的降级模式。这个失败会返回，不像 `-metrics-addr` 那样只记日志——要求启用它却没启用，应当在启动时就发现。
- 响应形状是 `common/nodeview`（节点）与 `common/workflowview`（模板与 job）的契约，与控制面共用一份声明。渲染采用**允许列表**：只输出该包点名的字段，而不是序列化 `NodeInfo` 或 `Descriptor` 碰巧持有的一切。运行时凭据本来就不在 `Snapshot` 里（它们在 Agent 的 `runtime.Config`，`common/tunnelwire` 过隧道前已丢弃 API key），允许列表是从这一侧保证它继续如此。
- 每个副本**只知道连到它自己身上的节点**（隧道设计第四条约束），因此文档里带 `replica_id` 与 `generated_at`；「整个机群」是控制面聚合出来的，不是任何单个副本能回答的。

## 模型拉取触发（STATUS.md 的 P2「模型分发」子任务二）

`-model-pull-addr` 打开一个独立的写入口，供运维触发一个已连接节点按名字拉取模型制品、并查询它的状态；不给地址就不启用，设计文档见 [`docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md`](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md)。

```bash
AISW_GATEWAY_MODEL_PULL_TOKEN=$(openssl rand -base64 32) \
go run ./service/aiServeWeaveGateway -model-pull-addr 127.0.0.1:8092 ...
```

| 端点 | 内容 |
| --- | --- |
| `POST /internal/v1/nodes/{node_id}/model-pulls` | body `{"names":["..."]}`，触发节点按名字拉取；202 只确认已下发到隧道，不确认任何名字被接受 |
| `GET /internal/v1/nodes/{node_id}/model-pulls` | 本副本对该节点最后已知的拉取状态：名字、阶段、已下载/总字节数、失败原因（封闭枚举）、更新时间 |

- **与 `-admin-addr` 刻意分处不同监听器、不同 token。** `-admin-addr` 一节的文档明文写着它"都不接受写操作"，触发一次拉取是写操作，塞进那个监听器会让那句话变成假话；两者 token 泄漏的滥用后果也不同——一个泄漏读到机群清单，这一个泄漏能让任意已连接节点开始下载它本地清单已经批准的一切，爆炸半径更大，因此没有理由比 `-admin-addr` 更宽松。
- **触发从不携带 URL，只有名字。** Agent 对着自己本地清单（`-model-pull-manifest`）解析，Gateway 只能从 Agent 已经批准的名字集合里选——即使这个监听器的 token 被盗，能做的也只是"从节点已批准的名字里选"，不能让节点访问任意地址；协议层的完整论证见隧道 README「模型拉取的按名字触发」一节与上述设计文档。
- **触发是异步的，HTTP 响应不携带名字级结果。** 202 只表示这次触发已经发到隧道的 Control 流上；未知名字、已在下载中等情况只能从随后的 `GET` 观察——Control 流"不设专门 ack 帧"是既有设计（`GatewayControl_Config` 同一先例），本端点不为了让响应更即时而破坏它。
- **本节点不转发。** 与推理数据面同一条边界（本文件顶部「tunnelserver 的四条约束」第四条）：一个只连着别的副本的节点，在这里表现为"未连接"。**跨副本路由已在控制面一层交付**——`internal/modelpullrouter`（并发问全部已配置副本、按结果合并）与 `ControlPlane` 挂载的 `POST`/`GET /operator/v1/nodes/:id/model-pulls`，详见 [ControlPlane README「模型拉取转发（P2 模型分发子任务二的控制面转发层）」](../aiServeWeaveControlPlane/README.md#模型拉取转发p2-模型分发子任务二的控制面转发层)；直接调用本节点两个端点的调用方仍需自己知道该问哪个副本，这一层的"不转发"本身没有改变。
- **没有 token 时拒绝启动**，与 `-admin-addr` 同一克制：一个能让节点开始下载的入口不该以未认证方式提供。
- **范围边界**：控制面转发层已交付，Console 可见性（子任务五）仍未排期；`common/modelroute.Target` 不新增"这个模型别名对应哪个制品名字"的映射，调用方（运维，无论是直接调用本节点端点还是经控制面转发）自己决定触发哪个名字。

## API Key 鉴权

鉴权有三种模式，按真实部署应当采用的优先级排列（实现在 `httpapi/auth.go`）：

1. **`-control-plane-addr`（推荐）** —— 对着控制面校验。key 以哈希存储、可吊销、携带租户。这是 README 安全设计那条「用户 API Key 只保存不可逆哈希」真正成立的路径。
2. **`-api-keys`** —— 静态明文列表，靠重启轮换。留给本地开发，以及控制面尚未部署时先把数据面跑起来。两者都配置时控制面胜出，并在启动时告警。
3. **两者皆无** —— 放行一切，仅在监听回环时才谈得上合适，启动时有告警。

**Gateway 发给控制面的是哈希，不是调用方的 key。** SHA-256 在这里算完，因此用户的凭据从不进入控制面的内存、它的请求日志，或两者之间的抓包。哈希足以查到一个 key，却无法用来在别处冒充它。

**校验结果在进程内缓存 `-key-cache-ttl`（默认 30s），吊销由 generation 长轮询主动失效。** Gateway 用同一把 `InternalToken` 持续调用 `GET /internal/v1/apikeys/revocations/watch?after=<generation>`；健康链路上，Redis generation 的变化在 1 秒内清空每个副本的正向缓存。长轮询每 2 秒心跳，客户端 5 秒硬超时；传输、超时、鉴权、状态码或响应格式任何一处失败，都会同步清空并禁用本地缓存，之后每次请求直接问控制面，控制面不可达仍返回 503。漏掉的 Pub/Sub 消息与切换控制面副本由 Redis 当前 generation 补偿，generation 回退则按 Redis epoch 重置先清缓存再接管新值。

`-key-cache-ttl` 仍是纵深防御的过期边界（默认 30 秒）。控制面 P07 已把吊销与待发 outbox 同事务保存，提交后通知前崩溃会在重启或其他副本上补发；数据库/Redis 故障期间仍不承诺零秒失效。已经完成鉴权并进入推理的请求不会因吊销被强行中断，边界是副本应用新 generation 后的下一次鉴权。

**控制面不可达时返回 503，不是 401。** 收到 401 的调用方会跑去重新生成 key，而如果只是控制面短暂宕机，那既浪费他们的时间也解决不了问题。这个区分由 `httpapi.ErrKeyRejected` 承载：只有它代表「这个 key 不行」，其余错误一律是「我们此刻答不了」。

`InternalToken` 用 `AISW_CONTROL_PLANE_TOKEN` 环境变量传，不要用 `-control-plane-token` flag —— flag 在 `ps` 输出里可见。

## 工作流 Job

三个端点已落地，实现在 `httpapi/jobs.go`、`httpapi/jobevents.go`、`httpapi/jobstore.go` 与 `workflow/`：

| 端点 | 行为 |
| --- | --- |
| `POST /v1/workflows/{workflow_id}/runs` | 按 `-workflow-templates` 里注册的模板绑定输入，选一个具备工作流能力的节点提交，返回 202 与本 Gateway 铸造的 `job_id` |
| `GET /v1/jobs/{job_id}` | 未结束的 job 去问运行它的那个节点，已结束的直接由存储作答 |
| `GET /v1/jobs/{job_id}/events` | SSE 进度事件流，帧带 `event:` 名，供浏览器按类型注册监听器；已结束的 job 直接回一帧终态而不去订阅 |
| `POST /v1/jobs/{job_id}/cancel` | 向运行该 job 的节点发中断请求，返回 202 与最后已知的 job 视图 |
| `GET /v1/jobs/{job_id}/artifacts` | 列举该次运行产出了什么，并为每个产物铸造公开 `artifact_id` |
| `GET /v1/artifacts/{artifact_id}` | 直通转发产物字节，边读边送 |

事件流的每一帧形如：

```text
event: progress
data: {"job_id":"job_…","type":"progress","node":"3","data":{"value":5,"max":20},"received_at":"…"}
```

`data` 里嵌的是后端自己的载荷（大小已由 ComfyUI 适配器限制）：进度数字与节点输出只存在于那里，丢掉它的流只会报告「有事在发生」，却说不出进行到哪一步。终态帧额外带 `status`，随后流结束。

设计上有十三条约束，改这里的代码时不能绕过：

1. **调用方给不出图。** 请求体只有 `inputs`，图来自已注册的模板。模板把每个可替换输入声明为「节点 + 字段 + 类型 + 范围」，且该字段必须已存在于图中——输入只覆盖模板作者放好的值，从不创建字段。声明错误的模板在 `workflow.Load` 时就失败，挂在运维的终端上而不是某个调用方的请求上。这是 README 顶层「平台不应允许普通 API 调用者随意修改整个节点图」的落实。
2. **`prompt_id` 不外泄。** 公开 id 是 Gateway 自己铸的 `job_...`，后端的 `prompt_id` 只存在 job 记录里。它不是我们该派发的东西，而且只在单个 ComfyUI 内部唯一。
3. **提交只在「后端确定没见过它」时才换节点。** `scheduler.submitRetryable` 比通用的 `retryable()` 窄：只有 `backpressure`、`rate_limited`、`connection_failed`、`runtime_closed` 才重试。上游错误与超时会让这次提交的下场变成未知，重试它就是又生成一张没人要的图。这是 README「ComfyUI 作业一旦开始执行，不应自动迁移到其他节点」在调度侧的一半。
4. **事件流是「运行如何结束」的权威。** `succeeded`/`failed`/`cancelled` 三种事件一到，job 状态就地写入存储并结束该流；此后状态查询由存储作答，不再为一次早已结束的运行去打扰节点。断连由 `tunnelserver.Response.Recv` 自己 select 请求 context 处理（`call.go`），取消让它立即返回，被 defer 的 `Close` 再把取消经隧道送给 Agent——前门这边不需要额外的看门狗协程。
5. **取消是请求，不是结论。** ComfyUI 的中断是异步的，因此 `cancel` 返回 202 后 job 仍是后端最后报告的那个状态，直到状态查询或事件流带回真正的结果——在这里就把它标成 `cancelled`，是 Gateway 在编造一个没人告诉过它的结果。已结束的 job 返回 409（请求与状态冲突），节点不具备中断能力时返回 501（`cancel_unsupported`），而不是笼统的 500——后者会让调用方跑到我们这边找问题。
6. **产物的公开 id 与后端路径无关。** 后端用 `filename`+`subfolder`+`type` 三元组定位产物，那是通往它自己磁盘布局的一条路径。这个三元组绝不作为标识符抵达调用方：`artifact_id` 在列举时铸造、经由存储解回，因此调用方无法伪造一个指向本次运行没有产出的文件的 id。id 在多次列举之间稳定——每次调用铸一套新的，会让每轮轮询都把存储撑大一点。
7. **产物下载走批量槽，且不落地。** `OPERATION_ARTIFACT_LIST` 是有界回复，走推理槽；`OPERATION_ARTIFACT_OPEN` 流出整个响应体，走批量槽，两类槽在隧道里物理隔离，一次大的下载挤不掉推理。前门用 `io.Copy` 直通转发，本进程从不完整持有一个产物，背压经由同一次读取抵达 Agent。回显进 `Content-Disposition` 的文件名先被清洗：目录部分、CR、LF、引号与控制字符一律移除而不是转义——那个名字来自后端，并经由工作流自己的保存节点前缀最终来自调用方。
8. **job 表在内存里，且有界。** 上限 `httpapi.DefaultMaxJobs`（10000），超出逐出最旧的一条；内存条目在副本重启时丢失，内存表不跨副本共享；配置控制面持久化后，已落库历史保留，非终态路由绑定可由后台恢复器读回（第十一条）。持久化属于控制面的 `jobs` 表，写入时机、失败语义与状态机的设计见 [ControlPlane README 的「Job 持久化契约」](../aiServeWeaveControlPlane/README.md#job-持久化契约j01j08-实现与边界)——核心原则是这条持久化链路是旁路记录，不能让控制面变成推理请求路径上的同步依赖，第十条约束是这条原则的具体落实。job 按租户隔离：不属于本租户的 job id 与不存在的 job id 得到同一个 404，产物 id 同理——产物就是生成出来的图像本身，那是这整个界面里最要紧的一处泄露。逐出一个 job 时，解析到它的产物 id 一并删除，否则被逐出的 job 的产物会留在一张不再受任何东西约束的表里继续可下载。
9. **后台同步器代替不再轮询的调用方推进 job。** `httpapi/jobsync.go` 的 `jobSyncer` 周期性向每个非终态 job 的节点问一次状态，实现在 `jobStore.dueForSync`/`syncSucceeded`/`syncFailed` 上；没有它，一次没人继续轮询、也没人挂着 SSE 的运行会永远停在最后被观测到的状态，即便后端早已跑完。它在三个维度上同时有界：`SyncBatchSize`（默认 200）限定一轮问多少个 job，`SyncConcurrency`（默认 8）限定同时问多少个，`SyncCallTimeout`（默认 10s）限定单次询问能挂多久；一轮必须跑完才安排下一轮的计时器（默认间隔 `SyncInterval` 5s），因此从不重叠、慢一轮只会推迟下一轮而不会堆积。节点消失时 `NodeRuntime.snapshot` 返回 `*runtime.RuntimeError{Code: ErrorConnection}`，这是预期内的失败，不当错误记日志、也不改 job 状态——README「state 是最后观测状态」在这里必须继续成立，一个节点短暂不可达不是运行本身发生变化的证据；连续失败会按 `syncFailures` 翻倍退避（上限 `SyncMaxBackoff`，默认 5 分钟），一个持续消失的节点因此被越问越少，而不是每轮都问。任何一次前台观测（状态轮询或 SSE 事件，两者共用 `jobStore.update`）都会清空这份退避：既然确实有什么触达到了它，此前的惩罚期就不再成立。`Server.Close` 停止这个后台循环并等待正在进行的一轮跑完——本身已被批次、并发与超时三重限定，因此这个等待有界，main.go 在 HTTP 监听器停止、隧道被拆除之前调用它，避免对着一条正在有意关闭的隧道打出一串「node is not connected」告警。
10. **后台持久化器把 job 记录写进控制面，且从不与推理路径同步。** `httpapi/jobpersist.go` 的 `jobPersister`（STATUS.md 的 J05）在 `submitRun`、`jobStatus` 轮询与 SSE 终态写入这三处观测点之后被非阻塞地 `nudge()` 提醒，但它自己的写入永远在另一个协程里进行——202、轮询响应、SSE 帧都在持久化调用返回之前就已经发给调用方。一个 job 需要持久化的条件是 `job.needsPersist()`：`persisted` 为 false（从未确认过 `CreateJob`），或 `persistedSeq < ObservedSeq`（已确认的落后于本副本最新的观测）；`ObservedSeq` 只在 `jobStore.update()` 里因 State 或 ErrorSummary 真正变化才自增，一次只确认同一状态的轮询不会触发一次白白的持久化写入。**结果不明时的重试只会针对同一个 job id 与同一份路由绑定再问一次控制面，绝不重新提交给节点、也绝不铸造新 job id**——`jobPersister` 唯一一个形似 `scheduler` 的依赖是 `artifactOpener`（P04 起为把产物字节复制进对象存储而存在），已经收窄到 `OpenArtifact` 这一个方法，类型里没有任何地方能发起 `Submit` 或派发，架构上就做不到重新提交，这正是 STATUS.md「结果未知时不盲目重提」在代码里的落实。批次、并发与超时的三重有界与退避机制与 `jobSyncer`同构，但用独立的 `persistFailures`/`nextPersistAt` 记账：控制面不可达与节点不可达是两个互不相关的故障域，合用一套退避会让一处故障拖住另一处本该继续的重试。`dueForPersist` 刻意不排除终态 job——一次运行的最终状态恰恰是最不该丢失的记录，也是 `jobSyncer` 自己的轮询在 job 到达终态那一刻起就不再覆盖的情形。**这是尽力而为的旁路，不是可靠队列**：重试状态存在 `jobStore` 自己的记账里，与内存 job 表其余部分同样在进程重启时丢失、同样受 `DefaultMaxJobs` 逐出上限约束——一个还没来得及持久化就被逐出的 job，这次持久化机会随之消失，这是已知且如实记录的限制，不是靠着承诺"不会丢"蒙混过去的隐患。
11. **后台恢复器在重启后找回非终态 job 的路由绑定，且从不发明结果。** `httpapi/jobrecover.go` 的 `jobRecoverer`（STATUS.md 的 J06）周期性地就 `scheduler.WorkflowCapableCandidates()` 报告的每一个当前已连接节点/runtime，向控制面问一句「我欠这个路由绑定什么」（`JobRecoveryClient.ListActiveJobsForRoute`），并用 `jobStore.recoverIfMissing` 把本副本尚不知道的 job 补回内存表——这正是重启后 `job.Candidate`（节点/运行时标识）与 `job.RunID`（后端运行标识）失而复得的地方，且从不序列化任何连接对象：`NodeRuntime` 本就在每次调用时重新按 (nodeID, runtimeID) 解析节点（见隧道那边的 `node_runtime.go`），恢复回来的 `Candidate` 不过是它一直以来的那两个字符串。恢复到的 job 会把 `ObservedSeq`/`persisted`/`persistedSeq` 播种为控制面已有的值，而不是从零开始——否则 `jobPersister` 头几次真实观测会因为本地序号"看起来更旧"而被控制面无声丢弃。**恢复的执行权刻意不是排他的**：这个节点/runtime 连接到的任何副本都可以恢复并操作同一个 job，多个副本各自独立同步或持久化同一个 job 在构造上就是安全的——控制面的 `observed_seq` 单调门槛（见 ControlPlane README「Job 持久化契约」）本就无需协调即可化解并发写入，这里没有锁要拿，因为没有什么需要锁来保护。**一个再也没有重新连接到任何副本的节点不会被当作失败处理**：本恢复器从不主动为一个够不着的 job 编造状态，该 job 只会停在最后观测到的记录上，与本 README 一贯反对"编造一个没人告诉过它的结果"的立场一致。
12. **产物字节可选地被复制进对象存储，下载优先读它。** `objectstore` 包（STATUS.md 的 P04）是一个 `Backend` 接口加三个实现——`local`（单机磁盘）、`s3`（S3-compatible，含 MinIO/Ceph RGW 等自建网关）、`webdav`（群晖/QNAP/TrueNAS 等只有 WebDAV、没有 S3 网关的 NAS）——由 `-artifact-storage=local|s3|webdav` 选择，留空则完全关闭这条路径，产物仍旧只能从产出它的节点实时拉取，与 P04 之前的行为完全一致。开启后，`jobpersist.go` 的 `jobPersister.persistArtifacts` 在上报产物元数据之前先经 `persistArtifactBytes` 把字节从节点拉到配置的后端：`countingReader` 包着 `io.TeeReader` 在 `objectstore.Backend.Put` 读取的同一遍里完成 SHA-256 与字节计数，不多读第二遍；存储 key 由 `path.Join(tenantID, jobID, artifactID)` 派生——用 `path.Join` 而不是字符串拼接，是因为未配置鉴权的部署（`-api-keys` 与 `-control-plane-addr` 都留空）请求不带租户，空 `tenantID` 直接拼接会产出一个开头的 `/`，被 `objectstore` 的 key 校验当绝对路径拒绝，这是一次真实撞上过的 bug，由端到端集成测试抓到。字节复制失败时**完全不发起 `CreateJobArtifact` 调用**，而不是退化成仅报元数据——控制面绝不能记一个尚不存在的 `StorageKey`；失败与产物元数据上报共用同一套按 `artifactPersistFailed` 计数的退避重试。哈希、大小、内容类型与存储 key 随 `CreateJobArtifact` 一起上报（详见 ControlPlane README 对应小节），但 `StorageKey` 从不出现在任何租户可见的响应里——一个 `types.JobArtifactResponse` 被 `getJobHistory`（租户侧）与内部列举接口共用，因此只携带 SHA256/SizeBytes/ContentType 这些描述租户自己文件的字段，存储后端的内部寻址与节点 id 一样不该被租户看到。`downloadArtifact` 只在 `jobStore` 的产物记录已经带有 `StorageKey` 时才尝试 `Backend.Open`，任何失败（含未找到）都直接回退到一贯的节点实时拉取，而不是把一次存储故障变成一次调用方无计可施的错误。字节复制用独立的 `ArtifactCopyTimeout`（默认 5 分钟）而不是 `CallTimeout`（默认 3 秒）：后者是为一次 JSON 往返设计的，套用在搬运真实文件字节上会把大产物的复制提前掐断。S3 一侧默认关闭 SDK 较新的 `aws-chunked` 结尾校验和并改用 `UNSIGNED-PAYLOAD` 签名——前者是 AWS 专有扩展、不是每个 S3-compatible 服务端都支持，后者是让流式上传不必先整体缓冲来算载荷哈希的代价，两者都是为了兼容本包真正瞄准的非 AWS 服务端而做的取舍。新增的两个依赖——`aws-sdk-go-v2`（S3-compatible 客户端）与 `studio-b12/gowebdav`（WebDAV 客户端）——仅被这个包引用，不影响 Agent/Registry 的最小依赖线；S3 与 WebDAV 的凭据一律经 `-artifact-storage-s3-access-key-id-file` 等 `-xxx-file` flag 从文件读取，不作为明文 flag 值出现在进程列表里，任何可能泄漏它们的错误文本都先经 `runtime.Redact` 清洗。保留期清理见第十四条；输入上传的隧道协议层与 HTTP 前门见第十三条。
13. **输入文件在同一次 HTTP 请求内原子完成上传与提交，不做跨节点重试。** `common/workflowtemplate` 新增 `InputFile` 输入类型（STATUS.md 的 P04），值不是 JSON——调用方带外提供字节，因此不可能像字符串输入那样夹带图结构；`Input.Default` 对 `InputFile` 类型直接被 `Validate` 拒绝，因为一个静态默认文件引用没有实际上传与之对应。`workflow.Template.Bind` 的签名相应拆成 `(values, files) (graph, pending, err)`：标量输入照旧当场写进图，`InputFile` 输入既不写图也不报错，而是作为 `PendingFile`（携带 Name/Node/Field/Filename/Size）回报——它对应哪个节点字段，在选定节点、字节真正上传过去之前根本无从知道。`httpapi.submitRun` 因此按请求的 `Content-Type` 分叉：不含文件的 `application/json` 走法与 P04 之前完全一致；含文件的 `multipart/form-data`（`inputs` 表单字段携带同样的 JSON、其余具名分片是文件本身）由 `parseRunRequest` 经标准库 `ParseMultipartForm` 解析——大分片按阈值溢写临时磁盘而不是无界驻留内存，这是在"纯流式解析 multipart 会禁止先收集完整的 headers 再决定上传目标节点"与"绝不无界缓冲"这两条约束之间选的折衷，注释里写明了取舍。有 `pending` 文件时，`submitWithFiles` 挑选 `scheduler.WorkflowCapableCandidates()` 的第一个候选，经新增的 `scheduler.UploadInput`（复用 `OpenArtifact` 的流式纪律，边读边送）逐个把文件推给它，用返回的 `InputRef` 经 `workflow.SetGraphField` 补全图，再用新增的 `scheduler.SubmitWorkflowTo` 提交给这同一个候选——**全程不重试**：`SubmitWorkflow` 原有的跨候选重试循环在这里不安全复用，换一个候选意味着刚上传到前一个候选的文件根本不在它会去找的地方，对每个可能重试到的候选都重新上传一遍，是这个 API 主动放弃、而不是悄悄掩盖的取舍，其代价与"单次请求原子提交"的设计选择直接对应。这条链路的隧道协议层——`OPERATION_INPUT_UPLOAD`（复用既有帧结构，见隧道 README「payload 编码约定」一节）、`tunnelserver.NodeRuntime.UploadInput`（走批量槽，边读边送）、Agent 侧 `dispatch.go` 的 `chanReader` 流式转发、`comfyui.Runtime.UploadInput` 经 `POST /upload/image` 落盘——已在更早一轮落地；本轮补上的是 `workflow`/`httpapi` 这一层，使其从"Gateway 能把字节送进 ComfyUI"变成客户端真正可用的端点。
14. **产物按类型区分保留期，到期由第四个后台循环清理，字节先于元数据行消失。** `httpapi/artifactcleanup.go` 的 `artifactCleaner`（STATUS.md 的 P04）是继 `jobSyncer`（J02）、`jobPersister`（J05）、`jobRecoverer`（J06）之后的第四个 `run()`/`tick()`/`Stop()` 后台循环，仅在 `Config.ArtifactCleanupClient` 非空（即控制面持久化已启用）时启动，与前三者同一条"未配置就整体不跑"的规则。它按 `Config.ArtifactRetention`（output 类型，默认 30 天）与 `ArtifactPreviewRetention`（temp 类型，默认 24 小时）两条各自独立的保留期分别向控制面新增的 `GET /internal/v1/job-artifacts/expired` 发问——这正是"预览图单独处理"的落实方式：不是给预览产物另开一条持久化或存储路径，只是给它一条短得多的到期线，P04 Stage 2 已有的持久化写入路径完全不变。`tick()` 逐类型调用 `sweepType`，对每一条到期记录调用 `reap()`：**先删对象存储里的字节（`objectstore.Backend.Delete`），只有这一步成功才继续删控制面那一行元数据**——顺序反过来会在删除失败时留下一行指向已经消失的字节的记录，而现在的顺序失败时最坏情况是留下一份没人再指向的孤儿字节，两者中显然是后者代价更小；`StorageKey` 为空（产物从未被复制进对象存储，或复制发生在启用对象存储之前）时跳过存储删除、只删行，这与 `downloadArtifact` 遇到空 `StorageKey` 时的处理是同一条判断。一条产物删除失败（无论是存储侧还是控制面侧）不影响同一轮里其余产物的清理，失败的那条留给下一轮 `Interval`（默认 10 分钟）自然重试，不是一次显式退避——到期产物的清理本就不是时间敏感操作，下一轮自然重试与专门实现一套退避策略相比是同等有效但更简单的选择。`main.go` 新增的 `-artifact-cleanup-interval`/`-artifact-retention`/`-artifact-preview-retention` 三个 flag 留空时各自退回上述默认值。`controlplaneclient.GatewayPersister` 同时满足 `httpapi.ArtifactCleanupClient`（新增）——与它已经满足的 `JobPersistClient`/`JobRecoveryClient` 是同一个适配器上追加的第三个接口，理由不变：`httpapi` 不能反向导入 `controlplaneclient`。`controlplaneclient.call()` 相应新增对 `http.StatusNoContent` 的识别——`DeleteJobArtifact` 是这条客户端目前唯一一个成功时不带响应体的调用，之前的 switch 只认 200/201，会把它的 204 错判成 `ErrOutcomeUnknown`。**已知边界**：输入上传的字节从不落盘到控制面或对象存储（P04 Stage 3 选择的"单次请求原子提交"架构决定了这一点），因此本条清理逻辑只覆盖已持久化的输出产物。上传文件的格式/内容类型校验见第十五条。
15. **上传文件校验分两层：文件名扩展名先过滤，再嗅探真实字节。** `httpapi/uploadformat.go`（STATUS.md 的 P04，补上"格式与大小限制"里大小限制之外、此前一直留白的格式那一半）在 `parseRunRequest` 解析 multipart 分片时先用 `validateUploadFilename` 按扩展名（大小写不敏感，`Config.AllowedUploadExtensions`，为空退回 `DefaultAllowedUploadExtensions` 的图片/视频/音频常见格式，没有关闭这项检查的开关，与几行之外的 `MaxWorkflowUploadBytes` 一样）拒绝明显不对的文件——这一步不读文件的一个字节，因为此刻甚至还不知道会不会选中一个能处理工作流的节点。第二层在 `submitWithFiles` 真正开始转发字节之前：`validateUploadContent` 用 `net/http.DetectContentType` 嗅探最多 512 字节（与该函数自己文档声明的窗口一致，因此无论文件大小如何都是一次有界读取，符合"任何一跳都不得无界缓冲"），核对结果是否落在这个扩展名该有的类别前缀里（`image/`、`video/`、`audio/`）——**这张"扩展名→类别前缀"表刻意不放进 `Config`：它编码的是格式本身长什么样，不是部署策略**，一个改名成 `.png` 的可执行文件即使通过了第一层的文件名检查，也会在这里因为嗅探结果不是 `image/*` 而被拦下。一个部署往 `Config.AllowedUploadExtensions` 加进这张类别表里没有的扩展名（比如某种不透明的二进制模型格式）时，第二层对它直接跳过，只凭扩展名放行——不透明的二进制格式本就没有一个统一的字节特征可供比对，强行套一个错误的类别只会制造假阳性。嗅探不消耗字节：`validateUploadContent` 返回的 reader 用 `io.MultiReader` 把已经读出的探测窗口接回原始流的前面，下游收到的仍是完整、逐字节不变的文件。两层校验的失败都会在 `errUnsupportedUploadFormat` 上打上标记，`submitRun` 据此在 `submitWithFiles` 返回的错误进入通用的 `handleDispatchError`（会把未识别错误一律映射成 500）之前拦下它，改答 400——这是调用方的输入问题，不是节点或后端的错。`main.go` 新增的 `-workflow-upload-allowed-extensions`（逗号分隔）留空时退回默认列表。

`-workflow-source=file`（默认）下，`-workflow-templates` 接受逗号分隔的文件或目录（目录下取 `*.json`，其余忽略），留空则不注册任何模板，此时提交一律 404。清单形如：

```json
{
  "id": "text-to-image",
  "inputs": [
    {"name": "prompt", "node": "6", "field": "text", "type": "string", "required": true, "max_length": 2000},
    {"name": "width", "node": "5", "field": "width", "type": "integer", "default": 1024, "min": 64, "max": 2048}
  ],
  "graph": { "…": "ComfyUI 导出的 API Format 工作流" }
}
```

`-workflow-source=controlplane`（P03）下改由控制面管理版本，见下一节；两种来源构建的是同一个 `workflow.Registry`，走同一条 `workflow.Handle` 读取路径，`submitRun` 与目录渲染都不需要知道背后是哪一种。文件模式加载的模板 `Version` 字段恒为空字符串，`job.WorkflowVersion` 相应记录为空——它从未被版本化，不该在 job 记录里声称一个自己没有的版本。

README 顶层「ComfyUI 任务 API」列出的六个端点已全部落地。产物列表这一步顺带扩了隧道契约：新增 `OPERATION_ARTIFACT_LIST`（`RunRef` 进、`ArtifactList` 出，走推理槽），`runtime.WorkflowRuntime` 相应新增 `Artifacts` 方法——ComfyUI 适配器早有这个实现，此前停在适配器里过不了隧道。

16. **任务时长/成功率/OOM/产物传输四项指标各只有一个记录点，不在多个调用方重复统计（STATUS.md 的 A06）。** `jobStore.update` 是 `jobSyncer`、`jobStatus` 轮询与 SSE 终态写入三条路径共用的唯一写入口，且早已用 `ObservedSeq`（第十条）分辨"真实变化"与"重复轮询"——`gateway_workflow_jobs_total`/`gateway_workflow_job_duration_seconds`/`gateway_workflow_job_oom_total` 就利用这同一个判断，只在一个 job **从非终态第一次转入终态**的那一次 `update` 调用里记录，此前所有非终态 `update` 与此后所有重复报告同一终态的 `update` 都不再记。时长取 `now - job.CreatedAt`（提交到终态的墙钟时间），不是后端自己上报的 `StartedAt`/`FinishedAt`——ComfyUI 适配器目前从不填充这两个字段，取 Gateway 自己记录的提交时间是唯一可靠的起点。OOM 计数完全依赖 `runtime.WorkflowStatus.OutOfMemory`，该字段由 ComfyUI 适配器对 history 的 `exception_type`/`exception_message` 做文本匹配设置（`common/runtime/workflow/comfyui/runtime.go` 的 `looksLikeOutOfMemory`），Gateway 侧只负责在终态转换时读取、从不重新判断。产物传输的两个指标记在 `jobpersist.go` 的 `persistArtifactBytes` 里，与它本就在做的 `countingReader` 字节计数共用同一次读取，不为指标多读一遍产物。三者的记录器都通过 `*recorder`（`jobPersistConfig.Metrics`/`jobStore.metrics`）注入，为 `nil` 时安全退化为不记录——多数测试与任何未接入 `-metrics-addr` 的部署都是这个状态。
17. **ComfyUI 队列深度记在 Agent 侧，不在 Gateway 这份目录里；对象存储用量记在控制面。** 队列深度只有持有真实隧道连接、能直接问 ComfyUI `GET /queue` 的那一侧才知道，因此 `comfyui_queue_running`/`comfyui_queue_pending`（`runtime_id` 标签）由 `common/runtime/workflow/comfyui` 适配器记录，复用 `Status`/`Cancel` 本就会发起的 `GET /queue`，不为指标新增任何请求——见 [Agent README](../aiServeWeaveAgent/README.md)。对象存储用量故意不做成 Gateway 侧的运行时累加计数器：Gateway 的 job/产物状态是有界内存、副本重启即丢（第八条），一个"上传 +size、清理 -size"的计数器会在每次重启后静默归零，却不影响对象存储里真实躺着的字节——因此改为控制面按 STATUS.md 的 A06 定期对 `job_artifacts` 表做 `SUM(size_bytes)`（只算 `storage_key` 非空的行），产物元数据本就持久化在那里，是这个数字唯一权威的来源；见 [ControlPlane README](../aiServeWeaveControlPlane/README.md)。
18. **`listArtifacts`/`downloadArtifact` 在本地未命中时回退到控制面（STATUS.md 的「Gateway 故障切换收尾」）。** J06 的 `jobRecoverer`（第十一条）只恢复非终态 job 的路由绑定，从不触及产物；一个终态 job 从跑它之外的副本被访问，或本副本重启后被问起，此前会直接 404——即使控制面早已持久化了这个 job 与它的产物。`httpapi/artifacts.go` 新增的回退不改变本地命中路径的任何行为，只在 `h.jobs.get`/`h.jobs.artifact` 未命中、且配置了 `Config.ArtifactRecoveryClient` 时才生效：`downloadArtifact` 按产物的裸公开 id（下载请求携带的唯一标识符，没有 job id）向控制面新增的 `GET /internal/v1/artifacts/{artifact_id}` 发问（`ArtifactRoute`），拿回 `StorageKey` 与所属 job 的 `NodeID`/`RuntimeID` 后照旧走"先试对象存储、失败再实时拉取"的既有逻辑；`listArtifacts` 先用 `JobExists` 确认 job 存在（用于把"job 不存在"与"job 存在但零产物"分开），再用既有的 `ListJobArtifacts`/`GetJob` 读回此前某次列举已经铸造、控制面已确认的产物 id——**绝不重新铸造一套新 id**，也绝不为此直接联系节点，因为这条路径存在的意义正是本副本可能压根没有通向该节点的活路由。恢复到的记录**不缓存进 `h.jobs`**：它在 `h.jobs.byID` 里没有可供一同逐出的所属条目，缓存会让它无边界地活得比这张表自己的逐出机制所能追踪的任何东西都久，代价是同一个"外来"产物每次下载都会再问一次控制面。一次从未被 `listArtifacts` 列举过的 job（异步 Job API 的调用方从未主动列举过产物）在任何副本上都无法恢复——这不是本条修的缺口，产物 id 本就只在列举那一刻铸造。`controlplaneclient.GatewayPersister` 新增第四个接口 `httpapi.ArtifactRecoveryClient`，与它已经满足的 `JobPersistClient`/`JobRecoveryClient`/`ArtifactCleanupClient` 是同一个适配器上追加的第四个。**已知边界**：终态 job 的产物下载依赖它此前确实被某个副本列举并成功持久化过（`persistArtifacts`，第十条/十二条）；一个刚完成、尚未轮到下一轮 `jobPersister.tick` 或复制仍在失败重试窗口内的产物，切换副本后依旧会短暂拿不到。

## 请求日志中间件与推送（P09/C28）

`httpapi/requestlog.go` 与 `httpapi/requestlogpush.go` 是 Gateway 一侧对 STATUS.md P09/C28（请求与错误检索）的实现：把一次已完成、已鉴权的前门请求，采集成可检索的脱敏元数据，异步批量推送给控制面。

1. **中间件插在鉴权之后、限流之前。** 完整链路是 `observe → withLogging → auth.middleware → requestLogMiddleware → rateLimit → mux`。放在 `auth.middleware` 之后，使 `IdentityFrom(ctx)` 已经就绪，不需要跨中间件共享指针；中间件包裹 `rateLimit` 与 `mux`，因此限流拒绝与业务 handler 的最终状态码都能被捕获。只对四个已知路径生效——`/v1/models`、`/v1/chat/completions`、`/v1/embeddings`、`/v1/responses`；其余路径（工作流 Job、产物下载等）直接跳过，不占用缓冲区容量。`h.requestLogs` 为 `nil`（未配置控制面推送）时中间件是纯粹透传。
2. **状态码到 outcome 的封闭映射。** `outcomeForStatus` 是只依赖状态码的纯函数，不读取、不拼接任何业务错误文本，因此 `chat.go`/`responses.go`/`embeddings.go`/`models.go` 都不需要为支持请求检索而改动：

   | 状态码 | Outcome |
   | --- | --- |
   | 2xx | `ok` |
   | 400 | `invalid_request` |
   | 401 | `unauthorized` |
   | 403 | `forbidden` |
   | 404 | `not_found` |
   | 429 | `rate_limited` |
   | 500 | `internal` |
   | 502/503/504 | `upstream_unavailable` |
   | 其他（含客户端提前断开、状态码未写出） | `error` |

3. **有界缓冲、批量异步推送，从不重试。** `requestLogPusher` 用一个容量 10000（`DefaultRequestLogBufferSize`）的有界 channel 承接中间件产生的记录；后台协程按攒够 500 条（`DefaultRequestLogBatchSize`）或每 5 秒（`DefaultRequestLogFlushInterval`）——以先到者为准——把一批推给控制面的 `POST /internal/v1/requestlogs`（`controlplaneclient.RequestLogsClient`）。**channel 满时丢弃新记录，同时对指标 `gateway_request_log_dropped_total` 自增，不阻塞正在处理的推理请求**——这是 AGENTS.md 安全红线「任何一跳都不得无界缓冲」的落实，请求记录是诊断性数据，允许极端负载下的少量丢失。一批推送失败（网络层错误）时整批本地丢弃，不重试——重试请求记录只会造成无界的本地积压。Gateway 优雅停止时对现有缓冲做一次尽力而为的 flush，不保证清空。
4. **上报字段不含任何需要脱敏的内容。** 记录只有 `request_id`（`common/reqid` 铸造，同时是控制面 `request_logs` 表的主键，天然防重）、`tenant_id`、`apikey.Display(key)` 的展示形式（前缀 + 明文前 8 位，不存完整 key 或哈希）、封闭 `endpoint` 枚举、原始状态码、`Outcome`、耗时毫秒数与创建时间；不记录请求体、响应体、模型名、node_id、Prompt 片段或鉴权头。

设计与验证细节见 [`docs/superpowers/specs/2026-09-11-p09-request-search-design.md`](../../docs/superpowers/specs/2026-09-11-p09-request-search-design.md)；控制面侧的表结构、内部推送端点幂等性、两个检索端点与保留期见 [ControlPlane README「请求日志检索（P09/C28）」](../aiServeWeaveControlPlane/README.md#请求日志检索p09c28)。

## 按租户/模型的持久化用量账本（STATUS.md 的 P2）

`httpapi/usageledger.go`、`httpapi/usageledgerpush.go` 与 `httpapi/ratelimit.go` 的 `recordUsage`/`ledgerUsage`，是 Gateway 一侧对 STATUS.md「按租户/模型的持久化用量账本」的实现：取代 `gateway_tokens_total`（每次副本重启即归零、且不带模型维度）成为供结算流程读取的依据。

1. **单一入账点。** 五个协议前门（OpenAI Chat、Embeddings、Responses、Anthropic Messages、Ollama 原生 chat/generate/embeddings）在得知一次请求的 `runtime.Usage` 后，都调用同一个 `h.recordUsage(ctx, usage, elapsed, endpoint, model)`——与它已经承担的「记指标、扣配额」职责相同的那一个函数，新增的 `endpoint`/`model` 只流向账本，从不进入 Prometheus 标签（原因见 `metrics.go` 关于模型基数的说明）。`model` 从不是客户端自由文本：这两个参数只在派发成功路径上被传入，此时调度器早已把请求解析到一个 `modelroute` 别名上。音频转录与 rerank 不按 token 计量，从不调用 `recordUsage`，因此账本里没有它们的 `endpoint` 取值。
2. **两个条件跳过入账，而不是写入一条空记录。** 全零用量（部分后端在中间 chunk 上不上报用量）与没有解析出租户身份的请求（未启用鉴权的部署没有可记账的租户）都直接返回，不占用推送缓冲。
3. **有界缓冲、批量异步推送、从不重试**，与请求日志推送器同一套参数（`DefaultUsageLedgerBufferSize` 10000、`DefaultUsageLedgerBatchSize` 500、`DefaultUsageLedgerFlushInterval` 5 秒）：满足 AGENTS.md「任何一跳都不得无界缓冲」，channel 满时丢弃并计入 `gateway_usage_ledger_dropped_total`，推送失败计入 `gateway_usage_ledger_push_failed_total`，均不重试——账本是最终一致的结算依据，不是逐笔确认收讫的交易日志；需要更强保证的消费方，应参照 P07 的事务 outbox 模式另行设计，这不在本项范围内。
4. **去重规则是主键本身。** `RequestID`（`common/reqid` 铸造）既是控制面 `usage_records` 表的主键，也是幂等写入的依据（`ON CONFLICT DO NOTHING`）——一次被重试的批量推送，第二次到达时被静默跳过，不会让同一次请求的用量被计两次。
5. **复用与请求日志相同的控制面连接。** `-control-plane-addr`/`-control-plane-token`（或 `AISW_CONTROLPLANE_TOKEN` 环境变量）驱动 `controlplaneclient.UsageLedgerClient` 推送到 `POST /internal/v1/usagerecords`；未配置控制面的部署，账本整体关闭（`h.usageLedger` 为 `nil`），不新增独立的地址/令牌配置项。

控制面侧的表结构、内部推送端点幂等性、两个结算查询端点（`/admin/v1/usage/summary`、`/operator/v1/usage/summary`）与保留期见 [ControlPlane README「用量账本（STATUS.md 的 P2）」](../aiServeWeaveControlPlane/README.md#用量账本status-md-的-p2)。已知缺口：定价/发票生成不在范围内——账本只给出按 (租户, 模型) 分组的 token 求和与请求计数，结算金额需要一个外部计费流程消费这份汇总；Console 尚未接入结算页面。

## 工作流模板版本与发布（P03）

`-workflow-source=controlplane` 下，由 `workflowsync` 从 `GET /internal/v1/workflow-templates/current` 拉取整套已发布模板；使用已有 `-control-plane-addr` 和 `AISW_CONTROL_PLANE_TOKEN`（或 `-control-plane-token`），与 P02 路由共用同一套控制面凭据，不增加数据库依赖。结构与 P02 路由完全同构（见下方「控制面管理路由」一节），区别只在发布的形状：路由是单一全局表，模板是多份各自独立版本化的文档，因此这里的回归防护按模板 id 分别追踪版本，整包状态用 `BundleDigest`（对已排序的 `(template_id, revision, digest)` 三元组取指纹）取代路由单一的 `revision`/`digest`。

```bash
mkdir -p ./data/gateway-1
aiserveweave-gateway \
  -workflow-source controlplane \
  -control-plane-addr http://controlplane:8090 \
  -workflow-state-file ./data/gateway-1/workflow-templates.json \
  -workflow-sync-interval 30s \
  -admin-addr 127.0.0.1:8091
```

此模式必须指定可写的状态文件，且不能同时设置 `-workflow-templates`。落盘缓存存放整份 `[]workflowtemplate.Snapshot`；启用整包大小上限 `workflowtemplate.MaxContentBytes * workflowtemplate.MaxTemplates`。校验规则与文件模式完全相同——两边都调用 `common/workflowtemplate.Validate`，不允许各自判断分叉（见该函数文档注释）。

`GET /internal/v1/workflows/status` 由运维监听器现有 Token 守卫，返回 `replica_id`、`generated_at`、`mode`、`template_count`、`bundle_digest`、`applied_at`、`checked_at` 与固定错误代号，供控制面的 `/operator/v1/workflow-templates/status` 聚合比对；现有 `GET /internal/v1/workflows` 端点不变，仍返回 `workflowview.TemplateCatalog`（不含图的目录），只是其元素现在多了 `version`、`visible_tenant_ids`、`outputs`、`dependencies` 四个字段。

**租户可见范围在提交路径强制执行，不只是目录过滤。** 每个模板版本携带一份 `VisibleTenantIDs`（空 = 对所有租户可见）；`POST /v1/workflows/{workflow_id}/runs` 对不在允许列表上的租户返回与「模板不存在」相同的 404，不泄露存在性——这是数据面的真实授权边界；控制面聚合出的 `/admin/v1/workflows` 菜单按同一份 `VisibleTenantIDs` 过滤只是给租户看的便利视图，两者独立生效。

**依赖检查仅做结构性声明校验，不核对节点实际能力。** 模板作者可以声明 `Dependencies{CustomNodes, Models}`（自定义节点包与模型 checkpoint 及其版本），发布时只检查非空、去重与数量上限，从不与任何已连接节点实际上报的已装列表交叉核对——因为目前没有节点上报这类信息，跨这条边界属于超出本轮范围的新协议设计。`Outputs` 同理：声明的是产出该结果的节点与种类，仅结构性校验该节点存在于图中，不核实运行后是否真的产出了声明种类的产物。

公开契约见 `common/workflowtemplate`；限额、控制面存储与 CAS 语义见 [ControlPlane README「工作流模板发布契约（P03）」](../aiServeWeaveControlPlane/README.md#工作流模板发布契约p03)。

## Responses API

`POST /v1/responses` **在前门转换成内部 canonical 请求**，不新增隧道操作——这是 README「外部协议只存在于系统边界」的字面落实，并且换来一件具体的好处：只会 Chat Completions 的后端（Ollama 就是）在不知道这个 API 存在的情况下也能服务 Responses 请求。vLLM 自己的 `/v1/responses` 因此没有被使用。

转换规则：`instructions` → 打头的 system 消息；`input` 的三种形式（裸字符串、`{role, content}` 数组、带 `input_text`/`output_text` 部件的数组）→ 同一份消息列表；`max_output_tokens` → `MaxTokens`；`text.format` → `ResponseFormat`；工具定义从 Responses 的扁平形状转成 Chat 的嵌套形状。

**不支持的字段被指名拒绝（400），不是静默忽略**，对应 README「不能静默丢弃参数」：

| 字段 | 为什么不行 |
| --- | --- |
| `previous_response_id` / `store` | 仅在配置了持久化会话历史的控制面时才被兑现（STATUS.md 的 P2「Responses 持久会话」，见下一节）；未配置控制面、或调用方未认证到真实租户时仍被指名拒绝——续接一段对话既需要 Gateway 持有它，又需要一个可供限定范围的租户 |
| `background` | 需要跨请求的服务端异步任务，本 Gateway 没有近似的东西 |
| 内置工具（`web_search`、`file_search`、`code_interpreter`、`mcp`） | 由 OpenAI 自己的服务执行。本 Gateway 只把请求转给模型、不运行任何东西 |
| 图像/音频/文件输入部件 | 需要一种本仓库尚不具备的 canonical 表示 |

**流式的事件嵌套是自己造出来的。** 下游隧道递上来的始终是一串扁平 delta，而 Responses 客户端的状态机建立在 `response` → `output_item` → `content_part` 的边界上，因此前门按那个顺序发：`response.created` → `in_progress` → `output_item.added` → `content_part.added` → `output_text.delta`×N → `output_text.done` → `content_part.done` → `output_item.done` → `completed`。`sequence_number` 在整条流上严格递增，那是客户端用来发现丢帧的东西。中途断流发 `response.failed`——响应头已经出去了，失败无法再表现为状态码。

**后端没上报 usage 时 `usage` 字段被省略，不发 `0/0/0`。**「这次不花钱」与「没人说过它花了多少」是两个不同的断言，而前者正是那种会出现在成本看板上的数字。

调度按 `CapabilityChat` 过滤而不是 `CapabilityResponses`：转换之后它就是一次 chat 请求。能力矩阵里的 `responses` 表示「后端原生支持 `/v1/responses`」，那条路径目前没有被使用。

### Responses 持久会话（P2）

`-control-plane-addr` 配置了控制面时，`store`/`previous_response_id` 从「指名拒绝」变成生效，沿用 Job 持久化（J01–J08）已确立的控制面表 + 内部 API + Gateway 客户端形状；控制面一侧的存储契约见 [ControlPlane README「Responses 持久会话（P2）」](../aiServeWeaveControlPlane/README.md#responses-持久会话p2)，设计见 [`docs/superpowers/specs/2026-09-16-p2-images-responses-multimodal-boundary-design.md`](../../docs/superpowers/specs/2026-09-16-p2-images-responses-multimodal-boundary-design.md) 第三节。

- **写入侧是有界异步的（`httpapi/responsespersist.go` 的 `responsePersister`）**：`store:true` 时，这一轮自己的输入加上 assistant 的回复被编码成不透明 JSON，交给一个信号量限流的后台写入（默认并发 8，`-control-plane-addr`/`-control-plane-token` 复用），绝不阻塞调用方正在等待的响应——并发已满时丢弃并计入 `gateway_response_persist_dropped_total`，写入失败计入 `gateway_response_persist_failed_total`，均不重试、不入持久队列，与请求日志推送同一纪律。
- **读取侧是同步的**：`previous_response_id` 非空时，Gateway 在派发请求之前先沿它逐跳调用控制面的 `GetResponseTurn` 走到根轮次，按时间顺序拼接每一跳自己的消息，作为前缀拼在这一轮自己的输入之前再发给后端——这一步是调用方请求路径的一部分，因为调用方点名了一段具体对话，需要一个确切答案而不是一段可能残缺的历史。链路深度有界（默认 50 跳，`maxResponseChainDepth`），超出时拒绝（503）而不是静默截断；未知或不属于该租户的 `previous_response_id` 答 400，不是编造一段更短的历史，也不是看起来像本 Gateway 自身故障的 500。
- **一轮只存自己的贡献**：持久化的 `messages` 是这一轮自己的 system 指示/用户输入加上 assistant 回复，不是从根到这一轮的完整对话——存储量与对话轮数成正比而非平方，代价是续接一段长对话需要多次控制面往返而非一次（已被 `maxResponseChainDepth` 有界）。
- **`store`/`previous_response_id` 都需要一个真实租户**：未配置 `Verifier`（静态 key 列表或完全不鉴权）意味着没有可供限定范围的租户，两个字段依旧被拒绝，即便控制面本身已经配置。
- 未接入 P09/C28 请求检索，与图像生成前门同一先例（`request_logs.endpoint` 是绑定数据库列的封闭枚举）。

## 配额与限流

三个维度，刻意是三个不同的问题（定义在 `common/quota`，由 `ratelimit/` 执行）：

| 维度 | 限制什么 | 何时扣减 |
| --- | --- | --- |
| `requests_per_minute` | 调用频率 | 入口 |
| `tokens_per_minute` | 工作量——一次 10 万 token 上下文的请求，代价远超一百次短请求 | **响应完成后**，因为后端上报之前无从得知代价 |
| `max_concurrent` | 同时性。它是三者中唯一保护容量而非公平性的维度 | 入口占用、出口归还 |

**零表示不限制**，未配置的租户得到的也是这个：一个升级到本功能的部署，绝不能突然开始拒绝它昨天还接受的流量。

**限制值随 API Key 校验结果下发**，不单独拉取——执行因此不给请求路径增加任何往返。P06 的 generation 通知只由 Key 吊销与用户禁用触发，单纯修改限制不会主动推送；旧限制仍按控制面 Redis TTL 与 Gateway `-key-cache-ttl` 自然过期。

**桶是连续补充的，不是每分钟计数器。** 固定窗口允许调用方在 0:59 花掉一整分钟的额度、在 1:01 再花掉下一分钟的，两秒之内跑出两倍于配置的速率。token 维度允许透支：一次超大的响应把它后面的请求恰好延迟它所耗费的那么多，欠账靠补充偿还而不是一笔勾销。

**超限返回 429 并带 `Retry-After`**，其秒数由桶的补充速率算出而非固定值——守规矩的客户端因此只在正确的时刻重试一次，而不是靠轮询把一次拒绝变成一场风暴。不排队：把超限请求挂住，消耗的正是限制所要保护的那份容量。

### 两个实现，一套测试

| 实现 | 精确性 | 代价 |
| --- | --- | --- |
| `Memory` | 单副本内精确；**N 个副本各放行一份完整额度**，限 60/分钟的租户实际拿到 60N | 无 |
| `Redis` | 整个集群精确 | 每请求一次 Redis 往返 |

由 `-redis-addr` 选择，留空用 `Memory` 并在启动时告警。**令牌桶因此存在两份**——`bucket.go` 一份 Go，`redis.go` 的 Lua 脚本一份——这正是本仓库视为「迟早要出事」的形状，缓解手段是结构性的：`contract_test.go` 是两个实现共同运行的同一套测试，一次分歧会让测试失败。改补充规则意味着两处都要改，而那套测试就是「你确实改了」的证明。

Redis 那一半默认不跑（`go test ./...` 保持自足），设 `AISW_REDIS_ADDR` 后加入同一套断言。

**限流器自身无法作答时请求被放行**，并记入 `gateway_rate_limiter_unavailable_total`。让请求失败会在 Redis 一眨眼的工夫里给每个调用方一个 5xx——配额短暂失去执行，比一次服务中断危害更小。该指标斜率非零意味着配额正在悄悄失效，那正是「直到有人收到账单才被发现」的那种退化。

**并发槽有 TTL 兜底**（`DefaultLeaseTTL`，30 分钟）。Gateway 副本可能在请求中途死掉，只靠显式释放腾出的槽此后会被一个已不存在的进程永久占着。该值必须长于最长的合法请求。

## 模型别名与节点标签

客户端请求逻辑模型名，调度器把它解析成一列有序的 target：

```json
[{
  "model": "qwen-coder",
  "targets": [
    {"runtime_model": "qwen3-coder:30b", "priority": 1, "node_selector": {"region": "local"}},
    {"runtime_model": "Qwen/Qwen3-Coder-FP8", "priority": 2}
  ]
}]
```

由 `-model-routes` 加载（逗号分隔的文件或目录，取 `*.json`）。**留空时模型 id 按节点声明的原样透传**，从不编写路由表的部署行为与此前完全一致。

三条规则：

1. **排序有两层，外层属于运维。** target 按 priority 依次尝试（数值小的在前，与 Kubernetes 一致），同优先级的可用 target 按权重随机排列（0 等同 1），再在各 target 内部按「空闲槽最多、上报队列占用最低、在途最少」三级选择候选（第二级见下方第 5 条）。这正是「先用本地那台 Mac，再用租来的 GPU」名副其实的原因：一个声明的偏好，不会被一台一时更空闲的机器推翻。优先级是排序不是排除——首选匹配不到节点时会落到次选。
2. **节点选择器是「与」。** 声明的每个标签都必须匹配；空选择器匹配所有节点。一条意为「其中任意一个」的规则根本无法表达「本地那台 4090」，而那正是运维实际会写的规则。
3. **别名在离开 Gateway 之前被改写成真实模型名**，客户端始终不会得知后者。`GET /v1/models` 因此在有路由表时只列别名——两者都公布等于邀请客户端绑定到某个运行时模型，而那正是别名要防止的事。没有活节点能服务的别名会被略去：一个用起来就 404 的目录条目，比一个缺失的条目更糟。
4. **`min_gpu_memory_bytes` 是准入门槛，不是排序维度**（STATUS.md 的 P2）。target 可选声明一个最小 GPU 显存总量；候选节点声明的显存（Agent 在 Hello 握手时上报的 `NodeResources.GpuMemoryBytes`，见 [Agent README](../aiServeWeaveAgent/README.md) 的 `hostresources/`）低于这个值就在候选阶段被整体排除，不参与后续任何排序。零值（默认）不做任何过滤。**未上报硬件、或上报零显存的节点永远不被此字段排除**——未声明的容量不是容量不足的证据，这样才能保证某个 target 第一次设置这个字段的那天，不会把所有还没升级 Agent 版本的节点一并挡在外面。详见 [P2 设计文档](../../docs/superpowers/specs/2026-09-15-p2-resource-aware-scheduling-design.md)。
5. **队列占用是排序维度，不是准入门槛，且只对 ComfyUI 有意义**（STATUS.md 的 P2 实时利用率子任务）。ComfyUI 同一时间只渲染一个任务，但它的软件并发上限与其他运行时共用同一个默认值——这意味着一个正在渲染的 ComfyUI 实例，空闲槽读数照样可以很高，「空闲槽最多」这一级排序因此分不清它和一个真正空闲的实例。`common/runtime/workflow/comfyui.Runtime.Health` 在存活性检查之外顺带查一次 `GET /queue`，写进 `HealthReport.QueueRunning`/`QueuePending`（`tunnel.proto` 的 `HealthReport` 消息新增字段，随 `RuntimeSnapshot` 一起到达 Gateway）；`pickBy` 在「空闲槽」相同的候选之间，优先选这两个数之和更低的一个，再退回「在途最少」。其余运行时种类这两个字段恒为 0，排序上等同于「确认空闲」——不上报这件事本身从不被当作惩罚，也不影响 Ollama/vLLM/SGLang 现有的排序结果。查询失败不影响健康检查本身，只是把这两个字段留在零值。必要性评估与范围收窄见 [必要性评估文档](../../docs/superpowers/specs/2026-09-16-p2-realtime-utilization-necessity-design.md)。

**节点标签由 Agent 的 `-labels` 声明**（`region=local,gpu=4090`），随 Hello 上报，重连即重新读取——它们描述的是机器而不是负载。畸形条目被丢弃而不是让 Agent 拒绝启动：为一个只影响「请求偏好去哪」的笔误让节点下线，代价不对等。

**标签是偏好，不是权限。** Gateway 原样采信 Agent 的声明，因此标签绝不能参与授权判断——被攻破的 Agent 可以声称任何标签。要让标签可信，需要 Registry 在签发证书时绑定它们，那是独立的一步。

## 健康过滤与熔断

`scheduler.candidates()` 现在有两层排除，都在 `service/aiServeWeaveGateway/scheduler/scheduler.go` 与 `breaker.go`：

1. **读 Agent 已经算好的健康状态。** `runtime.Snapshot.State` 是 `unhealthy`/`closed` 的 runtime 实例直接被过滤——这是 Agent 侧 `runtime.Manager` 探测出来的结论，Gateway 只是消费它，不重新判断一遍。
2. **Gateway 侧的熔断器。** 按 `(node_id, runtime_id)` 维护一个失败计数：`connection_failed`/`timeout`/`upstream_error` 三种错误计入连续失败，达到 `FailureThreshold`（默认 5）后该候选被排除一段冷却时间（默认 5s，翻倍退避到 2m 封顶），冷却结束后下一次请求本身就是一次探测，成功即整体复位。`ErrorBackpressure`/`ErrorRateLimited` 明确不计入——它们是"这一刻满了"，不是"坏了"。没有做教科书式的三态 half-open + 单飞探测：Gateway 本来就是零排队、失败立即换节点的模型，冷却期内多个请求同时探测同一个候选，最坏情况也只是各自快速失败再换节点。

`FailureThreshold`/`BaseCooldown`/`MaxCooldown` 是未经真实流量验证的初始默认值，`scheduler.New` 的 `Config` 参数可以覆盖；`main.go` 把它们暴露为三个 CLI flag（`-breaker-failure-threshold`、`-breaker-base-cooldown`、`-breaker-max-cooldown`，均默认 `0`，表示沿用 `scheduler` 包内的内置默认值 5 / 5s / 2m），便于在不同候选参数之间对比而不用重新编译。

P10 的合成后端长稳与同版逐副本替换不能校准这些值，因此默认值保持不变。真实流量校准的记录要求、长稳 CSV/JSON 归档方法与测试覆盖范围见 [P10 验收手册](../../deploy/p10-acceptance.md)；2026-09-12 的短时实测见 [验收记录](../../docs/acceptance/p10-2026-09-12/README.md)；2026-09-13 使用上述三个新 flag 对比候选参数的真实流量熔断校准见 [验收记录](../../docs/acceptance/p10-breaker-calibration-2026-09-13/README.md)。

## 工作流 Job 的有界排队（STATUS.md 的 P2）

「tunnelserver 的四条约束」第二条"不排队"说的是隧道数据面：`Dispatch` 没有空闲槽就立刻返回 `ErrorBackpressure`，从不在那一层等待，这一点没有变。本节说的是它上面一层——`scheduler.SubmitWorkflow` 在每一个当前候选都以可重试的背压类失败作答（即所有候选此刻都忙）时，可选地等待一段有界时间、期间重新轮询候选集，而不是立即把失败返回给调用方；详见 [P2 设计文档「有界任务排队」一节](../../docs/superpowers/specs/2026-09-15-p2-resource-aware-scheduling-design.md)。

- **默认关闭，行为与之前完全一致。** `-workflow-queue-max-wait` 默认 `0`，此时候选耗尽立即失败，等同于这个功能存在之前的行为。
- **只覆盖工作流 Job 提交（`SubmitWorkflow`），不覆盖 Chat/Embed。** OpenAI 前门是同步请求，客户端已经在等响应，在 Gateway 内部再排队没有意义（设计文档第六节第 3 条）；工作流 Job 本身是异步提交+轮询模型，天然适合排队。
- **候选完全不存在（`ErrNoCapableNode`）或失败不可重试时永远不排队。** 前者重试也不会凭空出现一个节点；后者（例如 ComfyUI 可能已经收到了这次提交）重试有制造第二次生成的风险，两者都保持立即失败。
- **`runtime.WorkflowRequest.MinGPUMemoryBytes` 把「模型别名与节点标签」第 4 条的准入门槛过滤接到了工作流提交路径**——这个过滤此前只经由一个有路由的模型的 `Target.MinGPUMemoryBytes` 生效，`workflowCandidates` 传的是空 `routing.Target{}`，导致它对工作流提交结构性地永远不生效，即便 P2 设计文档自己的首要场景就是"一个大模型工作流不应该被派给显存总量明显不够的节点"。字段由调用方设置（今天没有任何调用方设置它，默认零值不做过滤，行为不变），语义与 `Target.MinGPUMemoryBytes` 完全一致：达不到要求的候选在 `pickBy` 里被排除、绝不进入排队等待（等待无法让一个声明不足的显存总量变得足够，与"候选完全不存在时不排队"是同一类判断），未上报硬件的节点仍默认放行。**模板层面如何声明"这个工作流需要多少显存"仍未设计**——与 `modelroute.Target.MinGPUMemoryBytes` 今天完全靠运维手工声明、没有自动推断来源同一处境，是独立于本次调度器接线之外的后续任务。
- **两条独立的界，缺一不可**（AGENTS.md「任何一跳都不得无界缓冲」）：`-workflow-queue-max-wait` 限最长等待时长，`-workflow-queue-max-waiters`（默认 64）限同时在等的提交数，超过后新的提交立即被拒绝而不是排进一个更长的队。`-workflow-queue-retry-interval`（默认 500ms）是等待期间重新轮询候选集的间隔。
- **可观测**：`gateway_scheduler_queue_depth`（当前排队数）、`gateway_scheduler_queue_wait_seconds`（按 `outcome`∈`resolved`/`canceled`/`timeout` 分桶的等待时长）、`gateway_scheduler_queue_rejected_total`（因排队已满被拒绝的次数），见下方「指标」。

## 图像生成

`POST /v1/images/generations`（STATUS.md 的 P2）把 OpenAI-compatible 图像生成请求映射到管理员指定的单一 ComfyUI 工作流模板，边界设计见 [P2 设计文档「图像生成映射到 ComfyUI」](../../docs/superpowers/specs/2026-09-16-p2-images-responses-multimodal-boundary-design.md)。

- **`-images-workflow-id` 未配置时该路由照常挂载但答 404**，与其余工作流路由「路由总是挂载、由配置决定行为」的既有模式一致。配置了但对应模板在启动期缺少必填 `prompt` 字符串输入、或没有至少一个 `Type == "image"` 的 `Output`，进程直接启动失败（`main.go` 的 `validateImagesWorkflow`）——这是一次性静态检查，**不会**在 `-workflow-source=controlplane` 热替换目录时重新触发；一次剥离了所需字段的重新发布，只会在下一次请求时表现为 400/500，不是启动失败。
- **约定优于配置**：调用方的 `prompt` 绑定到模板声明的 `prompt` 输入，`size`（`"WIDTHxHEIGHT"`）仅在模板同时声明了 `width`/`height` 整数输入时才被接受，否则按名字拒绝。没有单独的输入名映射配置——这样模板经控制面热替换时，映射关系不会与它的 `Inputs` 声明脱节。
- **全程同步**：内部经 `scheduler.SubmitWorkflow`（与 `/v1/workflows/{id}/runs` 共用同一个入口，因此正确参与 P2 有界排队）提交后，以注入的 `runtime.Clock` 驱动的 500ms 固定间隔轮询 `WorkflowStatus` 直到终态或 `-images-generation-timeout`（默认 120s）超时；超时答 504，运行本身在节点上继续、不被取消，job 记录早于轮询循环写入，因此仍可用 `GET /v1/jobs/{job_id}` 查询。
- **产物筛选是运行期的扩展名约定，不是结构化的图判定**：`runtime.ArtifactRef` 不携带节点身份，因此无法把一次产物与模板声明的哪个 `Output.Node` 关联；实现上，`WorkflowArtifacts()` 结果里 `Type == "output"` 且文件名后缀属于已知图片扩展名（`.png .jpg .jpeg .webp .gif .bmp`）的才被当作生成图像，其余（包括模板作者自己保存的非图像调试产物）静默跳过。零个合格产物答 500。
- **`response_format=b64_json`（默认）** 经 `OpenArtifact` 有界读取（`MaxImageResponseBytes` 32 MiB，刻意远小于产物传输本身的 512 MiB 上限——把一个足尺寸产物 base64 膨胀进一个 JSON 响应体不是同步处理器该做的事）后 base64 编码；**`response_format=url`** 直接返回既有的 `GET /v1/artifacts/{artifact_id}` 路径，不新增同步持久化，靠该路径本就有的「优先读持久副本、失败回退实时节点拉取」覆盖异步持久化器还没赶上的窗口。
- **`n` 目前只接受 1**，`quality`/`style` 未实现，均按名字拒绝而不是静默忽略。
- 本项实现过程中发现并修复了一处独立于本功能之外的既有缺陷：`runtime.WorkflowStatus.OutOfMemory` 此前从未真正跨隧道传输（`tunnel.proto`/`common/tunnelwire` 都缺这个字段），意味着 A06 的 `gateway_workflow_job_oom_total` 指标在生产环境里从未被真正观测到过 true，详见设计文档 2.5 节。
- 本机无真实 ComfyUI/GPU 环境验证，与 A06 同一先例，默认测试套件（假节点）作为交付依据。

## Anthropic Messages

`POST /v1/messages`（STATUS.md 的 P2）是 Anthropic Messages 协议的 v1、纯文本子集，边界设计见 [P2 设计文档「Anthropic Messages 兼容边界」](../../docs/superpowers/specs/2026-09-16-p2-api-compat-boundary-design.md)。实现见 `httpapi/anthropic.go`。

- **架构与 Responses 相同：在边界处转换，不新增调度路径。** 请求在 `toRuntime()` 里被转换成与 `chat.go`/`responses.go` 完全同一个 canonical `runtime.ChatRequest`，经同一个 `Scheduler.Chat`/`ChatStream` 派发，因此一个只会 Chat Completions 的后端在不知道 Anthropic 协议存在的情况下就能服务这个端点；不改动 `common/runtime` 核心类型或调度逻辑。
- **v1 范围：纯文本，无工具，无图片/文档内容块。** `tools`、`tool_choice` 字段一旦出现即按名字拒绝（400），不静默忽略；`content` 数组里出现非 `"text"` 类型的块（`image`、`tool_use`、`tool_result` 等）同样按名字拒绝。原因是结构性的，不是尚未实现：今天的 `runtime.ChatMessage.Content` 是单一字符串，没有地方安放结构化内容块或工具调用，做到完整功能对等需要一次跨 OpenAI/Anthropic 两个前门共用的核心类型改动，设计文档§四.3 把它列为独立后续任务，不在本项范围内。
- **`system` 字段与 `content` 数组的归约规则相同**：顶层 `system`（字符串，或全为 `"text"` 块的数组）映射成一条前置的 `Role: "system"` 消息；每条 `messages[i].content`（字符串，或全为 `"text"` 块的数组）拼接成纯文本，多个文本块之间不插入分隔符。`messages[i].role` 只接受 `"user"`/`"assistant"`，其余角色（含 Anthropic 协议里不存在于 `messages` 数组的 `"system"`）按名字拒绝。
- **`max_tokens` 是必填字段**，Anthropic 协议本身如此要求；缺失或非正数答 400，不像 OpenAI 前门那样是可选参数。
- **流式响应是 Anthropic 自己的具名 SSE 帧**（`event: <name>\ndata: <json>\n\n`），不是 OpenAI 前门 `chat.go` 用的裸 `data:` 帧；两者共用底层 `runtime.Stream[runtime.ChatEvent]`，只是 `httpapi/anthropic.go` 另有一套 `writeAnthropicSSE`。`message_start`/`content_block_start` 在第一次 `Recv` 之前就无条件写出（不像 OpenAI 前门那样懒等首个 delta），这样即使一次生成完全没有产出内容，事件序列依然完整；结束时依次写出 `content_block_stop`/`message_delta`（携带 `stop_reason` 与 `usage`）/`message_stop`。
- **`stop_reason` 由后端不透明的 finish reason（OpenAI 风格：`"stop"`/`"length"`/`"tool_calls"`……）映射到 Anthropic 封闭词汇**（`anthropicStopReason`）：`"length"` → `"max_tokens"`，`"tool_calls"` → `"tool_use"`，其余（含空字符串）→ `"end_turn"`。
- **未接入 P09/C28 请求检索**：`request_logs.endpoint` 是绑定数据库列的封闭枚举，新增取值需要评估迁移，超出本轮范围，与 P2 图像生成一节的既有先例相同——`requestLogEndpoint` 未识别的路径会被中间件跳过，不记录也不报错。
- 错误体是 Anthropic 自己的 `{"type":"error","error":{"type":...,"message":...}}` 形状（`writeAnthropicError`），与 OpenAI 前门的 `openAIErrorBody` 分开；调度失败的分类逻辑（`errors.go` 的 `dispatchErrorDetails`）两边共用，同一次失败在两个协议下报告一致的状态码。

## Ollama 原生 API

`POST /api/chat`、`POST /api/generate`、`POST /api/embeddings`（STATUS.md 的 P2）是 Gateway 对客户端的第二套原生推理前门，边界设计见 [P2 设计文档「Ollama 原生 API 兼容边界」](../../docs/superpowers/specs/2026-09-16-p2-api-compat-boundary-design.md)。实现见 `httpapi/ollama.go`。**与 Agent 怎么连后端 Ollama 实例无关**：`common/runtime/ollama` 对那个后端刻意选择 OpenAI 兼容协议（见该包的包文档），这里只是在 Gateway 对客户端的一侧再开一扇门。

- **架构与 Anthropic 相同：边界处转换，不新增调度路径。** `toRuntime()` 把请求转换成与其他每个前门同一个 canonical `runtime.ChatRequest`/`runtime.EmbeddingRequest`，经同一个 `Scheduler.Chat`/`ChatStream`/`Embed` 派发；不改动 `common/runtime` 核心类型或调度逻辑。
- **范围是「纯推理端点」，与设计文档§九.2 的任务拆分一致**：`tools`、`format` 与每条消息的 `images` 一旦出现即按名字拒绝（400），不静默忽略——今天的 `runtime.ChatMessage.Content` 单一字符串没有地方安放它们；`/api/generate` 额外拒绝 `images`/`context`/`raw`/`template`/`suffix`，理由相同（`context` 尤其如此：这里没有可供延续的每请求状态）。`keep_alive` 与 `options` 里除采样参数外的其余旋钮（`num_ctx`、`num_gpu`、`mirostat`……）被静默忽略而非拒绝——这个 Gateway 不管理单个后端进程的内存驻留或上下文窗口大小，没有什么可供遵从或拒绝。
- **`stream` 的默认值与 OpenAI 前门相反**：Ollama 自己的协议里字段整体缺失即视为 `true`，因此 wire 结构体用 `*bool` 区分「缺失」与「显式 false」（`wantsStream()`）。
- **流式响应是换行分隔的 JSON（`application/x-ndjson`）**，不是 SSE：每行一个对象，末尾一行 `"done":true`，与 OpenAI/Anthropic 前门的 SSE 帧完全不同的协议形状。
- **`/api/generate` 把 `prompt`/`system` 归约成两条消息**：`system`（如果非空）映射成前置的 `Role: "system"` 消息，`prompt` 映射成一条 `Role: "user"` 消息，与 Anthropic 前门对 `system` 字段的归约方式一致。
- **不编造未测量的计时字段**：真实 Ollama 的响应还带 `total_duration`/`load_duration`/`prompt_eval_duration`/`eval_duration` 这类计时字段，本 Gateway 不追踪，因此整体不输出（而不是发送一个有误导性的零值）；`prompt_eval_count`/`eval_count` 映射到后端真实上报的 `resp.Usage.PromptTokens`/`CompletionTokens`，是可信数字。
- **模型管理端点明确答「不支持」，不是 404 或误导性的成功**：`/api/create`、`/api/pull`、`/api/push`、`/api/delete`、`/api/copy`、`/api/show`、`/api/tags`、`/api/ps` 统一挂载到 `ollamaUnsupported`，答 501 与 `{"error": "..."}` 说明——Gateway 从不管理任何节点上的本地模型文件，那是每个节点与自己后端之间的事，设计文档§五.3 把这条边界列为实现前必须明确声明的事项。
- **`/api/embeddings` 是单条 prompt 的旧版端点**，不是批量输入的新版 `/api/embed`——本 v1 前门不实现后者。
- **未接入 P09/C28 请求检索**：与 Anthropic 前门同一先例，`request_logs.endpoint` 是绑定数据库列的封闭枚举，`requestLogEndpoint` 未识别的路径会被中间件跳过，不记录也不报错。
- 错误体是 Ollama 自己的 `{"error": "..."}` 形状（`writeOllamaError`），调度失败的分类逻辑（`errors.go` 的 `dispatchErrorDetails`）与其余前门共用。

## 音频转录与翻译

`POST /v1/audio/transcriptions`、`POST /v1/audio/translations`（STATUS.md 的 P2）是 OpenAI 兼容的音频转录/翻译端点，边界设计见 [P2 设计文档「音频转录/翻译能力边界」](../../docs/superpowers/specs/2026-09-16-p2-api-compat-boundary-design.md)。实现见 `httpapi/audio.go`；不同于 Anthropic/Ollama 两个前门，这是唯一需要给 `common/runtime.InferenceRuntime` 新增方法（`Transcribe`）的一次破坏性接口变更，而不是纯粹在边界处转换。

- **`InferenceRuntime` 新增 `Transcribe(ctx, req AudioTranscriptionRequest, audio io.Reader) (AudioTranscriptionResponse, error)`**，转录与翻译共用一个方法与一个 `CapabilityAudioTranscription`：两者的 wire 与后端契约只在请求的 `Task` 字段上不同。`oaibase.Base.Transcribe` 是三个 OpenAI 兼容适配器（ollama、vllm、sglang）共享的实现，调用 `common/runtime/openai.Transcribe` 打 `/v1/audio/transcriptions` 或 `/v1/audio/translations`。
- **没有任何适配器的 `Discover` 会发布这项能力**：三个适配器都只是把调用委托给 `oaibase.Base.Transcribe`，而 `Base` 的能力门禁（`CapabilitiesFor` + `CapabilitySet.Require`）只认从 `Discover` 收到的证据，因此在真正为某个部署接入语音探测之前，每一次调用都会被本地拒绝为不支持，而不是被假定可行——「绝不假设后端支持」原则在这里第一次没有适配器把门打开。
- **音频体从不整体缓冲**：Gateway 收到的是 `multipart/form-data`（`r.ParseMultipartForm` 把过大的分片溢写到磁盘临时文件，与 `submitWithFiles` 对工作流 InputFile 分片的处理相同），文件部分随隧道消费而读取；隧道上新增的 `OPERATION_AUDIO_TRANSCRIBE` 复用 `OPERATION_INPUT_UPLOAD` 的「headers 之后跟 DataChunk」框架，并同样跑在 `SLOT_CLASS_BULK`（`tunnelserver.classFor`），与产物下载、输入上传物理隔离于推理之外。
- **不是 Chat/Embed 那样的多候选重试循环**：`Scheduler.Transcribe` 只接受调用方已经选定的一个 `Candidate`，`httpapi.audioTranscriptions`/`audioTranslations` 经 `Scheduler.TranscriptionCandidates(model)` 取候选、只用第一个——字节一旦开始流向某个节点，换节点重试就需要调用方从头重新读一遍整个文件，与 `UploadInput` 不跨节点重试的既有理由相同。
- **v1 范围**：`response_format` 只接受 `"text"`/`"json"`（默认 `"json"`，只含 `text` 字段），OpenAI 的 `srt`/`vtt`/`verbose_json` 按名字拒绝而非静默降级；`language` 字段只对转录有意义，翻译请求携带它会被拒绝（翻译的输出语言恒为英文）；固定、不可配置的扩展名允许列表（`.mp3`/`.wav`/`.ogg`/`.flac`）叠加与工作流上传共用的字节嗅探（`uploadformat.go` 的 `validateUploadContent`），拦下扩展名与实际字节不符的上传。
- **未接入 P09/C28 请求检索**：与 Anthropic/Ollama 前门同一先例，`request_logs.endpoint` 是绑定数据库列的封闭枚举，加值超出本轮范围。
- 错误体是 OpenAI 前门共用的 `openAIErrorBody` 形状，调度失败的分类逻辑（`errors.go` 的 `dispatchErrorDetails`）与其余前门共用。

## Rerank

`POST /v1/rerank`（STATUS.md 的 P2）把文档相对一个查询打分并按相关性排序，边界设计见 [P2 设计文档「rerank 能力边界」](../../docs/superpowers/specs/2026-09-16-p2-api-compat-boundary-design.md) 第七节。实现见 `httpapi/rerank.go`；这是 P2 API 兼容边界设计文档建议先行验证的「新增 `InferenceRuntime` 方法」这套破坏性变更模式的试点，音频转录随后复用了同一套模式（见上一节）。

- **`InferenceRuntime` 新增 `Rerank(ctx, req RerankRequest) (RerankResponse, error)`**，新增 `CapabilityRerank` 门禁。`oaibase.Base.Rerank` 是三个 OpenAI 兼容适配器（ollama、vllm、sglang）共享的实现，调用 `common/runtime/openai.Rerank` 打 `POST /v1/rerank`——这不是 OpenAI 自家定义的端点，而是 vLLM、TEI 等自托管 OpenAI 生态服务器沿用 Cohere/Jina rerank 契约形成的事实约定，与 `/v1/embeddings` 有 OpenAI 自己的定义不同。
- **没有任何适配器的 `Discover` 会发布这项能力**：三个适配器都只是把调用委托给 `oaibase.Base.Rerank`，而 `Base` 的能力门禁只认从 `Discover` 收到的证据（或运维经 `Config.CapabilityOverrides` 手工声明），因此在为某个部署确认后端支持之前，每一次调用都会被本地拒绝为不支持——与音频转录同一先例。
- **请求体是纯文本 JSON，不涉及二进制流**：`RerankRequest{Model, Query, Documents, TopN}`/`RerankResponse{Model, Results []RerankResult{Index, Score}}` 走与 Chat/Embed 同款的「wire 解析 → runtime 类型 → `Scheduler.Rerank` → 适配器」路径；隧道上新增的 `OPERATION_RERANK` 与 `OPERATION_EMBED` 同形状（`ShapeSingle`，无 `RequestBody`），跑在 `SLOT_CLASS_INFERENCE`，不是 `OPERATION_AUDIO_TRANSCRIBE` 那种 bulk 槽。
- **是 Chat/Embed 那样的多候选重试循环**：`Scheduler.Rerank` 与 `Scheduler.Embed` 同构，对可重试失败换下一个候选——与音频转录的单候选、不换节点重试不同，因为这里没有已经开始流向某个节点的字节需要担心。
- **未接入 P09/C28 请求检索**：与 Anthropic/Ollama/音频前门同一先例，`request_logs.endpoint` 是绑定数据库列的封闭枚举，加值超出本轮范围。
- 错误体是 OpenAI 前门共用的 `openAIErrorBody` 形状，调度失败的分类逻辑与其余前门共用。

## 指标

进程里只有一个 `metrics.Registry`（`common/metrics`），隧道服务端、调度器与前门都记录进它，由 `-metrics-addr`（默认 `127.0.0.1:9090`，留空则关闭）上的 `GET /metrics` 以 Prometheus 文本格式导出。**默认只绑回环**：导出内容会点出连到本副本的每一个 `node_id`，那是一份公网监听器没理由对外派发的资产清单；要让集群外的 Prometheus 抓取，前面必须先有鉴权。

三个包各自持有自己的目录（`tunnelserver.Descriptions()`、`scheduler.Descriptions()`、`httpapi.Descriptions()`），help 文本与分桶跟指标定义写在同一个文件里，`main.go` 只负责把它们并起来。

| 指标 | 标签 | 说明 |
| --- | --- | --- |
| `tunnel_server_connected_nodes` | `replica_id` | 与本副本保持可用隧道的节点数 |
| `tunnel_server_roster_version` | `replica_id` | 本副本最后广播的名册版本 |
| `tunnel_server_node_state` | `+node_id` | 0 gone / 1 connected / 2 draining |
| `tunnel_server_control_streams` | `+node_id` | 该节点存活的 Control 流数，长期为 2 说明没察觉死流 |
| `tunnel_server_heartbeats_total` | `+node_id` | 收到的心跳数 |
| `tunnel_server_heartbeat_interval_seconds` | `+node_id` | 相邻心跳间隔，长尾即"心跳迟到" |
| `tunnel_server_slots_total` | `+node_id,class,state` | `state`: idle\|busy |
| `tunnel_server_slot_faults_total` | `+node_id,reason` | Agent 违反帧契约导致关槽 |
| `tunnel_server_dispatch_total` | `+node_id,operation,result` | `result` 沿用六值约定，无空闲槽记 `backpressure` |
| `tunnel_server_dispatch_duration_seconds` | `+node_id,operation` | 分发到响应释放的整段时间 |
| `tunnel_server_stream_first_event_seconds` | `+node_id,operation` | 只统计渐进式响应，与 Agent 侧同名指标之差即隧道占的那份 TTFT |
| `tunnel_server_frame_bytes` | `+node_id,direction` | 数据面帧大小 |
| `tunnel_server_cancels_total` | `+node_id` | 调用方先走导致的取消 |
| `gateway_rate_limited_total` | `reason` | 被租户配额拒掉的请求。`reason` 取自封闭集合，**租户 id 不进标签**——那会让指标每多一个客户就多一条序列 |
| `gateway_rate_limiter_unavailable_total` | — | 因限流器无法作答而被放行的请求。斜率非零 = 配额正在悄悄失效 |
| `gateway_request_log_dropped_total` | — | 因有界推送缓冲已满而被丢弃的请求记录（P09/C28）。斜率非零 = 检索表正悄悄丢失最近的请求，不代表服务这些请求本身出了问题 |
| `gateway_scheduler_dispatches_total` | `node_id,runtime_id,result` | 与 `tunnel_server_dispatch_total` 之差 = 调度器根本没派出去的请求 |
| `gateway_scheduler_no_candidate_total` | `capability` | 完全找不到可用节点 |
| `gateway_scheduler_retries_total` | `capability` | 可重试失败后换候选 |
| `gateway_scheduler_candidates` | `capability` | 每次选择的候选数，向 1 收拢即失去冗余 |
| `gateway_scheduler_breaker_open` | `node_id,runtime_id` | 候选当前是否被熔断排除 |
| `gateway_scheduler_breaker_trips_total` | `node_id,runtime_id` | 熔断跳闸次数 |
| `gateway_scheduler_queue_depth` | 无 | 当前排队等待的工作流提交数（STATUS.md 的 P2） |
| `gateway_scheduler_queue_wait_seconds` | `outcome` | 排队提交的等待时长分布 |
| `gateway_scheduler_queue_rejected_total` | 无 | 因排队已满（`-workflow-queue-max-waiters`）被立即拒绝的次数 |
| `gateway_http_requests_total` | `endpoint,status` | `endpoint` 是本包十条路由加 `other`；带标识符的六条按形状匹配，工作流 id 与 job id 都不进标签。`job_events` 与 `jobs` 分开：一次 SSE 旁观持续整个生成过程，与毫秒级的状态查询共用直方图，哪个都描述不了 |
| `gateway_http_request_duration_seconds` | `endpoint` | 总响应时间 |
| `gateway_http_inflight_requests` | `endpoint` | 并发数 |
| `gateway_http_ttft_seconds` | `endpoint` | 首字节实际到达客户端（在 `Flush` 之后计量），只有流式会记 |
| `gateway_tokens_total` | `direction` | `prompt`\|`completion`，取后端上报值 |
| `gateway_output_tokens_per_second` | — | 输出 token 数除以请求耗时 |
| `gateway_workflow_jobs_total` | `result` | 工作流 job 按终态结果计数，`jobStore.update` 在一个 job**首次**到达终态那一刻记录（STATUS.md 的 A06）——`ObservedSeq` 已经把这一刻从后续重复轮询里分离出来，因此这里不是「调用方问了几次」的倍数，而是真实的完成量 |
| `gateway_workflow_job_duration_seconds` | `result` | 与上一行同一个记录点观测的墙钟时间：从 job 提交（`CreatedAt`）到首次到达终态 |
| `gateway_workflow_job_oom_total` | — | `gateway_workflow_jobs_total{result=failed}` 的子集：后端适配器（目前只有 ComfyUI）把这次失败分类为显存/内存耗尽（`runtime.WorkflowStatus.OutOfMemory`），依据 ComfyUI history 的 `exception_type`/`exception_message` 做尽力而为的文本匹配，不是稳定契约 |
| `gateway_artifact_transfers_total` | `result` | 一次产物「节点到存储」字节复制的结果，`jobpersist.go` 的 `persistArtifactBytes` 记录 |
| `gateway_artifact_transfer_bytes_total` | — | 已成功复制进对象存储的产物字节数，与上一行同一次调用产生，只在成功时累加 |
| `gateway_artifact_transfer_duration_seconds` | — | 单次产物字节复制耗时，无论成败 |

ComfyUI 适配器自身的队列深度指标（`comfyui_queue_running`/`comfyui_queue_pending`，`runtime_id` 标签）记在 Agent 侧，不在这份表里——见 [Agent README](../aiServeWeaveAgent/README.md) 的对应小节；控制面侦测的存储用量指标（`controlplane_artifact_storage_bytes`）同理，见 [ControlPlane README](../aiServeWeaveControlPlane/README.md)。三者合起来是 STATUS.md A06 要求的"队列长度、任务时长、成功率、OOM、产物传输与存储用量"六项观测。

**模型名不进任何标签，请求路径也不进。** 两者都是调用方在公开 API 的请求体/URL 里给的自由文本，进了标签就等于让单个客户端决定指标后端里有多少条序列。按模型记账属于用量记录，那里由本部署实际拥有的模型目录来约束。这条规则有可执行版本：`scheduler` 与 `tunnelserver` 各有一个标签基数测试，任何记录点开始传模型名都会当场失败。

排查要点：

- `tunnel_server_stream_first_event_seconds` 与 `gateway_http_ttft_seconds` 之差是前门自己的开销；前者与 Agent 侧 `tunnel_stream_first_event_seconds` 之差是隧道那一跳的开销。三个数字放在一起，"慢在哪一段"就不再需要猜。
- `gateway_scheduler_breaker_open` 持续为 1 而 `gateway_scheduler_breaker_trips_total` 不再增长，说明该候选是持续损坏而不是抖动；反过来跳闸计数斜率高而 gauge 反复归零，就是抖动。
- `aisw_metric_conflicts_total` 应当永远为 0。不为 0 说明某个包用与声明不符的类型记录了某个指标，那条序列的数据正在被丢弃。

## 下一步

1. **OpenTelemetry**：`runtime.Metrics` 这层抽象足以再接一个 OTel 导出器。P08 已经把 Gateway 前门铸造的 `request_id`（`common/reqid`）一路带到 Scheduler 派发决策、Tunnel 分发/完成与 Agent 后端调用，落地为可按 `request_id` 关联的结构化日志，但仍是日志而非独立的 trace 存储——接入真正的 span/trace 后端是独立的一步，见 STATUS.md 的 P08。
2. **Gateway↔Registry 双向认证**：目前 Gateway 只校验 Registry 的服务端证书，不向 Registry 出示客户端证书；要不要让 Gateway 也进入 Registry 签发的 mTLS 体系，等控制面需要更强隔离时再评估。

Registry 侧指标已在 P08 落地，见 [Registry README](../aiServeWeaveRegistry/README.md#指标)。

## 质量门禁

```bash
gofmt -l ./service
go vet ./service/aiServeWeaveGateway/...
go test -race ./service/aiServeWeaveGateway/...
```

`e2e` 包会真的监听回环端口、真的做 TLS 握手、真的跑 Agent 的隧道客户端。它不依赖 GPU、外部网络或真实后端——后端是脚本化的 `runtime.InferenceRuntime`，因为这个包测的是 Agent 与 Gateway 之间发生的事。

## 持久化与访问的现有限制

R04 对照 `httpapi/jobs.go`、`jobrecover.go` 与 `artifacts.go` 核实：公开 Job 响应尚未输出 J01 设计的 `durability` 字段；202 只表示后端提交成功，不确认落库。后台恢复仅扫描非终态 Job，下载只查本副本内存中的产物映射，不会按历史产物 ID 从控制面恢复映射。因此历史元数据可查不保证终态 Job 或原产物 ID 在重启/切换副本后仍能通过数据面访问；原节点离线也会影响文件可用性。

## 控制面管理路由（P02）

`-route-source=file` 是默认值，继续通过 `-model-routes` 读取文件/目录。显式选择 `-route-source=controlplane` 后，由 `routesync` 从 `GET /internal/v1/routes/current` 拉取整套不可变路由；使用已有 `-control-plane-addr` 和 `AISW_CONTROL_PLANE_TOKEN`（或 `-control-plane-token`），不增加数据库依赖。

```bash
mkdir -p ./data/gateway-1
# AISW_CONTROL_PLANE_TOKEN 与 AISW_GATEWAY_ADMIN_TOKEN 由部署环境注入。
aiserveweave-gateway \
  -route-source controlplane \
  -control-plane-addr http://controlplane:8090 \
  -route-state-file ./data/gateway-1/routes.json \
  -route-sync-interval 30s \
  -admin-addr 127.0.0.1:8091
```

此模式必须指定可写的状态文件，且不能同时设置 `-model-routes`。单次拉取超时 5s，默认每轮结束后等待 30s，不重叠；时钟与轮询间隔可注入测试。响应与缓存都限制为 1 MiB 路由正文加 64 KiB 元数据，拒绝空缺的 routes、无效摘要、版本倒退及同版本不同内容。每个副本使用自己的缓存文件，文件所属控制面环境不能混用。

新配置先完整校验，再写临时文件、fsync、原子替换及目录同步，成功后原子切换调度器快照。每个请求及一次模型列表读取只使用一份路由快照；在途请求和已有流不改目的地。控制面故障、无效新配置或缓存写入失败时保留最近有效配置。启动时先检查缓存并尝试拉取；两者均无有效版本则拒绝启动，绝不隐式退回透传。缓存回退后仍报告同步失败，恢复拉取才清除该状态。

`GET /internal/v1/routes` 由运维监听器现有 Token 守卫，返回 `replica_id`、`generated_at`、`mode`、`revision`、`digest`、`applied_at`、`checked_at` 与固定错误代号。文件模式也返回状态，但不算应用了控制面版本。控制面只对 `Fleet.Gateways` 明确配置的端点确认；副本丢失/重复身份、超过一分钟的时间偏差或版本/摘要不匹配均不能成为“全部应用”。这是一轮观测，不是全局原子发布协议。

路由仍是模型映射而非权限：没有匹配别名时保留原名请求语义，标签仍为 Agent 自述的筛选条件。P02 同时修复此前 Weight 只存储却未参与调度的缺口，同优先级权重现在实际影响首选目标；优先级、能力/健康过滤、每目标负载顺序与有限重试保持原职责。选择器也修正了空值边界：声明空标签值要求节点实际携带该标签，缺失标签不再被 map 零值误判为匹配。

### 从文件配置迁移

1. 在控制面受控启用一次 `Database.AutoMigrate`，创建带版本记录的路由表；完成后按部署策略关闭。
2. 平台运维在 Console `/operator/routes` 导入现有 JSON 文件，核对别名、目标模型、标签、优先级和权重，验证后发布第一版。多文件合并后同样限制 1 MiB，重复别名被拒绝。
3. 逐个将 Gateway 改为控制面模式，移除 `-model-routes`，为每个副本挂载独立持久缓存；在 Console 逐个检查实际版本与摘要。滚动接管期间新旧模式可能并存，显示未完成。
4. 配置内容回滚应在 Console 选择历史版本并发布为新版本。若需退出控制面模式，先导出选定版本的 `routes` 数组到文件、校验后显式切回 file 并重启；不得把包含 revision 等元数据的缓存文件直接当作旧路由数组使用。

公开契约见 `common/modelroute`；带宽/内存/历史容量上限和控制面 API 见 [ControlPlane README](../aiServeWeaveControlPlane/README.md#模型路由发布p02)。

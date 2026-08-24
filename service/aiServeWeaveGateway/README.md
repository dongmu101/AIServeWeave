# aiserveweave-gateway

数据面。对外终结 OpenAI / Anthropic 兼容 API，对内通过隧道把请求派给节点。

**当前进度：隧道服务端、调度器、OpenAI 前门、ComfyUI 工作流的提交与状态查询、Registry 名册订阅与指标导出均已落地。** 这个二进制现在能接住 Agent、知道每个节点能服务什么、把 HTTP 请求路由过去，自己的副本身份会同步给 Registry 维护的名册，并在 `-metrics-addr` 上导出 Prometheus 文本格式的指标。

| 目录 | 状态 | 内容 |
| --- | --- | --- |
| `tunnelserver/` | 已实现 | 隧道终结：mTLS 认证、节点表、槽池、十个 Operation 的分发、`NodeRuntime` |
| `routing/` | 已实现 | 逻辑模型到部署的映射：别名、节点选择器、优先级与权重 |
| `scheduler/` | 已实现 | 按模型与能力从节点表选节点，处理背压与重试语义，读 Agent 上报的健康状态并维护每候选的熔断器；工作流按 runtime 层能力选节点，见 `workflow.go` |
| `httpapi/` | 已实现 | `GET /v1/models`、`POST /v1/chat/completions`（含 SSE）、`POST /v1/embeddings`、`POST /v1/responses`（含 SSE）、`POST /v1/workflows/{workflow_id}/runs`、`GET /v1/jobs/{job_id}`、`GET /v1/jobs/{job_id}/events`（SSE）、`POST /v1/jobs/{job_id}/cancel`、`GET /v1/jobs/{job_id}/artifacts`、`GET /v1/artifacts/{artifact_id}`；鉴权见下面「API Key 鉴权」，工作流见「工作流 Job」 |
| `workflow/` | 已实现 | 管理员注册的 ComfyUI 工作流模板目录：清单加载、声明式输入、绑定与校验 |
| `ratelimit/` | 已实现 | 租户配额执行：连续补充的令牌桶，`Memory`（副本内）与 `Redis`（集群级）两个实现 |
| `registryclient/` | 已实现 | 向 Registry 的 `GatewayDirectory` 报到，把收到的名册转发给 `tunnelserver.Server.SetRoster` |
| `controlplaneclient/` | 已实现 | 对着控制面校验 API Key，进程内缓存；发出的是哈希而不是调用方的 key |
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

## API Key 鉴权

鉴权有三种模式，按真实部署应当采用的优先级排列（实现在 `httpapi/auth.go`）：

1. **`-control-plane-addr`（推荐）** —— 对着控制面校验。key 以哈希存储、可吊销、携带租户。这是 README 安全设计那条「用户 API Key 只保存不可逆哈希」真正成立的路径。
2. **`-api-keys`** —— 静态明文列表，靠重启轮换。留给本地开发，以及控制面尚未部署时先把数据面跑起来。两者都配置时控制面胜出，并在启动时告警。
3. **两者皆无** —— 放行一切，仅在监听回环时才谈得上合适，启动时有告警。

**Gateway 发给控制面的是哈希，不是调用方的 key。** SHA-256 在这里算完，因此用户的凭据从不进入控制面的内存、它的请求日志，或两者之间的抓包。哈希足以查到一个 key，却无法用来在别处冒充它。

**校验结果在进程内缓存 `-key-cache-ttl`（默认 30s）。** 每一次推理调用都携带 key，为每一次都去问控制面等于在每个 token 前面垫一跳 HTTP。代价是一个被吊销的 key 在本副本上最多还能用这么久——控制面那边是立即失效的。要把它压到零需要控制面向 Gateway 推送失效，那是独立的一步。

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

设计上有八条约束，改这里的代码时不能绕过：

1. **调用方给不出图。** 请求体只有 `inputs`，图来自已注册的模板。模板把每个可替换输入声明为「节点 + 字段 + 类型 + 范围」，且该字段必须已存在于图中——输入只覆盖模板作者放好的值，从不创建字段。声明错误的模板在 `workflow.Load` 时就失败，挂在运维的终端上而不是某个调用方的请求上。这是 README 顶层「平台不应允许普通 API 调用者随意修改整个节点图」的落实。
2. **`prompt_id` 不外泄。** 公开 id 是 Gateway 自己铸的 `job_...`，后端的 `prompt_id` 只存在 job 记录里。它不是我们该派发的东西，而且只在单个 ComfyUI 内部唯一。
3. **提交只在「后端确定没见过它」时才换节点。** `scheduler.submitRetryable` 比通用的 `retryable()` 窄：只有 `backpressure`、`rate_limited`、`connection_failed`、`runtime_closed` 才重试。上游错误与超时会让这次提交的下场变成未知，重试它就是又生成一张没人要的图。这是 README「ComfyUI 作业一旦开始执行，不应自动迁移到其他节点」在调度侧的一半。
4. **事件流是「运行如何结束」的权威。** `succeeded`/`failed`/`cancelled` 三种事件一到，job 状态就地写入存储并结束该流；此后状态查询由存储作答，不再为一次早已结束的运行去打扰节点。断连由 `tunnelserver.Response.Recv` 自己 select 请求 context 处理（`call.go`），取消让它立即返回，被 defer 的 `Close` 再把取消经隧道送给 Agent——前门这边不需要额外的看门狗协程。
5. **取消是请求，不是结论。** ComfyUI 的中断是异步的，因此 `cancel` 返回 202 后 job 仍是后端最后报告的那个状态，直到状态查询或事件流带回真正的结果——在这里就把它标成 `cancelled`，是 Gateway 在编造一个没人告诉过它的结果。已结束的 job 返回 409（请求与状态冲突），节点不具备中断能力时返回 501（`cancel_unsupported`），而不是笼统的 500——后者会让调用方跑到我们这边找问题。
6. **产物的公开 id 与后端路径无关。** 后端用 `filename`+`subfolder`+`type` 三元组定位产物，那是通往它自己磁盘布局的一条路径。这个三元组绝不作为标识符抵达调用方：`artifact_id` 在列举时铸造、经由存储解回，因此调用方无法伪造一个指向本次运行没有产出的文件的 id。id 在多次列举之间稳定——每次调用铸一套新的，会让每轮轮询都把存储撑大一点。
7. **产物下载走批量槽，且不落地。** `OPERATION_ARTIFACT_LIST` 是有界回复，走推理槽；`OPERATION_ARTIFACT_OPEN` 流出整个响应体，走批量槽，两类槽在隧道里物理隔离，一次大的下载挤不掉推理。前门用 `io.Copy` 直通转发，本进程从不完整持有一个产物，背压经由同一次读取抵达 Agent。回显进 `Content-Disposition` 的文件名先被清洗：目录部分、CR、LF、引号与控制字符一律移除而不是转义——那个名字来自后端，并经由工作流自己的保存节点前缀最终来自调用方。
8. **job 表在内存里，且有界。** 上限 `httpapi.DefaultMaxJobs`（10000），超出逐出最旧的一条；副本重启即丢失，也不跨副本共享。持久化属于控制面的 `jobs` 表，那张表还没建。job 按租户隔离：不属于本租户的 job id 与不存在的 job id 得到同一个 404，产物 id 同理——产物就是生成出来的图像本身，那是这整个界面里最要紧的一处泄露。逐出一个 job 时，解析到它的产物 id 一并删除，否则被逐出的 job 的产物会留在一张不再受任何东西约束的表里继续可下载。

`-workflow-templates` 接受逗号分隔的文件或目录（目录下取 `*.json`，其余忽略），留空则不注册任何模板，此时提交一律 404。清单形如：

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

README 顶层「ComfyUI 任务 API」列出的六个端点已全部落地。产物列表这一步顺带扩了隧道契约：新增 `OPERATION_ARTIFACT_LIST`（`RunRef` 进、`ArtifactList` 出，走推理槽），`runtime.WorkflowRuntime` 相应新增 `Artifacts` 方法——ComfyUI 适配器早有这个实现，此前停在适配器里过不了隧道。

## Responses API

`POST /v1/responses` **在前门转换成内部 canonical 请求**，不新增隧道操作——这是 README「外部协议只存在于系统边界」的字面落实，并且换来一件具体的好处：只会 Chat Completions 的后端（Ollama 就是）在不知道这个 API 存在的情况下也能服务 Responses 请求。vLLM 自己的 `/v1/responses` 因此没有被使用。

转换规则：`instructions` → 打头的 system 消息；`input` 的三种形式（裸字符串、`{role, content}` 数组、带 `input_text`/`output_text` 部件的数组）→ 同一份消息列表；`max_output_tokens` → `MaxTokens`；`text.format` → `ResponseFormat`；工具定义从 Responses 的扁平形状转成 Chat 的嵌套形状。

**不支持的字段被指名拒绝（400），不是静默忽略**，对应 README「不能静默丢弃参数」：

| 字段 | 为什么不行 |
| --- | --- |
| `previous_response_id` | 续接已存储的会话要求 Gateway 既持有那次会话、又把后续请求发给同一个节点。两者它都没有：响应不被存储，调度器按请求选节点 |
| `store` / `background` | 同上，都需要跨请求的服务端状态 |
| 内置工具（`web_search`、`file_search`、`code_interpreter`、`mcp`） | 由 OpenAI 自己的服务执行。本 Gateway 只把请求转给模型、不运行任何东西 |
| 图像/音频/文件输入部件 | 需要一种本仓库尚不具备的 canonical 表示 |

**流式的事件嵌套是自己造出来的。** 下游隧道递上来的始终是一串扁平 delta，而 Responses 客户端的状态机建立在 `response` → `output_item` → `content_part` 的边界上，因此前门按那个顺序发：`response.created` → `in_progress` → `output_item.added` → `content_part.added` → `output_text.delta`×N → `output_text.done` → `content_part.done` → `output_item.done` → `completed`。`sequence_number` 在整条流上严格递增，那是客户端用来发现丢帧的东西。中途断流发 `response.failed`——响应头已经出去了，失败无法再表现为状态码。

**后端没上报 usage 时 `usage` 字段被省略，不发 `0/0/0`。**「这次不花钱」与「没人说过它花了多少」是两个不同的断言，而前者正是那种会出现在成本看板上的数字。

调度按 `CapabilityChat` 过滤而不是 `CapabilityResponses`：转换之后它就是一次 chat 请求。能力矩阵里的 `responses` 表示「后端原生支持 `/v1/responses`」，那条路径目前没有被使用。

## 配额与限流

三个维度，刻意是三个不同的问题（定义在 `common/quota`，由 `ratelimit/` 执行）：

| 维度 | 限制什么 | 何时扣减 |
| --- | --- | --- |
| `requests_per_minute` | 调用频率 | 入口 |
| `tokens_per_minute` | 工作量——一次 10 万 token 上下文的请求，代价远超一百次短请求 | **响应完成后**，因为后端上报之前无从得知代价 |
| `max_concurrent` | 同时性。它是三者中唯一保护容量而非公平性的维度 | 入口占用、出口归还 |

**零表示不限制**，未配置的租户得到的也是这个：一个升级到本功能的部署，绝不能突然开始拒绝它昨天还接受的流量。

**限制值随 API Key 校验结果下发**，不单独拉取——执行因此不给请求路径增加任何往返，代价是限制变更的生效窗口与吊销相同（`-key-cache-ttl`，默认 30s）。

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

1. **排序有两层，外层属于运维。** target 按 priority 依次尝试（数值小的在前，与 Kubernetes 一致），只有在同一个 target 内部才由「空闲槽最多、在途最少」的负载启发式决定。这正是「先用本地那台 Mac，再用租来的 GPU」名副其实的原因：一个声明的偏好，不会被一台一时更空闲的机器推翻。优先级是排序不是排除——首选匹配不到节点时会落到次选。
2. **节点选择器是「与」。** 声明的每个标签都必须匹配；空选择器匹配所有节点。一条意为「其中任意一个」的规则根本无法表达「本地那台 4090」，而那正是运维实际会写的规则。
3. **别名在离开 Gateway 之前被改写成真实模型名**，客户端始终不会得知后者。`GET /v1/models` 因此在有路由表时只列别名——两者都公布等于邀请客户端绑定到某个运行时模型，而那正是别名要防止的事。没有活节点能服务的别名会被略去：一个用起来就 404 的目录条目，比一个缺失的条目更糟。

**节点标签由 Agent 的 `-labels` 声明**（`region=local,gpu=4090`），随 Hello 上报，重连即重新读取——它们描述的是机器而不是负载。畸形条目被丢弃而不是让 Agent 拒绝启动：为一个只影响「请求偏好去哪」的笔误让节点下线，代价不对等。

**标签是偏好，不是权限。** Gateway 原样采信 Agent 的声明，因此标签绝不能参与授权判断——被攻破的 Agent 可以声称任何标签。要让标签可信，需要 Registry 在签发证书时绑定它们，那是独立的一步。

## 健康过滤与熔断

`scheduler.candidates()` 现在有两层排除，都在 `service/aiServeWeaveGateway/scheduler/scheduler.go` 与 `breaker.go`：

1. **读 Agent 已经算好的健康状态。** `runtime.Snapshot.State` 是 `unhealthy`/`closed` 的 runtime 实例直接被过滤——这是 Agent 侧 `runtime.Manager` 探测出来的结论，Gateway 只是消费它，不重新判断一遍。
2. **Gateway 侧的熔断器。** 按 `(node_id, runtime_id)` 维护一个失败计数：`connection_failed`/`timeout`/`upstream_error` 三种错误计入连续失败，达到 `FailureThreshold`（默认 5）后该候选被排除一段冷却时间（默认 5s，翻倍退避到 2m 封顶），冷却结束后下一次请求本身就是一次探测，成功即整体复位。`ErrorBackpressure`/`ErrorRateLimited` 明确不计入——它们是"这一刻满了"，不是"坏了"。没有做教科书式的三态 half-open + 单飞探测：Gateway 本来就是零排队、失败立即换节点的模型，冷却期内多个请求同时探测同一个候选，最坏情况也只是各自快速失败再换节点。

`FailureThreshold`/`BaseCooldown`/`MaxCooldown` 是未经真实流量验证的初始默认值，`scheduler.New` 的 `Config` 参数可以覆盖。

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
| `gateway_scheduler_dispatches_total` | `node_id,runtime_id,result` | 与 `tunnel_server_dispatch_total` 之差 = 调度器根本没派出去的请求 |
| `gateway_scheduler_no_candidate_total` | `capability` | 完全找不到可用节点 |
| `gateway_scheduler_retries_total` | `capability` | 可重试失败后换候选 |
| `gateway_scheduler_candidates` | `capability` | 每次选择的候选数，向 1 收拢即失去冗余 |
| `gateway_scheduler_breaker_open` | `node_id,runtime_id` | 候选当前是否被熔断排除 |
| `gateway_scheduler_breaker_trips_total` | `node_id,runtime_id` | 熔断跳闸次数 |
| `gateway_http_requests_total` | `endpoint,status` | `endpoint` 是本包十条路由加 `other`；带标识符的六条按形状匹配，工作流 id 与 job id 都不进标签。`job_events` 与 `jobs` 分开：一次 SSE 旁观持续整个生成过程，与毫秒级的状态查询共用直方图，哪个都描述不了 |
| `gateway_http_request_duration_seconds` | `endpoint` | 总响应时间 |
| `gateway_http_inflight_requests` | `endpoint` | 并发数 |
| `gateway_http_ttft_seconds` | `endpoint` | 首字节实际到达客户端（在 `Flush` 之后计量），只有流式会记 |
| `gateway_tokens_total` | `direction` | `prompt`\|`completion`，取后端上报值 |
| `gateway_output_tokens_per_second` | — | 输出 token 数除以请求耗时 |

**模型名不进任何标签，请求路径也不进。** 两者都是调用方在公开 API 的请求体/URL 里给的自由文本，进了标签就等于让单个客户端决定指标后端里有多少条序列。按模型记账属于用量记录，那里由本部署实际拥有的模型目录来约束。这条规则有可执行版本：`scheduler` 与 `tunnelserver` 各有一个标签基数测试，任何记录点开始传模型名都会当场失败。

排查要点：

- `tunnel_server_stream_first_event_seconds` 与 `gateway_http_ttft_seconds` 之差是前门自己的开销；前者与 Agent 侧 `tunnel_stream_first_event_seconds` 之差是隧道那一跳的开销。三个数字放在一起，"慢在哪一段"就不再需要猜。
- `gateway_scheduler_breaker_open` 持续为 1 而 `gateway_scheduler_breaker_trips_total` 不再增长，说明该候选是持续损坏而不是抖动；反过来跳闸计数斜率高而 gauge 反复归零，就是抖动。
- `aisw_metric_conflicts_total` 应当永远为 0。不为 0 说明某个包用与声明不符的类型记录了某个指标，那条序列的数据正在被丢弃。

## 下一步

1. **Registry 侧指标**：Registry 目前不记录任何指标，也没有 `/metrics` 端点。`common/metrics` 已经就位，缺的是 Registry 自己的目录与记录点。
2. **OpenTelemetry**：`runtime.Metrics` 这层抽象足以再接一个 OTel 导出器，但真正缺的是 trace——`request_id` 已经贯穿全链路日志，把它接成 span 是独立的一步。
3. **Gateway↔Registry 双向认证**：目前 Gateway 只校验 Registry 的服务端证书，不向 Registry 出示客户端证书；要不要让 Gateway 也进入 Registry 签发的 mTLS 体系，等控制面需要更强隔离时再评估。

## 质量门禁

```bash
gofmt -l ./service
go vet ./service/aiServeWeaveGateway/...
go test -race ./service/aiServeWeaveGateway/...
```

`e2e` 包会真的监听回环端口、真的做 TLS 握手、真的跑 Agent 的隧道客户端。它不依赖 GPU、外部网络或真实后端——后端是脚本化的 `runtime.InferenceRuntime`，因为这个包测的是 Agent 与 Gateway 之间发生的事。

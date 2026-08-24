# aiserveweave-gateway

数据面。对外终结 OpenAI / Anthropic 兼容 API，对内通过隧道把请求派给节点。

**当前进度：隧道服务端、调度器、OpenAI 前门、Registry 名册订阅与指标导出均已落地。** 这个二进制现在能接住 Agent、知道每个节点能服务什么、把 HTTP 请求路由过去，自己的副本身份会同步给 Registry 维护的名册，并在 `-metrics-addr` 上导出 Prometheus 文本格式的指标。

| 目录 | 状态 | 内容 |
| --- | --- | --- |
| `tunnelserver/` | 已实现 | 隧道终结：mTLS 认证、节点表、槽池、九个 Operation 的分发、`NodeRuntime` |
| `scheduler/` | 已实现 | 按模型与能力从节点表选节点，处理背压与重试语义，读 Agent 上报的健康状态并维护每候选的熔断器 |
| `httpapi/` | 已实现 | `GET /v1/models`、`POST /v1/chat/completions`（含 SSE）、`POST /v1/embeddings`；鉴权见下面「API Key 鉴权」 |
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
| `gateway_scheduler_dispatches_total` | `node_id,runtime_id,result` | 与 `tunnel_server_dispatch_total` 之差 = 调度器根本没派出去的请求 |
| `gateway_scheduler_no_candidate_total` | `capability` | 完全找不到可用节点 |
| `gateway_scheduler_retries_total` | `capability` | 可重试失败后换候选 |
| `gateway_scheduler_candidates` | `capability` | 每次选择的候选数，向 1 收拢即失去冗余 |
| `gateway_scheduler_breaker_open` | `node_id,runtime_id` | 候选当前是否被熔断排除 |
| `gateway_scheduler_breaker_trips_total` | `node_id,runtime_id` | 熔断跳闸次数 |
| `gateway_http_requests_total` | `endpoint,status` | `endpoint` 是三条路由加 `other` |
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

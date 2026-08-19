# Gateway 接入规划

**目标：** 为互联网用户提供一个稳定的 OpenAI 兼容入口，把请求安全、低延迟地调度到分布在公网 GPU 服务器和 NAT 后 Mac 上的 vLLM、SGLang、Ollama、ComfyUI 后端，并对结果、用量和故障负责。

**架构：** Gateway 是**无状态、可水平扩展的纯数据面**，以多副本部署在负载均衡之后。它把外部协议转换成 canonical 请求，从节点状态快照中选出目标部署，通过 **Direct** 或 **Tunnel** 两条路径转发——两条路径在代码里是**同一个接口**，调度器不感知差别。每个 Agent 与**每个副本**各建一条隧道，因此任意副本都能直达任意在线节点，请求路径上不存在副本间转发。控制面数据（节点身份、模型、路由、配额定义）由 Registry 提供，Gateway 只读缓存。

**技术栈：** Go 1.26、`net/http`、`google.golang.org/grpc`、`log/slog`、Redis（仅用于跨副本配额），复用 `service/aiServeWeaveAgent/runtime` 的接口与类型，以及 `service/aiServeWeaveAgent/tunnel` 的 proto。

---

## 当前状态

Gateway 尚未开始实现，`main.go` 只有一行包声明。可复用的既有成果：

| 依赖 | 状态 | 对 Gateway 的价值 |
| --- | --- | --- |
| `runtime` 接口与类型 | 已实现 | 直接作为 Gateway 的内部统一协议，无需另造一套 canonical 类型 |
| `runtime/openai` 客户端 | 已实现 | Direct 模式转发即为该客户端；SSE 解析、错误映射全部复用 |
| `runtime.Manager` 与 `Snapshot` | 已实现 | 节点状态快照的数据结构，Gateway 侧直接消费 |
| `runtime.CapabilitySet.Resolve` | 已实现 | 调度器的能力过滤直接调用，不重复实现 |
| `runtime.Stream[T].Committed()` | 已实现 | 流式重试安全边界的判据 |
| 四个后端适配器 | 占位符 | Direct 模式依赖 vLLM/SGLang 适配器；MVP 可先只做 Tunnel + Ollama |
| Tunnel proto 与服务端 | 未开始 | 见 `service/aiServeWeaveAgent/tunnel/README.md`，两侧共用同一份 proto |
| Registry 服务 | 未开始 | **多副本下 Registry 是必需项**：节点证书签发、bootstrap token 一次性校验和副本名册都在它上面 |

多副本相比单副本新增的外部依赖只有两项：**Registry**（节点身份与副本名册）和 **Redis**（跨副本配额）。两者都不在请求的关键路径上——Registry 是 30s 拉取的只读缓存，Redis 只在配额扣减时异步访问。

## 全局约束

- **Gateway 无状态且副本对等。** 任意副本可处理任意请求；副本之间不通信、不选主、不共享内存状态。
- **请求路径上没有副本间转发。** 一个请求只经过一个 Gateway 副本，隧道由该副本直达目标 Agent。
- 外部协议只存在于边界。进入调度器之前一律转换为 `runtime` 的 canonical 类型。
- Direct 与 Tunnel 必须实现同一个 `runtime.InferenceRuntime` / `WorkflowRuntime` 接口，调度器不出现 `if tunnel` 分支。
- 请求路径上不做探测、不查数据库、不做同步磁盘 IO；只读内存中的状态快照。
- 流式响应逐事件 flush，全链路禁止缓冲。
- 已向客户端发出首个事件的流不得透明重试，判据是 `Stream.Committed()`。
- Prompt、生成内容、API Key 和上游 Authorization 不进日志。
- 默认测试不依赖真实后端、GPU、Redis 或外部网络。

## 范围

### 首期包含

- `POST /v1/chat/completions`（含 SSE）、`POST /v1/embeddings`、`GET /v1/models`。
- API Key 鉴权、租户识别、限流与跨副本配额。
- 逻辑模型到多个部署的路由与调度评分。
- Direct 与 Tunnel 双路径转发，统一接口。
- Tunnel 服务端：Control 流、槽池管理、节点视图维护。
- **多副本部署**：无状态水平扩展、副本名册下发、滚动升级零不可用窗口。
- 首字节前的有限重试与换节点。
- 熔断与自动恢复。
- 请求用量、时延、状态与错误的记录。
- 面向互联网的 TLS 终止、超时和优雅停机。

### 首期不包含

- Anthropic Messages API、Responses API、音频、rerank。
- ComfyUI Job API 与产物存储（协议层预留，实现见第二阶段）。
- Web 控制台与 Admin API。
- **独立 Tunnel Hub**（副本数增长到全连接不划算时才需要，触发条件见「多副本拓扑」）。
- 副本间的请求转发与会话迁移。
- 计费、发票与用量报表。
- 请求排队与准入控制（首期一律快速失败）。

## 设计原则

1. **两条路径，一个接口。** `DirectRuntime` 和 `RemoteRuntime` 都实现 `runtime.InferenceRuntime`，调度器面对的永远是接口。新增传输方式不改调度器。
2. **副本对等，视图自持。** 每个副本独立维护"我能到达哪些节点"的视图，不追求跨副本共识。视图分歧是正确行为，不是需要修复的不一致。
3. **canonical 就是 `runtime` 类型。** 不为 Gateway 另造一套内部协议，避免同一份字段在三个地方各写一遍转换。
4. **状态只读，快照驱动。** 调度决策只依赖内存中的节点快照；快照由 Tunnel 的 Control 流和 Direct 的健康检查异步刷新。
5. **快速失败优于排队。** 无可用节点、无空闲槽、超出配额一律立即返回明确错误，让客户端和上游决定退避。
6. **重试有边界。** 只有未收到首个事件的请求才可换节点重试；这条规则在 `Stream.Committed()` 上强制。
7. **TTFT 是第一指标。** 任何优化如果增加首字延迟，都必须先证明收益大于损失。多副本方案选择全连接隧道而非 Hub 转发，正是这条原则的直接结果。

## 总体架构

```text
                        互联网用户
                            │ HTTPS / SSE
                ┌───────────▼───────────┐
                │   L4/L7 负载均衡       │  必须关闭响应缓冲
                └─┬─────────┬─────────┬─┘
                  │         │         │
        ┌─────────▼──┐ ┌────▼─────┐ ┌─▼────────┐
        │ Gateway-1  │ │ Gateway-2│ │ Gateway-3│   副本对等，无状态
        └─────┬──────┘ └────┬─────┘ └─┬────────┘
              │             │         │
              │   每个 Agent 与每个副本各一条隧道
              └──────┬──────┴────┬────┘
                     │           │
              ┌──────▼───┐  ┌────▼─────┐
              │ Agent A  │  │ Agent B  │
              │ (NAT 后) │  │ (NAT 后) │
              └──────────┘  └──────────┘

        只读依赖（不在请求关键路径）：
        Registry ──► 节点身份、路由、配额定义、副本名册
        Redis    ──► 跨副本 token 配额扣减
```

单个副本内部：

```text
┌──────────────────────────────────────────────────────┐
│  Gateway 副本                                        │
│                                                      │
│  ┌──────────┐  ┌──────────┐  ┌────────────────────┐ │
│  │ protocol │─►│   auth   │─►│     scheduler      │ │
│  │ openai   │  │ apikey   │  │ 候选→过滤→评分→选点 │ │
│  └──────────┘  │ quota    │  └─────────┬──────────┘ │
│                │ ratelimit│            │            │
│                └──────────┘            ▼            │
│                             runtime.InferenceRuntime│
│                                 ▲           ▲       │
│                       ┌─────────┘           └─────┐ │
│                 DirectRuntime           RemoteRuntime│
│                 (openai client)         (tunnel slot)│
│                       │                       │     │
│  ┌────────────────────┼───────────────────────┼───┐ │
│  │ state cache ◄─健康检查┘        ◄──Control 流─┘   │ │
│  │  （本副本可达的节点视图）                        │ │
│  └──────────────────────────────────────────────┘ │
└────────┬─────────────────────────────────┬─────────┘
         │ HTTP                             │ gRPC mTLS
         ▼                                  ▼
   公网 GPU 服务器                     NAT 后 Mac Agent
   vLLM / SGLang / ComfyUI             Ollama / ComfyUI
```

请求全流程：

```text
LB 选中任意副本
  → 解析 OpenAI 协议，转换为 runtime.ChatRequest
  → 校验 API Key、租户、限流（本地）、配额（Redis）
  → 解析逻辑模型 → 候选部署列表
  → 过滤：本副本不可达、离线、维护、熔断、能力不匹配、租户无权
  → 评分：优先级 > 直连优先 > 最少在途 > 历史 TTFT > 权重
  → 取得 runtime.InferenceRuntime（Direct 或 Tunnel）
  → 调用 Chat / ChatStream
  → 首事件前失败 → 换下一个候选重试（最多 2 次）
  → 首事件后失败 → 以错误事件终止流，不重试
  → 逐事件 flush 给客户端
  → 记录用量、时延、状态
```

## 多副本拓扑

### 为什么是全连接而不是 Tunnel Hub

| 方案 | 请求路径 | 隧道连接数 | 额外延迟 | 实现成本 |
| --- | --- | --- | --- | --- |
| **全连接（选用）** | 用户 → 副本 → Agent | `节点数 × 副本数` | 0 | 低，Agent 侧多开几条连接即可 |
| Tunnel Hub | 用户 → 副本 → Hub → Agent | `节点数 × Hub 数` | **一跳内网 RTT** | 高，需要一致性哈希、Hub 集群、内转协议 |

全连接的唯一代价是连接数随副本数线性增长。按每个节点每副本 1 条 Control 流加 4 条常驻空闲槽估算，20 个节点 × 5 个副本 = 500 条 gRPC 流，单个副本承担 100 条——对 Go 的 gRPC 服务端而言完全不构成压力。

**切换到 Tunnel Hub 的触发条件**（任一满足即应重新评估）：

- Gateway 副本数超过 10，或节点数超过 200，使得空闲槽的常驻成本开始显著。
- Agent 侧出现因维持多条隧道导致的内存或 FD 压力（家用 Mac 的 FD 上限值得关注）。
- 需要跨地域部署，Agent 到远端副本的隧道成为长肥管道。

在触发之前引入 Hub 属于过早优化，且会用一跳转发换来无人需要的扩展性。

### 副本名册

Agent 需要知道有哪些副本要连。名册的权威来源是 Registry，下发路径有两条：

1. **启动时**：Agent 配置中的 `gateway_endpoints` 是**种子列表**，至少一个可达即可。
2. **运行时**：Agent 连上任意副本后，该副本通过 Control 流的 `GatewayRoster` 帧下发完整名册（含自身）。Agent 据此补齐缺失的连接、关闭已下线副本的连接。

名册变更（扩容、缩容、副本地址变化）由 Registry 推送给所有副本，各副本再通过各自的 Control 流广播给 Agent。名册中的副本带 `state` 字段：`active`（正常派活）、`draining`（不再派新请求，等在途结束）、`removed`（Agent 应关闭连接）。

**滚动升级由此变得平滑**：新副本上线 → 名册更新 → Agent 自动连上 → 旧副本标记 `draining` → LB 摘除 → 在途流跑完 → 旧副本退出。全程用户侧无不可用窗口。

### 节点视图分歧

Gateway-1 到 Agent A 的 Control 流断了，Gateway-2 的还连着——这时两个副本对"A 是否在线"的判断不同。

**这是正确行为，不需要修复。** 每个副本维护的是"**我能到达哪些节点**"，而不是"哪些节点客观在线"。Gateway-1 确实无法把请求送到 A，所以 A 就不该出现在它的候选列表里。强行做跨副本共识只会让 Gateway-1 选中一个自己够不着的节点。

推论：

- 节点在线状态**不写回 Registry**，它是副本本地的运行时视图。Registry 只保存节点的静态身份与配置。
- 控制台展示"节点是否在线"时，应聚合各副本的视图并展示为"3/5 个网关可达"，而不是一个可能误导人的布尔值。
- 如果某节点对**所有**副本都不可达，那才是真正的离线，由控制台的聚合逻辑判定并告警。

### 跨副本的共享状态

只有两类状态必须跨副本共享，其余一律副本本地：

| 状态 | 存储 | 一致性要求 | 理由 |
| --- | --- | --- | --- |
| Token 配额扣减 | Redis 原子操作 | 强 | 租户不能通过打到不同副本来超发 |
| bootstrap token 一次性校验 | Registry + PostgreSQL | 强 | 一次性凭证被用两次等于身份泄露 |
| 限流计数（QPS、并发） | 副本本地 | 弱 | 阈值按副本数均分，误差可接受，不值得为它付一次网络往返 |
| 节点在线视图 | 副本本地 | 无 | 见上一节，分歧是正确行为 |
| 部署健康与能力 | 副本本地 | 无 | 各副本从自己的 Control 流拿到同一份 Agent 上报，天然一致 |
| 熔断器状态 | 副本本地 | 无 | 熔断反映"本副本到该部署"的链路质量，本就该各自判断 |
| 会话亲和 | 无状态一致性哈希 | 无 | 见下 |

**限流阈值均分**：租户限额 300 QPS、5 个副本时，每副本本地限 60 QPS。代价是流量倾斜时可能提前限流；收益是限流判断始终是内存操作。副本数变化时通过名册同步调整分母。

**会话亲和不需要共享状态**：对亲和键做一致性哈希直接映射到候选部署，各副本用同样的算法和同样的候选列表，自然算出同样的结果。目标不可用时正常降级到评分选点。

**证书签发移交 Registry**：`Register`（bootstrap token 换节点证书）不再由 Gateway 提供。token 的一次性校验需要强一致存储，签发证书本就是控制面职责。Gateway 只做数据面，收到的每条隧道连接都已持有有效证书。这个调整同时消除了"CA 私钥要分发到每个 Gateway 副本"的安全问题。

## 统一转发接口

这是整个 Gateway 设计的支点。Direct 与 Tunnel 的差异被压缩在构造阶段：

```go
// 两者都实现 runtime.InferenceRuntime，调度器只持有接口。
type DirectRuntime = vllm.Runtime | sglang.Runtime | ollama.Runtime  // 概念示意
type RemoteRuntime struct { /* 持有 node_id + 本副本的槽池引用 */ }

// 调度器的签名里没有任何传输方式或副本拓扑的痕迹。
func (s *Scheduler) Pick(ctx context.Context, req runtime.ChatRequest) (runtime.InferenceRuntime, *Target, error)
```

| 维度 | Direct | Tunnel |
| --- | --- | --- |
| 构造 | `vllm.New(cfg, deps)` 指向节点地址 | `tunnel.NewRemote(nodeID, runtimeID, pool)` |
| 传输 | HTTP/1.1 或 HTTP/2 直连 | gRPC 双向流槽 |
| 健康 | Gateway 主动周期探测 | Control 流心跳 + 上报的 `Snapshot` |
| 适用 | 公网或内网可达的 GPU 服务器 | 家庭 Mac、办公网、NAT 后节点 |
| 额外延迟 | 0 | 约一个 RTT + 两次序列化 |
| 多副本 | 每个副本独立探测同一后端 | 每个副本独立持有到该节点的隧道 |
| 失败语义 | 连接错误、超时 | 同上，外加无空闲槽的背压错误 |

`RemoteRuntime` 的实现是薄的：把 `runtime` 方法调用编码成隧道帧，把返回帧解码回 `runtime` 类型。它不做能力判断（Agent 侧已做）、不做重试（调度器负责）、不做缓冲（逐帧转发），也不感知其他副本的存在。

**为什么不让 Gateway 直接反向代理 HTTP 到 Agent？** 那会要求 Agent 成为通用 HTTP 代理，与隧道方案"只传运行时语义"的安全约束冲突，并且能力门禁、限流和错误脱敏都要在 Gateway 重做一遍。当前设计让这些逻辑只在 `runtime` 层存在一份。

## 协议层

**实现文件：** `protocol/openai/`

外部协议只在这一层出现。首期实现：

| 端点 | canonical 映射 | 说明 |
| --- | --- | --- |
| `POST /v1/chat/completions` | `runtime.ChatRequest` | `stream=true` 时走 `ChatStream` |
| `POST /v1/embeddings` | `runtime.EmbeddingRequest` | — |
| `GET /v1/models` | 聚合本副本可达的健康部署对应的逻辑模型 | 去重后返回，不暴露节点信息 |
| `GET /healthz` | — | 存活探针，不检查后端 |
| `GET /readyz` | — | 就绪探针，要求至少一个健康部署 |

转换规则：

- 请求字段与 `runtime.ChatRequest` 一一对应；`Extra` 承载后端私有参数，与已建模字段冲突时返回 400 而不是静默覆盖。
- 客户端请求的能力（`tools`、`response_format`）在**调度前**参与候选过滤，而不是选完节点才发现不支持。
- 响应转换回 OpenAI 格式时保留 `usage`；后端不返回 usage 时字段置空而非编造。
- 错误映射：`runtime.ErrorCode` → HTTP 状态码 → OpenAI 错误体。`ErrorBackpressure` 与无可用节点均映射为 503 并带 `Retry-After`。

**`GET /v1/models` 在多副本下可能返回略有差异的列表**（各副本可达的节点不同）。这是可接受的：模型列表本就是"当前可用"的快照。若要求绝对一致，可选择只返回路由表中定义的逻辑模型而不过滤可用性，但那会让客户端看到实际不可用的模型——首期选择前者，如实反映。

### SSE 输出

```text
Content-Type: text/event-stream
Cache-Control: no-cache, no-transform
Connection: keep-alive
X-Accel-Buffering: no          ← 让 Nginx 一类代理放弃缓冲
```

- 每个 `ChatEvent` 写入后立即 `Flusher.Flush()`，不攒批。
- 正常结束发 `data: [DONE]`。
- 流中途出错：已发出内容的流以一个错误事件收尾并关闭，**不改写已发送内容**，也不重试。
- 客户端断开时 `Request.Context()` 被取消，取消信号必须传到隧道和后端，释放槽位与显存。
- 长时间无事件时按 `stream_idle_timeout` 终止，避免僵尸连接堆积。
- **LB 必须配置会话保持或直接透传**：SSE 是长连接，一旦建立就固定在某个副本上，中途切换副本等于断流。L4 LB 天然满足；L7 LB 需确认不会在副本健康检查抖动时主动迁移已建立的连接。

## 调度器

**实现文件：** `scheduler/`

### 候选与过滤

```text
逻辑模型 "qwen-coder"
  → Route 查表得到候选 Target 列表
  → 过滤 0：本副本不可达（Control 流未建立或已断开）
  → 过滤 1：节点离线
  → 过滤 2：部署状态非 healthy
  → 过滤 3：熔断器打开（本副本视角）
  → 过滤 4：能力不匹配（CapabilitySet.Resolve 非 supported）
  → 过滤 5：租户无权访问该节点或模型
  → 过滤 6：节点处于维护或排空状态
```

过滤 0 是多副本引入的新增项，也是"视图自持"原则的落点：本副本够不着的节点根本不进入候选。

能力过滤直接调用 `runtime.CapabilitySet.Resolve`。**`unknown` 视为不满足**：请求需要 `tools` 而部署的该项能力是 `unknown` 时，该部署不进入候选，而不是赌一把发过去。这与 `runtime` 的能力模型一致——未知不等于支持。

### 评分

首期按固定优先级排序，不引入可调权重公式：

1. **节点优先级**（管理员配置，用于把流量钉在特定节点）
2. **传输方式**：Direct 优于 Tunnel（少一个 RTT）
3. **最少在途请求数**（本副本视角，见下）
4. **历史 P50 TTFT**（本副本滑动窗口，冷启动时视为中位数）
5. **配置权重**（同分时的加权轮询）

**"最少在途"在多副本下只有本副本视角。** 副本 1 看到自己有 3 个请求在打 node-A，但不知道副本 2 也有 5 个。这会导致轻微的负载不均。首期接受，理由是：Agent 侧的 `runtime.limiter` 是真实的并发上限，超了会返回 `ErrorBackpressure`，调度器换节点即可——用一个明确的快速失败代替一套跨副本的在途计数同步，是更划算的交易。

### 重试与熔断

- 只有**未收到首个事件**的请求可以换节点重试，最多 2 次，且每次换不同节点。
- `Stream.Committed()` 为 true 后一律不重试——这是 `runtime` 层已实现的语义，Gateway 直接使用。
- 非流式请求在收到响应头前可重试；收到响应头后不可。
- 熔断：单个部署连续 5 次失败进入 open（30s），随后 half-open 放行 1 个请求探测，成功 2 次恢复。熔断器是**副本本地**的，因为它反映的是"本副本到该部署"的链路质量。
- 背压错误（`ErrorBackpressure`）**不计入熔断**——它表示节点健康但满载，应该立即换节点而不是把节点判死。多副本下这条尤其重要：多个副本同时向一个节点派活，触碰上限是常态而非故障。

## 认证、配额与限流

**实现文件：** `auth/`

- API Key 只存不可逆哈希；每个副本本地缓存哈希到租户的映射，TTL 60s，Registry 变更时主动失效。
- 鉴权在请求路径上必须是内存操作，禁止每请求查库。
- **限流（QPS、并发）走副本本地**，阈值按当前副本数均分。副本数从名册获取，变化时重新计算分母。超限返回 429 并带 `Retry-After`。
- **配额（token 计量）走 Redis 原子扣减**，异步执行，不阻塞请求路径。超额时下一个请求被拒绝，不中断在途请求。
- **Redis 不可用时降级为本地计数**并告警：宁可短暂放宽配额，也不能让配额存储故障导致整个平台不可用。降级窗口内的用量仍记录，恢复后补扣。
- 请求体大小、消息数、上下文长度均有上限，超限返回 400 而不是转发给后端。
- 匿名访问默认关闭。

## 状态缓存

**实现文件：** `state/`

每个副本内存中维护一份**本副本可达范围内**的节点与部署快照：

| 数据 | 来源 | 刷新方式 | 副本间 |
| --- | --- | --- | --- |
| 节点可达性 | 本副本的 Tunnel Control 流 | 事件驱动，心跳超时立即标记 | 可能不同，属正常 |
| 部署健康与能力 | Agent 上报的 `runtime.Snapshot` | 变更触发 + 60s 全量对账 | 天然一致 |
| Direct 后端健康 | 本副本主动探测 | 10s 周期，复用 `runtime.Manager` | 可能不同，属正常 |
| 路由与权重 | Registry | 30s 拉取或事件推送 | 最终一致 |
| API Key 与配额定义 | Registry | 60s TTL 缓存 | 最终一致 |
| 副本名册 | Registry | 事件推送 | 最终一致 |

快照是不可变的：刷新时构造新快照原子替换，请求路径只读。这保证调度决策不会看到半更新的状态，也不需要在请求路径上加锁。

**Registry 不可用时，Gateway 继续用最后一份缓存服务**，并停止接受配置变更、输出告警。控制面故障不应导致数据面停摆——这是控制面数据面分离的核心收益，必须在测试中显式验证。

## Tunnel 服务端

**实现文件：** `tunnel/server.go`、`tunnel/pool.go`、`tunnel/remote.go`

每个 Gateway 副本独立实现 `service/aiServeWeaveAgent/tunnel/README.md` 定义的 `Tunnel` gRPC 服务：

- `Control`：维护每个节点一条控制流；校验证书 SAN 的 `node_id` 与 `Hello.node_id` 一致；接收心跳与 `RuntimeStatus`，下发 `RuntimeConfig` 和 `GatewayRoster`。
- `Serve`：接收 Agent 打开的数据槽，按 `SlotClass` 放入该节点在**本副本**的空闲队列。

`Register` **不在 Gateway 上**：节点证书签发由 Registry 提供，理由见「跨副本的共享状态」。Gateway 收到的每条连接都必须已持有有效证书，否则 TLS 握手阶段即被拒绝。

槽池的 Gateway 侧规则：

- 取槽是**非阻塞**的：无空闲槽立即返回 `ErrorBackpressure`，由调度器换节点。
- 只有收到 `ResponseEnd` 后才把槽放回空闲队列。
- Control 流断开时，该节点在本副本的全部槽立即作废，在途请求以 `ErrorConnection` 结束；**其他副本不受影响**。
- 每帧携带 `request_id`；取消请求时发 `Cancel` 帧，Agent 侧按 `request_id` 匹配后才执行。
- 副本进入 `draining` 时，通过 Control 流通知 Agent，Agent 停止在该副本补充新槽，现有槽服务完在途请求后关闭。

## 面向互联网的部署

这一节是"稳定"落地的地方，配置错一项就会让前面所有低延迟设计失效。

### 负载均衡

| 层 | 推荐 | 说明 |
| --- | --- | --- |
| 用户入口 | L4（TCP）或 L7 直通 | SSE 长连接必须固定在一个副本上，中途迁移等于断流 |
| 健康检查 | `GET /readyz`，间隔 5s，2 次失败摘除 | `readyz` 要求至少一个健康部署，避免把请求送到无后端的副本 |
| 连接排空 | 摘除后至少保留 60s | 等待在途 SSE 流自然结束 |
| 算法 | 最少连接数优于轮询 | SSE 连接时长差异极大，轮询会造成显著倾斜 |

**不要用会话粘滞（sticky session）**：Gateway 无状态，任何副本都能处理任何新请求。粘滞只会在副本故障时把一批用户一起拖住。已建立的 SSE 连接固定在原副本是 TCP 连接的自然属性，不需要额外配置。

### 前置代理

如果 Gateway 前面有 Nginx、Envoy、ALB 或 CDN：

- **必须关闭响应缓冲。** Nginx 需要 `proxy_buffering off`；Gateway 已发送 `X-Accel-Buffering: no` 作为双保险。缓冲一旦开启，SSE 会变成"憋到结束一次性吐完"，TTFT 从毫秒级劣化到整个生成时长。
- **必须关闭响应压缩**（或至少对 `text/event-stream` 关闭）。压缩器需要攒够数据才出块。
- **读超时必须大于最长生成时间**，建议 30min；默认的 60s 会在长回答中途断开。
- **CDN 不要缓存 `/v1/*`。** 推理响应不可缓存，且缓存层通常自带缓冲。
- HTTP/2 到 Gateway 可选；SSE 在 HTTP/1.1 上工作良好，不必为此引入复杂度。

### TLS 与端口

| 端口 | 协议 | 面向 | 说明 |
| --- | --- | --- | --- |
| 443 | HTTPS | 互联网用户 | 公网 CA 证书，TLS 1.2+，在 LB 或副本上终止 |
| 8443 | gRPC mTLS | Agent 隧道 | 私有 CA，双向证书校验，**必须直连副本不经 L7 代理** |
| 9090 | HTTP | 内网 | 指标与探针，不暴露公网 |

用户入口与隧道入口**必须使用不同的证书体系**：用户侧是公网 CA，隧道侧是平台自签 CA。混用会导致任何持有公网证书的客户端都能尝试连接隧道端点。

**隧道端口的多副本暴露方式**：Agent 需要分别连到每个副本，因此 8443 不能藏在一个统一 VIP 后面（那样 Agent 无法控制连到哪个副本）。每个副本需要一个可从 Agent 侧解析和直连的稳定地址——Kubernetes 下用 headless Service 加 StatefulSet 或每副本一个 Service，裸机部署下用各自的域名。副本名册中下发的就是这些地址。

### 超时矩阵

| 环节 | 默认 | 说明 |
| --- | --- | --- |
| 读请求头 | 10s | 防慢速攻击 |
| 读请求体 | 60s | 大 payload 场景需放宽 |
| 非流式整体 | 5m | 与 `runtime.RequestTimeout` 对齐 |
| 流式空闲 | 60s | 两个事件之间的最大间隔 |
| 流式整体 | 30m | 上限保护 |
| 优雅停机 | 60s | 停止接受新请求，等在途流结束 |
| LB 摘除到进程退出 | ≥ 90s | 必须大于优雅停机时长 |

### 滚动升级与优雅停机

多副本的主要收益就在这里——升级过程对用户完全透明：

```text
1. 新副本启动 → readyz 通过 → Registry 名册加入
2. 各副本广播新名册 → Agent 自动建立到新副本的隧道
3. LB 把新副本加入后端池
4. 旧副本标记 draining：
   - readyz 转为失败 → LB 在 2 次检查内摘除
   - Control 流下发 draining 状态 → Agent 停止在该副本补槽
   - 拒绝新请求（503），在途 SSE 流继续跑完
5. 在途流结束或 60s 超时 → 关闭 gRPC 与 HTTP 服务 → 进程退出
```

**副本数至少为 2**，否则滚动升级退化为单副本的不可用窗口。建议 3 个起步，可容忍一个副本故障加一个副本升级同时发生。

## 可观测性

```text
gateway_replica_info{replica_id,version}           副本标识，所有指标可按此聚合或下钻
gateway_requests_total{tenant,model,protocol,result}
gateway_request_duration_seconds{model,operation}
gateway_ttft_seconds{model,transport}              transport: direct|tunnel
gateway_tokens_total{tenant,model,direction}
gateway_tokens_per_second{model}
gateway_active_streams{model}
gateway_schedule_result_total{model,reason}        reason: ok|unreachable|no_candidate|all_unhealthy|capability|quota
gateway_reachable_nodes{}                          本副本可达的节点数
gateway_roster_size{}                              本副本已知的副本数
gateway_retry_total{model,reason}
gateway_circuit_state{deployment}
gateway_upstream_errors_total{deployment,code,status}
gateway_auth_failures_total{reason}
gateway_ratelimit_rejections_total{tenant,dimension}
gateway_quota_backend_errors_total{}               Redis 故障降级次数
```

多副本下的排查要点：

- 所有指标必须带 `replica_id`，否则聚合视图会掩盖单副本故障（例如只有 Gateway-2 到某节点的隧道断了）。
- `gateway_reachable_nodes` 在副本间出现持续差异，说明某副本的网络或隧道有问题，是"视图分歧"从正常变为故障的判据。
- `gateway_ttft_seconds` 按 `transport` 分标签，与隧道侧的 `tunnel_stream_first_event_seconds` 相减，即可判断延迟来自隧道还是模型本身。
- `gateway_schedule_result_total` 的 `reason` 是排查"为什么用户拿到 503"的第一现场；`unreachable` 与 `no_candidate` 分开统计，前者指向隧道问题，后者指向容量问题。

每个请求生成 `request_id`，贯穿 Gateway 日志、隧道帧、Agent 日志和后端请求头，并在错误响应中返回给用户。日志中必须包含 `replica_id`，否则用户报障时无法定位是哪个副本处理的。

## 实施计划

```text
阶段 1 (HTTP 骨架 + 协议层)
   │
   ├──► 阶段 2 (鉴权 + 本地限流 + Redis 配额)
   │
   ├──► 阶段 3 (状态缓存 + 调度器)
   │        │
   │        ├──► 阶段 4 (Direct 转发)
   │        │
   │        └──► 阶段 5 (Tunnel 服务端 + RemoteRuntime)
   │                     │
   │                     ▼
   │              阶段 6 (副本名册 + 多副本协同)
   │                     │
   └──────────────► 阶段 7 (重试熔断 + 用量指标)
                         │
                         ▼
                  阶段 8 (多副本部署与端到端验证)
```

阶段 4 依赖 `runtime` 的 vLLM/Ollama 适配器；阶段 5 依赖 Tunnel 的阶段 1 到 4。两者可并行，**建议先做阶段 5**——Tunnel + Ollama 是 MVP 要验证的关键链路。阶段 6 是多副本的核心，必须在阶段 5 之后。

### 阶段 1：HTTP 骨架与 OpenAI 协议层

**文件：** 创建 `main.go` 装配、`server/`、`protocol/openai/` 及测试。

- [ ] 写协议转换表驱动测试：OpenAI 请求 ⇄ `runtime.ChatRequest` 往返，可选字段 nil 不出现在 JSON、显式 0 值保留、`Extra` 冲突返回 400。
- [ ] 写错误映射测试：每个 `runtime.ErrorCode` 对应的 HTTP 状态码与 OpenAI 错误体形状。
- [ ] 写 SSE 输出测试：每事件 flush、`[DONE]` 收尾、中途错误以错误事件终止、客户端断开触发 context 取消。
- [ ] 运行 `go test ./service/aiServeWeaveGateway/...`，确认失败。
- [ ] 实现 HTTP 服务、三个端点、探针和优雅停机；所有日志带 `replica_id`。
- [ ] 运行 `gofmt -l`、`go vet`、`go test -race`。

**验收：** 用 fake `InferenceRuntime` 即可跑通非流式与流式 Chat；协议层不含任何调度或传输逻辑。

### 阶段 2：鉴权、限流与跨副本配额

**文件：** 创建 `auth/`、`quota/` 及测试。

- [ ] 写 API Key 测试：哈希校验、缓存命中与失效、无效 Key 返回 401 且不泄露 Key 是否存在。
- [ ] 写本地限流测试：租户 QPS、租户并发、单 Key 并发三个维度分别触发；阈值按副本数均分，副本数变化时分母正确更新。
- [ ] 写 Redis 配额测试（用 miniredis 或 fake）：原子扣减、并发扣减无超发、异步执行不阻塞请求路径。
- [ ] 写 Redis 降级测试：Redis 不可用时降级为本地计数、输出告警、恢复后补扣，且降级期间平台可用。
- [ ] 写请求上限测试：请求体、消息数、上下文长度超限返回 400 且不转发给后端。
- [ ] 写脱敏测试：日志中不出现 Authorization、API Key 和 Prompt。
- [ ] 运行包测试，确认失败。
- [ ] 实现鉴权中间件、本地限流器与 Redis 配额客户端。
- [ ] 运行质量门禁全部命令。

**验收：** 鉴权与限流在请求路径上是纯内存操作；配额扣减跨副本无超发；Redis 故障不导致平台不可用。

### 阶段 3：状态缓存与调度器

**文件：** 创建 `state/`、`scheduler/` 及测试。

- [ ] 写快照测试：原子替换、请求路径只读、刷新过程中读取不会看到半更新状态。
- [ ] 写过滤测试：七类过滤各自生效（含新增的"本副本不可达"），能力 `unknown` 被视为不满足。
- [ ] 写评分测试：五级排序的每一级都能在前面同分时决定结果；Direct 优于 Tunnel。
- [ ] 写会话亲和测试：一致性哈希在相同候选列表下结果确定；目标不可用时正常降级。
- [ ] 写 Registry 降级测试：Registry 不可用时继续用最后一份缓存服务并告警。
- [ ] 写"无候选"测试：返回 503 且 `gateway_schedule_result_total` 区分 `unreachable` 与 `no_candidate`。
- [ ] 运行包测试，确认失败。
- [ ] 实现状态缓存与调度器。
- [ ] 运行 `go test -race`。

**验收：** 调度器代码中不出现任何传输方式或副本拓扑判断；决策全部基于只读快照。

### 阶段 4：Direct 转发

**文件：** 创建 `transport/direct.go` 及测试。

- [ ] 写构造测试：按部署配置创建对应 `runtime` 适配器并纳入 `runtime.Manager` 做周期健康检查。
- [ ] 写转发测试：非流式与流式各自透传成功、错误、取消三条路径。
- [ ] 写健康联动测试：适配器转为 unhealthy 后该部署从候选中消失。
- [ ] 运行包测试，确认失败。
- [ ] 实现 Direct 传输构造与生命周期管理。
- [ ] 运行 `go test -race`。

**验收：** Direct 路径除构造外没有专属代码；调度器拿到的是与 Tunnel 完全一致的接口。

### 阶段 5：Tunnel 服务端与 RemoteRuntime

**文件：** 创建 `tunnel/server.go`、`tunnel/pool.go`、`tunnel/remote.go` 及测试。

- [ ] 写 `Control` 测试：证书 SAN 与 `Hello.node_id` 不一致断流、心跳超时标记不可达、`RuntimeStatus` 更新状态缓存。
- [ ] 写槽池测试：非阻塞取槽、无空闲返回 `ErrorBackpressure`、`ResponseEnd` 后才归还、Control 断开时本副本全部槽作废。
- [ ] 写 `RemoteRuntime` 测试：九个 Operation 的编解码、流式逐帧转发、取消发出 `Cancel` 帧、错误码无损还原。
- [ ] 写接口一致性测试：同一组用例分别跑在 `DirectRuntime` 和 `RemoteRuntime` 上，断言语义一致。
- [ ] 运行包测试，确认失败。
- [ ] 实现 Tunnel 服务端与 RemoteRuntime。
- [ ] 运行 `go test -race`，确认无协程与连接泄漏。

**验收：** 用 fake Agent 可完成全部九个 Operation；`RemoteRuntime` 与 `DirectRuntime` 通过同一份契约测试。

### 阶段 6：副本名册与多副本协同

**文件：** 创建 `roster/`、`tunnel/roster.go` 及测试；修改 `state/`、`auth/`。

- [ ] 写名册下发测试：Agent 连上任一副本后收到完整名册（含自身）；名册变更触发广播。
- [ ] 写副本状态测试：`active`/`draining`/`removed` 三态各自的 Agent 侧预期行为。
- [ ] 写视图分歧测试：两个 fake 副本对同一节点的可达性不同时，各自的候选列表正确且互不影响。
- [ ] 写限流分母测试：副本数变化时本地阈值同步调整，扩缩容期间不出现阈值真空。
- [ ] 写 draining 测试：标记后 `readyz` 失败、拒绝新请求、Control 流下发 draining、在途流跑完才退出。
- [ ] 运行包测试，确认失败。
- [ ] 实现名册管理、广播与 draining 流程。
- [ ] 用两个进程内副本 + 一个 fake Agent 做集成测试：验证 Agent 同时连上两个副本、任一副本可独立服务请求。
- [ ] 运行 `go test -race`。

**验收：** 副本扩缩容全程无需重启 Agent；单副本故障不影响其他副本对同一节点的调度。

### 阶段 7：重试、熔断与用量

**文件：** 创建 `resilience/`、`usage/`、`metrics/` 及测试。

- [ ] 写重试测试：首事件前失败换节点、`Committed` 后不重试、最多 2 次且不重复同一节点。
- [ ] 写熔断测试：连续失败打开、半开探测、恢复；背压错误不计入熔断；熔断器为副本本地。
- [ ] 写用量测试：token 统计准确、后端不返回 usage 时字段置空而非编造、异步扣减不阻塞请求路径。
- [ ] 接入本文件「可观测性」列出的全部指标，写标签基数测试，断言所有指标带 `replica_id`。
- [ ] 运行包测试，确认失败。
- [ ] 实现重试策略、熔断器、用量记录与指标。
- [ ] 运行质量门禁全部命令。

**验收：** 重试边界在测试中被显式证明；`request_id` 与 `replica_id` 贯穿全链路可追溯。

### 阶段 8：多副本部署与端到端验证

**文件：** 创建 `deploy/docker-compose.yaml` 内容、`deploy/nginx.conf.example`、`deploy/kubernetes/`、`configs/gateway.yaml.example`；修改本 README 的实测数据。

- [ ] 补全 `docker-compose.yaml`：3 个 Gateway 副本、LB、Registry、PostgreSQL、Redis、Prometheus，含健康检查与依赖顺序。
- [ ] 提供 LB 配置样例：L4 直通、`readyz` 健康检查、连接排空 ≥ 60s、最少连接数算法。
- [ ] 提供前置代理配置样例，显式关闭缓冲与压缩，并写一条验证命令。
- [ ] 提供隧道端口的多副本暴露样例（每副本独立可解析地址）。
- [ ] 端到端验证主链路：`OpenAI SDK → LB → 任一副本 → Tunnel → NAT 后 Mac → Ollama → SSE 流式返回`。
- [ ] 验证 Agent 同时连上 3 个副本，且每个副本都能独立完成完整推理。
- [ ] 实测并记录 TTFT：Direct 与 Tunnel 各一组，与后端直连的基线对比，确认多副本未引入额外延迟。
- [ ] **滚动升级演练**：逐个替换 3 个副本，全程用一个持续的 SSE 流和一批短请求观测，确认零错误、零断流。
- [ ] 故障演练：kill 一个副本、kill Agent、拔网线、后端假死、Redis 宕机、Registry 宕机，六场景记录用户侧表现与恢复时间。
- [ ] 压测：验证限流、配额、背压换节点在高并发多副本下行为正确，无 goroutine 泄漏，跨副本配额无超发。
- [ ] 把实测数据填入本 README，偏差超预期的项说明原因。

**验收：** MVP 链路在多副本下完整跑通并有数据；滚动升级零不可用窗口经实测确认；部署文档可被第三方独立复现。

## 首期完成标准

- OpenAI SDK 无需改代码即可对接，非流式与 SSE 流式均正常。
- 3 个副本同时服务，任一副本可独立完成任意在线节点的推理；杀掉一个副本不影响其余副本。
- Agent 自动发现并连接全部副本；副本扩缩容无需重启 Agent。
- 滚动升级全程零不可用窗口，已建立的 SSE 流不被中断。
- 同一份契约测试同时通过 `DirectRuntime` 和 `RemoteRuntime`，调度器代码无传输方式或副本拓扑分支。
- 跨副本配额无超发；Redis 故障时降级可用并告警。
- Registry 故障时 Gateway 继续用缓存服务，控制面故障不导致数据面停摆。
- 能力 `unknown` 的部署不会被选中；参数不被静默丢弃。
- 首事件前可换节点重试，首事件后绝不重试，且有测试证明。
- 背压不触发熔断，节点满载时正确换点。
- 前置代理与 LB 配置样例经实测确认不引入 SSE 缓冲、不中途迁移连接。
- TTFT 有 Direct 与 Tunnel 两组实测数据，与直连基线的差值可解释。
- 六场景故障演练的用户侧表现与恢复时间均已记录。
- 所有指标与日志带 `replica_id`；不存在 API Key、Authorization、Prompt 和生成内容。
- `gofmt -l` 无输出，`go vet` 无告警，`go test -race ./service/aiServeWeaveGateway/...` 通过。

## 风险与待决问题

| 风险 | 影响 | 缓解 | 状态 |
| --- | --- | --- | --- |
| 前置代理开启缓冲或压缩 | SSE 失去流式特性，TTFT 劣化到整个生成时长 | 双保险（响应头 + 部署样例）+ 阶段 8 实测验证 | 需部署配合 |
| L7 LB 在健康检查抖动时迁移已建立连接 | 用户 SSE 流被中途切断 | 推荐 L4 直通；用 L7 时必须验证连接不迁移 | 需部署配合 |
| 隧道连接数随副本数线性增长 | Agent 侧 FD 与内存压力 | 副本数 ≤ 10 时可忽略；超过则评估 Tunnel Hub | 已接受 |
| "最少在途"只有本副本视角 | 多副本同时派活导致节点负载不均 | Agent 侧 limiter 兜底返回背压，调度器换节点 | 已接受 |
| 限流阈值均分在流量倾斜时提前触发 | 部分租户提前被限流 | 首期接受；必要时改为 Redis 集中限流 | 已接受 |
| 节点视图在副本间不一致 | 控制台展示困惑 | 明确为正确行为；控制台聚合展示"N/M 网关可达" | 已缓解 |
| Redis 成为新的故障点 | 配额不可用 | 降级为本地计数并告警，不阻断请求 | 已缓解 |
| 隧道端口需每副本独立可达 | 部署复杂度上升，云环境需额外配置 | 提供 K8s 与裸机两套暴露样例 | 需部署配合 |
| 家庭节点带宽与稳定性远低于机房 | 长回答中途断流 | Direct 优先于 Tunnel 的评分策略 + 熔断摘除 | 已缓解 |
| 用量异步扣减存在超发窗口 | 租户可短暂超配额 | 首期接受；窗口内超发量受并发上限约束 | 已接受 |
| 逻辑模型指向能力不一致的部署 | 同一模型名的行为随节点漂移 | 路由配置校验时比对候选的能力交集并告警 | 待定 |

待决问题：

1. `runtime` 包位于 `service/aiServeWeaveAgent/runtime`，Gateway 复用它会让依赖方向看起来是 Gateway 依赖 Agent。建议把公共契约下沉到共享路径；MVP 可先直接 import，但需在阶段 5 前决定。
2. **Registry 成为多副本的前置依赖**（节点证书签发、bootstrap token 校验、副本名册）。需确认 Registry 的实现排期能否早于 Gateway 阶段 6，否则阶段 6 需要一个临时的名册来源（如静态配置加人工维护）。
3. 副本名册的推送机制：Registry 主动推送还是 Gateway 轮询。推送更及时但需要 Registry 侧维护长连接，轮询更简单但扩缩容的感知有延迟。建议首期轮询 30s，阶段 6 前确认是否够用。
4. 用量记录的落库方式：首期只写请求摘要到 PostgreSQL，还是直接接入 ClickHouse。多副本下写入并发上升，影响阶段 7 的存储层设计。
5. ComfyUI Job API 是否进入首期。当前范围排除，但隧道协议已预留四个工作流 Operation。**多副本下它有额外约束**：Job 的进度订阅必须落在提交它的那个副本上，或者 Job 状态需要持久化到共享存储——这是首期排除它的又一个理由。

## 后续演进

1. **独立 Tunnel Hub。** 副本数超过 10 或节点数超过 200 时，把隧道终结从 Gateway 剥离到 Hub 集群，Gateway 通过一致性哈希 `node_id` 找到持有隧道的 Hub 实例。代价是每个请求多一跳内网转发。
2. **跨地域多活。** 按地域部署 Gateway 集群，用户就近接入，节点按 `region` 标签调度。需要解决跨地域的配额一致性。
3. **ComfyUI Job API 与产物存储。** 异步任务、SSE 进度、S3 兼容对象存储、签名下载 URL；需先解决 Job 与副本的绑定关系。
4. **更多协议。** Anthropic Messages、Responses、音频、rerank、OpenAI 兼容图像生成映射到 ComfyUI 模板。
5. **请求排队与准入控制。** 用可控排队替代一律快速失败，提高高负载下的资源利用率；多副本下需要共享队列或按副本独立排队。
6. **资源感知调度。** 接入 GPU 显存、队列长度、实测吞吐做更精细的评分；考虑跨副本的在途计数同步。
7. **多级缓存。** Prompt 前缀缓存亲和、Embedding 结果缓存。

这些能力不得提前塞进首期接口；扩展时优先新增窄接口，避免改动 `runtime.InferenceRuntime`。

# Agent Tunnel 接入规划

**目标：** 让部署在家庭 Mac、办公网和 NAT 后的节点，通过主动发起的 mTLS gRPC 长连接，把本地 vLLM、SGLang、Ollama、ComfyUI 的推理与工作流能力安全地交付给**多副本部署的远程 Gateway**，并为互联网用户维持低首字延迟和可预期的稳定性。

**架构：** Agent 是 gRPC 客户端，Gateway 是 gRPC 服务端。**Agent 与每个 Gateway 副本各建立一条独立隧道**，因此任意副本都能直达本节点，请求路径上不存在副本间转发。每条隧道内部：控制面走一条长期存在的双向流（心跳、状态上报、配置下发、副本名册）；数据面走一个由 Agent 预先打开并保持待命的**双向流槽池**，每条流槽同一时刻只服务一个请求，请求结束后归还槽位继续待命。隧道传输的是 `runtime` 包的**运行时语义**（Chat/Embed/Submit/Subscribe/Artifact），不是任意 HTTP 报文。

节点证书的签发由 **Registry** 负责，不在 Gateway 上——bootstrap token 的一次性校验需要强一致存储，且多副本下不应把 CA 私钥分发到每个数据面副本。

**技术栈：** Go 1.26、`google.golang.org/grpc`、`google.golang.org/protobuf`、`crypto/tls`、`context`、`log/slog`，复用已落地的 `service/aiServeWeaveAgent/runtime` 包。

---

## 当前状态

隧道尚未开始实现，本目录只有本文件。上游依赖 `runtime` 包的进度：

| 依赖 | 状态 | 对隧道的影响 |
| --- | --- | --- |
| `runtime` 公共契约（接口、类型、能力、错误、流） | 已实现 | 帧 payload 直接镜像这些类型，可立即定义 proto |
| `runtime.Manager`（健康状态机、Snapshot） | 已实现 | 控制流的状态上报可直接消费 `Snapshot()` |
| `runtime/openai` 共享客户端 | 已实现 | — |
| `vllm`、`sglang`、`ollama`、`comfyui` 适配器 | 占位符 | 阶段 5 的端到端联调必须等 Ollama 适配器（runtime 阶段 4）先落地 |
| Gateway 侧隧道服务端 | 未开始 | 见 `service/aiServeWeaveGateway/README.md`，两侧共用同一份 proto |
| Registry 服务 | 未开始 | **节点证书签发的唯一来源**；阶段 2 依赖它，需确认排期 |

因此隧道的阶段 1、3、4（协议、连接、槽池）**不依赖任何适配器**，可与 runtime 阶段 4 到 7 并行推进；阶段 2（身份）依赖 Registry 的证书签发接口先定义。

## 全局约束

- Agent 只主动出站建连，从不监听公网端口；不要求用户配置端口映射、DDNS 或 UPnP。
- 隧道传输运行时语义，不传输任意 URL、Host 或 Authorization；Agent 永远不会成为通用 HTTP 代理。
- `runtime_id` 必须命中 Agent 本地配置中的白名单，否则拒绝执行——即使 Gateway 被攻破，也无法让 Agent 访问未声明的地址。
- 隧道层不做能力判断、不做模型路由、不做重试；这些分别属于 `runtime` 的能力门禁和 Gateway 的调度器。
- 任何一跳都不得无界缓冲：流式事件逐条转发，产物边读边送，背压通过 gRPC 流控自然向上游传导。
- API Key、自定义鉴权头、完整 Prompt 和工作流 JSON 不得写入日志或错误文本，复用 `runtime.Redact`。
- 默认测试不依赖真实 Gateway、GPU 或外部网络。

## 范围

### 首期包含

- 从 Registry 用一次性 bootstrap token 换取节点证书，之后全程 mTLS。
- **多副本连接**：与每个 Gateway 副本各建一条隧道；名册驱动的自动建连与断连。
- 控制流：心跳、`runtime.Snapshot` 上报、Runtime 配置下发、副本名册接收、优雅下线。
- 数据流槽池：每副本独立槽池、按水位补充、空闲回收、跨副本额度分配。
- 请求分发：Chat、ChatStream、Embed、ListModels、Submit、Subscribe、Status、Cancel、OpenArtifact。
- 帧级取消、超时和 `request_id` 全链路透传。
- 指数退避重连、在途请求的确定性终止、单副本故障隔离。
- 隧道与 Direct 模式在 Gateway 侧共用同一个 `runtime.InferenceRuntime` / `WorkflowRuntime` 接口。

### 首期不包含

- 独立 Tunnel Hub（副本数超过 10 或节点数超过 200 时才评估，见文末）。
- 跨副本的精确并发协调（依靠本地 limiter 兜底，见「多副本额度分配」）。
- 隧道上的通用 HTTP/WebSocket 透传。
- Agent 自动升级、远程 Shell、文件系统访问等任何非推理能力。
- 断线后的请求续传（在途请求一律以明确错误结束，由 Gateway 决定是否换节点）。
- 隧道流量计费与限速（Gateway 侧的配额层负责）。

## 设计原则

1. **出站建连，槽位待命。** Agent 主动打开若干条双向流并停在 `Ready` 状态，Gateway 派活时无需新建流，省掉一个 RTT。
2. **一槽一请求。** 每条 gRPC 流同一时刻只服务一个请求，从而直接继承 HTTP/2 的 per-stream 流控，不需要自研 `stream_id` 多路复用和窗口算法，也不会让一次大产物下载阻塞另一个请求的 token 流。
3. **传运行时语义，不传 HTTP。** 帧的 payload 是 `runtime.ChatRequest` 等类型的 proto 镜像，Agent 侧直接调用 `rt.ChatStream(ctx, req)`，能力门禁、并发限流、错误脱敏全部复用已实现的 `runtime` 层。
4. **两侧同构。** Gateway 侧的隧道客户端实现与 Agent 本地适配器**完全相同的接口**，调度器因此不感知 Direct 与 Tunnel 的差别。
5. **快速失败优于排队。** 无空闲槽时立即返回背压错误让调度器换节点，不在隧道层排队。
6. **状态只有一个来源。** 节点是否可用由控制流心跳决定，Runtime 是否健康由 `runtime.Manager` 决定，隧道层不另建一套健康判断。
7. **副本之间完全隔离。** 到某个副本的隧道故障，不影响到其他副本的隧道；`runtime.Manager` 和本地 limiter 是所有副本共享的下游，隧道层之上没有任何跨副本协调。

## 总体架构

```text
        NAT / 防火墙                              公网
            │
┌───────────┼─────────────────┐      ┌──────────────────────────┐
│  Agent    │                 │      │  Gateway-1               │
│           │  ┌── Control ───┼─────►│   TunnelService          │
│  ┌────────┴┐ ├── Slot ×N ───┼─────►│    └── 本副本槽池          │
│  │ 连接管理 │ │              │      └──────────┬───────────────┘
│  │ (名册驱动)│ │              │                 │
│  └────────┬┘ ├── Control ───┼─────►┌──────────▼───────────────┐
│           │  ├── Slot ×N ───┼─────►│  Gateway-2               │
│           │  │              │      │   TunnelService          │
│           │  ├── Control ───┼─────►└──────────┬───────────────┘
│           │  └── Slot ×N ───┼─────►┌──────────▼───────────────┐
│           │                 │      │  Gateway-3               │
│           │                 │      └──────────┬───────────────┘
│           ▼                 │                 │
│  runtime.Manager            │            LB ──┴── 互联网用户
│   ├── vllm    (limiter)     │
│   ├── sglang  (limiter)     │      Registry ──► 证书签发、副本名册
│   ├── ollama  (limiter)     │
│   └── comfyui (limiter)     │
└─────────────────────────────┘
```

每条隧道是完全独立的连接、独立的槽池、独立的状态机。它们唯一的交汇点是下方的 `runtime.Manager` 与各 Runtime 的 limiter——那里才是节点真实并发能力的仲裁者。

一次流式推理的完整路径：

```text
用户 SSE 请求 → LB 选中任意副本（设为 Gateway-2）
  → Gateway-2 鉴权、限流、协议转换为 canonical
  → Scheduler 在「Gateway-2 可达的节点」中选中 node-A 的 deployment
  → RemoteRuntime.ChatStream 从 Gateway-2 到 node-A 的槽池取一个空闲槽
  → Gateway-2 发 RequestHeaders(operation=chat_stream, request_id, deadline)
  → Agent 校验 runtime_id 白名单 → runtime.Manager.Get → limiter 取额度
  → rt.ChatStream(ctx, req) 打到本地 vLLM
  → 每个 ChatEvent 一帧 Send，不聚合
  → Gateway-2 收到首帧立即 flush 给用户（TTFT 在此确定）
  → ResponseEnd 后 Agent 释放额度并发 Ready 归还槽位
```

整条路径上没有副本间转发：Gateway-2 直接持有到 node-A 的隧道。这是选择"每副本一条隧道"而非 Tunnel Hub 的全部理由——Hub 方案会在这里插入一跳内网 RTT，直接计入 TTFT。

## 协议定义

**实现文件：** `api/proto/tunnel/v1/tunnel.proto`，Agent 与 Gateway 共用同一份生成代码。

### 服务

```protobuf
// Gateway 侧服务：每个副本各自实现一份，Agent 分别连接。
service Tunnel {
  // 控制面长连接：心跳、状态上报、配置下发、副本名册、优雅下线。
  // 每个 Agent 对每个副本恰好一条。
  rpc Control(stream AgentControl) returns (stream GatewayControl);

  // 数据面流槽：Agent 打开后发 Ready 待命，Gateway 派发请求。
  // 一条流同一时刻只服务一个请求，结束后可再发 Ready 复用。
  rpc Serve(stream AgentFrame) returns (stream GatewayFrame);
}

// Registry 侧服务：全集群唯一，负责节点身份。
service NodeIdentity {
  // 用一次性 bootstrap token 换取节点证书。唯一不要求客户端证书的方法。
  rpc Register(RegisterRequest) returns (RegisterResponse);
  // 证书轮换，要求当前证书仍有效。
  rpc RenewCertificate(RenewRequest) returns (RenewResponse);
}
```

**`Register` 在 Registry 而非 Gateway** 有两个原因：bootstrap token 的一次性校验需要强一致存储，多个数据面副本各自判断会留下重放窗口；CA 私钥不应分发到每个 Gateway 副本。签发身份本就是控制面职责。

Gateway 的两个方法都要求有效客户端证书；每个副本必须独立校验证书 SAN 中的 `node_id` 与流内声明的 `node_id` 一致，不一致直接断流。

### 数据面帧

```protobuf
message GatewayFrame {
  string request_id = 1;              // 全链路追踪标识，每帧携带
  oneof body {
    RequestHeaders headers = 2;
    DataChunk      data    = 3;       // 请求体分片（Artifact 上传、工作流输入）
    RequestEnd     end     = 4;
    Cancel         cancel  = 5;
    Ping           ping    = 6;       // 应用层保活，独立于 gRPC keepalive
  }
}

message AgentFrame {
  string request_id = 1;              // Ready/Pong 时为空
  oneof body {
    Ready           ready   = 2;
    ResponseHeaders headers = 3;
    DataChunk       data    = 4;      // 流式事件或产物分片
    ResponseEnd     end     = 5;
    Pong            pong    = 6;
  }
}

message Ready {
  string    node_id  = 1;
  string    slot_id  = 2;             // Agent 生成，便于两侧日志对齐
  SlotClass class    = 3;
}

enum SlotClass {
  SLOT_CLASS_UNSPECIFIED = 0;
  SLOT_CLASS_INFERENCE   = 1;         // Chat / Embed / 工作流提交与事件
  SLOT_CLASS_BULK        = 2;         // Artifact 上传下载，与推理槽物理隔离
}

message RequestHeaders {
  string      runtime_id  = 1;        // 必须命中 Agent 本地白名单
  Operation   operation   = 2;
  int64       deadline_unix_ms = 3;   // 绝对截止时间，槽复用后 gRPC 流级 deadline 不可用
  bytes       payload     = 4;        // 按 operation 反序列化为对应请求类型
  map<string, string> trace = 5;      // 只允许 request_id、tenant_id 等固定键
}

enum Operation {
  OPERATION_UNSPECIFIED   = 0;
  OPERATION_LIST_MODELS   = 1;
  OPERATION_CHAT          = 2;
  OPERATION_CHAT_STREAM   = 3;
  OPERATION_EMBED         = 4;
  OPERATION_WORKFLOW_SUBMIT    = 5;
  OPERATION_WORKFLOW_SUBSCRIBE = 6;
  OPERATION_WORKFLOW_STATUS    = 7;
  OPERATION_WORKFLOW_CANCEL    = 8;
  OPERATION_ARTIFACT_OPEN      = 9;
}

message ResponseHeaders {
  string content_type = 1;            // 仅 Artifact 使用
  int64  size         = 2;            // 仅 Artifact 使用，未知为 -1
}

message DataChunk { bytes payload = 1; }

message ResponseEnd {
  TunnelError error = 1;              // nil 表示正常结束
}

// TunnelError 是 runtime.RuntimeError 的线上镜像，保证错误码、可重试标记
// 和脱敏后的消息能无损跨隧道传递。
message TunnelError {
  string code        = 1;             // runtime.ErrorCode 的字符串值
  string runtime_id  = 2;
  string kind        = 3;
  string operation   = 4;
  int32  status_code = 5;
  string message     = 6;             // 已脱敏
  bool   retryable   = 7;
  string cause       = 8;             // 哨兵错误名，供 errors.Is 在对端还原
}
```

### payload 编码约定

`RequestHeaders.payload` 与 `DataChunk.payload` 的内容按 `operation` 决定：

| Operation | 请求 payload | 响应形态 |
| --- | --- | --- |
| `LIST_MODELS` | 空 | 单个 `DataChunk`（`[]Model`） |
| `CHAT` | `ChatRequest` | 单个 `DataChunk`（`ChatResponse`） |
| `CHAT_STREAM` | `ChatRequest` | N 个 `DataChunk`，每个恰好一个 `ChatEvent` |
| `EMBED` | `EmbeddingRequest` | 单个 `DataChunk`（`EmbeddingResponse`） |
| `WORKFLOW_SUBMIT` | `WorkflowRequest`（模板另走 `DataChunk`） | 单个 `DataChunk`（`WorkflowRun`） |
| `WORKFLOW_SUBSCRIBE` | `run_id` | N 个 `DataChunk`，每个一个 `WorkflowEvent` |
| `WORKFLOW_STATUS` | `run_id` | 单个 `DataChunk`（`WorkflowStatus`） |
| `WORKFLOW_CANCEL` | `run_id` | 无 `DataChunk`，只有 `ResponseEnd` |
| `ARTIFACT_OPEN` | `ArtifactRef` | `ResponseHeaders` + N 个 `DataChunk`（字节流） |

payload 统一使用 protobuf 消息，与 `runtime` 包的 Go 类型一一对应，转换集中在 `convert.go`，禁止在分发逻辑里内联字段拷贝。工作流模板 JSON 体积可观，走 `DataChunk` 而不是塞进 `RequestHeaders`，避免单帧超过 `MaxCallRecvMsgSize`。

**流式事件严禁聚合。** `CHAT_STREAM` 每收到一个 `ChatEvent` 就 `Send` 一帧；Gateway 每收到一帧就 flush。这条规则是 TTFT 的唯一保障，任何"攒够 N 条再发"的优化都必须先证明不影响首字延迟。

### 控制面帧

```protobuf
message AgentControl {
  oneof body {
    Hello          hello     = 1;     // 流建立后的第一帧
    Heartbeat      heartbeat = 2;
    RuntimeStatus  status    = 3;     // runtime.Manager.Snapshot() 的镜像
    Draining       draining  = 4;     // 优雅下线声明
  }
}

message GatewayControl {
  oneof body {
    HelloAck       ack       = 1;
    HeartbeatAck   hb_ack    = 2;
    RuntimeConfig  config    = 3;     // 下发 Runtime 配置变更
    SlotHint       slot_hint = 4;     // 建议的槽位水位
    GatewayRoster  roster    = 5;     // 副本名册，多副本的核心
    Shutdown       shutdown  = 6;     // 要求 Agent 排空并断开
  }
}

message Hello {
  string node_id       = 1;
  string agent_version = 2;
  NodeResources resources = 3;        // CPU、内存、GPU、显存、OS
  repeated string runtime_ids = 4;    // 本地白名单，Gateway 据此校验派发目标
}

// GatewayRoster 是全部 Gateway 副本的权威列表，由 Registry 维护、
// 各副本通过自己的 Control 流广播。Agent 据此补齐或关闭连接。
message GatewayRoster {
  repeated GatewayReplica replicas = 1;
  int64  version = 2;                 // 单调递增，Agent 忽略旧版本
}

message GatewayReplica {
  string replica_id = 1;
  string endpoint   = 2;              // host:port，必须可从 Agent 侧直连
  ReplicaState state = 3;
}

enum ReplicaState {
  REPLICA_STATE_UNSPECIFIED = 0;
  REPLICA_STATE_ACTIVE   = 1;         // 正常派活，Agent 应保持连接
  REPLICA_STATE_DRAINING = 2;         // 不再派新请求，Agent 停止补槽、等在途结束
  REPLICA_STATE_REMOVED  = 3;         // 已下线，Agent 应关闭连接
}
```

`RuntimeStatus` 直接由 `runtime.Manager.Snapshot()` 转换而来，包含 `Descriptor`、`State`、`Probe`、`Health`、`Discovery`（版本、模型、能力集合、Warnings）和 `Degraded`。**Snapshot 不含凭据**，这是它可以安全上报的前提。上报采用变更触发加定期全量：状态变化立即上报，无变化时每 60s 发一次全量对账。

**同一份 Snapshot 会发给所有副本**，因为它来自同一个 `runtime.Manager`。这保证各副本对"本节点的部署是否健康、支持什么能力"的判断天然一致，无需任何跨副本同步——副本间唯一会分歧的是"能不能连上这个节点"，而那正是各副本应当自行判断的。

`RuntimeConfig` 下发的是 `runtime.Config` 的镜像，但**不含 `APIKey` 明文**，只含 `api_key_ref`；Agent 本地解析 Secret。收到配置后 Agent 调用 `Manager.Add`/`Replace`/`Remove`，成功与否通过下一帧 `RuntimeStatus` 反映，不单独设计确认帧。

## 连接生命周期

### 注册与身份

```text
首次启动
  → 读取 bootstrap token（一次性、有效期 15min、由控制台生成）
  → 向 Registry 调 NodeIdentity.Register(node_id 候选, CSR, token)
  → Registry 校验 token（强一致、一次性）→ 签发节点证书（30d）+ 返回 CA
  → Agent 落盘证书（0600）并删除 bootstrap token
  → 之后连接全部 Gateway 副本时使用同一份证书

后续启动
  → 直接加载证书
  → 剩余有效期 < 1/3 时，向 Registry 调 RenewCertificate 轮换
  → 新证书对所有副本连接生效，逐条重连，不同时中断
```

一个节点只有**一份身份**，被所有副本连接共用。证书轮换时逐条隧道重连而非同时重建，避免出现该节点对所有副本同时不可达的窗口。

节点私钥只在 Agent 本地生成，永不上传。证书文件权限不是 0600 时 Agent 拒绝启动。

### 状态机

**每条隧道（即每个副本）各持有一份独立的状态机**，互不影响：

```text
    disconnected
         │ dial + mTLS + Hello
         ▼
    connecting ──── Hello 失败/证书无效 ──► failed（不退避重连，需人工介入）
         │ HelloAck
         ▼
     connected ──── 预热 min_slots 条 Serve 流 ──► serving
         │                                            │
         │◄──── 槽数掉到 0 且 Control 仍在 ───────────┘
         │
         │ Control 流断开 / 心跳超时
         ▼
    reconnecting ── 指数退避 1s→30s，全抖动 ──► connecting
         │
         │ 收到 Shutdown / 名册标记 draining / 本地 SIGTERM
         ▼
      draining ──── 在途请求跑完或超时 ──► disconnected
         │
         │ 名册标记 removed
         ▼
       retired（不再重连，从连接表移除）
```

关键规则：

- **Control 流是该副本判断节点存活的唯一判据。** 它断了，**该副本**立即把节点从自己的调度候选中摘除，不等数据槽超时；其他副本不受影响。
- **单副本故障不触发全局降级。** 只要还有一个副本连着，节点就仍在服务；Agent 不因某条隧道失败而停止其他隧道。
- **在途请求不续传。** 重连后该副本的旧槽全部作废，在途请求以 `ErrorConnection` 结束；是否换节点重试由该副本的调度器按 `Committed` 语义决定——已向用户发出首个 token 的流不得重试。
- **证书无效对所有副本同时生效。** 证书过期或被吊销时所有隧道会一起进入 `failed`，这是正确的（身份是节点级的）；输出一条聚合日志而非每副本一条。
- **退避带全抖动且按副本独立计算。** `sleep = rand(0, min(30s, 1s * 2^attempt))`。副本各自的 attempt 计数独立，防止某个副本重启后所有 Agent 同时回连，也防止一个副本的故障影响其他副本的重连节奏。
- **`failed` 与 `retired` 的区别**：`failed` 是需要人工介入的错误终态；`retired` 是名册告知该副本已下线的正常终态，不产生告警。

### 多副本连接管理

**实现文件：** `roster.go`

```text
启动
  → 读取配置中的 gateway_endpoints（种子列表）
  → 并发尝试连接，任一成功即可继续
  → 从成功的连接收到 GatewayRoster
  → 对比名册与当前连接表：
      名册有、本地无        → 建立新隧道
      名册无、本地有        → 标记 retired 并关闭
      状态变为 draining     → 停止补槽，等在途结束
      状态变为 active       → 恢复补槽
  → 名册 version 单调递增，忽略旧版本
```

要点：

- **种子列表只需保证至少一个可达。** 名册会补齐其余副本，因此扩容时不需要改 Agent 配置。
- **名册来自任意一条隧道即可。** 各副本广播的是同一份 Registry 名册，Agent 按 `version` 去重，不需要判断哪个副本更权威。
- **名册为空或全部不可达时保留最后一份有效名册**并持续重连，不清空连接表——否则 Registry 抖动会导致节点全面掉线。
- **连接数上限保护。** 名册异常膨胀（配置错误或被污染）时，Agent 按 `max_gateways` 截断并告警，避免家用 Mac 的 FD 被耗尽。

### 心跳

两层，职责不同：

| 层 | 机制 | 周期 | 作用 |
| --- | --- | --- | --- |
| 传输层 | gRPC keepalive（HTTP/2 PING） | 20s，超时 10s | 检测 TCP 假连接，穿透 NAT 会话表老化 |
| 应用层 | `Control` 流 `Heartbeat` | 15s，连续 3 次无 `HeartbeatAck` 判死 | 检测服务端逻辑假死，携带轻量负载指标 |

NAT 设备的 UDP/TCP 会话老化时间常见为 30s 到 5min，20s 的传输层保活足以覆盖绝大多数家用路由器。`Serve` 数据槽长时间空闲也依赖同一条 TCP 的 PING 保活，不需要各自心跳。

## 槽池

**实现文件：** `slot.go`（单槽生命周期）、`pool.go`（Agent 侧水位管理）。

**每个副本一个独立槽池。** 下表参数是**单副本**的取值：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `min_slots` | 2 | 每副本常驻空闲槽，保证突发请求零建流延迟 |
| `low_watermark` | 1 | 空闲槽低于此值立即补充 |
| `max_slots` | 见下 | 每副本上限，由节点总额度按副本数分摊 |
| `bulk_slots` | 1 | 每副本 `SLOT_CLASS_BULK` 配额，防止大产物挤占推理槽 |
| `slot_idle_timeout` | 5min | 超时关闭多余槽，但保底不低于 `min_slots` |
| `max_gateways` | 16 | 名册异常时的连接数上限保护 |

### 多副本额度分配

节点的真实并发能力是固定的（各 Runtime `MaxConcurrent` 之和），但现在有 N 个副本各自派活。分配规则：

```text
per_replica_max = max(min_slots, ceil(node_total_slots / active_replica_count))
```

`active_replica_count` 从名册中的 `active` 副本数得到，副本增减时重算。取 `max(min_slots, ...)` 是为了保证副本数很多时每个副本仍有最低可用槽位，代价是槽位总和可能超过节点额度——**这是有意为之**。

**不追求精确的跨副本并发协调**，理由：

1. `runtime` 层的 limiter 已经是节点真实并发的**权威约束**。槽位超发时，Agent 侧 `Acquire` 会返回 `ErrorBackpressure`，Gateway 收到后换节点。
2. Gateway 侧已明确规定 `ErrorBackpressure` **不计入熔断**——多副本同时派活触碰上限是常态而非故障。
3. 要做精确协调，就得引入 Agent 侧的跨隧道额度仲裁，等于在请求路径上加一次跨副本协商，成本远高于偶尔一次换节点。

换言之：**槽位是软配额（避免明显过载），limiter 是硬配额（保证不过载）**。两者职责不同，不应合并。

常驻空闲槽的总成本也需按副本数计算：3 个副本 × `min_slots=2` = 6 条常驻空闲流，加 3 条 Control 流，共 9 条 gRPC 流。这是家用 Mac 完全可以承受的量级；`max_gateways=16` 是防止名册异常时的兜底。

槽的复用与竞态：

- 槽在 `Ready` → `busy` → `Ready` 之间循环。Gateway 只有收到 `ResponseEnd` 后才可把槽放回空闲队列。
- **槽属于某一条隧道，不跨副本迁移。** 一个副本的槽耗尽时不会去借用另一个副本的槽——那需要跨隧道协调，且 Gateway 侧根本感知不到其他副本的槽池状态。
- **每帧携带 `request_id`，Agent 必须丢弃 `request_id` 与当前请求不匹配的 `Cancel` 帧。** 否则一个迟到的取消会误杀刚复用该槽的新请求。
- 单槽连续服务 200 个请求或存活超过 1h 后主动重建，避免长连接上的隐性状态累积。
- 一个请求异常（panic、协议错误、解码失败）只作废该槽，不影响同一 TCP 连接上的其他槽。

**为什么不是单流多路复用？** 把所有请求塞进一条 `Control` 式的双向流，需要自研 `stream_id`、分片调度和应用层窗口；更致命的是一条 gRPC 流内的消息严格有序，一次 500MB 的产物下载会把后面排队的 SSE token 全部阻塞。一槽一请求让每个请求各自占据一条 HTTP/2 stream，天然获得独立流控窗口，帧在 TCP 连接上按 HTTP/2 的调度交错发送，互不阻塞。代价是需要维护槽池水位——这比自研流控便宜得多。

## Agent 侧请求分发

**实现文件：** `dispatch.go`

```text
收到 RequestHeaders
  → 校验 runtime_id ∈ 本地白名单              失败 → ErrorInvalidConfig
  → Manager.Get(runtime_id)                   失败 → ErrRuntimeNotFound
  → 类型断言 InferenceRuntime / WorkflowRuntime 失败 → ErrorCapability
  → ctx = WithDeadline(slotCtx, deadline_unix_ms)
  → limiter 取额度（非阻塞）                   失败 → ErrorBackpressure
  → 调用对应 runtime 方法
  → 逐条 Send，defer 释放额度
  → ResponseEnd(error) → Ready
```

要点：

- **额度必须 `defer` 释放**，覆盖 panic、取消和提前返回三条路径。流式请求在整个流生命周期持有额度，与 `runtime/limiter.go` 的既有语义一致。
- **`deadline` 由 Gateway 下发**，因为槽被复用后 gRPC 的流级 deadline 不再适用。Agent 取 `min(下发 deadline, 本地 RequestTimeout)`，两者都不设时用默认值。
- **能力门禁不在隧道层重复。** `runtime` 适配器入口已经对 `Discover` 结果调用 `Require`，隧道层只负责把返回的 `RuntimeError` 转成 `TunnelError`。
- **Cancel 帧调用 slot 的 `cancel()`**，取消信号沿 `context` 一路传到后端 HTTP 请求，进而关闭上游连接——这是隧道层唯一需要主动传播的控制信号。

`ComfyUI` 的 WebSocket **不上隧道**：Agent 本地持有到 ComfyUI 的 `/ws` 连接并归一化为 `WorkflowEvent`，隧道上只传归一化后的事件流。这样隧道协议不必处理 WebSocket 升级、二进制预览帧和重连对账，全部复杂度留在已规划好的 `runtime/workflow/comfyui` 内部。

## 文件职责规划

| 文件 | 职责 |
| --- | --- |
| `manager.go` | 多副本连接表：按名册建连与断连、全局生命周期、聚合状态 |
| `roster.go` | 名册接收、版本去重、连接表差分、`max_gateways` 保护 |
| `client.go` | 单条隧道的总装：连接状态机、退避重连、优雅下线 |
| `identity.go` | bootstrap token、CSR、向 Registry 换证与轮换、文件权限校验 |
| `control.go` | Control 流：Hello、心跳、Snapshot 上报、配置下发应用、名册接收 |
| `pool.go` | 单副本槽池水位管理、预热、空闲回收、分类配额、额度分摊 |
| `slot.go` | 单槽状态机、帧读写循环、per-request context 与取消 |
| `dispatch.go` | Operation 到 `runtime` 方法的分发、白名单校验、限流接入 |
| `convert.go` | proto ⇄ `runtime` 类型双向转换，含 `RuntimeError` ⇄ `TunnelError` |
| `backoff.go` | 全抖动指数退避 |
| `metrics.go` | 隧道指标，复用 `runtime.Metrics` 接口 |

测试与实现同目录，优先黑盒测试（`package tunnel_test`）；帧编解码和退避算法这类内部细节用包内测试。跨包共用的 fake Gateway 放在 `tunnel/internal/tunneltest`，与 `runtime/internal/runtimetest` 的做法保持一致。

## 配置

```yaml
tunnel:
  enabled: true
  node_id: mac-mini-01

  # 种子列表：至少一个可达即可，其余副本由 GatewayRoster 补齐。
  # 扩容 Gateway 副本时不需要修改这里。
  gateway_endpoints:
    - gw-1.example.com:8443
    - gw-2.example.com:8443
  max_gateways: 16              # 名册异常时的连接数上限保护

  identity:
    registry_endpoint: registry.example.com:8444   # 证书签发与轮换
    cert_file: /var/lib/aiserveweave/node.crt
    key_file:  /var/lib/aiserveweave/node.key
    ca_file:   /var/lib/aiserveweave/ca.crt
    bootstrap_token_file: /var/lib/aiserveweave/bootstrap.token  # 用后自删

  # 以下为「每个副本」的槽池参数。
  slots:
    min: 2
    low_watermark: 1
    bulk: 1
    idle_timeout: 5m
    # 节点总槽位，按 active 副本数分摊到各副本；留空则取各 Runtime
    # MaxConcurrent 之和。
    node_total: 32

  keepalive:
    transport_interval: 20s
    transport_timeout: 10s
    heartbeat_interval: 15s
    heartbeat_failure_threshold: 3

  limits:
    max_frame_bytes: 4Mi        # 单帧上限，SSE 事件远小于此
    max_request_bytes: 64Mi     # 工作流模板与输入文件
    max_deadline: 30m           # 拒绝超过此值的下发 deadline

  backoff:
    initial: 1s
    max: 30s

  # 允许被 Gateway 派发的 runtime，缺省为 runtimes 段中的全部 id。
  # 显式收窄后，即使控制面下发了其他 id 也会被拒绝。
  allowed_runtimes: []
```

校验规则：

- `gateway_endpoints` 至少一项，每项必须是 `host:port`，禁止 `http://` 前缀和 URL 路径。
- `registry_endpoint` 同上；它与 Gateway 端点必须可分别解析，**不能指向同一个 VIP**——Agent 需要精确控制连到哪个副本。
- `node_id` 必须与证书 SAN 一致，不一致时 Agent 启动即失败，不等到运行时被 Gateway 拒绝。
- `min ≤ low_watermark 上界`、`bulk ≤ node_total`、`max_gateways ≥ len(gateway_endpoints)`，违反时聚合报错。
- 名册中的 `endpoint` 同样要校验；不合法的条目跳过并告警，不影响其余副本的连接。
- `max_deadline` 是本地保护，即使 Gateway 下发更长的 deadline 也按本地值截断。
- 配置格式化必须脱敏证书路径以外的敏感信息；bootstrap token 内容禁止进日志。

## 安全要求

- 传输全程 mTLS，最低 TLS 1.3；每个 Gateway 副本与 Agent 双向校验。
- bootstrap token 一次性、短有效期、绑定租户；**校验在 Registry 上完成**（强一致存储），使用后立即失效并写审计日志。多副本各自校验会留下重放窗口，这是把 `Register` 移出 Gateway 的核心安全理由。
- **CA 私钥只在 Registry 上**，不分发到任何 Gateway 副本。副本被攻破不会导致可以签发任意节点身份。
- 节点私钥本地生成，永不出网；证书自动轮换，吊销通过 CRL 或短有效期实现。
- 每个 Gateway 副本独立校验证书 SAN 的 `node_id` 与 `Hello.node_id` 一致，否则断流。
- **名册是受信输入但仍需校验**：Agent 对名册中的 endpoint 做格式校验并受 `max_gateways` 约束，防止被污染的名册让 Agent 向任意地址发起连接。
- Agent 侧 `runtime_id` 白名单是**最后一道防线**：控制面下发的配置也要经过它，防止被攻破的 Gateway 让 Agent 扫描内网。
- 帧大小、请求体大小、deadline 全部有本地上限，超限返回 `ErrorResponseTooLarge` 而不是尝试处理。
- 日志只记录 `node_id`、`slot_id`、`request_id`、`runtime_id`、`operation`、错误码和延迟；不记录 payload。
- Agent 不提供任何远程执行、文件读写或端口转发能力；协议里没有这类 Operation，也不留扩展位。

## 可观测性

所有隧道指标都带 `replica_id` 标签，否则无法区分是某个副本的链路问题还是节点整体问题：

```text
tunnel_connection_state{node_id,replica_id}          0=disconnected 1=connecting 2=connected 3=draining 4=retired
tunnel_connected_replicas{node_id}                   当前已连上的副本数，掉到 0 才是真正离线
tunnel_roster_version{node_id}                       已应用的名册版本，副本间不一致说明广播有问题
tunnel_reconnects_total{node_id,replica_id,reason}
tunnel_control_heartbeat_rtt_seconds{node_id,replica_id}
tunnel_slots_total{node_id,replica_id,class,state}   state: idle|busy
tunnel_slot_acquire_failures_total{node_id,replica_id,class}
tunnel_limiter_rejections_total{node_id,runtime_id}  软配额超发后被硬配额拦下的次数
tunnel_requests_total{node_id,replica_id,operation,result}   result 沿用 runtime 的六值约定
tunnel_request_duration_seconds{node_id,replica_id,operation}
tunnel_stream_first_event_seconds{node_id,replica_id,operation}
tunnel_frame_bytes{node_id,replica_id,direction}
tunnel_cancel_total{node_id,replica_id,reason}
```

多副本下的排查要点：

- `tunnel_connected_replicas` 是节点健康的真正判据。等于 0 才是离线；小于名册规模说明部分副本不可达，应对照 `tunnel_connection_state` 定位是哪个。
- `tunnel_limiter_rejections_total` 持续偏高，说明槽位软配额分配得比节点真实能力宽太多，应调低 `node_total` 而不是提高 limiter。
- `tunnel_roster_version` 在副本间长期不一致，说明 Registry 到某个副本的名册推送有问题。
- `request_id` 由 Gateway 生成并在每帧携带，Agent 转发给后端并出现在两侧全部日志中。日志还需带 `replica_id`，否则无法把用户报障对应到具体链路。
- `tunnel_stream_first_event_seconds` 与 Gateway 侧端到端 TTFT 的差值，是判断"慢在隧道还是慢在模型"的核心手段，必须在阶段 7 一并接上。

## 首字延迟预算

面向互联网用户的目标是端到端 TTFT 不劣于直连太多。按单跳跨城 30ms RTT 估算：

| 环节 | 预算 | 保障手段 |
| --- | --- | --- |
| 用户 → Gateway TLS 与鉴权 | ≤ 5ms | 会话复用、鉴权走内存缓存 |
| Gateway 调度选点 | ≤ 1ms | 只读 Snapshot，不在请求路径做探测 |
| 取槽 | ≈ 0 | 槽预热待命，不新建流 |
| Gateway → Agent 单程 | ≈ RTT/2 | 无额外握手；槽已建立 |
| Agent → 本地后端 | ≤ 2ms | 环回或内网 |
| 模型首 token | 主要成本 | 不在隧道控制范围 |
| 首 token 回程 | ≈ RTT/2 | 逐条 Send，两侧立即 flush |

因此隧道引入的额外开销约等于一个 RTT 加两次序列化，可控在 10ms 量级。真正的杀手是各层缓冲：**任何一处 `bufio.Writer` 未 flush、启用 gRPC 压缩、或 Gateway 前置反向代理开了响应缓冲，TTFT 都会从毫秒级劣化到秒级。** 流式路径默认关闭 gRPC 压缩（压缩器需要攒够数据才出帧），`SLOT_CLASS_BULK` 的产物传输可以单独开启。

## 实施计划

阶段依赖：

```text
阶段 1 (proto + 转换)
   │
   ├──► 阶段 2 (身份与证书, 依赖 Registry)
   │        │
   │        ▼
   ├──► 阶段 3 (单条隧道: Control 流) ──┐
   │                                    │
   └──► 阶段 4 (槽池 + Serve) ──────────┤
                                        ▼
                            阶段 5 (分发接入 runtime)
                                        │
                                        ▼
                            阶段 6 (多副本连接管理)
                                        │
                                        ▼
                            阶段 7 (指标 + 端到端联调)
```

阶段 1、3、4 不依赖任何后端适配器，可与 `runtime` 的阶段 4 到 7 并行。阶段 2 依赖 Registry 的 `NodeIdentity` 服务先落地。阶段 5 的端到端验证需要至少一个可用适配器（建议 Ollama，本地成本最低）。

**阶段 3 到 5 先只做单条隧道**，把连接、槽池、分发跑通；阶段 6 再把它扩展成多副本连接表。反过来做会让前三个阶段的调试同时面对连接管理和协议两类问题。

### 阶段 1：proto 定义与类型转换

**文件：** 创建 `api/proto/tunnel/v1/tunnel.proto`、`tunnel/convert.go`、`tunnel/convert_test.go`；修改 `go.mod`。

- [ ] 写 `runtime` 类型 ⇄ proto 的往返测试：`ChatRequest` 的指针字段为 nil 时不出现在 proto 中、显式 0 值原样保留、`Extra` 原样透传。
- [ ] 写 `RuntimeError` ⇄ `TunnelError` 往返测试：错误码、`retryable`、哨兵 `Cause` 在对端可被 `errors.Is` 匹配。
- [ ] 写 payload 编码测试：每个 `Operation` 的请求与响应类型对应关系，未知 Operation 返回 `ErrorProtocol`。
- [ ] 运行 `go test ./service/aiServeWeaveAgent/tunnel -run TestConvert`，确认失败。
- [ ] 定义 proto、生成代码、实现双向转换。
- [ ] 运行质量门禁全部命令。

**验收：** 转换层是隧道与 `runtime` 的唯一耦合点；`grep` 不到分发逻辑里的字段级拷贝。

### 阶段 2：节点身份与证书

**文件：** 创建 `tunnel/identity.go`、`identity_test.go`。**前置：** Registry 的 `NodeIdentity` 服务已定义接口。

- [ ] 写测试：bootstrap token 换证成功后 token 文件被删除、证书权限非 0600 时拒绝启动、`node_id` 与 SAN 不一致时启动失败、剩余有效期低于 1/3 触发轮换。
- [ ] 写 TLS 配置测试：最低版本 TLS 1.3、必须携带客户端证书、CA 校验生效。
- [ ] 写多副本共用测试：同一份证书可用于建立到多个不同 endpoint 的连接。
- [ ] 运行包测试，确认失败。
- [ ] 实现密钥生成、CSR、向 Registry 调用 `Register`/`RenewCertificate`、证书落盘与轮换判定。
- [ ] 运行质量门禁全部命令。

**验收：** 私钥不出网；证书类错误进入 `failed` 终态而非无限重连；Gateway 侧不持有任何签发能力。

### 阶段 3：单条隧道的 Control 流与连接状态机

**文件：** 创建 `tunnel/client.go`、`tunnel/control.go`、`tunnel/backoff.go` 及对应测试；创建 `tunnel/internal/tunneltest`（fake Gateway）。

- [ ] 写状态机测试：Hello 成功进入 connected、心跳连续 3 次失败进入 reconnecting、收到 Shutdown 进入 draining、证书错误进入 failed 不重连。
- [ ] 写退避测试：全抖动落在 `[0, min(30s, 2^n))`，注入 `runtime.Clock` 控制时间，不使用真实 sleep。
- [ ] 写 Snapshot 上报测试：状态变化立即上报、无变化时 60s 全量、上报内容不含 `APIKey` 与 Header 值。
- [ ] 写配置下发测试：`RuntimeConfig` 触发 `Manager.Add`/`Replace`/`Remove`，失败通过下一帧 `RuntimeStatus` 反映。
- [ ] 运行包测试，确认失败。
- [ ] 实现连接状态机、Control 流收发、退避重连、优雅下线。
- [ ] 运行 `go test -race`，确认无协程泄漏。

**验收：** 断网、Gateway 重启、证书过期三种场景下的行为均可预测且有明确日志。

### 阶段 4：槽池与 Serve 流

**文件：** 创建 `tunnel/pool.go`、`tunnel/slot.go` 及对应测试。

- [ ] 写水位测试：启动预热 `min_slots`、空闲降到 `low_watermark` 触发补充、超过分摊后的上限拒绝新建、空闲超时回收但保底 `min_slots`。
- [ ] 写额度分摊测试：`per_replica_max = max(min_slots, ceil(node_total / active_replicas))`，副本数为 1、3、100 时结果符合预期。
- [ ] 写分类隔离测试：`BULK` 槽耗尽不影响 `INFERENCE` 槽的获取。
- [ ] 写复用竞态测试：`ResponseEnd` 之后到达的 `Cancel` 帧不影响复用该槽的新请求。
- [ ] 写槽作废测试：单槽协议错误只关闭该槽，同连接其他槽继续服务。
- [ ] 写槽轮换测试：达到请求数或存活时长上限后主动重建。
- [ ] 运行包测试，确认失败。
- [ ] 实现槽池水位管理与单槽帧循环。
- [ ] 运行 `go test -race`，确认无协程与连接泄漏。

**验收：** 槽池在压测下数量稳定收敛；任意单槽故障不产生连锁失败。

### 阶段 5：请求分发接入 runtime

**文件：** 创建 `tunnel/dispatch.go`、`dispatch_test.go`；修改 `service/aiServeWeaveAgent/main.go`。

- [ ] 写白名单测试：未在 `allowed_runtimes` 中的 `runtime_id` 被拒绝，即使 `Manager` 中存在该实例。
- [ ] 写九个 `Operation` 的分发测试，用 `runtimetest` 的 fake Runtime 覆盖成功、能力不支持、背压和上游错误四类结果。
- [ ] 写流式测试：每个 `ChatEvent` 独立成帧、`Committed` 后取消不产生重试、`ResponseEnd` 携带终止错误。
- [ ] 写额度测试：panic、取消、提前返回三条路径下额度都被释放。
- [ ] 写 deadline 测试：取下发值与本地上限的较小者。
- [ ] 运行包测试，确认失败。
- [ ] 实现分发器并在 `main.go` 中装配 Manager 与隧道客户端。
- [ ] 运行 `go test -race ./service/aiServeWeaveAgent/...`。

**验收：** 对着 fake Gateway 可以完成全部九个 Operation；`runtime` 层的错误语义无损跨隧道传递。

### 阶段 6：多副本连接管理

**文件：** 创建 `tunnel/manager.go`、`tunnel/roster.go` 及对应测试；修改 `client.go`、`pool.go`。

- [ ] 写种子连接测试：`gateway_endpoints` 中部分不可达时仍能启动，只要有一个成功。
- [ ] 写名册差分测试：新增副本自动建连、移除副本进入 `retired` 并关闭、`draining` 停止补槽但在途请求跑完。
- [ ] 写版本去重测试：旧 `version` 的名册被忽略，不会让已关闭的副本复活。
- [ ] 写名册降级测试：名册为空或 Registry 不可用时保留最后一份有效名册，不清空连接表。
- [ ] 写上限保护测试：名册超过 `max_gateways` 时截断并告警。
- [ ] 写故障隔离测试：一条隧道进入 `reconnecting` 或 `failed` 时，其他隧道的连接、槽池和在途请求均不受影响。
- [ ] 写额度重算测试：`active` 副本数变化时各副本槽位上限同步调整，不出现额度真空。
- [ ] 写证书轮换测试：新证书生效时逐条隧道重连，不同时中断全部连接。
- [ ] 运行包测试，确认失败。
- [ ] 实现连接表、名册处理与聚合状态上报。
- [ ] 用多个 fake Gateway 做集成测试：验证同时连上 3 个，任一被 kill 后其余继续服务。
- [ ] 运行 `go test -race`，确认无协程与连接泄漏。

**验收：** 副本扩缩容无需重启 Agent；单副本故障不影响其他副本；`tunnel_connected_replicas` 准确反映可用链路数。

### 阶段 7：指标、端到端联调与压测

**文件：** 创建 `tunnel/metrics.go`；修改本 README 的实测数据小节。

- [ ] 接入本文件「可观测性」列出的全部指标，写标签基数测试（禁止 payload 内容进标签），断言所有指标带 `replica_id`。
- [ ] 与 Gateway 侧完成端到端联调：本机 Ollama + 隧道 + **3 个 Gateway 副本**，跑通非流式与 SSE 流式 Chat。
- [ ] 验证每个副本都能独立完成完整推理，且请求路径上无副本间转发。
- [ ] 实测隧道段 TTFT 与端到端 TTFT，与直连对比，记录差值；确认多副本未引入额外延迟。
- [ ] 压测：并发拉满槽池，验证无空闲槽时返回背压而非排队；观察 `tunnel_limiter_rejections_total` 是否在可接受范围，据此校准 `node_total`。
- [ ] 故障注入：拔网线、kill 单个 Gateway 副本、kill 全部副本、证书过期、后端假死、名册抖动，六种场景各记录恢复时间与用户侧表现。
- [ ] 滚动升级演练：配合 Gateway 侧逐个替换副本，确认 Agent 全程保持至少一条可用隧道。
- [ ] 长稳测试：24h 连续运行，验证无内存增长、无协程泄漏、连接数与槽数稳定。
- [ ] 把实测数据填入本 README 的「首字延迟预算」并说明偏差原因。

**验收：** 端到端 TTFT 相对直连的额外开销可解释；六种故障场景恢复时间符合预期；滚动升级期间节点始终可服务。

## 首期完成标准

- Agent 在 NAT 后可用一次性 token 向 Registry 完成注册并建立 mTLS 隧道，全程无需入站端口。
- Agent 自动连接名册中的全部 Gateway 副本；副本扩缩容无需重启 Agent、无需改配置。
- 单个副本故障时其余隧道继续服务；全部副本不可达才判定节点离线。
- 某副本的 Control 流断开后，**该副本**在一个心跳周期内把节点从调度候选中摘除，其他副本不受影响。
- 九个 Operation 全部可通过任意副本的隧道执行，错误码与可重试标记在两侧一致。
- 流式响应逐条转发，隧道段 TTFT 有实测数据支撑，多副本未引入额外延迟。
- 槽位软配额按副本分摊，limiter 硬配额兜底；无空闲槽时立即返回背压错误，不排队、不阻塞。
- 未在白名单中的 `runtime_id` 一律被拒绝，控制面下发也不例外。
- 断线重连后该副本的旧槽全部作废，在途请求以明确错误结束，不产生重复推理。
- CA 私钥不在任何 Gateway 副本上；bootstrap token 的一次性校验无重放窗口。
- 日志与指标带 `replica_id`，且不存在 API Key、Prompt、工作流 JSON 和产物内容。
- `gofmt -l` 无输出，`go vet` 无告警，`go test -race ./service/aiServeWeaveAgent/...` 通过。
- 24h 长稳测试无泄漏，故障注入六场景恢复行为符合预期，滚动升级期间节点始终可服务。

## 风险与待决问题

| 风险 | 影响 | 缓解 | 状态 |
| --- | --- | --- | --- |
| 槽复用导致迟到 `Cancel` 误杀新请求 | 用户请求被无故中断 | 每帧携带 `request_id`，不匹配即丢弃 | 已缓解 |
| 大产物传输挤占推理槽 | TTFT 劣化 | `SLOT_CLASS_BULK` 物理隔离 + 独立配额 | 已缓解 |
| 单 TCP 连接丢包引发该副本全部槽卡顿 | 到该副本的请求延迟抖动 | 多副本天然分散：其他副本的隧道是独立 TCP 连接，不受影响 | 已缓解 |
| 隧道连接数随副本数线性增长 | 家用 Mac 的 FD 与内存压力 | 每副本 `min_slots=2`；`max_gateways=16` 兜底；副本数 ≤ 10 时总流数在 30 条量级 | 已缓解 |
| 槽位软配额总和超过节点真实并发 | 多副本同时派活触碰 limiter 上限 | 有意为之：limiter 返回背压，Gateway 换节点且不计入熔断 | 已接受 |
| 名册被污染或配置错误 | Agent 向非预期地址发起连接 | 名册经 mTLS 信道下发；endpoint 格式校验 + `max_gateways` 截断 | 已缓解 |
| 名册广播延迟导致副本视图不一致 | 扩容后部分 Agent 迟迟未连上新副本 | `version` 单调递增 + 各副本独立广播，任一条隧道收到即生效 | 已缓解 |
| Gateway 前置代理开启响应缓冲 | SSE 变成"一次性吐完"，TTFT 秒级 | 部署文档强制要求关闭缓冲，阶段 6 实测验证 | 需部署配合 |
| 家用网络 NAT 会话老化 | 隧道静默失效 | 20s 传输层 PING + 15s 应用层心跳 | 已缓解 |
| 断线时在途流式请求无法续传 | 用户看到截断的回答 | 明确以错误结束；`Committed` 后禁止重试，由客户端决定 | 已接受 |
| Gateway 被攻破后下发恶意 `runtime_id` | Agent 沦为内网扫描器 | 本地白名单是最终防线，协议无通用代理能力 | 已缓解 |

待决问题：

1. `runtime` 包目前位于 `service/aiServeWeaveAgent/runtime`，但 Gateway 侧需要复用它的接口与类型。建议在阶段 1 前把公共契约（接口、类型、capability、errors、stream）下沉到共享路径；MVP 可先直接 import 现有路径，不阻塞开发，但需在阶段 6 前决定是否迁移。
2. **Registry 的 `NodeIdentity` 服务是阶段 2 的硬前置。** 需确认其排期；若 Registry 晚于隧道，阶段 2 需要一个临时方案（如离线签发证书并手工分发），且必须在上线前替换。
3. `node_id` 的分配方式：控制台预分配还是 Agent 提交候选后由 Registry 确认。影响 `Register` 的接口形状，需在阶段 2 前确定。
4. Secret 引用 `api_key_ref` 的解析方式（本地文件、环境变量还是外部 Secret 管理器），需与 `runtime` 阶段 8 的结论保持一致。
5. **名册的下发时效**：Registry 到 Gateway 是推送还是轮询（Gateway 侧待决问题 3）。若为 30s 轮询，则副本扩容后 Agent 最长 30s 才会连上新副本，需确认这个窗口可接受。
6. **`node_total` 的默认取值**：取各 Runtime `MaxConcurrent` 之和是否合理，还是应该更保守。需在阶段 7 的压测中用 `tunnel_limiter_rejections_total` 校准。

## 后续演进

多副本链路稳定后再独立规划：

1. **独立 Tunnel Hub。** 触发条件：Gateway 副本数超过 10、节点数超过 200，或 Agent 侧出现维持多条隧道的 FD/内存压力。届时把隧道终结从 Gateway 剥离到 Hub 集群，Gateway 按一致性哈希 `node_id` 找到持有隧道的 Hub。代价是每个请求多一跳内网转发，因此不到触发条件不做。
2. **跨副本的精确并发协调**，用共享计数替代当前的"软配额 + limiter 兜底"。只有在 `tunnel_limiter_rejections_total` 高到影响调度效率时才值得做。
3. **单副本多 TCP 连接分散槽位**，进一步规避单连接的 TCP 层队头阻塞。多副本已经提供了一层天然分散，优先级不高。
4. **QUIC 传输**，在弱网和移动网络下改善丢包恢复；需评估 gRPC-over-QUIC 的生态成熟度。
5. **跨地域副本的就近连接**，Agent 优先连接同地域副本，远端副本作为备份。需要名册携带地域标签。
6. **在途请求续传**，为非流式和尚未 `Committed` 的流式请求提供跨连接恢复——多副本下还可以尝试跨副本恢复。
7. **隧道级限速与优先级**，让高优先级租户的 token 流优先于批量产物传输。
8. **Agent 自动升级**，通过 Control 流下发版本并灰度。

这些能力不得提前塞进首期协议；确需扩展时优先新增 Operation 或独立 RPC，避免改动已有帧结构。

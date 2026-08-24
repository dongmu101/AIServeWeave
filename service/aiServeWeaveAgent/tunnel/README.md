# Agent Tunnel 接入规划

**目标：** 让部署在家庭 Mac、办公网和 NAT 后的节点，通过主动发起的 mTLS gRPC 长连接，把本地 vLLM、SGLang、Ollama、ComfyUI 的推理与工作流能力安全地交付给**多副本部署的远程 Gateway**，并为互联网用户维持低首字延迟和可预期的稳定性。

**架构：** Agent 是 gRPC 客户端，Gateway 是 gRPC 服务端。**Agent 与每个 Gateway 副本各建立一条独立隧道**，因此任意副本都能直达本节点，请求路径上不存在副本间转发。每条隧道内部：控制面走一条长期存在的双向流（心跳、状态上报、配置下发、副本名册）；数据面走一个由 Agent 预先打开并保持待命的**双向流槽池**，每条流槽同一时刻只服务一个请求，请求结束后归还槽位继续待命。隧道传输的是 `runtime` 包的**运行时语义**（Chat/Embed/Submit/Subscribe/Artifact），不是任意 HTTP 报文。

节点证书的签发由 **Registry** 负责，不在 Gateway 上——bootstrap token 的一次性校验需要强一致存储，且多副本下不应把 CA 私钥分发到每个数据面副本。

**技术栈：** Go 1.27、`google.golang.org/grpc`、`google.golang.org/protobuf`、`crypto/tls`、`context`、`log/slog`，复用已落地的 `common/runtime` 包。

---

## 当前状态

阶段 1 到 7 已落地，阶段 7 剩下的是需要真实部署（真实后端、会断的网线、会过期的证书、24 小时）才能做的验收项：`api/proto/tunnel/v1/tunnel.proto` 定义了完整线上契约（`Tunnel` 与 `NodeIdentity` 两个服务、数据面帧、控制面帧、全部 `runtime` 类型镜像），`common/tunnelwire` 实现了双向转换与 payload 编解码（阶段 7 从 `tunnel/convert.go` 拆出，与 Gateway 共用），`tunnel/identity.go` 实现了本地密钥生成、向 Registry 换证与轮换、证书落盘与 mTLS 配置，`tunnel/client.go` + `control.go` + `backoff.go` 实现了单条隧道的连接状态机、Control 流与退避重连，`tunnel/pool.go` + `slot.go` 实现了单副本槽池的水位管理与单槽帧循环，`tunnel/dispatch.go` 把九个 Operation 接到 `runtime.Manager` 上，`tunnel/manager.go` + `roster.go` 实现了多副本连接表与名册处理，`tunnel/metrics.go` 接上了全部 13 个指标，`main.go` 完成装配。Gateway 侧的隧道服务端见 [service/aiServeWeaveGateway/README.md](../../aiServeWeaveGateway/README.md)，三副本端到端联调在 `service/aiServeWeaveGateway/e2e`；阶段 7 剩余项见该阶段的清单。

| 文件 | 状态 | 说明 |
| --- | --- | --- |
| `api/proto/tunnel/v1/tunnel.proto` | 已实现 | 两侧共用；`go generate ./api/...` 重新生成 |
| `common/tunnelwire` | 已实现 | 类型镜像、`Operation` payload 契约表、`RuntimeError` ⇄ `TunnelError`；两端共用 |
| `identity.go` | 已实现 | 密钥与 CSR、`Register`/`RenewCertificate`、文件权限与 SAN 校验、`Identity.TLSConfig` |
| `client.go` | 已实现 | 连接状态机、退避重连、优雅下线、`Transport`/`ControlStream` 抽象与 gRPC 实现 |
| `control.go` | 已实现 | Hello、心跳、Snapshot 上报、配置下发应用、名册与槽位建议转发、排空 |
| `backoff.go` | 已实现 | 全抖动指数退避 |
| `pool.go` | 已实现 | 单副本槽池：预热、水位补充、空闲回收、分类配额、额度分摊、`SlotHint` clamp |
| `slot.go` | 已实现 | 单槽帧循环、`Handler`/`ResponseSink` 接口、per-request context 与取消、槽轮换 |
| `dispatch.go` | 已实现 | 九个 Operation 的分发、白名单、能力断言、deadline 取小、limiter 额度、大小上限 |
| `manager.go` | 已实现 | 多副本连接表、名册差分、额度重算、证书轮换时逐条重连、聚合状态 |
| `roster.go` | 已实现 | 名册校验、版本去重、空名册降级、`max_gateways` 截断、`active` 计数 |
| `metrics.go` | 已实现 | 13 个指标的记录点、封闭标签词表、六值 `result` 映射、丢弃型默认 sink |
| `internal/tunneltest/` | 已实现 | 内存 Gateway（Control 与 Serve 两种流）与多副本 Fleet、可推进的假时钟、假 `runtime.Manager`、假 Inference/Workflow Runtime、内存指标收集器（仅测试可见） |

上游依赖 `runtime` 包的进度：

| 依赖 | 状态 | 对隧道的影响 |
| --- | --- | --- |
| `runtime` 公共契约（接口、类型、能力、错误、流） | 已实现 | 帧 payload 直接镜像这些类型，proto 已定义 |
| `runtime.Manager`（健康状态机、Snapshot） | 已实现 | 控制流的状态上报可直接消费 `Snapshot()` |
| `runtime/openai` 共享客户端 | 已实现 | — |
| `vllm`、`sglang`、`ollama`、`comfyui` 适配器 | 已实现 | 位于 `common/runtime/<kind>/` |
| Gateway 侧隧道服务端 | 已实现 | `service/aiServeWeaveGateway/tunnelserver`，见该服务的 README；两侧共用同一份 proto 与 `common/tunnelwire` |
| Registry 服务 | 接口已定义、服务未实现 | **节点证书签发的唯一来源**；阶段 2 的 Agent 侧已按 proto 实现并对 fake Registry 验证，上线前需要真实 Registry |

因此隧道的阶段 1、3、4（协议、连接、槽池）**不依赖任何适配器**，可与 runtime 阶段 4 到 7 并行推进；阶段 2（身份）只依赖 proto 中的 `NodeIdentity` 接口形状，已完成。

阶段 1 新增两项第三方依赖：`google.golang.org/protobuf`（proto 运行时，转换层必需）和 `google.golang.org/grpc`（服务桩代码，阶段 3 起使用）。两者都在本文件「技术栈」中列出。

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

payload 统一使用 protobuf 消息，与 `runtime` 包的 Go 类型一一对应，转换集中在 `common/tunnelwire`，禁止在分发逻辑里内联字段拷贝。工作流模板 JSON 体积可观，走 `DataChunk` 而不是塞进 `RequestHeaders`，避免单帧超过 `MaxCallRecvMsgSize`。

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

**实现文件：** `identity.go`

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

节点身份写在证书的 **URI SAN** `aiserveweave://node/<node_id>` 上（`tunnel.NodeURI` 是两侧共用的构造函数），CN 同步写一份仅供人读。用 URI 而非 DNS SAN，是因为 `node_id` 是运维自己取的标签，不必是合法域名；对只会签 DNS SAN 的 CA，Agent 也接受"恰好一个 DNS SAN 等于 `node_id`"这一种形式。Gateway 校验的就是这个 SAN 与 `Hello.node_id` 是否一致，**CN 不参与校验**。

### 状态机

**实现文件：** `client.go`（状态机与重连）、`control.go`（Control 流）、`backoff.go`（退避）。

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

**实现文件：** `control.go`（应用层心跳）、`client.go`（`NewGRPCTransport` 里的传输层 keepalive）。

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

| 文件 | 职责 | 状态 |
| --- | --- | --- |
| `../../../api/proto/tunnel/v1/tunnel.proto` | 线上契约，Agent / Gateway / Registry 三方共用 | 已实现 |
| `common/tunnelwire`（原 `convert.go`） | proto ⇄ `runtime` 类型双向转换、`Operation` payload 契约表、`RuntimeError` ⇄ `TunnelError` | 已实现 |
| `identity.go` | bootstrap token、CSR、向 Registry 换证与轮换、文件权限校验、mTLS 配置 | 已实现 |
| `client.go` | 单条隧道的总装：连接状态机、退避重连、优雅下线；`Transport` 与 gRPC 实现 | 已实现 |
| `control.go` | Control 流：Hello、心跳、Snapshot 上报、配置下发应用、名册与槽位建议转发 | 已实现 |
| `backoff.go` | 全抖动指数退避 | 已实现 |
| `pool.go` | 单副本槽池水位管理、预热、空闲回收、分类配额、额度分摊 | 已实现 |
| `slot.go` | 单槽状态机、帧读写循环、per-request context 与取消 | 已实现 |
| `dispatch.go` | Operation 到 `runtime` 方法的分发、白名单校验、限流接入 | 已实现 |
| `manager.go` | 多副本连接表：按名册建连与断连、全局生命周期、聚合状态 | 已实现 |
| `roster.go` | 名册接收、版本去重、连接表差分、`max_gateways` 保护 | 已实现 |
| `internal/tunneltest/` | 跨测试共用的内存 Gateway 与 Fleet、假时钟、假 Manager 与假 Runtime、内存指标收集器 | 已实现 |
| `metrics.go` | 隧道指标：记录点、标签词表、`runtime.Metrics` 之上的类型化封装 | 已实现 |

计划中的文件已全部落地。

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
    renew_fraction: 0.33          # 剩余有效期低于总时长的这个比例即轮换

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

  control:
    hello_timeout: 10s          # 超时未收到 HelloAck 即判定为 failed
    drain_timeout: 30s          # 排空时等待在途请求的上限
    status_poll_interval: 2s    # 轮询 Manager.Snapshot 检测变更的周期
    status_full_interval: 60s   # 无变更时的全量对账周期

  limits:
    max_frame_bytes: 4Mi        # 单帧上限，SSE 事件远小于此
    max_request_bytes: 64Mi     # 工作流模板与输入文件
    max_deadline: 30m           # 下发 deadline 超过此值即按此值截断

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
- `node_id` 必须与证书 SAN 一致，不一致时 Agent 启动即失败，不等到运行时被 Gateway 拒绝；留空表示由 Registry 分配。
- `cert_file`、`key_file` 权限必须恰好 `0600`，`bootstrap_token_file` 必须对组与其他用户完全不可访问，违反即拒绝启动。
- `renew_fraction` 取值范围 `(0, 1)`，缺省 `1/3`。
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

除三个节点级指标外，所有隧道指标都带 `replica_id` 标签，否则无法区分是某个副本的链路问题还是节点整体问题。标签取值全部来自封闭枚举或本地配置，**任何来自对端的自由文本都不进标签**（`metrics.go` 的 `NodeScopedMetrics` 与 `metrics_test.go` 的 `metricLabelKeys` 是这条规则的可执行版本）：

```text
tunnel_connection_state{node_id,replica_id}          0=disconnected 1=connecting 2=connected 3=draining 4=retired 5=failed
tunnel_connected_replicas{node_id}                   当前已连上的副本数，掉到 0 才是真正离线（节点级，无 replica_id）
tunnel_roster_version{node_id}                       已应用的名册版本，副本间不一致说明广播有问题（节点级，无 replica_id）
tunnel_reconnects_total{node_id,replica_id,reason}
tunnel_control_heartbeat_rtt_seconds{node_id,replica_id}
tunnel_slots_total{node_id,replica_id,class,state}   state: idle|busy
tunnel_slot_acquire_failures_total{node_id,replica_id,class}
tunnel_limiter_rejections_total{node_id,runtime_id}  软配额超发后被硬配额拦下的次数（节点级配额，无 replica_id）
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
- `tunnel_stream_first_event_seconds` 与 Gateway 侧端到端 TTFT 的差值，是判断"慢在隧道还是慢在模型"的核心手段。它只统计**渐进式响应**（事件流与产物体）的首帧；一问一答的操作首帧就是全部答案，混在一起两个分布都失去意义。
- `tunnel_slot_acquire_failures_total` 非零说明副本接受了 Control 却拒绝 Serve（进程半死、流配额、代理故障）。Agent 在一次失败后暂停开槽 `slotOpenBackoff`，因此该计数器的斜率是有界的——它突然变陡意味着副本反复短暂恢复，而不是 Agent 在自旋。

**导出方式。** 这些指标由 `common/metrics` 的注册表接住，Agent 在 `-metrics-addr`（默认 `127.0.0.1:9091`，留空则关闭）上以 Prometheus 文本格式提供 `GET /metrics`。`Descriptions()` 是本包的目录——help 文本与直方图分桶跟指标常量写在同一个文件里，`main.go` 只负责把它交给 `metrics.New`。Agent 从不监听公网端口这条约束同样适用于这个端点，因此它默认只绑回环。

同一个注册表也交给了 `runtime.Dependencies.Metrics`：节点只有一个指标后端，「后端慢」与「隧道慢」才是可以互相相减的数字。

Gateway 侧有一份对称的服务端指标，前缀 `tunnel_server_`，刻意不与这里同名——两端从相反方向测量同一条链路，其分歧本身就有意义（这边数的是 Agent 执行了多少，那边数的是副本递交了多少，两者之差正是请求丢在哪里的答案）。清单见 [Gateway README「指标」](../../aiServeWeaveGateway/README.md#指标)。

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

### 实测数据与偏差

方法与出处见阶段 7 实测小节（`e2e/stress_test.go` 的 `TestTunnelSegmentLatency`；本机真实 Ollama + 真实 mTLS 隧道 + Gateway HTTP 前门的手工实测）。两组数字都来自同一台 Apple Silicon Mac：Gateway、Agent、Ollama 全部在本机，链路是 loopback，不是预算表假设的跨城网络。

| 环节 | 预算 | 实测 | 偏差说明 |
| --- | --- | --- | --- |
| 隧道单跳（Gateway ↔ Agent 往返，含两次序列化） | ≈ RTT/2（跨城 30ms RTT 场景下约 15ms） | **0.3ms**（loopback，20 次采样均值，`TestTunnelSegmentLatency`，与同进程直连对比） | 实测环境 RTT≈0，这个数字只证明了"隧道本身的序列化与调度开销可忽略"，验证的是预算表"因此隧道引入的额外开销约等于一个 RTT 加两次序列化"这句话里"两次序列化"那部分，不是"一个 RTT"那部分——跨城 RTT 的真实贡献仍需真实网络部署才能测。 |
| 端到端 TTFT，流式、模型已热 | 隧道自身贡献 ≤10ms，其余是模型成本 | **129ms / 293ms / 165ms**（三次采样，从 Gateway 收到 HTTP 请求到第一个 SSE chunk `Flush()`） | 与预算表"模型首 token：主要成本，不在隧道控制范围"一致：这组数字由 Ollama 生成首个 token 的真实推理延迟主导，隧道 + HTTP 前门的开销（量级与上一行的 0.3ms 相当）在其中可以忽略；三次采样间 130ms 左右的波动来自模型推理本身的抖动（GPU/内存带宽、系统当时的负载），不是隧道引入的抖动，因此不构成对隧道设计的偏差。 |
| 端到端 TTFT，非流式、模型冷启动 | 不适用——预算表假设的是流式路径 | 约 **10.0s** | 非流式端点的"首字延迟"定义上等于总响应时间，而这次测到的 10s 基本是 Ollama 把 19GB（Q4 量化）权重第一次读进内存的成本，衡量的是"模型冷启动"而不是"隧道额外开销"，两者是不同的问题。放在这里是为了说明这个数字为什么不该被拿来对照预算表，而不是作为预算达标或不达标的证据。 |

**预算表里仍未验证的两行：** "用户 → Gateway TLS 与鉴权 ≤5ms" 和 "Gateway → Agent 单程 ≈ RTT/2" 都以真实网络 RTT 为前提，而目前的实测环境 RTT 恒为 0——同一台机器上的三个组件互相用 loopback 通信。这两行要有实测数字，Agent 和 Gateway 至少要分处两台不同网络位置的机器，且用户需经真实公网访问 Gateway；这正是 README 别处反复强调的"阻塞点是没有部署，不是没有 Gateway"在这里的具体表现。

## 质量门禁

与 `runtime` 包同一套标准，每个阶段结束时全部通过才能勾选该阶段：

```bash
gofmt -l ./service/aiServeWeaveAgent/tunnel ./api
go vet ./...
go generate ./api/...   # 生成结果必须与仓库内文件一致
go test ./service/aiServeWeaveAgent/tunnel/...
go test -race ./service/aiServeWeaveAgent/...
```

补充要求：

- `gofmt -l` 必须无输出；所有导出标识符具备以标识符开头的完整 doc comment。
- 协程泄漏在 `main_test.go` 的 `TestMain` 中统一断言，不逐个测试手写检查。
- 测试不使用真实 `time.Sleep` 推进时间，一律通过注入的 `runtime.Clock` 控制。
- 生成代码（`*.pb.go`）不手工编辑；改动只发生在 `.proto` 上，随后重新生成。
- 新增第三方依赖需在阶段说明中列出理由。
- 表驱动测试的每个用例必须有可读 `name`，失败信息包含期望值与实际值。
- 公共 API 变更同步更新本 README 的协议定义与文件职责表，README 与代码不一致视为缺陷。

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

- [x] 写 `runtime` 类型 ⇄ proto 的往返测试：`ChatRequest` 的指针字段为 nil 时不出现在 proto 中、显式 0 值原样保留、`Extra` 原样透传。
- [x] 写 `RuntimeError` ⇄ `TunnelError` 往返测试：错误码、`retryable`、哨兵 `Cause` 在对端可被 `errors.Is` 匹配。
- [x] 写 payload 编码测试：每个 `Operation` 的请求与响应类型对应关系，未知 Operation 返回 `ErrorProtocol`。
- [x] 运行 `go test ./service/aiServeWeaveAgent/tunnel -run TestConvert`，确认失败。
- [x] 定义 proto、生成代码、实现双向转换。
- [x] 运行质量门禁全部命令。

实现说明（正文只给了名字、没给定义的地方，以及为落地补的决定）：

- **`Operation` payload 契约做成了数据表而不是散落的 switch。** `OperationSpec{Request, Response, Shape, RequestBody}` 由 `SpecFor` 查表返回，是「payload 编码约定」那张表的唯一实现；`TestConvertOperationSpecCoversEveryOperation` 遍历 proto enum 断言无遗漏，新增 Operation 忘了配表会在这里失败，而不是等到阶段 5 的分发期。`ShapeStream` 的 doc comment 里写明了「逐条发送」的理由，避免后人把它当成可批量化的实现细节。
- **哨兵 `Cause` 用固定名字表跨线，非哨兵 `Cause` 一律丢弃。** `RuntimeError.Cause` 按 `runtime` 包的契约可以携带未脱敏内容（上游 URL、响应体、凭据），而 `Message` 是已脱敏的。因此 `TunnelError.cause` 只写 `sentinelWireNames` 里的 11 个名字（9 个 runtime 哨兵加 `context.Canceled`/`context.DeadlineExceeded`），其余置空。`TestConvertErrorDropsUnsanitizedCause` 用一个含 API Key 的 `Cause` 断言序列化结果里搜不到凭据和上游地址。
- **非 `RuntimeError` 的裸错误只保留分类，文本换成常量。** 正文没规定这种情况。`context.DeadlineExceeded` → `ErrorTimeout`，`context.Canceled` → `ErrorConnection`（错误码表里没有单独的「已取消」，而 `ErrorConnection` 正是隧道拆链时终止在途请求用的码），其余 → `ErrorUpstream` 加固定文案。原始文本不上线，节点本地日志仍有完整错误。
- **`optional` 而不是普通 proto3 标量。** `ChatRequest` 的 `temperature`/`top_p`/`max_tokens`/`seed` 和 `EmbeddingRequest.dimensions` 都用 proto3 presence，与 Go 侧指针一一对应；普通标量会把「显式 0」静默降级成「未设置」，这正是 `runtime/types.go` 用指针要避免的事。`TestConvertChatRequestOptionalFieldPresence` 不只看结构体，还跑一遍真实 marshal/unmarshal。
- **`RuntimeConfig` 拆成 `RuntimeConfig`（动作）+ `RuntimeSpec`（配置本体）。** 正文只说「`runtime.Config` 的镜像，不含 `APIKey` 明文，只含 `api_key_ref`」，没说 Add/Replace/Remove 怎么表达；这里用 `ConfigAction` 枚举承载，`REMOVE` 只需要 `runtime_id`。`ConfigToProto` 的签名是 `(cfg, apiKeyRef)`，从类型上就无法把 `cfg.APIKey` 送上线。
- **时长一律显式编码，包括 0 和负值。** `runtime.Config` 把 0 读作「用默认值」、负值读作「显式关掉默认」，所以 `durationToProto` 不做「零值省略」优化，否则控制面下发的 `-1` 会在对端变成 0 并悄悄启用默认值。
- **空 JSON 与 nil 不作区分。** `json.RawMessage` 映射为 `bytes`，长度为 0 的一律归一化成 nil（空 JSON 文档本就不是合法 JSON）。同理，非 nil 的空 slice 往返后变成 nil，这是 protobuf repeated 字段的固有语义。
- **`WorkflowRequest` 的 `Template` 在 proto 里根本没有字段**，而不是「有字段但约定不填」——契约层面杜绝有人图省事把模板塞进 `RequestHeaders`。
- **`ModelList` 信封**：protobuf 没有顶层 repeated，`LIST_MODELS` 的 `[]Model` 需要一个包装消息。
- `NodeIdentity` 的 `Register`/`Renew` 请求响应按最小可用形状定义（`node_id`、CSR、bootstrap token、证书与 CA 的 PEM、`not_after`）；`node_id` 究竟是控制台预分配还是 Agent 提交候选（待决问题 3）不影响这个形状，响应里的 `node_id` 始终是权威值。
- proto 生成命令固化在 `api/proto/tunnel/v1/generate.go` 的 `go:generate` 里，`go generate ./api/...` 的输出与仓库内文件逐字节一致。

**验收：** 转换层是隧道与 `runtime` 的唯一耦合点；`grep` 不到分发逻辑里的字段级拷贝。这条约束针对的是**类型镜像**：只有 `common/tunnelwire` 可以读写 `runtime` 的数据类型字段，其余文件只允许使用 `runtime.Clock`、`runtime.RuntimeError` 与 `runtime.ErrorCode` 这类横切设施（`identity.go` 即是如此）。

### 阶段 2：节点身份与证书

**文件：** 创建 `tunnel/identity.go`、`identity_test.go`。**前置：** Registry 的 `NodeIdentity` 服务已定义接口。

- [x] 写测试：bootstrap token 换证成功后 token 文件被删除、证书权限非 0600 时拒绝启动、`node_id` 与 SAN 不一致时启动失败、剩余有效期低于 1/3 触发轮换。
- [x] 写 TLS 配置测试：最低版本 TLS 1.3、必须携带客户端证书、CA 校验生效。
- [x] 写多副本共用测试：同一份证书可用于建立到多个不同 endpoint 的连接。
- [x] 运行包测试，确认失败。
- [x] 实现密钥生成、CSR、向 Registry 调用 `Register`/`RenewCertificate`、证书落盘与轮换判定。
- [x] 运行质量门禁全部命令。

实现说明（正文只给了名字、没给定义的地方，以及为落地补的决定）：

- **身份错误与传输错误分成两类，用 `ErrFatal` 标记。** 正文只说"证书类错误进入 `failed` 终态"，没说怎么判定。`IsFatal(err)` 是阶段 3 状态机的唯一判据：权限不对、`node_id` 与证书不符、证书不挂在配置的 CA 下、Registry 回 `Unauthenticated`/`PermissionDenied`/`InvalidArgument` 都是致命的，重试一万次也是同样结果；`Unavailable`/`DeadlineExceeded`/`ResourceExhausted` 只说明 Registry 此刻不可用，标 `Retryable` 交给退避。两类错误都是 `*runtime.RuntimeError`，与隧道其余部分同一种错误类型。
- **Registry 不可达时不下线。** `Ensure` 在轮换失败且错误可重试时记一条 WARN 并**继续使用当前证书**——证书还有效，Registry 抖动不该让节点整体退服；只有致命错误才向上返回。这是"剩余 1/3 就开始轮换"这个提前量的意义所在。
- **轮换连私钥一起换。** `Renew` 生成新密钥再签，而不是拿旧密钥换新证书。ECDSA P-256 的密钥生成开销可以忽略，30 天一次的轮换顺带缩短了私钥的暴露窗口。密钥用 PKCS#8 PEM 落盘，`0600`。
- **文件权限只校验不修复。** 证书与私钥的权限必须**恰好** `0600`，发现更宽松时报致命错误而不是 `chmod` 收紧：既然已经宽松过，就可能已经被别的用户读走，静默收紧等于掩盖一次泄露。bootstrap token 放宽到"组和其他用户无任何权限"，因为它由控制台生成、由用户手工放置。
- **三个文件都是原子写，且先写私钥后写证书。** 同目录临时文件加 `rename`，读者不会看到写了一半的身份文件。顺序上私钥优先：只有私钥没有证书还能重新注册，只有证书没有私钥则完全不可用。
- **落盘之前先完整校验。** `adopt` 会验证证书与私钥配对、证书挂在返回的 CA 下、SAN 里的 `node_id` 与本地配置一致、响应的 `not_after` 与证书本身一致，任何一条不过都**不写文件**，避免一个应答异常的 Registry 破坏磁盘上仍然可用的身份。
- **加载已存证书时把"过期"和"链不对"分开。** 直接用当前时间做链校验的话，过期证书会得到一句"不挂在 CA 下"的误导性报错。加载路径把校验时间落在证书自身有效期内，只判断链与用途，过期与否由 `Ensure` 显式判断并给出可操作的报错；刚签发的证书则一律按当前时间校验，Registry 给回一张已过期或尚未生效的证书会被当场拒绝。
- **证书过期后若本地存在 bootstrap token 则重新注册**，否则报致命错误要求换发新 token。系统时钟早于 `NotBefore` 时同样报致命错误并提示查时钟——这在家用 Mac 上比证书本身出问题更常见。
- **Registry 连接抽象成 `RegistryConnector`。** `Connect(ctx, id)` 的 `id` 为 nil 表示 bootstrap，是全契约里唯一不带客户端证书的调用；非 nil 则用当前身份做 mTLS。测试据此断言 `Register` 从不带证书、`RenewCertificate` 一定带证书，同时整个阶段不需要真实 Registry 或任何监听端口。生产实现是 `NewGRPCRegistryConnector`。
- **`Identity.TLSConfig()` 每次返回一份新的 `*tls.Config`**，TLS 1.3 起步、始终携带节点证书、始终用 Registry CA 校验对端。返回副本而不是共享指针，是为了多副本下一条隧道设置 `ServerName` 不会污染另一条；`ServerName` 由 gRPC 从各自的 dial target 推导，因此同一份身份天然服务全部副本。
- **`node_id` 允许留空**（待决问题 3 的两种方案都不阻塞）：留空则 CSR 不带 SAN、由 Registry 分配并在响应里给出权威值；配置了就必须与 Registry 返回的一致，不一致按配置错误拒绝，绝不"静默改名"上线。
- **端点校验 `validateEndpoint` 落在本文件**，`host:port`，禁止 scheme 与路径。阶段 6 的名册校验会复用同一个函数，避免两处规则漂移。

**验收：** 私钥不出网；证书类错误进入 `failed` 终态而非无限重连；Gateway 侧不持有任何签发能力。测试用一次性的进程内 CA 与 fake Registry，通过 `net.Pipe` 上的真实 mTLS 握手验证同一份证书能连上两个不同名字的副本，不监听任何端口。

### 阶段 3：单条隧道的 Control 流与连接状态机

**文件：** 创建 `tunnel/client.go`、`tunnel/control.go`、`tunnel/backoff.go` 及对应测试；创建 `tunnel/internal/tunneltest`（fake Gateway）。

- [x] 写状态机测试：Hello 成功进入 connected、心跳连续 3 次失败进入 reconnecting、收到 Shutdown 进入 draining、证书错误进入 failed 不重连。
- [x] 写退避测试：全抖动落在 `[0, min(30s, 2^n))`，注入 `runtime.Clock` 控制时间，不使用真实 sleep。
- [x] 写 Snapshot 上报测试：状态变化立即上报、无变化时 60s 全量、上报内容不含 `APIKey` 与 Header 值。
- [x] 写配置下发测试：`RuntimeConfig` 触发 `Manager.Add`/`Replace`/`Remove`，失败通过下一帧 `RuntimeStatus` 反映。
- [x] 运行包测试，确认失败。
- [x] 实现连接状态机、Control 流收发、退避重连、优雅下线。
- [x] 运行 `go test -race`，确认无协程泄漏。

实现说明（正文只给了名字、没给定义的地方，以及为落地补的决定）：

- **`Transport` 与 `ControlStream` 两个窄接口把 gRPC 挡在状态机之外。** 生产实现是 `NewGRPCTransport`（mTLS + 20s/10s keepalive，取自「心跳」一节），测试用 `internal/tunneltest.Gateway` 的内存实现——因此整个阶段没有监听端口、没有 TLS 握手、没有真实网络，却仍然逐帧驱动真实的收发逻辑。阶段 4 给 `Transport` 加 `Serve` 方法即可，状态机不动。
- **`Run` 的三种返回语义。** `nil` 表示 ctx 取消后已优雅排空；`ErrShutdownRequested` 表示副本主动请我们离开（正常终态，是否重连交给阶段 6 的名册决定，Client 自己绝不重拨）；`IsFatal(err)` 为真表示身份/协议层面的死结，进 `failed`。其余错误一律不外露，内部退避重连。阶段 2 的 `ErrIdentityFatal` 与本阶段的连接级致命错误合并成同一个 `ErrFatal`/`IsFatal`——状态机只需要一个判据，两个哨兵只会让调用方漏判其一。
- **取消 ctx 触发排空而不是切流。** 流挂在 `context.WithoutCancel(ctx)` 上，Client 自己在排空结束后取消它。否则 SIGTERM 会让 `Draining` 帧发不出去，在途请求以连接错误告终——正是「优雅下线」要避免的。
- **握手失败一律是致命的。** 首帧不是 `HelloAck`、`HelloTimeout` 内无应答、`Unauthenticated`/`PermissionDenied`/`Unimplemented`，都进 `failed` 不重连；`Unavailable`、流中断等传输问题才退避重试。README 只写了「Hello 失败/证书无效 → failed」，这里把它落成 gRPC 状态码的具体划分。
- **心跳判死取"检查在发送之前"。** 每个心跳周期先看未被 ack 的心跳数是否已达阈值，达到就判死，否则发一帧并计数加一。默认 15s/3 次意味着最坏 60s 判死。收到 `HeartbeatAck` 清零并按回显的时间戳记录 RTT（`HeartbeatRTT()`，阶段 7 的 `tunnel_control_heartbeat_rtt_seconds` 直接取它），两侧都不需要信任对方的时钟。
- **状态上报用轮询 + 物料指纹。** `runtime.Manager` 没有变更订阅，因此以 `StatusPollInterval`（默认 2s）轮询 `Snapshot()`；比较的是**清掉 `UpdatedAt`/`ProbedAt`/`CheckedAt`/`Latency` 之后**的确定性 proto 序列化结果——这些字段每次健康检查都在变，拿它们比较会让"变更触发上报"退化成刷屏。实例集合发生增减时一律发全量（子集表达不了"删除"），否则只发变化的那几个实例且 `full=false`；60s 的全量对账照常。
- **配置下发的失败不打断隧道。** 白名单拒绝、Secret 解析失败、`Manager.Add` 报错都只写日志，随后照常发一帧**全量** `RuntimeStatus`——协议里没有 ack 帧，副本正是通过"这个实例没出现在报告里"观察到失败的。反过来，一个坏配置也不该让一条健康隧道掉线。
- **`api_key_ref` 走 `SecretResolver` 接口，没有配置解析器时直接拒绝**下发的带凭据配置，而不是装一个没有凭据的运行时。待决问题 4 的三种实现（文件、环境变量、外部 Secret 管理器）都落在这个接口后面，与 runtime 阶段 8 的结论对齐时不必改隧道。
- **`Remove` 不过白名单，`Add`/`Replace` 必须过。** 白名单防的是"被攻破的 Gateway 让 Agent 去连未声明的地址"，删除动作不含地址；最坏是拒绝服务，而拒绝服务本来就在副本能力范围内。空白名单表示调用方未收窄，`main.go` 负责按 README 的语义传入 runtimes 段的全部 id。
- **`rearmingTimer` 而不是 ticker。** `runtime.Clock` 只提供一次性定时器（这是它可被测试完全控制的原因），因此周期任务在每次触发后重新 arm；慢一次只是推迟下一次，不会堆积。
- **读取用独立 goroutine，生命周期与单次连接严格绑定。** `controlReader` 把阻塞的 `Recv` 变成 channel，`connectAndServe` 的 defer 顺序保证先 cancel 流 ctx、再等 reader 退出，因此 `TestMain` 的协程泄漏断言对每一次重连都成立。
- **测试的时间同步靠"已 arm 的定时器计数"。** `tunneltest.Clock` 记录累计 arm 次数并对已过期的注册立即触发，测试推进时钟前先等待被测代码确实 arm 到了预期数量——否则推进可能"越过"一个尚未注册的定时器，让测试挂死而不是失败。

**验收：** 断网、Gateway 重启、证书过期三种场景下的行为均可预测且有明确日志。全部测试用内存 Gateway 与假时钟，`go test -race` 无数据竞争、无协程泄漏。

### 阶段 4：槽池与 Serve 流

**文件：** 创建 `tunnel/pool.go`、`tunnel/slot.go` 及对应测试。

- [x] 写水位测试：启动预热 `min_slots`、空闲降到 `low_watermark` 触发补充、超过分摊后的上限拒绝新建、空闲超时回收但保底 `min_slots`。
- [x] 写额度分摊测试：`per_replica_max = max(min_slots, ceil(node_total / active_replicas))`，副本数为 1、3、100 时结果符合预期。
- [x] 写分类隔离测试：`BULK` 槽耗尽不影响 `INFERENCE` 槽的获取。
- [x] 写复用竞态测试：`ResponseEnd` 之后到达的 `Cancel` 帧不影响复用该槽的新请求。
- [x] 写槽作废测试：单槽协议错误只关闭该槽，同连接其他槽继续服务。
- [x] 写槽轮换测试：达到请求数或存活时长上限后主动重建。
- [x] 运行包测试，确认失败。
- [x] 实现槽池水位管理与单槽帧循环。
- [x] 运行 `go test -race`，确认无协程与连接泄漏。

实现说明（正文只给了名字、没给定义的地方，以及为落地补的决定）：

- **`Handler` 与 `ResponseSink` 两个窄接口把分发挡在槽外。** 正文的「Agent 侧请求分发」整节都属于阶段 5，因此本阶段只定义槽与分发之间的边界：槽负责帧循环、请求生命周期与槽复用，`Handler.Handle(ctx, *Request, ResponseSink)` 负责一切与 `runtime` 有关的事。槽不认识 `runtime_id`、不认识 `Operation`、不解 payload，`dispatch.go` 落地时只需实现这一个接口，`pool.go`/`slot.go` 不动。
- **收帧循环与请求处理分属两个协程。** 处理器跑在自己的协程里，收帧循环继续转发请求体分片与 `Cancel`——否则一次 Artifact 上传会自我死锁（处理器在等 body，收帧循环在等处理器）。两个协程都写同一条流，因此所有发送经过 `slot.send` 的互斥锁，满足「一条 gRPC 流同时只能有一个 sender」。
- **`Request.Body` 是无缓冲 channel，处理器不读就丢。** 正文只说「不得无界缓冲」。收帧循环投递分片时同时 select 请求 ctx：处理器一返回 ctx 即取消，在途分片被丢弃而不是堆积，也不会把收帧循环钉死在一个永远不读 body 的处理器上。`TestSlotIgnoresABodyNobodyReads` 断言这种槽随后仍可复用。
- **迟到帧分两类，只有一类杀槽。** 迟到的 `Cancel` 与迟到的 `DataChunk` 都按 `request_id` 不匹配丢弃：处理器提前返回（背压、上游报错）时，Gateway 还在发的请求体分片本就是常态，为此作废一条流代价太大。真正的协议错误只有两种——槽忙时又派进一个请求，以及 `RequestHeaders` 不带 `request_id`——这两种说明两侧对槽的状态认知已经分叉，后续任何帧都不可信，因此关闭该槽。
- **单槽故障的隔离靠「一槽一 context」。** 每个槽的流挂在自己的 `context.WithCancel` 上，作废一个槽只取消它自己；同一条 TCP 上的兄弟槽是独立的 HTTP/2 stream，既不共享 context 也不共享发送锁。`TestSlotProtocolErrorClosesOnlyThatSlot` 在同一个 Gateway 上验证这一点。
- **正在打开的槽计入水位。** 补槽判据是 `idle + opening < low_watermark` 而不是 `idle < low_watermark`：从「决定开槽」到「收到第一帧 Ready」之间会发生多次 reconcile，只看 idle 会让这段窗口里的每一次 reconcile 都再开一条流，压测下槽数不收敛。这正是验收条款「数量稳定收敛」要防的失败。
- **槽的增删只发生在一个协程里。** `Pool` 的槽状态回调（`slotIdle`/`slotBusy`/`slotFinished`）只改计数并往 `wake` 投一个 token，真正的开槽与回收统一在维护协程的 `reconcile` 里做。这样不存在两个协程同时为同一条水位补槽的竞态，也不需要在锁里起协程。
- **`BULK` 配额整份常驻。** 正文只说 `bulk_slots` 是配额。这里取 `floor = low = ceiling = bulk_slots`：物理隔离只有在产物请求到达时确实有一条 BULK 槽待命才成立，按需新建等于把建流延迟加回产物路径。`bulk_slots` 配成负数表示该节点不承接产物——0 无法与「未配置」区分，会被默认值覆盖。
- **年龄轮换不受 `min_slots` 约束，请求数轮换在请求边界发生。** 空闲槽只要超过 `MaxSlotAge` 就在同一轮 reconcile 里换掉（同一轮就补回来），否则安静节点上的流会因为「已经在最低水位」而无限期存活；忙槽则在当前请求 `ResponseEnd` 之后才退，绝不打断在途请求。
- **`SlotHint` 只能在本地配置范围内调整。** `min` 可被抬高但不超过 `per_replica_max`，`max` 可被压低但不超过本地上限，`bulk` 只能调小。副本永远无法让 Agent 开出超过本地配置的流数，这与「名册是受信输入但仍需校验」是同一种防御姿态。
- **槽池挂在连接的 context 上，不是 `Run` 的 ctx 上。** `Client.connectAndServe` 用 `streamCtx` 启动槽池：一次重连让旧槽全部作废、在途请求以明确错误结束，正是正文要求的「在途请求不续传」；而取消 `Run` 的 ctx 走的是排空路径，槽池活到排空结束。`Client.InFlight` 默认取槽池的忙槽数，因此 `drain` 无需额外接线就会等在途请求跑完。
- **处理器 panic 只赔一个请求和一条槽。** 兜底 recover 把 panic 转成固定文案的 `ErrorUpstream`——panic 值可能引用请求内容，不上线；本地日志只记 stack，不记 panic 值本身。
- **`OnServing` 承载 connected ⇄ serving 跳变。** 槽池拿到第一条已 park 的槽即报 serving，最后一条消失即回落，`Client` 据此改状态且不覆盖 `draining`/`failed` 等终态。

**验收：** 槽池在压测下数量稳定收敛；任意单槽故障不产生连锁失败。全部测试用内存 Gateway 与假时钟，`go test -race` 无数据竞争、无协程泄漏。

### 阶段 5：请求分发接入 runtime

**文件：** 创建 `tunnel/dispatch.go`、`dispatch_test.go`；修改 `service/aiServeWeaveAgent/main.go`。

- [x] 写白名单测试：未在 `allowed_runtimes` 中的 `runtime_id` 被拒绝，即使 `Manager` 中存在该实例。
- [x] 写九个 `Operation` 的分发测试，用 fake Runtime 覆盖成功、能力不支持、背压和上游错误四类结果。
- [x] 写流式测试：每个 `ChatEvent` 独立成帧、`Committed` 后取消不产生重试、`ResponseEnd` 携带终止错误。
- [x] 写额度测试：panic、取消、提前返回三条路径下额度都被释放。
- [x] 写 deadline 测试：取下发值与本地上限的较小者。
- [x] 运行包测试，确认失败。
- [x] 实现分发器并在 `main.go` 中装配 Manager 与隧道客户端。
- [x] 运行 `go test -race ./service/aiServeWeaveAgent/...`。

实现说明（正文只给了名字、没给定义的地方，以及为落地补的决定）：

- **fake Runtime 放在 `tunnel/internal/tunneltest`，不是 `runtimetest`。** 本节原计划复用 `runtime/internal/runtimetest`，但 Go 的 internal 规则把它锁在 `runtime/` 子树里，隧道包 import 不到；而且它只实现了基础 `Runtime`，没有分发需要的 Inference/Workflow 方法。因此在隧道自己的 `internal/` 下加了 `BaseRuntime` + `InferenceRuntime` + `WorkflowRuntime` 三个可脚本化的假实现，与 `runtimetest` 的写法一致。
- **限流器由分发器持有，按实例。** `runtime.Manager` 不做请求代理，`Limiter` 是独立类型，正文说的"limiter 取额度"没指明谁来建。分发器按 `runtime_id` 持有一张表，值上记着建表时的 `Descriptor`；`Descriptor` 是可比较结构，因此一次 `Replace`（换地址或换 `MaxConcurrent`）会自然产生新的限流器，旧的继续把已放行的请求排空。`Manager.Get` 找不到实例时顺手删掉对应条目，反复重配的节点不会攒下死闸门。
- **`deadline` 用注入的 `Clock` 算预算，用 `context.WithTimeout` 落地。** `context` 的定时器永远走真实时间，把假时钟推导出的绝对时刻交给它，请求会在开始之前就过期。因此 `Clock` 只负责回答"还能跑多久"，真实定时器负责执行。下发的 deadline 已经过期时直接返回 `ErrorTimeout` 且不碰后端——为一个没人等的结果占一次后端并发没有意义。
- **`max_deadline` 是截断而不是拒绝。** 正文配置注释与校验规则原本一处写"拒绝"、一处写"截断"，这里按校验规则实现（取较小者），并把注释改成一致的说法。
- **白名单拒绝与实例不存在返回同一个码。** 两者都是 `ErrorInvalidConfig`，只有 `Cause` 不同（后者带 `ErrRuntimeNotFound`）。探测"这个节点上有哪些 runtime"因此拿不到额外信息。空 `runtime_id` 一律拒绝，即使白名单为空——"未收窄"不等于"连没有 id 都行"。
- **能力缺口用接口断言判定，不重复 `Require`。** 后端没实现 `InferenceRuntime`/`WorkflowRuntime` 就是 `ErrorCapability` 加 `ErrCapabilityUnsupported`，让副本换节点；模型级能力门禁仍然只在 `runtime` 适配器入口做一次。
- **`Committed` 之后的错误一律清掉 `Retryable`。** 正文说"已向用户发出首个 token 的流不得重试"，但没说在哪一层保证。分发器在流已提交时把 `RuntimeError.Retryable` 改成 false 再返回，产物流也一样（第一块字节发出即算提交）。这样即使 Gateway 侧调度器忘了检查，重试也不会发生。
- **产物边读边发，读缓冲就是 `max_frame_bytes`。** 500MB 的产物只占一个缓冲区，不做整体读入；每块发送前都检查帧上限，请求体（工作流模板）在收集过程中就按 `max_request_bytes` 判超，而不是先攒完再拒。
- **`RequestHeaders.trace` 不进日志。** 协议允许携带 `tenant_id` 等固定键，但日志白名单里没有它们，分发器因此完全不读 trace——它是副本自己做关联用的。
- **`main.go` 的隧道参数走 flag。** Agent 还没有配置文件（runtime 实例同样尚未从磁盘加载），因此 `-gateway`、`-registry`、证书三件套与 `-allowed-runtimes` 暂时用 flag 表达，命名与 README 配置段的键一一对应，配置文件落地后整体替换。不传 `-gateway` 时隧道不启动，Agent 行为与之前一致。（阶段 6 起 `-gateway` 接受逗号分隔的种子列表，并由连接表接管。）

**验收：** 对着 fake Gateway 可以完成全部九个 Operation；`runtime` 层的错误语义无损跨隧道传递。全部测试用假 Runtime 与假时钟，不依赖真实后端或网络。

### 阶段 6：多副本连接管理

**文件：** 创建 `tunnel/manager.go`、`tunnel/roster.go` 及对应测试；修改 `client.go`、`pool.go`。

- [x] 写种子连接测试：`gateway_endpoints` 中部分不可达时仍能启动，只要有一个成功。
- [x] 写名册差分测试：新增副本自动建连、移除副本进入 `retired` 并关闭、`draining` 停止补槽但在途请求跑完。
- [x] 写版本去重测试：旧 `version` 的名册被忽略，不会让已关闭的副本复活。
- [x] 写名册降级测试：名册为空或 Registry 不可用时保留最后一份有效名册，不清空连接表。
- [x] 写上限保护测试：名册超过 `max_gateways` 时截断并告警。
- [x] 写故障隔离测试：一条隧道进入 `reconnecting` 或 `failed` 时，其他隧道的连接、槽池和在途请求均不受影响。
- [x] 写额度重算测试：`active` 副本数变化时各副本槽位上限同步调整，不出现额度真空。
- [x] 写证书轮换测试：新证书生效时逐条隧道重连，不同时中断全部连接。
- [x] 运行包测试，确认失败。
- [x] 实现连接表、名册处理与聚合状态上报。
- [x] 用多个 fake Gateway 做集成测试：验证同时连上 3 个，任一被 kill 后其余继续服务。
- [x] 运行 `go test -race`，确认无协程与连接泄漏。

实现说明（正文只给了名字、没给定义的地方，以及为落地补的决定）：

- **连接表以 `endpoint` 为键，不是 `replica_id`。** 正文两个字段都提到了，但只有 endpoint 在任何时刻都存在：种子只有地址，`replica_id` 要到 `HelloAck` 才知道，而拨号用的正是地址。`replica_id` 在名册或握手给出后记在条目上，只用于日志与指标。
- **种子在第一份名册到达后即失效，包括名册没列出的种子。** 正文说「名册无、本地有 → 标记 retired 并关闭」，没说种子算不算「本地有」。这里算：名册是权威的，种子只是入口；否则一个写错的种子地址会被永远重拨。`Roster.Seed` 因此不设置「已接受」标记，第一份名册无论 version 多少都覆盖它。
- **自行退出的隧道要等「新消息」才重建。** 副本发 `Shutdown` 让 Agent 离开、或证书被拒进 `failed` 之后，reconcile 若立刻重连就是死循环。连接表为此记一条 `stoppedRecord{version, generation}`，只有名册版本前进或证书轮换才重建——这正是正文「是否重连交给阶段 6 的名册决定」的落地方式。被连接表自己关掉的隧道不记录，否则轮换后就再也起不来。
- **「全部副本失败」才让 `Run` 返回错误。** 不可达的副本永远不会让 `Client.Run` 返回（它自己退避重试），因此只有名册里每个副本都留下了 `IsFatal` 记录、且表里一条隧道都不剩时才向上报错。这样「三个种子挂了两个」不影响启动，而「证书被所有副本拒绝」会明确失败。
- **证书轮换按 `generation` 计数逐条替换。** 身份是节点级的，一次轮换要换掉所有隧道；同时换会让节点对全部副本瞬时不可达，正是轮换要避免的事。连接表记录每条隧道建立时的证书代次，轮换时逐条 stop → open → 等它回到 `connected`（上限 `RotateTimeout`，超时就继续下一条而不是卡住全部）。判断「是否换了证书」用 `NodeID` + 有效期窗口，因为轮换必然连私钥一起换、必然产生新的有效期。
- **Registry 不可达不下线，证书致命错误一次性上报。** `IdentityManager.Ensure` 已经把可重试的轮换失败吞掉并返回当前证书，连接表只需区分致命与否：致命就直接结束 `Run`（证书是节点级的，报一次而不是每副本报一次），非致命只记一条 WARN。
- **`draining` 落到槽池的三个上限一起归零。** 正文只说「停止补槽、等在途结束」。实现上 `Pool.SetDraining(true)` 让 floor/low/ceiling 全为 0，并在 reconcile 里立刻回收空闲槽——空闲槽存在的唯一意义就是被派活，副本已经不派了。忙槽不受上限约束（reconcile 从不关忙槽），所以在途请求跑完为止。
- **名册状态与额度在表稳定之后统一下发。** 先建连、再关连、最后统一 `SetActiveReplicas`/`SetDraining`，这样新加入的副本已经计入分母，不会出现某条隧道短暂按旧副本数分配额度的「额度真空」。
- **`max_gateways` 截断是确定性的。** 按 endpoint 排序取前 N，同一份名册两次到达得到同一个子集，连接表不会来回抖动。它是防污染的安全阀而不是调度策略——真出现超限，要修的是名册。
- **`ManagerConfig.Client` 是模板而不是十几个平铺字段。** 每条隧道的 `ClientConfig` 从模板拷贝，只覆盖 `Endpoint` 与三个回调；否则连接表要逐字段转发心跳、状态上报、退避、槽池等全部配置，加一个字段就要改两处。
- **测试用 `tunneltest.Fleet`：一组按 endpoint 寻址的内存副本。** 任何被拨的地址都会即时生成一个副本（名册扩容本来就会指向没预先注册的地址），`Unreachable` 模拟宕机。整套多副本测试没有监听端口、没有 TLS、没有真实网络，`fakeIdentities` 直接构造 `tunnel.Identity` 来驱动证书轮换，因此也不需要 CA。

**验收：** 副本扩缩容无需重启 Agent；单副本故障不影响其他副本；`tunnel_connected_replicas` 准确反映可用链路数（`Manager.ConnectedReplicas`）。三副本集成测试验证每个副本都能独立完成推理，kill 其一后其余继续服务。

### 阶段 7：指标、端到端联调与压测

**文件：** 创建 `tunnel/metrics.go`、`metrics_test.go`、`stress_test.go`；修改本 README 的实测数据小节。

- [x] 接入本文件「可观测性」列出的全部指标，写标签基数测试（禁止 payload 内容进标签），断言所有指标带 `replica_id`。
- [x] 压测：并发拉满槽池，验证无空闲槽时返回背压而非排队；观察 `tunnel_limiter_rejections_total` 是否在可接受范围，据此校准 `node_total`。
- [x] 故障注入（内存副本可表达的部分）：kill 单个 Gateway 副本、副本接受 Control 但拒绝 Serve，两种场景各有断言恢复行为的测试。
- [x] 落地 Gateway 侧隧道服务端（`service/aiServeWeaveGateway/tunnelserver`），三边共用 `common/runtime` 与 `common/tunnelwire`。
- [x] 与 Gateway 侧完成端到端联调：真实 TCP + 真实 mTLS + **3 个 Gateway 副本**，跑通非流式与流式 Chat（`service/aiServeWeaveGateway/e2e`）。
- [x] 验证每个副本都能独立完成完整推理，且请求路径上无副本间转发。
- [x] 实测隧道段 TTFT 与直连对比，记录差值；确认多副本未引入额外延迟。
- [x] 故障注入：kill 单个副本后其余继续服务；副本在原地址重启后 Agent 自行找回。
- [x] 接上真实后端（本机 Ollama）跑通 SSE 流式 Chat，并把 Gateway 的 HTTP 前门算进端到端 TTFT。
- [x] 故障注入剩余四场景：拔网线、kill 全部副本、证书过期、后端假死，各记录恢复时间与用户侧表现（`service/aiServeWeaveGateway/e2e/faultinjection_test.go`，`AISW_FAULT_INJECTION=1` 开启）。
- [x] 滚动升级演练：配合 Gateway 侧逐个替换副本，确认 Agent 全程保持至少一条可用隧道（`service/aiServeWeaveGateway/e2e/rollingupgrade_test.go`，跑在常规 `go test`，无需 `AISW_FAULT_INJECTION`）。
- [ ] 长稳测试：24h 连续运行，验证无内存增长、无协程泄漏、连接数与槽数稳定（工具已落地为 `TestSoak`，见下；本次 24h 运行进行中，完成后补实测数据并勾选）。
- [x] 把实测数据填入本 README 的「首字延迟预算」并说明偏差原因（见该节「实测数据与偏差」；隧道单跳与真实 Ollama 端到端两组数字已有，跨城 RTT 相关两行仍标注为待真实多机部署验证，不是数字缺失）。

**剩下四项的阻塞点是「没有部署」，不再是「没有 Gateway」。** 隧道服务端、Gateway 调度器与 OpenAI 前门都已经存在，真实 Ollama 的端到端 SSE 也已经用真实机器跑通；剩下的是需要一根会断的网线、一张会过期的证书、24 小时这类真实时间和真实故障的事。这些不能用测试代替，也不该用假件伪造。

实现说明（正文只给了名字、没给定义的地方，以及为落地补的决定）：

- **指标接口复用 `runtime.Metrics`，不引第三方依赖。** Agent 已经有一套 `Counter`/`Gauge`/`Histogram` 抽象，隧道再定义一套等价接口只会让节点出现两个指标后端。`main.go` 因此把同一个 sink 同时交给 `runtime.Dependencies` 与隧道；今天它是丢弃实现，接真实后端是一处改动。
- **`metrics.go` 里的方法就是标签白名单。** 记录点调用的是 `Request(op, result, d)` 这样的具体方法而不是 `Counter(name, labels)`，标签由这一个文件拼装。要把用户内容写进标签，得先改这里——评审能看见，测试也能断言。
- **`Cancel.reason` 永远不进标签。** 它是副本给的自由文本，无界且可能引用请求内容；指标只拿 `gateway_cancel` / `slot_closed` 这个二值分类，原文只留在 debug 日志里。同理 `reconnect` 的原因是错误码的粗分类而不是错误消息，`operation` 遇到本 build 不认识的枚举值记 `unknown` 而不是数字。
- **三个指标没有 `replica_id`，且这是有意的。** `tunnel_connected_replicas` 与 `tunnel_roster_version` 描述的是整个节点；`tunnel_limiter_rejections_total` 背后的 limiter 是**节点级**的每实例闸门，被所有副本共享，把一次拒绝算到"恰好抢输的那个副本"头上是编故事。这三个名字记在 `NodeScopedMetrics` 里，测试对着它做双向断言：不在表里的指标必须带 `replica_id`，在表里的必须不带。
- **握手前的 `replica_id` 记 `unknown`。** 从拨号到 `HelloAck` 之间隧道有状态要报但还不知道对端是谁。留空标签会让后端把它和其他系列混在一起，`unknown` 至少是个能被看见、基数为一的值。
- **`tunnel_connection_state` 合并了两对状态。** `reconnecting` 记成 `connecting`（对看板而言都是"暂时没有可用链路，正在建"，重连本身有专门的计数器），`serving` 记成 `connected`（两者只差槽有没有 park，而那正是 `tunnel_slots_total` 在报的）。这样"2 就是这个副本可以派活"始终成立。README 的取值表补了 `5=failed`。
- **TTFT 从 `RequestHeaders` 到达开始算。** 不是从处理器被调用开始：调度、取额度、建 deadline 都是 Agent 的账，藏在起点之前就等于把自己的开销从指标里减掉。
- **槽开不出来时暂停 `slotOpenBackoff` 再试。** 接指标时发现的实际缺陷：副本接受 Control 却拒绝 Serve 时，`slot.run` 失败 → `slotFinished` → `poke` → 立刻再开，形成无退避的自旋，两侧都烧 CPU 且日志被同一行刷屏。现在一次失败后暂停开槽，重试落到维护 tick 的节奏上，任一槽 park 成功即清除。
- **被 reconcile 判死的槽从计数里摘掉（`poolReaping`）。** 关槽是异步的——要等槽自己的协程退栈才会从表里消失。在这个窗口里第二次 reconcile 会把它当成"还闲着的槽"再判死一个，把池子压到 `min_slots` 以下。压测下这是必然发生的，不是偶发。
- **压测与故障注入进常规 `go test -race`，不是发布前的手工动作。** `stress_test.go` 用内存副本与假时钟回答阶段 7 里不需要真实 Gateway 的问题：槽数在饱和下收敛到上限并在负载消失后回到下限、额度耗尽时立即返回背压而不是排队、单副本死亡时其余链路的指标与在途请求不受影响。绝对延迟数字不在其中——那需要真实链路。
- **Gateway 侧的隧道服务端落在 `service/aiServeWeaveGateway/tunnelserver`，节点表按 `node_id` 建、跨流共享。** Control 与每条 Serve 都是独立的 gRPC 流，到达顺序没有保证（槽可能比 Control 先重连），因此节点条目由两者共享，最后一条流走了才删除。`Nodes()` 报的 `IdleSlots` 是**服务端自己数的已 park 槽**，不是心跳里的 `idle_slots`——后者是 Agent 那一侧的快照，读到时已经过期，用它调度会把请求发给刚满的节点。
- **`Dispatch` 写完请求就返回，不等第一帧。** 等第一帧会把节点的 TTFT 算进这次调用里，看板上就再也分不清"隧道慢"和"模型慢"。取不到空闲槽时立刻返回 `ErrorBackpressure` + `ErrNoIdleSlot`，`Retryable: true`——与待决问题 7 的结论一致。
- **响应帧一帧一交，中间没有缓冲。** 槽的读循环把帧直接塞给调用方，调用方不读就阻塞在那里，背压顺着 gRPC 流控传回 Agent。这是"任何一跳都不得无界缓冲"在服务端的落地方式：没有队列可以涨，也就没有队列需要限长。
- **槽死掉时在途请求以不可重试的连接错误结束。** 服务端无法知道 Agent 是否已经吐出过 token，而"已提交的流不得重试"是硬约束，所以一律按已提交处理。想重试未提交请求的调度器得从自己写出去了什么来判断。
- **`NodeRuntime` 让隧道对面看起来就是一个 `runtime.InferenceRuntime`。** 这是把 `runtime` 下沉到 `common/` 的兑现：Gateway 的调度器与 API 层面对的接口，和 Agent 的适配器实现的是同一个。`Descriptor`/`Probe`/`Health`/`Discover` 不过隧道，直接读 Control 流报上来的清单——协议里本来就没有探测 Operation，让每个副本对每个节点重新探测一遍等于把探测流量乘以副本数。
- **认证只看 `VerifiedChains`，不看 `PeerCertificates`。** 后者是"对端发来的"，前者是"TLS 栈验过的"。读错一个，任何人自签一张证书就能声称自己是任意 `node_id`；没开客户端校验的副本因此认不出任何人，而不是认可所有人。
- **`service/aiServeWeaveGateway/e2e` 是唯一同时依赖两个服务的包。** 两个服务谁也不 import 谁，把连接它们的测试放在两者之外正是维持这一点的办法。它自己签一个 CA、把节点证书按 0600 写到临时目录，走的正是本文件说的"离线签发"路径——证书是真的、名字是对的，只是没有拿 bootstrap token 换过。后端是脚本化的 `runtime.InferenceRuntime`，因为需要 GPU 的测试等于不会跑；后端协议由各适配器自己的测试负责。
- **实测：loopback + mTLS 单跳的 TTFT 开销约 0.3ms**（`TestTunnelSegmentLatency`，20 次采样，与同进程直连对比）。这个数字只说明"一跳 TLS gRPC 不是延迟来源"，真实链路的数字要等真实部署。测试断言的是 100ms 上限，因为回归到那个量级就是流式体验的分水岭，而不是因为 0.3ms 有什么可保证的。
- **Gateway 的调度器与 OpenAI 前门落在 `service/aiServeWeaveGateway/scheduler` 与 `service/aiServeWeaveGateway/httpapi`。** 这是清单第一项真正的阻塞点：Gateway 此前只有隧道服务端，没有任何调用方能触发一次真实推理。`scheduler.Scheduler` 直接读 `tunnelserver.Server.Nodes()`（不额外记状态），按空闲槽数与在途请求数选节点，`ChatStream` 自己读一次首帧来判定 `Committed()`，只在首帧之前重试——这是正文"流式请求只有在返回第一个 token 之前可以安全重试"在调度层的落地。`httpapi` 只做 `POST /v1/chat/completions`（含 SSE）、`POST /v1/embeddings`、`GET /v1/models` 三个第一阶段端点；`POST /v1/responses` 没做——`common/runtime` 没有 `Responses` 请求/响应类型，隧道协议的九个 `Operation` 里也没有对应枚举，要支持它得先扩协议，不是前门范畴内的事，顶层 README 路线图也把它排进第二阶段。
- **Agent 新增 `-ollama-url`/`-ollama-id` 两个 flag，仅为了让这次真实链路有后端可测。** Agent 目前还不能从配置文件加载 runtime 实例（阶段 5 的实现说明里已经提过这个空当），这两个 flag 是同样性质的过渡占位：留空则不注册任何 runtime，行为与之前一致。
- **实测：真实 Ollama + 真实 mTLS 隧道 + Gateway HTTP 前门的端到端 TTFT。** 本机（Apple Silicon Mac，Ollama 0.x，模型 `gemma4:26b`，19GB，Q4 量化）离线签发一次性 CA 和证书（手法与 `e2e/pki_test.go` 相同），起一个 Gateway 副本和一个连到真实 Ollama 的 Agent，用真实 TCP + 真实 mTLS 连接，`curl` 打 `/v1/chat/completions`：
  - 非流式，模型冷启动（首次加载进内存）：总耗时约 10.0s——这个数字基本是 Ollama 把 19GB 模型读进内存的时间，不是本链路的开销；非流式端点的"TTFT"定义上等于总耗时，所以这一项本身不能反映前门开销。
  - 流式，模型已热（同一模型第二次及以后请求）：从 Gateway 收到 HTTP 请求到第一个 SSE chunk `Flush()`，三次采样为 165ms、293ms、129ms。这个量级由 Ollama 生成首个 token 的真实推理延迟主导；与隧道段单跳 0.3ms 的开销相比，Gateway 前门 + 隧道往返在其中可忽略不计——**多副本、多一跳 mTLS 网络请求没有引入可观测的额外延迟**，验证了阶段 6/7 一直依赖的假设。
  - 流式响应逐块到达（用 `-N` 加时间戳观察，两次 SSE chunk 间隔在几十毫秒量级），不是一次性吐完，确认 Gateway 前门没有对 SSE 做缓冲。
  - 这组数字只对这台机器、这个模型有效，重点是方法论证：真实部署下 TTFT 由推理时延主导，隧道与前门本身不是瓶颈。真实生产数字需要在目标硬件上重新采样。
- **剩余四场景：本机单进程模拟真实故障，而不是内存假件。** 这台机器没有免密 sudo，`pfctl` 需要交互式输入密码，无法从测试进程里非交互驱动，因此"拔网线"改用一个用户态 TCP 中继（`chaosLink`，见 `faultinjection_test.go`）：正常时透明转发字节，"剪断"时对两端都保持连接不关闭，只是不再转发任何字节——这正是网线被拔掉时的现象（没有 RST、没有 FIN，只有沉默直到超时发现），比 `pfctl block`（会立即回 RST）更贴近真实断网，只是发生在内核外而不是内核里。其余三个场景不需要绕过 sudo：真实 TCP 监听被真的 kill 掉、真实证书按真实挂钟过期、真实 HTTP 客户端顶着一个真的不回应的 TCP 对端等真实超时——这些都不需要 root。四个场景默认跳过（`go test ./...` 不受影响），设置 `AISW_FAULT_INJECTION=1` 才运行，原因和 `ollama/live_test.go` 一样：它们等真实挂钟时间，不是假 Clock。实测数据（本机 Apple Silicon Mac，全部来自 `go test -race`，可重复运行）：
  - **拔网线**（`TestFaultInjectionNetworkPartition`）：3 副本，其中一个经 `chaosLink` 连接，心跳超时调到 2s 便于在测试时间尺度上观察。剪断链路后 replica-0 的 `Node().Live` 在 **2.02s** 后转为 false（一个心跳超时窗口，符合预期）；期间另外两个副本的隧道是独立 TCP 连接，请求继续正常完成，用户侧看不到任何异常。恢复链路后 **20ms** 内 replica-0 重新变为 `Live`——这一步比预想快得多，因为连接本身没断（gRPC 流仍在等待字节），链路一通就立即收到之前被拦住的心跳，不必走重连退避。
  - **kill 全部副本**（`TestFaultInjectionAllReplicasKilled`）：3 副本同时 `grpc.Stop()`。`tunnel.Client.Run` 按设计没有返回错误——不可达是可重试状态，不是致命状态——三边都不可达时用户请求只会在调度层看到"无可用节点"，不会有请求被静默丢弃。3 副本在原地址重新起进程后，全部重新变为 `Live` 共耗时 **1.41s**，此后请求立即恢复正常。
  - **证书过期**（`TestFaultInjectionCertificateExpiry`）：签发一张 6s 有效期的证书（`writeShortLivedIdentity`），`IdentityInterval` 调到 1s。证书过期后 **13.5ms** 内 `tunnel.Manager.Run` 返回致命错误（`unauthorized: ... expired ... no bootstrap token`），此时三个副本上该节点的路由同时消失，验证了 README:388 "证书失效对所有副本同时生效"。`tunnel.Manager.Run` 是一次性的——同一个 Manager 不能第二次 `Run`——所以恢复的形状和重启 Agent 进程一样：写入一张新证书到同样的三个文件，起一个新 Manager，**23.6ms** 后三个副本重新变为 `Live`。也就是说恢复时间几乎全部是"运维介入换证书"的等待时间，系统自身的探测和重连开销在这个场景里可以忽略。
  - **后端假死**（`TestFaultInjectionBackendHang`）：一个 Ollama 兼容 HTTP 服务器正常应答 `/api/version`、`/api/tags`（探测通过），但 `/v1/chat/completions` 收到请求后完全不回应（连接保持打开，只是不写字节）。`RequestTimeout=1s` 时请求在 **1.00s** 后失败，错误分类为 `runtime.ErrorTimeout` 且 `Retryable=true`——调度器可以安全地把它换到另一个节点重试。关键发现：这个超时完全来自后端适配器自己包一层 `context.WithTimeout`（`common/runtime/internal/oaibase`），不依赖隧道 `Dispatcher.run` 对 `ir.Chat` 的调用本身有任何超时包装——`dispatch.go` 里那一行是直接阻塞调用，如果适配器不主动尊重 `ctx`，这个调用会永远卡住（一个真实的运维风险，值得记在这里而不是留给下一次事故发现）。假死后端恢复应答后，同一节点上的下一个请求 **4.6ms** 内成功，说明前一个卡住的请求没有污染槽位或隧道状态。
- **长稳测试：`TestSoak`（`service/aiServeWeaveGateway/e2e/soak_test.go`），时长和采样周期都由环境变量驱动而不是写死。** `AISW_SOAK_DURATION`（如 `24h`）控制运行多久，未设置则跳过；`AISW_SOAK_SAMPLE_INTERVAL`（默认 5m）控制采样周期；`AISW_SOAK_OUTPUT` 指定 CSV 报告路径（默认写到 `os.TempDir()` 下一个带时间戳的文件，运行开始时打日志说明路径，因为 24h 之后测试进程早就不在前台了）。真实运行时长远超 `go test` 默认的 10 分钟超时，必须带 `-timeout 0`。三副本 fleet 起来后，每个副本各有一个 goroutine 以 200ms 周期发真实 Chat 请求（保持槽位持续被借还，而不是停在下限空转），采样器每个周期读一次 `runtime.NumGoroutine()`、`runtime.MemStats.HeapAlloc`（采样前先 `runtime.GC()`，否则 GC 时机的噪声会在 24h 窗口里盖过真实趋势）、`Manager.ConnectedReplicas()`、三个副本上该节点空闲槽之和。判定用运行自己前四分之一采样的均值做基线，而不是写死的绝对值——机器和 `SlotHint` 配置会变，但"相对基线翻倍"这个信号不会：协程数或堆增长超过基线 100% 判失败，`connected_replicas` 全程必须等于副本数，请求失败率超过 1% 判失败。30s/20s 冒烟测试（`AISW_SOAK_DURATION=20s`）验证了工具本身：`go test -race` 无告警，goroutine/heap 在这个尺度上是持平或略降（噪声范围内），`connected_replicas` 全程为 3。**真实的 24h 数据仍在采集**，完成后把最终数字和 CSV 摘要补进这里。
- **滚动升级：一次替换一个副本，从未把可用隧道降到零。**（`TestRollingUpgradeKeepsAtLeastOneTunnelAvailable`）用常规 `newFleet`（3 副本、脚本化后端，不需要 `AISW_FAULT_INJECTION`——不涉及真实网络时钟，纯粹是"一次一个"的时序保证）依次 `stop()` 每个副本、在同一地址起替换进程、等它重新 `Live`，再换下一个；期间一个后台协程以 5ms 间隔轮询当前仍 `Live` 的副本并真的发一次 Chat。重复 5 次的实测：每次请求成功率均 ≥98%（181-314 次尝试里最多失败 2 次，都是替换进程尚未完全 `Live` 时的探测性失败，不是空窗期），连续成功请求之间最长间隔 12-18ms——远小于任何客户端重试超时。三副本一次换一个，任何时刻都至少有两个在服务，这与"kill 全部副本"（`TestFaultInjectionAllReplicasKilled`）故意制造的全体不可达窗口形成对照：滚动升级的安全性来自"一次一个"这个操作纪律，不是隧道协议本身的保证。

**验收：** 指标部分已达成（全部 13 个指标有记录点、标签基数有测试、无 payload 内容进标签）。端到端联调已达成：三副本、真实 mTLS、每副本独立完成推理、无副本间转发。真实 Ollama 端到端 SSE 已达成：Gateway 调度器 + OpenAI 前门落地，真实链路验证多副本未引入额外延迟。剩余四场景故障注入已达成（本机单进程真实网络/真实证书时钟/真实 HTTP 超时，见上）。滚动升级演练已达成（本机单进程逐个替换，见上）。24h 长稳测试工具已落地（`TestSoak`），本机单进程 24h 运行进行中，尚未勾选待补数据。

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
- 故障注入六场景恢复行为符合预期、滚动升级期间节点始终可服务（均已达成，见阶段 7）；24h 长稳测试无泄漏正在本机运行验证中。

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

1. ~~**`runtime` 包的位置**~~ **已定：整棵 `runtime/` 下沉到 `common/runtime`，`convert.go` 拆出为 `common/tunnelwire`。** 决定于 Gateway 动工之前。两件事一起做的原因是它们是同一件事的两半：Gateway 要的不只是 `Stream`、`RuntimeError` 和九个 Operation 的类型，还有这些类型与 proto 之间的转换 —— 隧道两端做的是同一次转换的正反两向，各写一份必然漂移，而「凭据不过隧道」「nil 与显式零值不等价」这两条不变量正是靠这次转换保证的。适配器（`ollama/`、`vllm/`、`comfyui/` 等）跟着 `runtime` 一起走，因为它们依赖 `runtime/internal/`，Go 的 internal 规则不允许它们留在原地；Gateway 不 import 它们，也就不会链进去。隧道侧只有 `convert.go` 搬家、四个文件加 `tunnelwire.` 前缀，`operationName` 与 `classifyBareError` 因为跨文件使用而导出为 `OperationName`、`ClassifyBareError`。
2. **Registry 的 `NodeIdentity` 服务仍未实现。** Agent 侧阶段 2 已按 proto 落地并对 fake Registry 验证，因此不再阻塞隧道开发；但**上线前必须有真实 Registry**，包括 bootstrap token 的一次性强一致校验与 CA 私钥保管。临时用离线签发的证书手工分发也能让 Agent 跑起来（只要 SAN 形式一致），但那条路上没有轮换。
3. `node_id` 的分配方式：控制台预分配还是 Agent 提交候选后由 Registry 确认。**已不阻塞**：`Register` 两种都支持（配置留空即由 Registry 分配），响应中的 `node_id` 始终权威，配置与之不符时 Agent 拒绝启动。仍需在上线前定下运维口径。
4. Secret 引用 `api_key_ref` 的解析方式（本地文件、环境变量还是外部 Secret 管理器），需与 `runtime` 阶段 8 的结论保持一致。
5. **名册的下发时效**：Registry 到 Gateway 是推送还是轮询（Gateway 侧待决问题 3）。若为 30s 轮询，则副本扩容后 Agent 最长 30s 才会连上新副本，需确认这个窗口可接受。
6. **`node_total` 的默认取值**：取各 Runtime `MaxConcurrent` 之和是否合理，还是应该更保守。压测（`stress_test.go`）已经能给出"这个 `node_total` 下会被硬配额拦下多少"的数字，但真实取值要等接上真实后端的吞吐才能定。
7. ~~**背压错误的 `retryable` 标记两侧不一致。**~~ **已定：由 `runtime.Limiter` 给 `ErrorBackpressure` 置 `Retryable: true`。** 选择置位而不是在契约里写「背压看码不看标记」，因为标记的含义本来就是「这个请求可以再跑一次」，而背压恰恰是请求**根本没到后端**的那一类失败——它比大多数 `Retryable` 的错误更安全，让它独自成为例外只会给 Gateway 埋一个必须记住的特例。`ErrorClosed` 保持不可重试并加了对称断言：实例已经没了，重试它永远不会成功。码与标记因此各司其职——标记回答「能不能重试」，`code == "backpressure"` 回答「该换个节点而不是原地重试，且不计入熔断」。

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

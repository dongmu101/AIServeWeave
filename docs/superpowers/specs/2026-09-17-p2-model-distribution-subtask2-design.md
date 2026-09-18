# P2「模型分发」子任务二：Gateway 触发的按名字拉取与状态回传

延续 [`2026-09-17-p2-model-distribution-design.md`](2026-09-17-p2-model-distribution-design.md)（子任务一：Agent 本地校验和下载器）第二节列出的子任务二：「Gateway/控制面触发的拉取指令与状态回传」。本文档交付这一项在 Gateway↔Agent 之间的部分；控制面/Console 的接入显式推迟到子任务五（见第六节范围边界）。

## 一、协议红线核查与设计调整

子任务一文档写这句话时没有对照 `tunnel.proto` 自己的约束重新核实。动工前重读该文件头部注释，发现两条 load-bearing 规则：

> No message here carries a credential... No message here can express "fetch this URL" or "run this command". The Agent is not a general-purpose proxy, and the protocol deliberately leaves no extension point that would make it one.

AGENTS.md 安全红线同样写着：「Agent 永远不做通用 HTTP 代理：隧道只传运行时语义，不传任意 URL、Host 或 Authorization。」

子任务一文档第二节原本设想的子任务二——「Gateway 把 URL+校验和下发给 Agent」——直接违反这条规则，不是风格问题。本文档改为**按名字触发**：

- Gateway 只能对 Agent 说"现在开始拉 `qwen3-coder:30b`"，从不携带 URL。
- Agent 收到名字后，在自己本地已加载的清单（子任务一的 `-model-pull-manifest`）里查找；名字不在清单里就是一次纯粹的本地拒绝，从不发起任何网络请求。
- 即使 Gateway 副本被攻破，能做的也只是"从 Agent 已经批准的名字集合里选"，不能让 Agent 访问任意地址——与 `runtime_id` 白名单、`tunnel.AllowedRuntimes` 同一威胁模型（子任务一文档第 1.6 节已经论证过这个方向，本文档把它落到实处而不是自相矛盾地绕过它）。

第二个需要在动工前修正的问题：错误文本不能跨隧道原样传递。`Spec.SourceURL` 可能是带签名 token 的预签名 URL（S3/OSS 常见），Go 标准库的 `http.Client` 传输错误（`*url.Error`）默认把完整请求 URL 编进 `Error()` 文本。子任务一里这类错误只留在 Agent 本地日志，不构成跨信任边界的泄漏；子任务二一旦把状态经隧道发给 Gateway，原样转发 `err.Error()` 就会把 URL（进而可能是其中的签名 token）带到 Agent 之外的信任域。做法与 P09 的 `request_logs.outcome` 封闭枚举同一先例：**Status 的失败原因是一个封闭词表，从不携带原始错误文本**；完整错误详情继续只留在 Agent 自己的日志里。

## 二、为什么走 Control 流，不走 Serve 槽

`RequestHeaders.runtime_id` 是 `Serve` 槽每一次派发的必填字段（`tunnel.proto:306`：「must match the Agent's local allowlist」），十三个既有 Operation 全部挂在某个 `runtime_id` 之下。模型拉取不属于任何一个推理运行时实例——它是节点级的本地文件操作，如果勉强塞进一个假的 `runtime_id` 命名空间，会让调度器、槽池、熔断器这些以 runtime 为单位的机制全部对不上语义。

`Control` 流（`AgentControl`/`GatewayControl`）已经是「每 (Agent, 副本) 一条、承载节点级语义」的现成信道——心跳、`RuntimeConfig` 下发、`GatewayRoster` 广播、`Draining`/`Shutdown` 都在这里，没有一个绑定到具体 runtime_id。新增两个 oneof 分支：

```proto
message GatewayControl {
  oneof body {
    ...
    ModelPullTrigger model_pull_trigger = 8; // STATUS.md P2 模型分发子任务二
  }
}

message AgentControl {
  oneof body {
    ...
    ModelPullReport model_pull = 6; // STATUS.md P2 模型分发子任务二
  }
}
```

`GatewayControl_Config` 的既有先例直接决定了 `ModelPullTrigger` 的应答方式：**成功与失败都通过状态报告观察，不设专门的 ack 帧**（`control.go:108` 原注释：「Success or failure is observed through the status report rather than a dedicated ack frame, so one always follows.」）。收到 `ModelPullTrigger` 后 Agent 调用 `Puller.Trigger`（同步、非阻塞），然后立即强制发送一次 `ModelPullReport`——未知名字会在这次报告里直接以 `FAILED` + `unknown_name` 出现，已知名字变成 `PENDING` 或 `DOWNLOADING`。

## 三、消息定义

```proto
// ModelPullTrigger asks the Agent to start pulling model artifacts it
// already knows about from its local manifest (STATUS.md's P2 model
// distribution subtask 1), referenced only by name. It can never carry a
// URL or any other fetch instruction: the load-bearing rule at the top of
// this file forbids a message that expresses "fetch this URL", and a
// replica whose trust were compromised must not be able to hand the Agent
// an arbitrary source.
//
// ModelPullTrigger 要求 Agent 开始拉取它本地清单（STATUS.md P2 模型分发子
// 任务一）里已经认识的制品，只按名字引用。它永远不能携带 URL 或任何其他取
// 数指令：本文件头部的 load-bearing 规则禁止表达"fetch this URL"的消息，一
// 个信任被攻破的副本不能借此让 Agent 访问任意来源。
message ModelPullTrigger {
  repeated string names = 1;
}

// ModelPullReport mirrors the Agent-local Puller's current status for every
// name it knows about — every entry in its local manifest, whether or not a
// ModelPullTrigger has ever named it. It carries no source URL, matching
// ModelPullTrigger's restriction in the other direction, and no raw error
// text: ModelPullStatus.reason is a closed vocabulary so a transport error
// that happens to embed spec.SourceURL (a presigned URL can carry a
// signature token in its query string) never crosses the tunnel.
//
// ModelPullReport 镜像 Agent 本地 Puller 当前已知的每一个名字的状态——本地
// 清单里的每一条，无论 ModelPullTrigger 是否点过它的名。它不携带来源
// URL，与 ModelPullTrigger 在另一个方向上的限制对称；也不携带原始错误文
// 本：ModelPullStatus.reason 是封闭词表，这样一个恰好把 spec.SourceURL
// （预签名 URL 的查询串可能带签名 token）编进错误文本的传输错误，永远不
// 会跨越隧道。
message ModelPullReport {
  repeated ModelPullStatus pulls = 1;
}

enum ModelPullState {
  MODEL_PULL_STATE_UNSPECIFIED = 0;   // known to the manifest, never triggered
  MODEL_PULL_STATE_PENDING = 1;       // queued behind another in-flight pull
  MODEL_PULL_STATE_DOWNLOADING = 2;
  MODEL_PULL_STATE_DONE = 3;
  MODEL_PULL_STATE_FAILED = 4;
}

enum ModelPullFailureReason {
  MODEL_PULL_FAILURE_REASON_UNSPECIFIED = 0;
  MODEL_PULL_FAILURE_REASON_UNKNOWN_NAME = 1;        // not in the Agent's local manifest
  MODEL_PULL_FAILURE_REASON_INVALID_SPEC = 2;        // manifest entry itself malformed
  MODEL_PULL_FAILURE_REASON_NOT_ALLOWLISTED = 3;     // source_url outside -model-pull-allowlist
  MODEL_PULL_FAILURE_REASON_QUOTA_EXCEEDED = 4;
  MODEL_PULL_FAILURE_REASON_FETCH_FAILED = 5;        // transport error, detail stays in the Agent's own log
  MODEL_PULL_FAILURE_REASON_UNEXPECTED_STATUS = 6;   // non-200/206 HTTP response
  MODEL_PULL_FAILURE_REASON_CHECKSUM_MISMATCH = 7;
  MODEL_PULL_FAILURE_REASON_STORAGE_ERROR = 8;       // local disk write/rename failure
}

message ModelPullStatus {
  string name = 1;
  ModelPullState state = 2;
  int64 bytes_downloaded = 3;
  int64 bytes_total = 4;                 // 0 when unknown (Spec.SizeBytes was 0)
  ModelPullFailureReason reason = 5;     // set only when state == FAILED
  int64 updated_unix_ms = 6;
}
```

## 四、Agent 侧：`modelpull.Puller`

新文件 `service/aiServeWeaveAgent/modelpull/puller.go`，在既有 `RunManifest`（保留不变，仍是同步、一次性处理整份清单的公开 API，供子任务一的启动路径与测试使用）之上新增一个持续运行、按名字触发的封装：

```go
// Puller tracks and drives on-demand pulls of a fixed set of named Specs —
// the Agent-local manifest loaded once at startup. Trigger is safe to call
// repeatedly and concurrently (from the Control stream's single goroutine,
// and once at startup); it never blocks on a download. Only one download
// runs at a time — the same "no concurrent downloads" restraint RunManifest
// documents — so a Trigger arriving mid-download enqueues behind whatever is
// already running rather than starting a second one.
type Puller struct {
    ctx   context.Context // canceled at Agent shutdown; aborts any in-flight fetch
    cfg   Config
    clock runtime.Clock
    byName map[string]Spec

    mu      sync.Mutex
    status  map[string]Status
    queue   []string
    queued  map[string]struct{}
    running bool
    budget  *int64 // established once per worker session, shared across it
}

func NewPuller(ctx context.Context, cfg Config, specs []Spec, clock runtime.Clock) *Puller

// Trigger enqueues names for pulling. A name unknown to the manifest is
// recorded as StateFailed/ReasonUnknownName immediately and never queued. A
// name already pending, downloading, or queued is left alone — Trigger is
// idempotent for an in-flight name, not a request to restart it.
func (p *Puller) Trigger(names []string)

// Snapshot returns the current status of every name the manifest declares,
// sorted by name for deterministic wire encoding.
func (p *Puller) Snapshot() []Status
```

`Status`、`State`、`FailureReason` 定义在新的共享包 `common/modelpullstatus`（第五节），`Puller.Snapshot()` 直接返回该包的类型，`common/tunnelwire` 据此编解码。

下载逻辑复用现有 `pullOne`，新增一个可选的进度回调参数：

```go
func pullOne(ctx context.Context, client *http.Client, spec Spec, budget *int64, onProgress func(downloaded int64)) error
```

`RunManifest` 调用时传 `nil`（行为不变）；`Puller` 传一个把 `Status.BytesDownloaded` 写回 `p.status[name]` 的闭包，挂在 `io.Copy` 已经在用的 writer 链上，不额外读一次文件、不新开 goroutine。`pullOne` 内部的失败路径改为包一层哨兵错误（`errFetchFailed`、`errUnexpectedStatus`、`errChecksumMismatch`、`errStorageFailed`），`RunManifest` 面向本地调用方的 `Result.Failed` 不变（依然是完整 `error`，本地日志可读全部细节）；`Puller.runOne` 用 `errors.Is` 把同一个错误分类成 `common/modelpullstatus.Reason`，只把分类结果放进 `Status.Reason`，原始错误只经 `slog` 打到 Agent 自己的日志。

`Config.QuotaBytes` 语义从"单次 `RunManifest` 调用"扩展为"单次 worker session"：`Trigger` 使队列从空变为非空时新建一个 worker goroutine 并建立一份新预算，这份预算被同一个 worker session 处理的所有名字共享，session 处理完队列清空后退出；下一次 `Trigger` 重新起一个 session、一份新预算。跨 session（也就是跨"这一批"）不做累计账本，与子任务一"单次运行的字节预算"是同一类简化，只是把"一次运行"的边界从"一次 RunManifest 调用"精确到"一个 worker session"。

## 五、共享契约包：`common/modelpullstatus`

```go
// Package modelpullstatus is the wire contract for STATUS.md's P2 model
// distribution subtask 2: the Agent-to-Gateway status of a model pull, and
// the vocabulary a Gateway-to-Agent trigger names. It carries no source URL
// and no raw error text — see the design doc's 第一/三节 for why both are
// excluded on purpose, not by oversight.
package modelpullstatus

type State int

const (
    StateUnspecified State = iota
    StatePending
    StateDownloading
    StateDone
    StateFailed
)

type FailureReason int

const (
    ReasonUnspecified FailureReason = iota
    ReasonUnknownName
    ReasonInvalidSpec
    ReasonNotAllowlisted
    ReasonQuotaExceeded
    ReasonFetchFailed
    ReasonUnexpectedStatus
    ReasonChecksumMismatch
    ReasonStorageError
)

type Status struct {
    Name            string
    State           State
    BytesDownloaded int64
    BytesTotal      int64
    Reason          FailureReason
    UpdatedAt       time.Time
}
```

`common/tunnelwire` 新增 `ModelPullReportToProto`/`ModelPullReportFromProto`（`[]modelpullstatus.Status` ↔ `*tunnelv1.ModelPullReport`）与 `ModelPullTriggerToProto`/`ModelPullTriggerFromProto`（`[]string` ↔ `*tunnelv1.ModelPullTrigger`），与包内其余转换函数同一形状、同一文件组织习惯（新文件 `common/tunnelwire/modelpull.go`）。

## 六、两端接线

### Agent（`service/aiServeWeaveAgent/tunnel`）

- `ClientConfig` 新增可选字段 `ModelPuller ModelPuller`（接口，非具体类型，方便测试用假实现替换真实下载）：
  ```go
  type ModelPuller interface {
      Trigger(names []string)
      Snapshot() []modelpullstatus.Status
  }
  ```
  `nil` 是合法值（未配置清单、或调用方不关心），与 `OnRoster`/`OnSlotHint` 同一"可选钩子"风格；`control.go` 里每处使用都判空。
- `control.go`：
  - `GatewayControl_ModelPullTrigger` 分支：调用 `ModelPuller.Trigger`，随后强制发送一次 `ModelPullReport`（`Config` 分支的既有先例，「success/failure 由状态报告体现，不设专门 ack」）。
  - 复用 `statusPoll` 定时器（不新增 ticker）：每次轮询周期额外调用一次 `reportModelPull(false)`，内部用 `proto.Marshal` 整个 `ModelPullReport` 的字节比较做变更检测——不采用 `RuntimeStatus` 的逐实例增量合并，因为一份运维手写的清单条目数量有限，整份发送的开销可以忽略，没必要照搬那一层复杂度。
  - 初始连接时（`sess.report(true)` 旁边）同样强制发一次 `ModelPullReport`，让副本从第一次心跳起就有拉取状态可查。
- `main.go`：`startModelPull` 改造为 `newModelPuller(ctx, logger, opts) *modelpull.Puller`——清单加载失败仍是非致命（记日志、退化为空清单），启动时仍对整份清单调用一次 `Trigger`（保留子任务一"启动时自动拉取"的既有行为），返回值接入 `startTunnel` 新增的 `puller *modelpull.Puller` 参数、进而进入 `ClientConfig.ModelPuller`。原来的 `modelPullDone` 等待在 `run()` 里去掉：旧注释"ctx 已取消所以 fetch 会很快 unwind"仍然成立，但等待本身此前只是为了日志顺序整洁（模型拉取从不触碰 `manager` 拥有的任何资源），现在 Puller 的工作跨越连接的整个生命周期、可能被 Gateway 随时重新触发，不再是一次有界操作，等待它"空闲"天然是竞态的（等待期间可能又收到一次 Trigger）。取消掉这个等待不引入新的正确性问题：中断的 `.part` 文件本就是被完整设计支持的可续传状态。

### Gateway（`service/aiServeWeaveGateway/tunnelserver`）

- `node.go`：`node` 结构体新增 `modelPulls map[string]modelpullstatus.Status`（`n.mu` 保护），`newNode` 里初始化；新增 `applyModelPullReport`（整份替换，不做增量合并——Agent 每次报告都是当前已知的全集，见上）与只读 `modelPullSnapshot`（按名字排序）。
- `control.go`：`AgentControl_ModelPull` 分支调用 `tunnelwire.ModelPullReportFromProto` 后转给 `n.applyModelPullReport`。
- 新增导出方法（`server.go` 或新文件 `modelpull.go`）：
  ```go
  // TriggerModelPull asks the node to start pulling names, if it is
  // connected to this replica. Per the tunnelserver "no forwarding" boundary
  // (README's 四条约束第四条), a node connected only to a sibling replica
  // is reported as not connected here — the caller (Gateway's model-pull
  // listener) is expected to be told which replica by whatever tracks
  // roster membership, the same limitation admin-addr's per-replica views
  // already document.
  func (s *Server) TriggerModelPull(nodeID string, names []string) error

  // ModelPullStatus returns this replica's last-known status for nodeID and
  // whether the node is known here at all.
  func (s *Server) ModelPullStatus(nodeID string) ([]modelpullstatus.Status, bool)
  ```
  两者都通过既有 `s.lookup`/`n.broadcast` 实现，不新增连接管理机制。

### Gateway：新的写入口 `-model-pull-addr`

`-admin-addr` 的文档原句是「都不接受写操作」（README「运维清单监听器」一节），触发一次模型拉取是写操作，不能塞进那个监听器,否则这句话就成了假话。新增一个独立、默认关闭的监听器，与 `-admin-addr`/`-metrics-addr` 同一风格：

```
AISW_GATEWAY_MODEL_PULL_TOKEN=$(openssl rand -base64 32) \
go run ./service/aiServeWeaveGateway -model-pull-addr 127.0.0.1:8092 ...
```

新增包 `service/aiServeWeaveGateway/modelpullapi/`：

| 端点 | 方法 | 行为 |
| --- | --- | --- |
| `/internal/v1/nodes/{node_id}/model-pulls` | `POST` | body `{"names":["..."]}`；调用 `Server.TriggerModelPull`；节点未连到本副本答 404；成功答 202，body 只确认"已下发"，不代表任何名字最终会成功——名字校验结果只能从下面的 GET 端点观察，与 Job 提交"受理不等于成功"的既有语义一致 |
| `/internal/v1/nodes/{node_id}/model-pulls` | `GET` | 调用 `Server.ModelPullStatus`；节点未连到本副本答 404；成功答 200，body 是 `[]{"name","state","bytes_downloaded","bytes_total","reason","updated_at"}` |

鉴权：Bearer token 常数时间比较，来自 `AISW_GATEWAY_MODEL_PULL_TOKEN`；给了地址没给 token 拒绝启动（`-admin-addr` 同一先例："一份谁连上端口就能触发节点拉取的入口不是值得保留的降级模式"，写操作比只读清单的滥用后果更重，没有理由比 `-admin-addr` 宽松）。与 `-admin-addr` 用不同端口、不同 token：触发节点动作与聚合只读清单是两类不同的滥用后果，合用一个监听器意味着一次 token 泄漏的爆炸半径从"读到机群清单"变成"读到清单且能让任意节点开始下载"。

## 七、范围边界：本轮不做什么

- **不接入控制面或 Console。** 子任务五原文写明依赖子任务二"先把状态回传到 Gateway/控制面"；本文档把"Gateway/控制面"拆成两步，本轮只交付 Gateway 一侧的触发与查询能力。控制面新增一层转发端点（类似 P01 `/operator/v1/nodes/:id/*` 转发给 Registry 的形状，但目标是 Gateway 而不是 Registry，还要解决"一个节点只连着某一个副本"的跨副本路由问题）与 Console 页面，留给子任务五。
- **不解决跨副本路由。** `TriggerModelPull`/`ModelPullStatus` 只在目标节点连接的那个 Gateway 副本上有效，与 `-admin-addr` 现有的"每个副本只知道连到它自己身上的节点"边界相同；一个不知道该问哪个副本的调用方，今天必须自己试探或依赖控制面聚合（子任务五范围）。
- **不做并发下载、跨重启累计配额账本、磁盘可用空间探测。** 这三项仍是子任务四的范围，未受本轮影响。
- **不做 Ollama 原生拉取。** 仍是子任务三的范围；`Puller` 触发的依然是子任务一那个通用 HTTP 下载器，不理解 Ollama 的 blob 存储格式。
- **模板/路由层"这个模型别名对应哪个制品名字"仍未定义。** `common/modelroute.Target` 不新增字段——按名字触发的调用方（无论是本轮手工调用 HTTP 端点的运维，还是未来子任务五的 Console）自己决定要触发哪个名字，Gateway 不做"别名到制品名字"的翻译。这层映射如果需要，应该是子任务五或更后续的范围，本轮不预先设计一个可能用不上的抽象。
- **Trigger 的 HTTP 响应不携带名字级校验结果。** 202 只确认"已经下发到 Control 流"，未知名字、已在下载中等情况只能从状态报告/GET 端点观察——这是 Control 流"不设专门 ack 帧"设计决定的直接后果（第三节），本轮不为了让 HTTP 响应看起来更即时而破坏这个既有先例。

## 八、测试计划

- `common/modelpullstatus`：零逻辑，不需要专门测试（纯类型定义）。
- `common/tunnelwire`：表驱动往返测试，覆盖每个 `State`/`FailureReason` 取值、空列表、`BytesTotal=0`。
- `service/aiServeWeaveAgent/modelpull`：`Puller` 新增测试覆盖——按名字触发未知名字（立即 Failed/UnknownName，不触网）、重复触发同一个在途名字不重新入队、多个名字排队顺序执行、下载中途查询 `Snapshot` 能看到 `BytesDownloaded` 增长（用一个可控速率/分块的 `httptest.Server` handler）、配额在一个 worker session 内共享、一个 session 结束后新 session 拿到全新配额、每种失败路径映射到期望的 `FailureReason`（尤其是"错误文本不外泄"——断言 `Status.Reason` 是枚举值而不是断言到某个字符串包含 URL，同时可以额外断言 `Status` 序列化后确实不包含原始错误文本，用来防止未来的改动不小心把 err.Error() 塞回去）。
- `service/aiServeWeaveAgent/tunnel`：`control_test.go` 新增用例覆盖 `GatewayControl_ModelPullTrigger` 触发后立即发出强制 `ModelPullReport`、以及常规轮询周期内变化/不变化时是否发送。
- `service/aiServeWeaveGateway/tunnelserver`：`control_test.go`/`node_test.go` 新增覆盖 `AgentControl_ModelPull` 合并进 `node.modelPulls`、`TriggerModelPull`/`ModelPullStatus` 对已连接/未连接节点的行为。
- `service/aiServeWeaveGateway/modelpullapi`：HTTP 层表驱动测试，覆盖鉴权拒绝、节点未连接的 404、成功触发的 202、状态查询的 200、非法 JSON body 的 400，均用一个假 `Server` 接口（不起真实 gRPC）。

全量质量门禁（`gofmt`/`go vet`/`go build`/`go generate ./api/...` 无 diff/`go test ./...`/`go test -race ./service/...`）照旧要求全部通过；本轮不涉及真实网络/真实数据库，不需要额外的 live test 隔离。

## 九、文档同步

- [隧道 README](../../../service/aiServeWeaveAgent/tunnel/README.md)：Operation 计数与 Control 流一节补充 `ModelPullTrigger`/`ModelPullReport`。
- [Agent README](../../../service/aiServeWeaveAgent/README.md)「当前实现」/模型拉取一节：补充子任务二的按名字触发能力与 `Puller` 的行为边界。
- [Gateway README](../../../service/aiServeWeaveGateway/README.md)：新增「模型拉取触发（P2 子任务二）」一节，与「运维清单监听器」一节平行，明确写操作与只读清单是两个不同监听器、不同 token。
- STATUS.md「模型分发」条目：补充子任务二已交付的说明与范围边界，条目整体仍不打勾（子任务三/四/五未排期）。

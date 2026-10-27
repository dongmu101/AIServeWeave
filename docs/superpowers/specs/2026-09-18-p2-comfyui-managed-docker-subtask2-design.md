# P2「ComfyUI Managed Docker 部署」子任务二：控制面触发生命周期动作 + Agent 状态回传

本文档交付子任务一设计文档（[`2026-09-18-p2-comfyui-managed-docker-design.md`](2026-09-18-p2-comfyui-managed-docker-design.md)）第四节列出的子任务二："控制面下发部署规格 + Agent 状态回传。可参考 `tunnel.proto` 的 `RuntimeConfig` ADD/REPLACE/REMOVE 先例，或模型拉取子任务二'节点级 Control 流按名字触发 + 状态报告'的先例；需要新的部署规格契约"。

范围边界：**只做协议层 + Agent 侧执行 + Gateway 侧触发/查询的 HTTP 面，不做 ControlPlane 跨副本路由聚合、不做 Console 可见性、不做排空升级检查**——这三项分别是子任务三、四、五和一个未排期的路由聚合增量，与模型分发子任务二"先交付协议层与 Gateway 一侧能力,控制面转发层是后续独立交付"同一先例。仍然只支持 Agent 本地单实例。

## 一、核心设计发现：为什么"下发部署规格"必须收窄

子任务一文档在推导子任务二时写道，落地后需要重新评估子任务一"配置来自本机可信 flag"的信任假设。核实 `api/proto/tunnel/v1/tunnel.proto` 头部的 load-bearing 规则后发现，这个"重新评估"必须往一个特定方向收敛，而不是简单地"信任升级"：

> Two rules are load-bearing and must survive any future edit:
> - No message here carries a credential. ...
> - No message here can express "fetch this URL" or "run this command". ...

子任务一设计文档原设想的"下发部署规格"如果字面实现——把 `Spec.Image`/`GPUDevices`/`ModelPaths`/`StoragePaths` 从 Gateway 推给 Agent——就是把镜像名+运行参数交给 Agent 执行，这正是"run this command"本身，比模型分发子任务二当初设想的"下发 URL"更直接地撞上这条规则：一个部署规格本质上就是"用这些参数跑这个镜像"的指令，而 URL 好歹还只是"去这个地址取数据"。

模型分发子任务二（[`2026-09-17-p2-model-distribution-subtask2-design.md`](2026-09-17-p2-model-distribution-subtask2-design.md)）当年在同样的设计过程中发现"下发 URL"违规，转向"Agent 本地清单 + 按名字触发"（`ModelPullTrigger{names}` / `ModelPullReport`）。本文档沿用完全相同的转向：

**镜像、GPU 设备、挂载路径、内存限制仍然 100% 来自 Agent 本地 flag（子任务一已有的信任级别不变，不需要"重新评估"成更高信任）；控制面只能对 Agent 本地已声明的那一个 Managed 实例下发一个封闭的生命周期动作（START / STOP / RESTART），Agent 用自己本地的 Spec 执行这个动作，永远不接受外部传入的镜像名或路径。**

这样"部署规格"的"下发"变成了"（远程触发）应用本地当前配置"——运维想换镜像版本，仍然要先改本机 flag/清单文件再触发 RESTART，这正是子任务一"已知缺口"里"换版本必须先手动 Stop 再改 flag 重启 Agent"的正式解法：不必重启整个 Agent 进程，但镜像来源的信任边界完全不变。子任务三（排空升级编排）可以直接在 RESTART 之前插入"检查有没有正在跑的 Job"，不需要新的信任升级。

推论：`ComfyUIManagedAction` 甚至不需要携带容器名——今天一个 Agent 进程最多管理一个 Managed 实例，动作总是针对 Agent 本地已声明的那一个 Spec 执行；一台机器上想管理多个 Managed 实例，本身就需要子任务一已知缺口"每个部署一个 ID"的部署规格契约先落地，本文档不提前设计一个没有消费方的字段。

## 二、现状核实

- `RuntimeConfig`/`ModelPullTrigger` 是仓库里两种不同"下发"模式的现成先例：`RuntimeConfig{action, runtime_id, spec}` 是"声明式全量下发"（本次故意不采用，因为 spec 里正是镜像/路径这些不能跨隧道的字段）；`ModelPullTrigger{names}`/`ModelPullReport{pulls}` 是"按名字触发 + 轮询式全量状态回传"（本次采用的模式）。
- Agent 侧既有 `tunnel.ModelPuller` 接口（`service/aiServeWeaveAgent/tunnel/client.go`）：`Trigger(names []string)`、`Snapshot() []modelpullstatus.Status`，均不带 `ctx`——耗时工作的 `ctx` 在 `modelpull.NewPuller(ctx, cfg, specs, clock)` 构造时就已经注入并长期持有。`control.go` 的 `handle()` 收到 `GatewayControl_ModelPullTrigger` 时调用 `Trigger` 后立即强制发一次报告；日常汇报则是 `statusPoll` 定时器触发时轮询 `Snapshot()`，与上次发送的确定性编码比较，不同才真正发送。
- Gateway 侧 `tunnelserver.Server.TriggerModelPull`/`ModelPullStatus` 只对连到本副本的节点生效，不跨副本转发（`tunnelserver` 包的"不转发"边界）。**没有等价的 `PushConfig`/`TriggerConfig` 方法**——`RuntimeConfig` 目前只在 Agent 侧单测里被直接构造，Gateway 从未真正下发过它，因此本次 `TriggerComfyUIManagedAction`/`ComfyUIManagedStatus` 只能模仿 `TriggerModelPull`/`ModelPullStatus` 的形状，没有"声明式下发"那条路可抄。
- Gateway 侧 HTTP 面 `service/aiServeWeaveGateway/modelpullapi/`：独立监听器 `-model-pull-addr`（compose 里是 `:8092`，只在容器内部网络可达，不发布宿主机端口）+ 独立 token `AISW_GATEWAY_MODEL_PULL_TOKEN`，与只读的 `-admin-addr`（`:8091`）分开——"触发是写操作,泄漏后果比读清单重"。本文档新增 `comfyuimanagedapi`，用 `:8093` 与独立的 `AISW_GATEWAY_COMFYUI_MANAGED_TOKEN`——不能并进 `-model-pull-addr`：两者都是写操作但控制的是不同的能力（下载模型 vs 启停容器），泄漏后果面不同，沿用"每种写能力一把独立密钥"的既有先例。
- `runtime.Manager.Snapshot()` 已经通过既有的 `RuntimeStatus`/`RuntimeSnapshot` 机制把每个已注册 runtime（含 Managed ComfyUI 实例，它用与 External 完全相同的 `manager.Add` 路径注册）的健康状态（`HealthReport{State,ErrorSummary,...}`）汇报给 Gateway——**这条汇报链路已经存在，不需要本次重建**。本次要新增汇报的是"容器还没被 `manager.Add` 注册之前，或被 `manager.Remove` 之后"这段 `RuntimeStatus` 覆盖不到的容器生命周期状态（pending/starting/running/stopped/failed），二者互补而非重复。因此**不做**"ready/degraded"这种结合两条汇报链路的复合状态——那需要在 Gateway 侧关联两份数据，且当前没有消费方，保持子任务一文档 2.2 节"这处缺口留给后续评估"的判断不变，不在本次扩大范围。
- `runtime.Registry` 的 `Factory func(cfg Config, deps Dependencies) (Runtime, error)` 在测试里可以独立注册假实现；`common/runtime/internal/runtimetest.Runtime` 是 `internal` 包，`comfyuimanaged` 的测试不能导入，但可以在测试文件里手写一个满足 `runtime.Runtime` 接口的最小假实现并注册到测试自己构造的 `Registry`——本文档的 Supervisor 单测即采用此法，不依赖真实 Docker 或真实 ComfyUI HTTP 服务器。

## 三、契约设计

### 3.1 共享类型：`common/comfyuimanagedstatus`

新增包，结构比照 `common/modelpullstatus`（包级双语文档、`int`-based 枚举 + `String()`、字段双语行内注释）：

```go
package comfyuimanagedstatus

type State int
const (
	StateUnspecified State = iota // 零值：本地未配置 Managed 实例
	StatePending                  // 容器尚不存在
	StateStarting                 // 容器已创建/正在重启，端口尚未应答
	StateRunning
	StateStopped                  // 容器正常退出（exit code 0）
	StateFailed                   // 容器非零退出，或 docker 本身出错
)

type Action int
const (
	ActionUnspecified Action = iota
	ActionStart
	ActionStop
	ActionRestart
)

type Status struct {
	ContainerName string
	State         State
	UpdatedAt     time.Time
}
```

`comfyuimanaged.State`（子任务一已有）改为该包的类型别名（`type State = comfyuimanagedstatus.State`），既有五个常量改成对该包常量的重导出——子任务一在本文档落地时尚未提交，直接原地改不算破坏性变更；这样 `Launcher.Status` 的返回值不需要转换就能直接用于协议层，与 `modelpull` 包本身就直接使用 `modelpullstatus.State`（没有自己另定义一套）是同一种"State 只在 common 里定义一次"的先例。

### 3.2 协议契约：`api/proto/tunnel/v1/tunnel.proto`

```proto
message AgentControl {
  oneof body {
    ...
    ComfyUIManagedReport comfyui_managed = 7;
  }
}

message GatewayControl {
  oneof body {
    ...
    ComfyUIManagedAction comfyui_managed_action = 9;
  }
}

message ComfyUIManagedAction {
  ComfyUIManagedActionType action = 1;
}

enum ComfyUIManagedActionType {
  COMFYUI_MANAGED_ACTION_UNSPECIFIED = 0;
  COMFYUI_MANAGED_ACTION_START = 1;
  COMFYUI_MANAGED_ACTION_STOP = 2;
  COMFYUI_MANAGED_ACTION_RESTART = 3;
}

message ComfyUIManagedReport {
  repeated ComfyUIManagedStatus instances = 1;
}

message ComfyUIManagedStatus {
  string container_name = 1;
  ComfyUIManagedState state = 2;
  int64 updated_unix_ms = 3;
}

enum ComfyUIManagedState {
  COMFYUI_MANAGED_STATE_UNSPECIFIED = 0;
  COMFYUI_MANAGED_STATE_PENDING = 1;
  COMFYUI_MANAGED_STATE_STARTING = 2;
  COMFYUI_MANAGED_STATE_RUNNING = 3;
  COMFYUI_MANAGED_STATE_STOPPED = 4;
  COMFYUI_MANAGED_STATE_FAILED = 5;
}
```

`ComfyUIManagedAction` 不携带容器名（一节已论证）；`ComfyUIManagedReport` 用 `repeated` 而不是单个 `ComfyUIManagedStatus`，是跟随 `ModelPullReport` 的既有形状，为将来真正支持多实例的 Agent 留一个不需要再做一次协议迁移的扩展点，今天 Agent 端始终只发 0 或 1 个条目。

### 3.3 跨隧道编解码：`common/tunnelwire/comfyuimanaged.go`

比照 `common/tunnelwire/modelpull.go`：`ComfyUIManagedActionToProto`/`FromProto`、`ComfyUIManagedReportToProto`/`FromProto`（按 `container_name` 排序）及枚举转换函数。测试覆盖 Action 四值往返、State 六值往返、Report 空/非空切片往返。

## 四、Agent 侧设计

### 4.1 `comfyuimanaged.Supervisor`

新文件 `service/aiServeWeaveAgent/comfyuimanaged/supervisor.go`，对 Launcher/Manager 的编排：

- `Start(ctx)`：`Launcher.Start` → `Launcher.WaitReady` → 若 `Manager.Get` 未命中才 `Manager.Add`（已注册则跳过，因为 `Manager.Add` 对重复 ID 会报错）——与 Agent 启动时的既有顺序完全相同，因此 `main.go` 的启动块改为构造一个 `Supervisor` 并调用它的 `Start`，消除这段逻辑在启动路径和 `Trigger` 内部各写一遍的重复。
- `Stop(ctx)`：先 `Manager.Remove`（让调度器停止向它派发新请求）再 `Launcher.Stop`；`Manager.Remove` 失败只记日志、不算致命，与 `Manager.Remove` 自己对未知 ID 的容忍度一致。
- `Trigger(action)`：在 Supervisor 自己长生命周期的 `ctx`（构造时注入，与 `modelpull.NewPuller` 同一做法）下后台执行，避免一次缓慢的 Docker 拉取或 `WaitReady` 轮询阻塞隧道 Control 会话的帧循环；收到一个 Trigger 时若已有另一个动作在途，直接丢弃并记日志、不排队——只有一个容器，排队一个 Stop 在一个在途 Start 之后没有意义。RESTART = Stop 后接 Start。
- `Snapshot()`：每次调用一次真实的 `docker inspect`（`Launcher.Status`），与 `Puller.Snapshot` 的"始终反映当下、靠轮询获取"约定一致，隧道 Control 会话的节流上报依赖这一点；总是恰好返回一个条目。

### 4.2 Agent 侧隧道接线

`tunnel.ClientConfig` 新增 `ComfyUIManaged ComfyUIManager` 字段（接口，比照 `ModelPuller`），`control.go` 新增 `GatewayControl_ComfyuiManagedAction` 分支（触发 + 强制上报）与 `reportComfyUIManaged`/`forceReportComfyUIManaged`（比照 `reportModelPull`/`forceReportModelPull` 的确定性编码去重）。启动时的强制上报、`statusPoll` tick 都并列加上这一路。

`main.go` 把 Managed ComfyUI 的启动块改为构造 `Supervisor` 并把它注入 `ClientConfig.ComfyUIManaged`——未启用 Managed 模式时该字段为 nil interface（不是包着 nil 指针的非 nil interface：`startTunnel` 显式判断 `comfyUIManaged != nil` 才赋值，避免经典的 typed-nil 陷阱）。

## 五、Gateway 侧设计

### 5.1 `tunnelserver`

`node.go` 新增 `comfyUIManaged map[string]comfyuimanagedstatus.Status`（比照 `modelPulls`，整份替换）；`Server.TriggerComfyUIManagedAction(nodeID, action)`/`ComfyUIManagedStatus(nodeID)` 逐行比照 `TriggerModelPull`/`ModelPullStatus`：同样的"节点是否连到本副本"检查、同样的"不转发"边界。`control.go` 新增 `AgentControl_ComfyuiManaged` 分支，落到 `applyComfyUIManagedReport`。

### 5.2 `comfyuimanagedapi`

新包，结构逐一比照 `modelpullapi`：`Config{Token, Trigger, Status, Clock}`，路由 `POST`/`GET /internal/v1/nodes/{node_id}/comfyui-managed`。POST body `{"action":"start"|"stop"|"restart"}`，未知/空字符串 400（`parseAction` 显式拒绝而不是静默映射到 `ActionUnspecified`，一次请求体拼写错误应该是 400 而不是一个悄悄成功的空动作）；成功 202、节点未连接 404；GET 返回 `{generated_at, instances:[{container_name,state,updated_at}]}`。鉴权与 `modelpullapi` 同一常数时间比较。

`main.go` 新增 flag `-comfyui-managed-addr`（默认空即禁用）与环境变量 `AISW_GATEWAY_COMFYUI_MANAGED_TOKEN`，监听/关闭逻辑逐行比照 `-model-pull-addr`。

## 六、已知缺口

如实记录，不阻塞本次交付验收，也不阻塞后续子任务排期：

- **仍是单实例。** `ComfyUIManagedAction` 不携带容器名，一台机器上想管理多个 Managed 实例，需要先有子任务一已知缺口"每个部署一个 ID"的部署规格契约，本文档不提前设计一个没有消费方的字段。
- **没有 ControlPlane 跨副本路由聚合。** 与模型分发子任务二先交付 Gateway 一侧、路由聚合作为后续独立交付同一先例——调用方必须自己知道该问哪个 Gateway 副本；补一层类似 `internal/modelpullrouter` 的聚合留给后续排期。
- **RESTART 不检查有没有正在跑的 Job，也不对比镜像版本是否真的变了。** 纯粹是 Stop 再 Start；排空升级编排是子任务三的范围，子任务三可以直接在调用 RESTART 之前插入 Job 检查，不需要改动本文档的协议或 Supervisor 接口。
- **不新增"ready/degraded"复合状态。** 容器生命周期状态与已有的 `RuntimeStatus` 健康状态是两条独立汇报链路，仍需调用方自己关联；当前没有消费方证明这个组合值得做。
- **审计缺口未补。** Trigger 只写结构化日志，不写审计日志——子任务一文档已如实记录这个缺口依赖控制面把触发动作挪到自己一侧后才有意义，本文档没有改变这个前提（触发仍然经 Gateway 的 `comfyuimanagedapi`，不经控制面）。
- **Console 可见性（子任务五）未做。**

## 七、验证

- `go generate ./api/...` 后无 diff。
- `gofmt`/`go vet`/`go build ./...` 全绿。
- `go test ./...`：新增 `common/tunnelwire` 转换往返测试；`comfyuimanaged` 包新增 `Supervisor` 测试（手写假 `runtime.Manager` + 既有假 docker 脚本双重打桩，覆盖幂等注册/跳过重复注册、Stop 的取消注册顺序、Restart 的 Stop→Start 序列、忙时丢弃重叠触发、Snapshot 对 docker 状态的即时反映）；`tunnel` 包新增 Control 会话的触发转发/节流上报测试；`tunnelserver` 包新增触发/查询对已连接/未连接节点的行为测试；`comfyuimanagedapi` 包新增鉴权、400/404、202/200 响应形状测试。
- `go test -race ./service/...` 全绿。
- `docker compose -f deploy/docker-compose.yaml config`（配合占位环境变量）解析通过，新增 flag 与 `AISW_GATEWAY_COMFYUI_MANAGED_TOKEN` 均出现在渲染结果中。

## 八、与 STATUS.md 的关系

STATUS.md 第 119 条在子任务一交付说明后追加子任务二交付说明，条目整体保持未勾选（子任务三、四、五仍未排期）。与模型分发条目同一先例：现状核实、设计推导与本轮子任务在同一个 session 内完成，其余子任务留给后续排期。

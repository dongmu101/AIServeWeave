# P2「ComfyUI Managed Docker 部署」子任务四：自定义节点允许列表 + 固定版本安装 + 审计日志

本文档交付子任务一设计文档第四节列出的子任务四：「自定义节点允许列表 + 固定版本安装 + 审计日志，并把 `workflowtemplate.Dependencies` 声明的节点名+版本与一个 Managed 实例实际安装的节点对账」。子任务二、三（协议层、Agent 执行、Gateway 触发/查询、ControlPlane 跨副本路由聚合、排空升级检查）已交付，详见 STATUS.md 第 119 条与其引用的两份设计文档。

范围边界：与子任务二/三同一先例——只做协议层 + Agent 侧执行 + Gateway 侧触发/查询 + ControlPlane 跨副本转发与审计，**不做 Console 可见性**（子任务五）。对账做成一个纯函数 + 一个只读 Gateway 端点，**不接入调度决策**（调度器今天也不做模型依赖的硬校验，custom node 依赖没有理由先于模型依赖被拿来挡请求）。

## 一、现状核实与设计收窄

- 1.8 节（子任务一文档）已确认：`workflowtemplate.Dependencies.CustomNodes` 只做结构校验，从不与任何已连接节点实际安装的东西交叉核对，因为"目前没有节点上报这类信息"。子任务四要补的正是这条汇报链路，以及一个能做比较的地方。
- `tunnel.proto` 头部的 load-bearing 规则（"不得表达 fetch this URL/run this command"）与子任务二收窄"下发部署规格"为"触发本地已声明动作"的推导，同样适用于自定义节点：一个自定义节点的安装源是一个 git 仓库 URL——这正是 "fetch this URL"本身。因此安装源必须留在 Agent 本地配置（新增 `-comfyui-managed-custom-nodes` flag，形状比照 `-comfyui-managed-model-paths` 的 `key=value,key=value` 解析惯例），控制面/Gateway 只能按**名字**触发"安装 Agent 本地允许列表里已经声明的这一个节点"——与 `ModelPullTrigger{names}` 完全同一模式，而不是子任务二收窄出的"封闭动作枚举"模式（那是因为生命周期动作只有三种、不带参数；装哪个自定义节点是一个开放的名字集合，模型拉取的先例更贴切）。
- ComfyUI 官方镜像与社区镜像的自定义节点目录路径不统一（常见 `/comfyui/custom_nodes`，也有 `/ComfyUI/custom_nodes` 等）。本文档不猜测某个具体镜像的路径，新增独立 flag `-comfyui-managed-custom-nodes-dir`（默认 `/comfyui/custom_nodes`，运维按自己选用的镜像覆盖）——如实记录这是一个假设，不是所有镜像都验证过。
- 容器里是否有 `git` 可执行文件同样是镜像决定的，不是本包能控制的；安装失败时的错误信息里会包含这一点，作为已知缺口如实记录（六节）。
- 安装动作在容器**运行中**执行（`docker exec`），不需要容器重启就能把文件写进 `custom_nodes` 目录；但 ComfyUI 本身只在进程启动时扫描该目录，新装的节点要生效仍需要运维之后另外触发一次 RESTART（子任务二已有能力）——本文档不让安装动作自动级联一次重启，理由与 `Supervisor.Trigger` 本身"一次只做一件事、不自动串联"的既有克制一致，安装和生效是两个可独立观察、独立失败的步骤。

## 二、契约设计

### 2.1 共享类型：`common/comfyuimanagedstatus`

新增 `CustomNodeStatus{Name, Version string}`（`Version` 是从容器里读回的、实际生效的 pinned ref，不是允许列表里声明的期望值——两者可能不一致，例如安装失败后残留了一次旧版本）。`Status` 结构新增字段 `CustomNodes []CustomNodeStatus`。

新增一个纯比较函数 `ReconcileCustomNodes(declared []DeclaredCustomNode, installed []CustomNodeStatus) []CustomNodeMismatch`，`DeclaredCustomNode{Name, Version string}`（调用方自己把 `workflowtemplate.NodeDependency` 转换成这个本地类型，本包不导入 `workflowtemplate`，避免两个無关注意力的包互相耦合）。`CustomNodeMismatch{Name, Reason}`，`Reason` 取值 `"missing"`（声明了但未安装）或 `"version_mismatch"`（都存在但 `Version` 不同；`Declared.Version` 为空表示"任意版本都行"，不产生 mismatch）。这是本文档"对账"的全部实现：纯函数，无 I/O，调用方决定拿它做什么（本文档二.4 节用它搭一个只读端点，不做别的）。

### 2.2 协议契约：`api/proto/tunnel/v1/tunnel.proto`

```proto
message GatewayControl {
  oneof body {
    ...
    ComfyUIManagedCustomNodeInstallTrigger comfyui_managed_custom_node_install = 10; // subtask 4
  }
}

// 只按名字触发，从不携带仓库 URL——与 ModelPullTrigger 同一条 load-bearing 理由。
message ComfyUIManagedCustomNodeInstallTrigger {
  string name = 1;
}

message ComfyUIManagedStatus {
  string container_name = 1;
  ComfyUIManagedState state = 2;
  int64 updated_unix_ms = 3;
  repeated ComfyUIManagedCustomNode custom_nodes = 4; // subtask 4
}

message ComfyUIManagedCustomNode {
  string name = 1;
  string version = 2; // 容器里实际读回的版本，不是允许列表声明的期望值
}
```

### 2.3 跨隧道编解码：`common/tunnelwire/comfyuimanaged.go`

新增 `ComfyUIManagedCustomNodeInstallTriggerToProto`/`FromProto`；`comfyUIManagedStatusToProto`/`FromProto` 扩展 `CustomNodes` 字段的编解码（按名字排序，与 Report 整体排序同一纪律）。

## 三、Agent 侧设计

### 3.1 `Launcher` 新方法（`service/aiServeWeaveAgent/comfyuimanaged/customnodes.go`）

```go
// NodeSpec is one allowlisted custom node's pinned install source, entirely
// local to this Agent (never accepted from the Gateway or control plane).
type NodeSpec struct {
	RepoURL string // git remote, cloned with --depth 1
	Ref     string // branch, tag, or commit; recorded back as the installed version
}

// InstallCustomNode installs name (looked up in allowlist; unknown names are
// refused before any docker command runs) into containerName's
// customNodesDir via `docker exec` + git — idempotent: a directory that
// already exists is fetched and checked out to Ref rather than re-cloned.
func (l *Launcher) InstallCustomNode(ctx context.Context, containerName, customNodesDir, name string, allowlist map[string]NodeSpec) error

// ListCustomNodes reads back, via `docker exec`, every subdirectory of
// customNodesDir and the pinned-version marker this package itself writes
// after a successful install (a directory with no marker — installed by
// something other than this package, or never finished installing —
// reports Version "unknown", not an error).
func (l *Launcher) ListCustomNodes(ctx context.Context, containerName, customNodesDir string) ([]comfyuimanagedstatus.CustomNodeStatus, error)
```

要点：

- `buildInstallScript(dir, name string, spec NodeSpec) string` 是纯函数（与 `buildRunArgs` 同一纪律）：不存在的目录 `git clone --depth 1 --branch <ref> <repoURL> <dir>/<name>`；已存在则 `git -C <dir>/<name> fetch --depth 1 origin <ref> && git -C <dir>/<name> checkout FETCH_HEAD`；成功后 `echo <ref> > <dir>/<name>/.aisw-version`。整段脚本经 `docker exec <container> sh -c '<script>'` 一次性执行，`sh -c` 的参数只由本地允许列表的 `RepoURL`/`Ref`（而不是任何跨隧道输入）拼接而成——`Ref`/`name` 校验只接受 `[A-Za-z0-9._/-]` 字符集，拒绝含空格或 shell 元字符的值，避免本地配置的排版失误意外改变脚本结构（配置来源可信，但同样的校验成本很低，做了更安心）。
- `InstallCustomNode` 对未知 `name`（不在 `allowlist` 里）直接拒绝，不发任何 docker 命令——与 `modelpull.Puller` 对未知名字的"before any network call"拒绝同一纪律。
- `ListCustomNodes` 用 `docker exec <container> sh -c 'for d in <dir>/*/; do ...; done'` 一次性列出所有目录名 + 各自的 marker 内容，不逐个目录单独 `docker exec`（避免 N 次进程创建开销）。

### 3.2 `Supervisor` 扩展

`NewSupervisor` 新增参数 `customNodesDir string, allowlist map[string]NodeSpec`。新增方法 `InstallCustomNode(name string)`：复用 `Trigger` 同一套忙碌互斥（`s.busy`）与后台 goroutine 模式——安装与生命周期动作共享同一把互斥锁，因为两者都在操作同一个容器，允许并发的收益不值得引入第二套状态机。`Snapshot()` 扩展为额外调用一次 `Launcher.ListCustomNodes`，并入返回的 `Status.CustomNodes`。

### 3.3 Agent 接线

`main.go` 新增 `-comfyui-managed-custom-nodes`（`name=repoURL@ref,...`）与 `-comfyui-managed-custom-nodes-dir`（默认 `/comfyui/custom_nodes`）两个 flag，解析进 `map[string]comfyuimanaged.NodeSpec`，传给 `NewSupervisor`。`tunnel.ClientConfig.ComfyUIManaged`（`ComfyUIManager` 接口）新增方法 `TriggerCustomNodeInstall(name string)`。`control.go` 的 `handle()` 新增 `GatewayControl_ComfyuiManagedCustomNodeInstall` 分支：调用 `Trigger` 后同样强制上报一次（与生命周期动作分支同一模式）。

## 四、Gateway 侧设计

### 4.1 `tunnelserver`

`Server.TriggerComfyUIManagedCustomNodeInstall(nodeID, name string) error`——逐行比照 `TriggerComfyUIManagedAction`：同样的"连到本副本才转发"检查，广播 `GatewayControl_ComfyuiManagedCustomNodeInstall`。`applyComfyUIManagedReport` 不用改（它已经整份替换 `n.comfyUIManaged`，新的 `CustomNodes` 字段随 `comfyuimanagedstatus.Status` 一起被整份替换）。

### 4.2 `comfyuimanagedapi`

新增路由 `POST /internal/v1/nodes/{node_id}/comfyui-managed/custom-nodes`，body `{"name":"..."}`，空名 400，转发失败（未连接本副本）404，成功 202——逐行比照现有触发端点，不做排空检查（安装不影响正在运行的推理请求，只写文件）。`instanceStatusJSON` 新增 `custom_nodes` 字段（`[]{"name","version"}`），随现有 GET 端点一起返回,不新增端点。`Config` 新增 `TriggerCustomNodeInstall func(nodeID, name string) error`。

## 五、ControlPlane 侧设计

`comfyuimanagedrouter.Router` 新增 `InstallCustomNode(ctx, nodeID, name string) (Result, error)`，逐行比照 `Trigger`；`InstanceStatus` 新增 `CustomNodes []CustomNodeStatus` 字段（`CustomNodeStatus{Name, Version string}`），`renderInstances` 随 wire 格式扩展一并解码。`logic.Service` 新增 `InstallComfyUICustomNode`，逐行比照 `TriggerComfyUIManagedAction`，成功转发时记审计 `model.ActionComfyUIManagedCustomNodeInstall`（新增审计代号）。`handler` 新增路由 `POST /operator/v1/nodes/:id/comfyui-managed/custom-nodes`（沿用 `ComfyUIManagedRouter != nil` 才挂载的既有判断），`types` 新增 `ComfyUIManagedCustomNodeInstallRequest{Name string}`；GET 响应的 `ComfyUIManagedInstanceStatus` 新增 `CustomNodes` 字段。复用既有 `ComfyUIManaged` 配置块与 token，不新增监听器或密钥。

## 六、对账端点（Gateway 只读）

新增 `GET /admin/v1/comfyui-managed/reconcile?node_id=X&template_id=Y`（挂在既有只读 `adminapi`，会话/token 边界与该包其余端点一致）：读 `tunnelserver.Server.ComfyUIManagedStatus(nodeID)` 得到 `installed`，读工作流目录得到 `template_id` 的 `Dependencies.CustomNodes` 得到 `declared`，用 `comfyuimanagedstatus.ReconcileCustomNodes` 得到差异列表原样返回。节点未连接本副本或模板不存在均答 404。这只是一次只读比较，不写审计、不影响调度——如实记录：这不是"调度前置校验"（六节已知缺口）。

## 七、已知缺口

- **不做调度前置校验。** 对账端点是运维/未来 Console 可以主动查询的诊断工具，不接入 P03 的调度决策——模型依赖同样没有这层硬校验，custom node 不应该先于它被特殊对待。
- **镜像必须自带 `git`，且自定义节点目录路径因镜像而异。** 两者都只在安装失败时的错误信息里体现，本文档不做镜像探测或路径自动发现。
- **安装成功不自动重启。** 运维仍需另外触发 RESTART 才能让 ComfyUI 加载新节点；不自动级联，理由三节已述。
- **不做依赖安装（`requirements.txt`/pip）。** 只克隆代码到目录；ComfyUI 官方镜像大多把常见依赖已经打进镜像或靠 ComfyUI-Manager 处理，本文档的允许列表机制不是 ComfyUI-Manager 的替代品，只解决"从 Agent 本地已批准的固定源把代码放到目录里"这一步。
- **审计只覆盖经 ControlPlane 转发触发的安装。** 与子任务二对生命周期动作的审计范围一致——绕过 ControlPlane 直接打 Gateway 的 `comfyuimanagedapi` 不会留痕，因为审计表只存在于控制面。
- **对账端点不认证租户边界**（沿用 `adminapi` 既有的运维/平台可见性假设，不引入新的鉴权模型）。
- **Console 可见性未做**（子任务五范围）。

## 八、验证

- `go generate ./api/...` 后无 diff。
- `gofmt`/`go vet`/`go build ./...` 全绿。
- `go test ./...`：`common/comfyuimanagedstatus` 新增 `ReconcileCustomNodes` 表驱动测试；`common/tunnelwire` 新增新消息的编解码往返测试；`comfyuimanaged` 包新增 `buildInstallScript` 纯函数测试与假 docker 脚本驱动的 `InstallCustomNode`/`ListCustomNodes` 集成测试，以及 `Supervisor.InstallCustomNode` 的忙碌互斥测试；`tunnel`/`tunnelserver`/`comfyuimanagedapi`/`comfyuimanagedrouter`/`logic`/`handler`/`adminapi` 各自新增对应用例。
- `go test -race ./service/...` 全绿。

## 九、与 STATUS.md 的关系

STATUS.md 第 119 条追加子任务四交付说明，条目整体保持未勾选（仅剩子任务五 Console 可见性未排期）。

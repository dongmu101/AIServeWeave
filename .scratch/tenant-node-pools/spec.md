# 租户专属节点（A05 租户节点池）

Status: ready-for-agent

Triage（2026-10-31）：被绑定租户同时可用共享节点；一台节点可绑定多个租户；Console 运维页面本期交付。

## 目标

平台运维可以把一个 Agent 节点指定为一个或多个租户的**专属节点**：被绑定的租户可以调度到它，其他租户的任何请求都不会落到它上面，也在租户可见的视图里看不到它。未绑定的节点保持现状，由全体租户共享。

## 现状与缺口

- 调度器按模型名选节点，不感知租户；未配置路由的模型名会派发到任何上报了该名字的节点（`scheduler.candidatesKeyed`）。
- 节点标签由 Agent 自报，只是偏好，不能当权限（docs/architecture.md「节点标签是偏好，不是权限」）。
- 模型白名单（`common/modelaccess`）按模型别名授权，空列表即不限制，无法表达「某节点只给某租户」。
- 原 A05 拆分文档（已从仓库删除，见 git 历史 `docs/superpowers/specs/2026-09-15-a05-scheduling-extension-breakdown-design.md`）给出的原则继续沿用：授权与标签是两个数据源；写入方只能是平台运维；未配置等于不限制。

## 规则

- 一台节点 `n` 绑定的租户集合记为 `T(n)`。`T(n)` 为空表示共享节点。
- 租户 `t` 可以使用节点 `n`，当且仅当 `T(n)` 为空，或 `t ∈ T(n)`。
- 绑定按 **node_id** 建立。node_id 来自 Registry 签发的节点证书，Agent 无法伪造成别的节点；标签不参与授权。
- 被绑定的租户依然可以使用共享节点；专属节点只是对其他租户关闭。
- 节点选择器、模型白名单、熔断与排序都照旧生效；专属节点过滤是在 `pickBy` 里额外加的一道独立过滤。

## 契约：`common/nodeaccess`

ControlPlane 和 Gateway 共用一个新包，形状参照 `common/modelaccess`：

- `Policy{Excluded []string}`：对某个租户关闭的节点集合。`Allows(nodeID)` 判断能否使用；`Unrestricted()` 在集合为空时为真。
- `NormalizeTenants`：去空白、去重、排序，并检查每台节点的租户数上限。
- 上限：`MaxDedicatedNodes = 1000`，`MaxTenantsPerNode = 64`。

下发的是「对该租户关闭的节点」，而不是全量绑定表，原因有三：

1. Gateway 的判断只需一次集合查询；
2. 租户永远拿不到这份数据（它只出现在内部校验接口里）；
3. 没有这个字段时，Gateway 行为与现在完全一致。

## ControlPlane

- **数据库**：在 base 迁移中新增 `0011_node_access.sql`，PostgreSQL 与 MySQL 各一份；同步 `schemacheck`。
  - 表 `node_access(node_id PK, tenants_json, updated_by, updated_at)`，与 `model_access` 同构。
  - 删除一行即恢复共享。
- **store**：gormstore 与 memstore 实现 `ListNodeAccess`、`GetNodeAccess`、`PutNodeAccess`、`DeleteNodeAccess`。
- **logic**：
  - `SetNodeAccess(actor, nodeID, tenants)`：只允许平台运维调用。校验 node_id 格式和每个租户都存在（不存在返回 404，与 `checkModelAccessScope` 一致），并检查节点数上限。写入后触发 `InvalidateAll`，并写审计 `node_access.update`。
  - `nodePolicy(tenantID)`：读取全部行，算出 `Excluded`。
- **校验接口**：`VerifyKeyHash` 新增 `Nodes *nodeaccess.Policy`；`/internal/v1/apikeys/verify` 响应新增 `nodes` 字段，不受限时省略。
- **运维 API**（`requirePlatformSession`）：
  - `GET /operator/v1/node-access`：返回全部专属绑定，最多 1000 行，一次返回。
  - `GET /operator/v1/node-access/:node_id`
  - `PUT /operator/v1/node-access/:node_id`，请求体 `{"tenants":[...]}`。空列表表示恢复共享。不自动重试。
- **租户模型页**：`TenantModels` 去掉对该租户关闭的节点上的服务。可以与 `TenantModelFilter` 合并成一次授权读取。

## Gateway

- `controlplaneclient.Verifier` 解码 `nodes`，填入 `Identity.Nodes`。
- 鉴权中间件在 `Identity.Nodes` 非空时调用 `scheduler.WithNodeAccess(ctx, policy)`，与 `WithModelAccess` 并列。
- scheduler：
  - `pickBy` 及其上游（`candidates*`、`pick`、`workflowCandidates`）接收 `ctx`，对 `!nodeAllowed(ctx, node.NodeID)` 的节点直接跳过，判断时机与节点选择器相同。
  - 对外的 `MessagesCandidates`、`ImageCandidates`、`TranscriptionCandidates`、`WorkflowCapableCandidates` 增加 `ctx` 参数。
  - 直接向既定候选派发的方法也要校验节点，不通过时返回 `ErrNodeNotAllowed`。该错误包装 `ErrNoCapableNode`，对外与「模型不存在」无法区分。涉及的方法：
    - `Messages`（工具续接）
    - `Transcribe`
    - `SubmitWorkflowTo`
    - `UploadInput`
  - `Models()`：在未配置路由的分支里跳过关闭的节点；配置了路由的分支经由 `candidatesWith(ctx, …)` 自然生效。
- **不受影响的路径**：已提交 Job 的 Status、Subscribe、Cancel、Artifacts、OpenArtifact，以及后台 Job 恢复。原因：
  - run 已经在那台节点上，Job 归属已经按租户校验过；
  - 后台恢复的 context 不带授权，本来就不受限。
  - 后果：绑定变更后提交的新工作会立即受约束，变更前提交的 Job 能把结果取回。
- **Messages 工具续接**：如果续接时目标节点已对该租户关闭，按「不存在」拒绝，会话结束，客户端需要重新开一个会话。

## 生效与兼容

- 变更后，经由 `InvalidateAll` 让 Gateway 的校验缓存立即失效；控制面若在提交后、发布失效前崩溃，最迟在校验缓存 TTL（默认 30s）后生效。
- **ControlPlane 新、Gateway 旧**：Gateway 会忽略 `nodes` 字段，绑定不生效。**要先升级全部 Gateway，再配置绑定。**这一点写进部署文档。
- **Gateway 新、ControlPlane 旧**：没有这个字段，行为与现在一致。
- 静态 key 模式和免认证模式下没有控制面，也就没有绑定，不受影响。

## Console

- 平台运维页面 `/operator/node-access`：
  - 列出节点，节点来源是 Fleet；
  - 为每台节点多选租户，保存后整体 PUT；
  - 只走 `surface: "operator"` 白名单，有界请求体，不自动重试；
  - 文案覆盖八种语言。
- 契约镜像放在 `lib/console/node-access.ts`，并配测试。

## 验收

1. 节点 N 绑定给 A 后：
   - B、C 请求 N 上的模型返回 404 / `model_not_found`，路由别名和原始模型名都一样；
   - A 的请求可以落到 N；
   - B、C 的 `/v1/models` 和租户模型页都看不到 N 上的服务。
2. 共享节点和 N 上报同一个模型名时，B、C 的请求只落在共享节点。
3. 没有 `ctx` 授权的调度（后台 Job 恢复）、已提交 Job 的状态与产物读取，不受绑定影响。
4. 绑定变更后，下一次请求（经过缓存失效后）立即按新绑定执行。
5. 未配置任何绑定时，所有既有测试的预期不变。
6. 迁移在 PostgreSQL 与 MySQL 上都可重复执行；校验和与 dirty 检查通过。

## 任务拆分

1. `common/nodeaccess` 契约
2. ControlPlane：迁移、store、logic、运维 API、校验接口、租户模型页过滤
3. Gateway：verifier、中间件、调度器执行点
4. Console 运维页面（视 triage）
5. 文档：根 STATUS A05、ControlPlane/Gateway STATUS、architecture 代码地图、AGENTS 代码地图、部署升级顺序

## 已知取舍

- 不提供「租户只能用专属节点」的独占模式；需要时再在 `Policy` 上加字段。
- 不提供按标签批量绑定；绑定只认 node_id。
- 绑定不检查 node_id 是否已在 Registry 注册，可以先绑后接入；页面从 Fleet 列表选节点，以减少笔误。

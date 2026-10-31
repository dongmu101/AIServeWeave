# 商户自带节点（自有 Agent，只供本商户使用）

Status: ready-for-agent

实现（2026-10-31）：代码与默认测试已完成，行为说明以 [ControlPlane STATUS「商户自有节点」](../../service/aiServeWeaveControlPlane/STATUS.md#商户自有节点) 为准；`0012` 迁移的真实数据库验证与真实 Agent 接入验收未做。

Triage（2026-10-31）：准入用控制面全局开关，默认开启、每商户最多 10 台（开启后所有商户都可自助接入，含自助注册商户）；请求跑在商户自己的节点上不扣点数；平台运维对商户自有节点可停用、可设维护，但不能把它绑定给其他商户。出售给其他商户、分成结算不在本期。

## 目标

商户的 owner/admin 在租户控制台自助生成 Agent 接入包，装到自己的机器上。该节点只服务这个商户：其他商户的请求不会调度到它，也在任何租户可见视图里看不到它。商户可以随时删除自己的节点，删除即在 Registry 停用该身份并断开现有连接。

## 关键决定：归属写进 node_id

商户自有节点的 node_id 由控制面生成，格式为 `tnode.<tenant_id>.<32 位小写十六进制>`，并签发**绑定该 node_id** 的一次性令牌。node_id 由 Registry 写入证书，Agent 无法改动。

- Gateway 与控制面只看 node_id 就能判断归属：`nodeaccess.OwnerOf`。不需要把「其他商户的自有节点」放进每个 Key 校验结果的排除名单——那样名单会随商户数增长，且每次接入都要让所有缓存失效。
- 新节点在任何缓存刷新之前连上来，也不会被别的商户用到：判断不依赖任何同步数据。
- 前缀 `tnode.` 开头但格式不对的 id 视为「无人可用」（fail-closed）。
- Registry 拒绝用**未绑定**令牌注册 `tnode.` 前缀的 node_id，防止有人自报一个别家商户的 id 再骗运维批准。
- 运维的专属节点绑定接口拒绝 `tnode.` 节点（409），运维页面把它们显示为只读的「商户自有」。

规则（`nodeaccess.Permits(tenant, policy, node)`）：

1. node 是商户自有：仅当 owner == tenant 时允许。
2. 否则按既有专属节点 policy（排除名单）判断。

没有身份的 context（后台 Job 恢复、未接控制面的部署）不受限制，与既有专属节点语义一致；后台恢复只处理已在该节点上的 run。

## 控制面

配置 `TenantNodes`（默认开启；未生效时路由不挂载 → 404，生效条件见 ControlPlane STATUS）：

| 键 | 默认 | 含义 |
| --- | --- | --- |
| `Enabled` | `true` | 总开关；还要求两个地址与 `Registry.Addr`/`AdminToken`/`CACertFile` 都已配置才生效 |
| `RegistryAddr` / `GatewayAddr` | 空 | 开启时必填，写进接入包的外部地址（host:port），不作为服务端网络目标 |
| `MaxPerTenant` | `10` | 每商户同时有效（未删除）的自有节点数，1–100 |
| `MaxCreatesPerDay` | `10` | 每商户滚动 24 小时内可生成的接入包数，1–1000；防止反复建删刷 Registry 停用名单 |

表 `tenant_nodes`（迁移 `0012_tenant_nodes.sql`，PostgreSQL/MySQL）：`node_id` PK、`tenant_id`、`created_by`、`created_at`、`enroll_by`（注册截止时间）、`revoked_at`（可空）、`revoked_by`；索引 `(tenant_id, created_at)`。归属以 node_id 为准，该表只用于列表、计数与审计。

租户 API（租户会话；读：所有角色，写：owner/admin）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin/v1/nodes` | 本商户自有节点，最新在前，最多 200 条；附在线状态（机群不可用时为未知） |
| POST | `/admin/v1/nodes` | `{"ttl_minutes":15..1440}` → 201，接入包 ZIP（base64）只返回一次 |
| DELETE | `/admin/v1/nodes/:node_id` | Registry `DisableNode` 成功后标记删除；已删除或非本商户的返回 404 |

生成顺序：校验额度 → 写行 → 签发令牌并打包；签发失败删除该行。并发下额度可能被超出一两个（软上限，与专属节点上限同样取舍）。审计（租户范围）：`node.tenant.create`、`node.tenant.revoke`，不记令牌。

计费：Gateway 只对 `tnode.` 节点在用量记录里附 `node_id`；控制面在 owner == 记录租户时把费用记为 0（仍写 `usage_charges` 行，`charged=false`）。只附自有节点的 id，使未开启本功能的部署推送内容不变，旧控制面不会因未知字段拒收批次。

商户模型页（`/admin/v1/models`）按 `Permits` 过滤。

## Gateway

- `scheduler.WithNodeAccess(ctx, tenantID, policy)`：鉴权中间件只要有租户身份就附上；`nodeAllowed` 改用 `nodeaccess.Permits`。
- 用量记录 `node_id` 仅自有节点附带。

## Console

- 租户导航新增「我的节点」`/console/nodes`：列表（状态、生成时间、注册截止、删除）、生成接入包（下载 ZIP，提示只显示一次）、删除确认。owner/admin 才显示写操作。
- 运维「专属节点」页：`tnode.` 节点只读，标「商户自有」。
- 8 种语言。

## 不在本期

- 出售给其他商户、分成与提现。
- 余额耗尽（402）的 prepaid 商户仍被拦在 Gateway 前门，不能改为「只用自有节点」继续服务。
- 商户自有节点的模型分发、ComfyUI 管理等运维动作仍只由平台运维发起。

## 升级顺序

Registry（保留前缀检查）、控制面、Gateway 全部升级后再让 `TenantNodes` 生效；分批升级时先设 `Enabled: false`。旧 Gateway 不认识 `tnode.` 归属，会把自有节点当作共享节点派发给所有商户。

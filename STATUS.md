# Agent 默认不共享与运维控制（2026-11-09）

机群页现可直接对每个平台注册节点确认开启或停止共享（2026-11-10）。新增 Agent 接入/审批共享选项与共享设置，默认不共享；未配置的 Agent 也不进入普通租户的模型目录和调度池。有效的独享账号购买权益与专属租户绑定保留。基础迁移 `0015_node_sharing.sql`、授权载荷和 Gateway 检查已实现，使用及升级顺序见[Agent 共享设置](docs/agent-sharing.md)。

控制面与 Gateway 相关默认/竞态测试、326 项 Console 单元测试、共享操作与运维宽窄屏 Chrome 检查通过；隔离 MySQL 的迁移、模式持久化与账号购买回归通过。本地 Gateway、控制面和 Console 已备份后重建，真实运维/租户登录验证：1 台 Agent 已接入、无显式共享，租户模型目录不包含其服务；共享设置 API 返回 200，租户写入被拒绝。PostgreSQL live、生产升级和真实 CLI 账号联调尚未验收。


## Agent 账号独享包月（2026-11-09）

已交付 Claude Code 与 Codex 账号商品、点数余额购买/续费、独享授权与 Console 入口。同一个 Agent 中的两种账号可分别售给不同商户。账号以 Agent ID + 运行时 ID 定位，按 UTC 自然月延长期限；购买后切换为账号包月模式，仅可调用自己已购且未到期的账号。接口、迁移和升级顺序见 [账号包月说明](docs/billing-accounts.md)。

账号包月现支持独立开关 `AccountSales.Enabled`，不要求配置 Dodo。新增表单改为选择 Agent 与实际上报的 Claude Code / Codex 账号，排除重复商品，并支持刷新清单与失败重试。本地 Docker 的控制面和 Console 已重建、启用独立开关，真实运维登录后账号 API 返回 200；Agent 接入后的默认共享准入见上节，真实 CLI 账号售卖联调尚未验收。

两种账号同 Agent 分别售卖的 logic 测试和真实隧道加模拟后端的 scheduler 测试（Claude 原生 Messages / Codex Chat）已通过竞态检查；两种真实上游登录的售卖联调仍待验收。

默认测试、相关竞态检查、Console 类型检查与生产构建通过；Chrome 计费专项 11 项和隔离 MySQL 9.7 账号事务实测通过。PostgreSQL live 测试未运行，生产升级、真实付款及实际 CLI 账号联调尚未验收。

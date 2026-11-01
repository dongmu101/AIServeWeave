# 01 控制面：兑换码存储、生成与兑换

Status: ready-for-agent

依据：[spec.md「控制面」](../spec.md#控制面)。

## 范围

- 迁移 `base/{postgres,mysql}/0013_agent_install_codes.sql`；`model`、`schemacheck`、`store` 接口、gormstore、memstore。
- 兑换码生成（20 位 Crockford Base32）、规范化、SHA-256；条件更新原子兑换，Mint 失败复位。
- `AgentInstall` 配置（`PublicURL`、`RedeemPerMinute`）与校验；部署级限流闸门（仿 `signupgate`）。
- 路由：`POST /operator/v1/agent-install-codes`、`POST /admin/v1/nodes/install-codes`、`POST /internal/v1/agent-install/redeem`、`GET /internal/v1/agent-install/ca`；挂载条件与 `routes.go` 头注释同步。
- `credential=token` 直接签发；商户码写 `tenant_nodes` 并计入配额，兑换时检查节点未删除。
- 审计 `node.install_code.create` / `node.install_code.redeem`。
- 更新 ControlPlane STATUS、`controlplane.example.yaml`。

## 验收

- [x] spec「Testing Decisions」控制面各项，含并发兑换 `-race`
- [x] 错误与日志不含兑换码、令牌
- [x] AGENTS 质量门禁

## Comments

2026-10-31：控制面实现与默认测试完成（含 `-race` 并发兑换），文档见 ControlPlane STATUS「Agent 安装兑换码」。未覆盖：`redeemgate` 的 Redis 实现无单测（与 `signupgate` 同样没有）；`0013` 迁移未在真实 PG/MySQL 上跑过；Gateway 转发（任务 02）未做。

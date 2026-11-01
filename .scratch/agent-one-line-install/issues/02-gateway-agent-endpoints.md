# 02 Gateway：公开 `/agent/*` 入口

Status: ready-for-agent
Blocked by: 01

依据：[spec.md「Gateway」](../spec.md#gateway)。

## 范围

- 新 flag：`-agent-dist-dir`、`-agent-public-url`（必须 HTTPS）、`-agent-github-repo`；两者齐备才挂载，半配置拒绝启动。
- 与 `mountDocs` 同样挂在 `topMux`，不经 API Key 中间件。
- 脚本占位符替换、manifest、当前版本目录静态下载（拒绝路径穿越与其他版本）。
- 兑换与 CA 转发控制面（复用 `AISW_CONTROL_PLANE_TOKEN`），请求体 ≤ 1 KiB，超时有界，不记录请求/响应体，错误统一映射。
- 更新 Gateway STATUS。

## 验收

- [x] spec「Testing Decisions」Gateway 各项
- [x] 未配置时现有路由行为不变
- [x] AGENTS 质量门禁

## Comments

2026-10-31：实现于 `service/aiServeWeaveGateway/agentdist`，文档见 Gateway STATUS「Agent 公开安装入口」。未覆盖：对真实控制面的端到端验证；制品目录等任务 03。

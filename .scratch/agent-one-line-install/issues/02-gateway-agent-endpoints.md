# 02 Gateway：公开 `/agent/*` 入口

Status: needs-triage
Blocked by: 01

依据：[spec.md「Gateway」](../spec.md#gateway)。

## 范围

- 新 flag：`-agent-dist-dir`、`-agent-public-url`（必须 HTTPS）、`-agent-github-repo`；两者齐备才挂载，半配置拒绝启动。
- 与 `mountDocs` 同样挂在 `topMux`，不经 API Key 中间件。
- 脚本占位符替换、manifest、当前版本目录静态下载（拒绝路径穿越与其他版本）。
- 兑换与 CA 转发控制面（复用 `AISW_CONTROL_PLANE_TOKEN`），请求体 ≤ 1 KiB，超时有界，不记录请求/响应体，错误统一映射。
- 更新 Gateway STATUS。

## 验收

- [ ] spec「Testing Decisions」Gateway 各项
- [ ] 未配置时现有路由行为不变
- [ ] AGENTS 质量门禁

## Comments

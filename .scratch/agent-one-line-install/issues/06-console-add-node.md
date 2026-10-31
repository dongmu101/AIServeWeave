# 06 Console：「添加节点」一行命令对话框

Status: needs-triage
Blocked by: 01

依据：[spec.md「Console」](../spec.md#console)。

## 范围

- 运维「Agent 接入」与商户「自有节点」：系统 / 接入方式 / 来源选择，命令拼装与复制，到期时间；ZIP 入口收进「高级」。
- `upstream-routes.ts` 两条写路由；`lib/console/` 契约镜像；i18n 各语言文案。
- 只发一次、不自动重试、离开不可找回；同源校验与有界请求体沿用现有写路由。

## 验收

- [ ] Console AGENTS 的 lint、build、typecheck、test
- [ ] 命令拼装覆盖三系统 × 三方式 × 两来源

## Comments

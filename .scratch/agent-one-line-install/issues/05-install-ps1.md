# 05 引导脚本 install.ps1 与 agent.ps1 提交号版本

Status: needs-triage
Blocked by: 02, 03

依据：[spec.md「引导脚本」](../spec.md#引导脚本)。

## 范围

- 新增 `deploy/agent/install.ps1`：scriptblock 与 `irm | iex` 两种调用、四种凭据方式、三种来源、`-Upgrade`、拒绝提权、作用域不泄漏。
- `agent.ps1` 支持 7–40 位提交号版本（仅本地文件模式），规则与 `agent.sh` 一致。
- `deployment.Tests.ps1` 增加对应用例；`agent-desktop.yml` 覆盖。

## 验收

- [ ] Windows PowerShell 5.1 与 7 下用例通过（CI windows-2025、windows-11-arm）
- [ ] 输出不含兑换码与令牌

## Comments

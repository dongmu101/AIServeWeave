# 07 文档：部署说明、Agent 指南、STATUS 与验收矩阵

Status: done
Blocked by: 01, 02, 03, 04, 05, 06

## 范围

- [x] `deploy/README.md`「接一个 Agent」改为一行命令为主；补充 Nginx 需把 `open7x.com/agent/` 转到 Gateway HTTP、`AGENT_PUBLIC_URL` 与 `AgentInstall.PublicURL` 配置、已有部署启用步骤。
- [x] `deploy/agent/README.md` 增加「一行命令安装」章节，原手动流程保留为高级用法。
- [x] `deploy/agent/acceptance.md` 增加「一行命令安装」列；根 STATUS 与各服务 STATUS 状态。
- [x] `RELEASING.md` 补充 Release 新增的脚本资产。

## Comments

- 2026-11-01 完成。改动：`deploy/README.md`（一行命令为主、新增「已有部署启用一行命令安装」含 Nginx `location /agent/`）、`deploy/agent/README.md`（新章节，手动流程改称高级用法）、`deploy/agent/acceptance.md`（新列、补充验收项、开发机检查）、`RELEASING.md`（脚本资产与占位符）、根 STATUS、Gateway/ControlPlane/Console/Agent STATUS（含删除已过时的「尚未做」）。
- 只检查路径、锚点与命令，未运行 Go/Console 构建（纯文档）。Nginx 片段与启用步骤未在真实 Nginx 上执行，由任务 08 验证。

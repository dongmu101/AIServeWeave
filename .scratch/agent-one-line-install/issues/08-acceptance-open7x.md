# 08 真实环境验收（open7x.com）

Status: ready-for-human
Blocked by: 07

## 步骤

1. 按 07 更新的部署文档升级平台，确认 `https://open7x.com/agent/v1/manifest` 返回当前版本。
2. Ubuntu 24.04：兑换码命令与交互输入各接入一台（或接入后删除再接入），确认「节点管理」在线、普通请求与 SSE 正常。
3. macOS（Apple Silicon）与 Windows 11：各以兑换码方式接入一次。
4. 重复使用同一兑换码、过期兑换码、删除节点后的兑换码均失败。
5. `--upgrade` 在平台升级后完成节点升级。
6. 在 acceptance.md 记录系统版本、架构、结果与日期。

## Comments

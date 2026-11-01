# 05 引导脚本 install.ps1 与 agent.ps1 提交号版本

Status: ready-for-agent
Blocked by: 02, 03

依据：[spec.md「引导脚本」](../spec.md#引导脚本)。

## 范围

- 新增 `deploy/agent/install.ps1`：scriptblock 与 `irm | iex` 两种调用、四种凭据方式、三种来源、`-Upgrade`、拒绝提权、作用域不泄漏。
- `agent.ps1` 支持 7–40 位提交号版本（仅本地文件模式），规则与 `agent.sh` 一致。
- `deployment.Tests.ps1` 增加对应用例；`agent-desktop.yml` 覆盖。

## 验收

- [ ] Windows PowerShell 5.1 与 7 下用例通过（CI windows-2025、windows-11-arm）——本机无 Windows，未验证；仅在 Linux PowerShell 7.5 容器通过 `install.Tests.ps1`，且 5 个 PS 脚本均通过语法解析
- [x] 输出不含兑换码与令牌（`install.Tests.ps1` 断言）

## Comments

### 完成记录

- 新增 `deploy/agent/install.ps1`；`agent.ps1` 增加 7–40 位小写提交号版本（仅本地文件模式，规则与 `agent.sh` 一致，错误文本同步）。
- 脚本整体是一个匿名块 `& { param(...) ... } @args`：`iex` 与 scriptblock 两种调用都不泄漏变量、函数、严格模式与 `$ErrorActionPreference`，也不受 ExecutionPolicy 限制（`agent.ps1` 同样以脚本块运行）。代价：参数经 `$args` 转发，只能写 `-Upgrade` 这种裸开关，`-Upgrade:$false` 与哈希表展开（开关值会变成位置参数）不可用——测试辅助函数据此逐个写明参数。
- 新增 `deploy/agent/tests/install.Tests.ps1`（本地 HTTPS 桩 + 记录型 `agent.ps1`）与共用桩 `platform_stub.py`（`test_install_sh.py` 同改为复用）；`deployment.Tests.ps1` 增加提交号用例；`agent-desktop.yml` 增加 Windows 上 5.1 与 7 两次运行及 `setup-python`。
- 已验证：`install.Tests.ps1` 在 PowerShell 7.5（Linux 容器，amd64 仿真）全部通过；`install.sh` 的 17 个 Python 用例回归通过；四个 PS 脚本语法解析 0 错误。
- 未验证：Windows PowerShell 5.1、Windows 真机（管理员拒绝、`Read-Host -AsSecureString` 交互、真实 `agent.ps1` 注册计划任务）、`deployment.Tests.ps1` 新增用例（依赖 Windows 注册表）、CI 上 `windows-11-arm` 的 Python/openssl 可用性、macOS-15 CI 上 curl 是否认 `CURL_CA_BUNDLE`。

# Claude 工具隔离拒绝诊断

日期：2026-11-11。源码基线 `fbba2ba`。以下时间统一为台北时间；Agent 日志原始时区为 `+09:00`。

## 已确认的故障

生产 Gateway 在 16:28–16:29 连续收到 `protocol_error` 并返回通用 400。将请求 ID 与 Agent 当前日志关联后，确认固定原因均为 `unsafe_tools`，涉及原生与带 safeguards 的请求。此前提供的 `/tmp/aisw-agent-fixed.log` 已停止更新，属于前一天运行的进程；当前 LaunchAgent 使用的是自己的 `-log-file`。

旧实现对三种情况返回同一哨兵错误，无法从日志确定此次实际分支：

- CLI 初始化注册了不允许的本机工具。
- 上游 `tool_use` 返回请求中未声明的工具名。
- 上游工具调用缺少 ID。

现将这些拒绝包装为带固定分类和事件行号的错误，保留 `errors.Is(err, errUnsafeTools)`。新日志分别为 `unexpected_local_tool`、`undeclared_upstream_tool`、`missing_upstream_tool_id`；不记录工具名、参数、请求内容或 CLI 原始输出。不改变工具隔离、声明校验、公开错误分类及重试行为。

## 验证与部署

本机回归先复现旧实现缺少分类/行号，再验证三类拒绝被保留且诊断完备，不泄露合成私有载荷。以下检查通过：

```sh
go test -race ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/e2e -count=1 -timeout=180s
go vet ./service/aiServeWeaveAgent/claudecode
AISW_CLAUDE_CONTRACT_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestNativeCLI(SafeguardsContract|OriginalClientContract)$' -count=1 -timeout=120s -v
```

真实 CLI 为 `2.1.322`。回环模拟 API 下通过 8 轮原生契约，原版客户端在直连、经 Agent、经 Gateway 三条链路各完成 12 段工具测试。

最初远端执行指定测试返回 `[no tests to run]`，当时源码没有同步新测试，不能作为验证通过。之后通过 SSH 在实际承载 Agent 的 macOS arm64 主机上应用诊断补丁，直接使用该主机的 Go `1.27.4`，绕过开发者的远程 Go 测试转发器。`TestUnsafeToolFailureLocation` 的三个子测试均实际执行并通过，随后在该主机构建新 Agent。

待当前后端调用结束后备份旧二进制，替换并重启 `com.aiserveweave.agent.dev`。已核对安装后二进制 SHA-256 与新构建一致，新 PID 启动并重新连接隧道；未修改 Gateway、凭据、配置或 CLI 权限。客户端与 Agent 均使用 CLI `2.1.322`。

## 真实重试结果与边界

原报错客户端重试后收到正常工具调用，最后于 17:07:33 记录 `stop_reason: end_turn`。更新后的 Agent 观测窗口内累计完成 28 轮请求、没有新失败，其中包含其他会话，不应全部归为本次任务；原会话约 1.47–1.48 MiB 的请求也正常完成。

这次没有再次触发 `unsafe_tools`，所以尚未捕获实际拒绝分支，**不能据此宣称根因已修复**。诊断变更本身不放宽任何拒绝条件。后续若复发，可直接从运行进程日志取得 `cli_event_error` 和 `cli_event_line`，再建立对应触发条件的回归测试。其他部署节点尚未更新，源码变更仍待提交与发布。

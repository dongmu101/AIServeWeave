# Codex CLI 桥接实验

与 Claude CLI 实验并列，通过 Agent 的 `codexcli` 包验证本机 ChatGPT 登录、动态工具往返和隔离进程。当前没有提供 `/v1/responses`，不是可部署的共享 Gateway。

默认只显示帮助，不使用订阅：

```bash
go run ./experiments/codex-cli-bridge
go test ./service/aiServeWeaveAgent/codexcli ./experiments/codex-cli-bridge -count=1
go test -race ./service/aiServeWeaveAgent/codexcli ./experiments/codex-cli-bridge -count=1
```

显式运行真实验证：

```bash
go run ./experiments/codex-cli-bridge -live -codex /absolute/path/to/codex
go run ./experiments/codex-cli-bridge -live -codex /absolute/path/to/codex -sessions 2
```

使用 CLI 的现有 ChatGPT 登录，`-model` 未指定时保留本机模型配置；不会自动更换模型。`-sessions` 只接受 1–2，`-timeout` 默认 90 秒，上限两分钟。live 会使用账户额度，每条成功会话要求两次合成工具调用。

输出的 `stage`、`turn_status`、`failure_code` 用于定位阶段性失败，`instruction_files` 记录本机账户通用指令的加载数量。`codex_probe_passed` 要求两个实际工具请求、随机结果校验和成功终态。任一会话失败即返回非零退出码。

`responses_api_compatibility` 固定为 `not_tested`：工具由实验宿主提供结果，尚未完成原版客户端 → Gateway → Agent 的请求与历史转换。所有当前结果都应按此范围理解，不能把正常结束但未调用工具的回合算作通过。

更详细的本机指令继承与进程边界见 [Agent 包说明](../../service/aiServeWeaveAgent/codexcli/README.md)。

2026-09-30 的单会话及双会话真实验证均通过，实际证据和兼容限制见 [实验记录](RESULTS.md)。

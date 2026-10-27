# Claude Code CLI/MCP 桥接验证器

独立的第一阶段验证程序，验证本机已登录的 Claude Code 能否调用桥接工具并消费调用方返回的结果。支持 macOS 和 Linux，使用 Go 标准库，无新增模块依赖。

原有验证模式覆盖 CLI 进程、受认证的回环 MCP 端点、有界工具关联及事件检查。新增 `-live -serve 127.0.0.1:18080` 模式提供隔离实验 Messages 前门，需要 `AISW_CLAUDE_BRIDGE_KEY` 环境变量（可选不同的 `AISW_CLAUDE_BRIDGE_SECOND_KEY`）。正式接入已迁入 [Agent claudecode](../../service/aiServeWeaveAgent/claudecode/README.md)，实验前门不用于部署。完整接入的验收范围见 [设计](../../docs/superpowers/specs/2026-09-30-claude-code-cli-bridge-design.md)。

## 运行

默认只输出参数帮助，不使用订阅：

```bash
go run ./experiments/claude-code-bridge
go test ./experiments/claude-code-bridge -count=1
go test -race ./experiments/claude-code-bridge -count=1
```

在已经登录 Claude 订阅的机器上显式启动真实验证：

```bash
go run ./experiments/claude-code-bridge -live -claude /absolute/path/to/claude -model sonnet
go run ./experiments/claude-code-bridge -live -claude /absolute/path/to/claude -model sonnet -sessions 2
go run ./experiments/claude-code-bridge -live -claude /absolute/path/to/claude -model sonnet -scenario parallel
```

`-claude` 默认为 PATH 中的 `claude` 可执行文件，不调用 shell 中同名的 alias/function。`-sessions` 只接受 1 或 2；`-scenario` 为 `sequential` 或 `parallel`；总超时默认 90 秒，最多 2 分钟。live 会使用本机订阅额度，每个会话要求两次合成工具调用。

也可显式运行 live 测试：

```bash
AISW_CLAUDE_LIVE_TEST=1 AISW_CLAUDE_PATH=/absolute/path/to/claude go test ./experiments/claude-code-bridge -run TestLiveProbe -v -count=1
```

只接受 CLI 报告的 `claude.ai` 登录。沙箱可能无法访问系统钥匙串，沙箱内的未登录结果不能代表普通终端也未登录；在同一执行权限下先执行 `claude auth status --json` 核对。验证器不会替用户解锁钥匙串、重新登录或复制 token。

## 如何判断结果

每个会话输出一份 JSON 摘要：

- `cli_mcp_probe_passed`：两次 MCP 工具请求、对应模型回合及最终回复随机值均符合预期。
- `synthetic_client_calls` 与 `broker_result_order`：模拟调用方向 broker 投递的次数与顺序；parallel 按 `second`、`first` 投递。HTTP handler 并发写出，因此这个字段不证明 CLI 的实际接收顺序。
- `events`：只记录事件数量和是否消费调用方随机值，不输出模型正文、Prompt、认证数据或随机值本身。
- `messages_api_compatibility`：固定为 `not_tested`。CLI/MCP 通过不等于原版客户端经 Gateway 调用通过。

任一会话失败时命令退出码为非零，即使 CLI 曾输出文本也不会报告成功。无成功 result、非零进程退出、超大/畸形事件、意外内建工具均失败。parallel 要求同一模型回合发出两次工具调用，若模型改为逐次调用则实验超时失败，不把模型未按要求并行当成并行测试通过。

## 实现边界

每个会话有独立的 CLI 进程、临时工作目录、回环 MCP 端口、随机 MCP token 和随机结果。临时目录权限为 0700，MCP 配置为 0600。CLI 使用 `--restricted`、空内建工具列表、严格 MCP 配置及指定工具许可，不开启权限绕过。认证仍由 CLI 自行处理。

MCP 只提供合成 `probe_echo`，不读取文件、执行命令或访问数据库。端点拒绝 Origin、不匹配 Host 和错误 token；模型输入不能决定可执行文件、进程参数或服务地址。

broker 最多八个待决调用、每会话累计最多 64 次调用；身份绑定到模拟 tenant/key，重复和未知结果被拒绝。每条 CLI/MCP JSON 消息最多 1 MiB、认证 stdout 最多 64 KiB、CLI 事件最多 10000 条。stderr 丢弃，不在错误中回显。超时/取消杀死 CLI 进程组并等待退出，关闭本地 HTTP 服务和工具等待者。

这些是验证器边界。它尚未证明真实 Gateway Key 授权、HTTP 请求之间的会话续接、共享 Redis 映射、完整 Anthropic 历史/扩展字段或调用者真实文件工具的位置边界。

## DBX 参考

本实现参考了用户提供的 [DBX](https://github.com/t8y2/dbx)，只借鉴接入模式，没有复制其 Rust 源码或引入 DBX 依赖。固定核对版本为 `9192af28aefb434fb90d27dad78831961ce0f1e8`：

- [Claude Code 适配器](https://github.com/t8y2/dbx/blob/9192af28aefb434fb90d27dad78831961ce0f1e8/crates/dbx-ai-provider/src/ai_claude_code_cli.rs)：隔离 cwd、临时 MCP 文件、stdin 提示词、JSON 流事件及受限 MCP 工具。
- [通用 CLI agent](https://github.com/t8y2/dbx/blob/9192af28aefb434fb90d27dad78831961ce0f1e8/crates/dbx-ai-provider/src/ai_cli_agent.rs)：`build_cli_agent_prompt` 将历史转成文本并省略旧工具调用，适用于 DBX 内置助手，不能直接作为本项目完整 Messages API 兼容的证据。

采用 DBX 的隔离目录与 MCP 配置模式，同时保留本仓库对有界输出和凭据不外发的要求。完整客户端代理仍需实现协议适配及有状态续接。

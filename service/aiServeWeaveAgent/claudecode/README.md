# Claude Code CLI 运行时

Agent 本地 `kind: claude`，标准库实现，无新增第三方依赖。使用同一系统账户的 `claude.ai` 登录，CLI 自行管理认证；不读取、导出或转发 token。仅通过 Gateway 的原生 `/v1/messages` 服务，发布 `anthropic_messages` 能力和 `sonnet` 模型，不提供 Chat/Responses/嵌入接口。上游固定为 `claude-sonnet-4-6`，避免浮动模型别名改变已验证协议行为。

## 启用

确保 Agent 的 PATH 能找到真实 `claude` 可执行文件，以相同账户完成 `claude auth login`。在本地配置中添加并重启 Agent：

```yaml
runtimes:
  - id: claude-local
    kind: claude
```

`base_url` 省略或为空，不配置 API Key、请求头、TLS 或认证文件。设置了 `gateway.allowed_runtimes` 时加入 `claude-local`。Gateway 下发配置不能创建未在本地声明的 Claude ID。配置页面可选择 Claude Code CLI，保存后仍需重启 Agent。

所有 Gateway 副本连接同一 Redis（`-redis-addr`），并启用控制面 API Key 校验。调用方使用控制面签发的 Key；静态 `-api-keys` 没有租户/Key 归属，不能用于此运行时。将公共模型 `claude-sonnet-4-6` 路由到 `sonnet`：

```json
[{"model":"claude-sonnet-4-6","targets":[{"runtime_model":"sonnet"}]}]
```

已验证的原版客户端配置：

```bash
export ANTHROPIC_BASE_URL="https://your-gateway.example"
# ANTHROPIC_API_KEY 设置为控制面签发的调用方 Key。
export MAX_THINKING_TOKENS=0
export CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1
export CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1
export DISABLE_PROMPT_CACHING=1
export CLAUDE_CODE_MAX_OUTPUT_TOKENS=4096
export CLAUDE_CODE_SIMPLE=1
claude --bare --print --model claude-sonnet-4-6 --tools Read,Edit --allowedTools Read,Edit --permission-mode acceptEdits "你的任务"
```

客户端工具权限由调用方管理。上述 `--bare` 用于连接 Gateway 的客户端；Agent 后端采用 `--restricted`，保留原生订阅登录读取能力。客户端若继续使用本机订阅或其他认证配置，应先隔离客户端配置，避免与 Gateway Key 混用。此配置验证了单次编程任务的连续工具流程，交互式多轮聊天和完整客户端功能没有验收。

## 支持范围

支持初始单条 user 文本、system 文本、调用方定义的工具及其 JSON Schema、文本工具结果（包括 `is_error`）、JSON/SSE、多轮工具往返。后续请求必须携带未改动的完整历史和本回合全部 `tool_result`。客户端补入工具 schema 明确声明的默认值视为等价，其他参数修改被拒绝。

支持 `max_tokens`（1–64000）、`temperature`（0–1）、`thinking: {"type":"disabled"}`、`tool_choice: {"type":"auto"}` 和 `output_config.effort`（low/medium/high/xhigh/max）。`metadata` 为调用方注记，不转发给 CLI。不支持 thinking 内容、缓存控制、图片/文件/音频、多轮新增 user 提问、任意预填 assistant 历史、强制工具选择、其他采样参数、输出 schema、上下文压缩或客户端扩展。未知字段严格拒绝，不静默丢弃。

并行工具只允许可通过名称和规范化参数区分的调用；同轮同名同参数会明确失败，因为公开 MCP 回调没有原生 tool ID，不能可靠消歧。工具仅在调用方执行；Agent 临时目录内无用户项目，内建执行工具、hooks、memory、浏览器与其他 MCP 均关闭。

## 生命周期与限额

每个运行时最多两个独立 CLI 会话，所有 Gateway 副本共用该 Agent 限额。等待工具和生成阶段均最多两分钟，会话总寿命十分钟；每会话最多 64 次工具调用、同时最多 8 个待决工具。原生请求最多 1 MiB、CLI 单事件 1 MiB、最多 10000 行、每回合输出最多 8 MiB。流式事件逐条传递，使用同步背压。

Redis 原子保存工具 ID 的完整集合、租户/Key、公共模型及原节点/runtime/实际模型，TTL 两分钟，不保存消息或工具结果。每次续接重新认证，不允许换节点或自动重试；已应用或结果不确定的工具结果不可重放。HTTP 断开取消未完成回合；正常完成的工具回合保留进程等待下一请求。Agent 关闭、会话超时及 CLI 退出会清理进程组、MCP 监听和临时文件。Agent 重启或节点失联后开始新任务，不恢复旧 CLI。

## 验证

默认测试使用离线事件和注入时钟，不访问真实模型或 Redis：

```bash
go test ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/messagesession ./common/tunnelwire
```

真实测试显式使用本机订阅额度；Redis 测试仅使用随机测试 key，不清空数据库：

```bash
AISW_CLAUDE_LIVE_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run TestLiveMessages -v -count=1
AISW_MESSAGES_REDIS_ADDR=127.0.0.1:6379 go test ./service/aiServeWeaveGateway/messagesession -v -count=1
AISW_CLAUDE_LIVE_TEST=1 AISW_MESSAGES_REDIS_ADDR=127.0.0.1:6379 go test ./service/aiServeWeaveGateway/e2e -run TestLiveClaudeClientsThroughTunnel -v -count=1
```

`AISW_CLAUDE_PATH` 可为测试指定本地可执行文件；生产程序仅从可信 Agent PATH 解析 `claude`，外部请求不能设置程序路径、命令、环境变量或目标地址。运行证据与尚未验证的范围见 [RESULTS.md](../../../experiments/claude-code-bridge/RESULTS.md)。

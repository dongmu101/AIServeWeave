# Claude Code CLI 运行时

Agent 本地 `kind: claude`，标准库实现，无新增第三方依赖。使用同一系统账户的 `claude.ai` 登录，CLI 自行管理认证；不读取、导出或转发 token。仅通过 Gateway 的原生 `/v1/messages` 服务，发布 `anthropic_messages` 能力，不提供 Chat/Responses/嵌入接口。

模型发现参考 [dbx 的 Claude CLI 适配器](https://github.com/t8y2/dbx/blob/03db4cad8c154a3126bc784f3b2428ba60357511/crates/dbx-ai-provider/src/ai_claude_code_cli.rs)：向隔离的 `claude --print --input-format stream-json --output-format stream-json` 进程发送 `initialize` 控制请求，从响应的 `models` 读取并去重模型 ID，保留 CLI 原始顺序及 `displayName`（兼容 `display_name`）显示名称。此步骤不提交用户提示词、不执行推理、不消耗模型额度。只发布 CLI 实际返回的目录，不使用静态候选列表兜底；CLI 不支持该查询、目录为空或响应畸形时标记未就绪并按既有策略重试。每次查询受 `probe_timeout`（CLI 默认 15 秒）限制，输出最多 1 MiB、目录最多 256 项。

请求只能选择最近一次成功发现的模型；CLI 返回 `resolvedModel` 时，会话使用该具体模型，否则使用 CLI 返回的 ID。工具续接保持原会话的上游模型。`default`、`sonnet`、`opus` 等别名及具体模型名是否出现取决于本机 CLI，不能据模型名称推断完整 API 兼容性；真实工具流程目前只验收过 `claude-sonnet-4-6`。

## 启用

确保 Agent 的 PATH 能找到真实 `claude` 可执行文件，以相同账户完成 `claude auth login`。在本地配置中添加并重启 Agent：

```yaml
runtimes:
  - id: claude-local
    kind: claude
```

`base_url` 省略或为空，不配置 API Key、请求头、TLS 或认证文件。设置了 `gateway.allowed_runtimes` 时加入 `claude-local`。Gateway 下发配置不能创建未在本地声明的 Claude ID。配置页面可选择 Claude Code CLI，保存后仍需重启 Agent。

所有 Gateway 副本连接同一 Redis（`-redis-addr`），并启用控制面 API Key 校验。调用方使用控制面签发的 Key；静态 `-api-keys` 没有租户/Key 归属，不能用于此运行时。未启用模型别名路由时，`/v1/models` 保留 CLI 顺序列出发现的模型，并以 `name` 和 `display_name` 返回显示名称；`id` 始终保留 CLI 模型 ID，用于发起请求。客户端需要读取名称字段并保留返回顺序才能显示成 DBX 的列表；自行按 ID 排序或忽略名称的客户端需要调整其导入/显示逻辑。启用路由时只列出具有可用目标的公共别名。例如，将公共模型 `claude-sonnet-4-6` 路由到目录中的同名具体模型：

```json
[{"model":"claude-sonnet-4-6","targets":[{"runtime_model":"claude-sonnet-4-6"}]}]
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

真实模型发现测试仅查询本机 CLI，不执行推理：

```bash
AISW_CLAUDE_LIVE_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestLiveModelDiscovery$' -v -count=1
AISW_CLAUDE_LIVE_TEST=1 go test ./service/aiServeWeaveGateway/e2e -run '^TestLiveClaudeModelCatalogueThroughTunnel$' -v -count=1
```

若在 macOS 沙箱中查询出现未登录，先在正常终端以同一账户执行 `claude auth status --json`，确认是否因沙箱无法访问系统钥匙串；不要导出 token 绕过认证。

其余真实测试显式使用本机订阅额度；Redis 测试仅使用随机测试 key，不清空数据库：

```bash
AISW_CLAUDE_LIVE_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run TestLiveMessages -v -count=1
AISW_MESSAGES_REDIS_ADDR=127.0.0.1:6379 go test ./service/aiServeWeaveGateway/messagesession -v -count=1
AISW_CLAUDE_LIVE_TEST=1 AISW_MESSAGES_REDIS_ADDR=127.0.0.1:6379 go test ./service/aiServeWeaveGateway/e2e -run TestLiveClaudeClientsThroughTunnel -v -count=1
```

`AISW_CLAUDE_PATH` 可为测试指定本地可执行文件；生产程序仅从可信 Agent PATH 解析 `claude`，外部请求不能设置程序路径、命令、环境变量或目标地址。运行证据与尚未验证的范围见 [RESULTS.md](../../../experiments/claude-code-bridge/RESULTS.md)。

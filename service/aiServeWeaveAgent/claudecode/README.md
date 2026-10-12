# Claude Code CLI 运行时

Agent 本地 `kind: claude`，标准库实现，无新增第三方依赖。使用同一系统账户的 `claude.ai` 登录，CLI 自行管理认证；不读取、导出或转发 token。发布 `anthropic_messages` 能力，通过 Gateway 的原生 `/v1/messages` 和兼容 `/v1/chat/completions` 服务。Chat 在 Gateway 边界转换，仍使用 Messages 隧道；不提供 Responses/嵌入接口。

模型发现参考 [dbx 的 Claude CLI 适配器](https://github.com/t8y2/dbx/blob/03db4cad8c154a3126bc784f3b2428ba60357511/crates/dbx-ai-provider/src/ai_claude_code_cli.rs)：向隔离的 `claude --print --input-format stream-json --output-format stream-json` 进程发送 `initialize` 控制请求，从响应的 `models[].resolvedModel` 读取真实模型 ID，按真实 ID 去重并保留首次出现的顺序。CLI 的 `value` / `id` 简称及友好名称不作为公开模型名；没有 `resolvedModel` 的条目不发布。此步骤不提交用户提示词、不执行推理、不消耗模型额度。只发布 CLI 实际返回的目录，不使用静态候选列表兜底；CLI 不支持该查询、目录为空或响应畸形时标记未就绪并按既有策略重试。每次查询受 `probe_timeout`（CLI 默认 15 秒）限制，输出最多 1 MiB、目录最多 256 项。

请求只能选择最近一次成功发现的真实模型 ID，传给 CLI 的 `--model` 与请求 ID 完全一致。`default`、`sonnet`、`opus`、`fable`、`haiku` 等简称不进入目录，也不能直接用于请求；同一真实模型经多个简称返回时只发布一次。工具续接保持原会话模型。不能据模型名称推断完整 API 兼容性；原版 Claude Code 客户端的工具流程目前只验收过 `claude-sonnet-4-6`；Chat 兼容层已验证 `claude-sonnet-5-5` 的文本、流式、多轮历史和工具续接。

## OpenAI Chat Completions 兼容

客户端可保留 OpenAI Chat Completions 类型，Base URL 为 Gateway 地址（通常为 `http://127.0.0.1:8080/v1`），使用控制面签发的 Bearer Key，模型 ID 原样使用目录中的 `claude-sonnet-5-5` 等真实 ID。支持 JSON、SSE、`stream_options.include_usage`、文本、前置 system/developer、function 工具及工具结果续接。Gateway 同样要求身份校验和共享 Redis，工具仍由调用方执行。

普通多轮聊天以带角色标记的 JSON 历史文本作为新 CLI 回合上下文，不伪造 CLI 的 assistant 消息，也不重放历史工具。当前工具链保留原进程，并校验身份、完整历史、全部工具结果和原节点绑定。Chat 将 assistant 文本和工具分别存放，Agent 仅对带内部 `metadata.aisw_chat_completions` 标记的会话按这两个字段校验等价性；文本、工具顺序、ID 和参数仍必须一致。原生 Messages 保持块顺序校验。升级本兼容层后需要同时更新 Gateway 和重启 Agent。

支持 `max_tokens` 或 `max_completion_tokens`（1–64000，默认 4096；同时指定必须相同）、`temperature`（0–1）和自动工具选择。兼容 DeepSeek Harness 等 SDK 默认发送的 `store: false`，不持久化保存 Chat 回复；`store: true` 明确返回 400。接受 OpenCode 等客户端的 `tools[].function.strict: false`（或省略/null），保留工具定义与 schema；`strict: true` 要求严格 schema 保证，明确返回 400。不支持图片/音频、强制工具选择、`top_p`、`seed`、`stop`、输出 schema 或未知参数，明确返回 400；已知但不支持的 SDK 字段（如 `reasoning_effort`）会在错误中点名，不回显任意字段名或参数值。Chat 请求及转换后的 Messages 请求均最多 1 MiB，响应上限沿用 8 MiB；流式逐事件输出，不缓冲完整回合再伪装流式。Codex 的 Chat/Responses 路径独立，不受此转换影响。真实 `claude-sonnet-5-5` 已验证 JSON/SSE 文本、多轮历史和跨 Gateway 工具续接；原版 Claude 客户端验收范围仍见下文。

## 启用

确保 Agent 的 PATH 能找到真实 `claude` 可执行文件，以相同账户完成 `claude auth login`。在本地配置中添加并重启 Agent：

```yaml
runtimes:
  - id: claude-local
    kind: claude
```

`base_url` 省略或为空，不配置 API Key、请求头、TLS 或认证文件。设置了 `gateway.allowed_runtimes` 时加入 `claude-local`。Gateway 下发配置不能创建未在本地声明的 Claude ID。配置页面可选择 Claude Code CLI，保存后仍需重启 Agent。

所有 Gateway 副本连接同一 Redis（`-redis-addr`），并启用控制面 API Key 校验。调用方使用控制面签发的 Key；静态 `-api-keys` 没有租户/Key 归属，不能用于此运行时。未启用模型别名路由时，`/v1/models` 保留 CLI 顺序列出发现的模型，其中 `id`、`name` 和 `display_name` 均为同一个真实模型 ID，例如 `claude-haiku-5-5`，不使用 `Haiku 5.5` 等友好名称替代。启用路由时只列出具有可用目标的公共别名。例如，将公共模型 `claude-sonnet-4-6` 路由到目录中的同名具体模型：

```json
[{"model":"claude-sonnet-4-6","targets":[{"runtime_model":"claude-sonnet-4-6"}]}]
```

客户端 Base URL 统一使用 Gateway 的 `/v1` 地址，OpenAI Chat Completions、Anthropic Messages 和 DeepSeek Harness 均相同。Anthropic SDK 额外追加的 `/v1` 由 Gateway 兼容；旧的根地址配置仍可用。以下配置要求部署支持统一基础路径的新版 Gateway：

```bash
export ANTHROPIC_BASE_URL="https://your-gateway.example/v1"
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

部分新模型仍会生成内部 `thinking` / `redacted_thinking` 块。这些块由保留的 CLI 管理，不对外输出；文本与工具块的公开索引重新连续编号，确保后续工具回合可以正常续接。这不提供客户端 thinking 内容或开启 thinking 的请求参数。

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
AISW_CLAUDE_LIVE_TEST=1 AISW_CLAUDE_MODEL=claude-sonnet-5-5 AISW_MESSAGES_REDIS_ADDR=127.0.0.1:6379 go test ./service/aiServeWeaveGateway/e2e -run '^TestLiveClaudeChatThroughTunnel$' -v -count=1
AISW_CLAUDE_LIVE_TEST=1 AISW_CLAUDE_MODEL=claude-sonnet-5-5 AISW_MESSAGES_REDIS_ADDR=127.0.0.1:6379 AISW_OPENCODE_PATH=/path/to/opencode go test ./service/aiServeWeaveGateway/e2e -run '^TestLiveOpenCodeChatThroughTunnel$' -v -count=1
```

OpenCode 实机测试使用隔离配置与临时文件，验证读取、编辑、再次读取及最终回复，不修改用户的客户端设置或项目文件。

`AISW_CLAUDE_PATH` 可为测试指定本地可执行文件；生产程序仅从可信 Agent PATH 解析 `claude`，外部请求不能设置程序路径、命令、环境变量或目标地址。运行证据与尚未验证的范围见 [RESULTS.md](../../../experiments/claude-code-bridge/RESULTS.md)。

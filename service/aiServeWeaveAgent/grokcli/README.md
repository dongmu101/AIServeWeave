# Grok CLI 运行时

Agent 本地 `kind: grok`，标准库实现，无新增第三方依赖。复用本机 Grok CLI（xAI 官方 `grok`）的登录，CLI 自行管理认证；Agent 不读取、导出或转发 token。发布 `chat` / `chat_stream` 能力，通过 Gateway 现有的 `/v1/chat/completions`（及转换到 Chat 的 `/v1/responses`）服务，仅支持纯文本对话。

实现参考 [dbx 的 Grok CLI 适配器](https://github.com/t8y2/dbx/blob/main/crates/dbx-ai-provider/src/ai_grok_cli.rs)（命令行参数、`streaming-json` 事件、ACP 模型发现及隔离目录的做法），但去掉了 dbx 的 MCP 工具面：本适配器不给模型任何工具。

## 启用

以运行 Agent 的同一系统账户执行 `grok login`，确认 `grok` 在 Agent 的 PATH 中，或位于官方安装器使用的 `$HOME/.grok/bin`。在本地配置中添加并重启 Agent：

```yaml
runtimes:
  - id: grok-local
    kind: grok
```

`base_url`、API Key、Headers、TLS 均必须为空。设置了 `gateway.allowed_runtimes` 时加入 `grok-local`。Gateway 下发的配置不能创建未在本地声明的 Grok ID。配置页面可选择 Grok CLI（地址留空），保存后仍需重启 Agent。

## 隔离

每次发现或推理都新建临时目录，结束后删除：

- 进程以该目录为工作目录，`HOME` 与 `GROK_HOME` 指向它，其中只有一份 `config.toml`（`auto_update = false`、`use_leader = false`），不声明任何 MCP 服务。用户自己的 Grok 配置、MCP、项目规则都不会加载。
- 登录文件按 `GROK_AUTH_PATH` → `$GROK_HOME/auth.json` → `$HOME/.grok/auth.json` 的顺序解析，以 `GROK_AUTH_PATH` 传给子进程，只复用、不复制。
- 环境变量按允许列表传递：语言、代理、证书、`XAI_API_KEY`（若运维设置）等；其余一律不传。
- 推理命令：`--no-leader --prompt-file <临时文件> --output-format streaming-json --disable-web-search --no-subagents --no-plan --disallowed-tools <全部内建工具> --verbatim --model <id>`。不传 `--always-approve` 与 `--allow`，没有任何东西可被批准；一旦出现 `tool_call` / `tool_call_update` 事件，立即终止整个进程组并返回协议错误。
- 提示词写入临时文件而不是命令行参数，避免 argv 长度限制，也不出现在进程列表里。

## 模型发现

向 `grok agent --no-leader stdio` 发送一次 ACP `initialize` 请求，从响应的 `modelState.availableModels` 读取模型 ID（兼容 `_meta` / `meta` / 顶层及 camelCase / snake_case 写法）；该路径失败时退回解析 `grok models` 的人类可读列表。两条路径都不提交提示词、不执行推理。

只发布 CLI 实际返回的真实 ID，按 CLI 顺序去重，不发布 `default` 别名，也不使用静态候选列表兜底；目录为空、格式异常或报告未登录时运行时标记未就绪，按既有策略重试。以 `-` 开头或含空白的 ID 不发布，避免被 CLI 当作参数。每次查询受 `probe_timeout`（CLI 默认 15 秒）限制，最多 256 个模型。

请求只能选择最近一次成功发现的模型 ID，原样传给 `--model`。

## 支持范围

- 支持：`system` / `developer` / `user` / `assistant` 文本消息（含纯文本 `content` 片段）、JSON 与 SSE、`tool_choice` 为空 / `auto` / `none`、`response_format: text`。最后一条消息必须是非空的 `user`。
- 多轮历史以带角色标记的 JSON 文本作为一次全新 headless 回合的上下文，不伪造 CLI 会话，Agent 不保存跨请求状态。
- `max_tokens` / `max_completion_tokens` 等正数输出上限作为兼容参数接收，**CLI 不执行该上限**。Gateway 在成功响应中以 `X-Gateway-Ignored-Parameters` 与 `Warning: 299 AIServeWeave "Grok compatibility mode: output token limit is not enforced"` 明确提示，规则与 [Codex 的输出上限兼容](../codexcli/README.md#harness-与输出上限兼容) 相同。
- 不支持（明确拒绝）：工具定义与工具历史、图片/音频/文件输入、`temperature`、`top_p`、`seed`、`stop`、JSON Schema 输出、强制工具选择、未知扩展字段、嵌入、重排、转录。
- `thought` 事件（推理过程）不对外输出；`end` 事件中的 token 用量作为 `usage` 返回。

## 生命周期与限额

每个请求一个独立进程，并发上限为 `max_concurrent`（默认 32）。请求总期限与流空闲期限由注入的 `runtime.Clock` 控制，客户端断开、流关闭、超时或 Agent 关闭都会终止整个进程组并等待回收。单行事件与提示词各最多 1 MiB，每回合文本最多 1 MiB、最多 10000 行。文本增量逐条同步转发，不预读；结束事件只在 CLI 以零状态退出后发送，非零退出即使已输出部分文本也按失败处理。

错误只含固定类别（未登录 → `unauthorized`，回合失败 → `upstream_error`，输出异常或工具事件 → `protocol_error`），不包含 CLI 输出、提示词或路径。

## 验证

命令行参数与 `streaming-json` 事件格式以 dbx 针对 `@xai-official/grok` 1.0.0 的验证为准，本仓库尚未用真实 Grok CLI 实机验收。启用后按以下顺序确认：

1. 以 Agent 的同一账户执行 `grok models`，确认已登录且能列出模型；
2. 启动 Agent，Gateway `/v1/models` 应出现上一步的真实模型 ID（不含 `default`）；
3. 用 `/v1/chat/completions` 分别发送一条非流式与 `stream: true` 的纯文本消息。

升级 CLI 后若行为变化，先核对 `grok --help` 中上述参数是否仍然存在。

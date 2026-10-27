# 编程智能体 CLI 运行时

Agent 本地 `kind: grok` / `opencode` / `cursor` / `codebuddy` / `qoder` / `pi`。标准库实现，没有新增第三方依赖。

这些后端复用本机对应 CLI 自己的登录，认证由 CLI 管理；Agent 不读取、导出或转发 token。它们发布 `chat` / `chat_stream` 能力，通过 Gateway 现有的 `/v1/chat/completions`（以及转换到 Chat 的 `/v1/responses`）对外服务，只支持纯文本对话。

命令行参数、输出事件和模型发现方式参考 [dbx](https://github.com/t8y2/dbx) 的各个 CLI 适配器，但去掉了 dbx 的 MCP 工具面：本包不给模型任何工具。

## 结构

所有 CLI 共用的部分只实现一次，在 `runtime.go`、`process.go` 和 `prompt.go`：

- 进程生命周期；
- 期限，由注入的 `runtime.Clock` 控制；
- 背压与各项上限；
- 提示词渲染；
- 错误类别。

每种 CLI 在自己的文件里登记一份 `cliSpec`，内容包括：

- 候选可执行文件名，以及 `$HOME` 下的安装目录；
- 额外透传的环境变量；
- 隔离目录的准备；
- 未登录措辞；
- 模型发现；
- 回合命令行；
- 输出方言的解析器。

CodeBuddy 与 Qoder 共用 Claude Code 风格的 `stream-json` 解析器，见 `claudestyle.go`。

## 启用

1. 以运行 Agent 的同一系统账户登录对应 CLI（例如 `grok login`、`opencode auth login`、`cursor-agent login`）。
2. 确认可执行文件在 Agent 的 PATH 中，或位于下表的 `$HOME` 安装目录。候选名按表中顺序逐个查找，每个名字先查 PATH、再查安装目录。
3. 在本地配置中添加运行时，然后重启 Agent：

```yaml
runtimes:
  - id: opencode-local
    kind: opencode
```

`base_url`、API Key、Headers 与 TLS 都必须留空。

设置了 `gateway.allowed_runtimes` 时，要把这些 ID 加进去。

Gateway 下发的配置不能创建未在本地声明的 ID，也不能把已声明的 ID 换成别的种类。

配置页面的「本机 CLI」分组里可以直接选择这些 CLI，保存后仍需重启 Agent。

| kind | 可执行文件 | `$HOME` 安装目录 | 额外透传的环境变量 |
| --- | --- | --- | --- |
| `grok` | `grok` | `.grok/bin` | `XAI_API_KEY` |
| `opencode` | `opencode` | `.opencode/bin` | `XDG_*_HOME`、所有 `*_API_KEY` |
| `cursor` | `cursor-agent`，其次 `agent`（通用名字，放在后面） | `.local/bin` | `CURSOR_API_KEY` |
| `codebuddy` | `codebuddy`，其次 `cbc` | — | `CODEBUDDY_*` |
| `qoder` | `qodercli` | — | `QODER_*` |
| `pi` | `pi` | — | `PI_CODING_AGENT_DIR`、所有 `*_API_KEY` |

## 隔离

### 每次运行都在新的临时目录里

每次发现或推理都在新建的临时目录中运行，进程以它为工作目录，结束后目录被删除。

### 环境变量

环境变量按允许列表传递，其余一律不传。允许列表包括：

- `HOME`、语言、代理与证书相关变量；
- 上表中各 CLI 额外透传的变量。

`CLAUDECODE` 这类变量自然不会传入。

OpenCode 与 Pi 透传所有 `*_API_KEY`，这同样包括托管供应商用的变量。托管运行时的 `api_key_env` 应使用 Agent 专用的名字（例如 `AISW_OPENAI_KEY`），不要用 `OPENAI_API_KEY` 这类通用名字，否则这两个 CLI 也会拿到这把 Key。

### 用户级配置仍会加载

临时目录只隔离项目级配置。除 Grok 之外，CLI 仍以运维账户的 `HOME` 运行，所以该账户的用户级设置、hooks、记忆文件等仍可能被加载：

- CodeBuddy、Qoder 显式使用 `--setting-sources user`；
- Cursor、OpenCode、Pi 读取各自的用户配置目录。

建议用一个专用系统账户运行 Agent，该账户下只做 CLI 登录，不放个人配置。

### 各 CLI 的隔离方式

**Grok**

- `HOME` 与 `GROK_HOME` 指向临时目录。目录里只有一份 `config.toml`，不声明 MCP，所以用户自己的配置和项目规则都不会加载。
- 登录文件按以下顺序解析，以 `GROK_AUTH_PATH` 传给子进程，只复用、不复制：
  1. `GROK_AUTH_PATH`
  2. `$GROK_HOME/auth.json`
  3. `$HOME/.grok/auth.json`
- 推理时禁用全部内建工具、网页搜索、子代理和计划模式。

**OpenCode**

- 设置以下环境变量：
  - `OPENCODE_DB=:memory:`
  - `OPENCODE_DISABLE_PROJECT_CONFIG=1`
  - `OPENCODE_CONFIG_CONTENT={"permission":{"*":"deny"}}`
- 以 `--pure` 运行。

**Cursor**

- 临时工作区写入 `.cursor/cli.json`，拒绝 `Shell` / `Read` / `Write` / `WebFetch`。
- 不传 dbx 使用的 `--approve-mcps`。

**CodeBuddy**

参数为 `--permission-mode dontAsk --setting-sources user --strict-mcp-config --tools "" --no-session-persistence`。

**Qoder**

参数为 `--permission-mode dont_ask --setting-sources user --strict-mcp-config --tools "" --no-session-persistence`。

**Pi**

参数为 `--mode rpc --no-session --no-extensions --no-skills --no-prompt-templates --no-context-files --no-builtin-tools --no-approve`。

### 出现工具事件即中止

解析器一旦看到任何工具事件，立即终止整个进程组，回合按协议错误失败。工具事件包括：

- `tool_call` / `tool_use` / `tool_result`；
- 流式 `tool_use` 块；
- Pi 的 `tool_execution_*` 与 `toolcall_*`。

### 提示词的传递

| CLI | 提示词传递方式 |
| --- | --- |
| Grok | 写入临时目录中的 `prompt.txt`（`0600`），以 `--prompt-file` 传入，随目录一起删除 |
| Pi | 作为一条 RPC `prompt` 命令发送 |
| 其余 CLI | 写入 stdin 后关闭管道 |

提示词从不出现在 argv 或进程列表里，只有 Grok 会把它写到磁盘。stdin 在独立协程中写入，大提示词不会与未读的 stdout 互相等待。

## 模型发现

各 CLI 读取模型目录的方式如下，都不提交提示词、不执行推理：

| CLI | 模型来源 |
| --- | --- |
| Grok | ACP `initialize` 的 `modelState.availableModels`；失败时退回 `grok models` |
| OpenCode | `opencode models --verbose --pure` 中不缩进的 `provider/model` 行 |
| Cursor | `--list-models` 中 `<id> - <label>` 形式的行（去掉 ` (current)`） |
| CodeBuddy | stream-json `control_request` `initialize` 的 `control_response.models`；`account: null` 不视为未登录 |
| Qoder | `--list-models`，JSON（`models` / `data` / `items`）或表格的第一列 |
| Pi | RPC `get_available_models`，发布为 `provider/id` |

发布规则：

- 只发布 CLI 实际返回的 ID，按 CLI 顺序去重。
- 不发布 `default`；CodeBuddy 的 `default-model` 也不发布。
- 不使用静态候选列表兜底。
- 以 `-` 开头、或含空白与控制字符的 ID 不发布，避免被 CLI 当作参数。
- Cursor 的 `auto` 与 Qoder 的档位名（`auto`、`performance` 等）是 CLI 可接受的 `--model` 值，照常发布。

以下情况都会让运行时标记为未就绪，并按既有策略重试：

- 目录为空；
- 输出格式异常；
- CLI 报告未登录。

每次查询受 `probe_timeout`（CLI 默认 15 秒）限制，列表输出最多 1 MiB。过滤前最多保留 4096 个原始条目；过滤去重后最多发布 256 个模型，更长的目录被截断而不是拒绝。

请求只能选择最近一次成功发现的模型 ID，并原样传给 `--model`。Pi 例外：在第一个 `/` 处拆成 `--provider` 与 `--model`。

## 回合结束的判定

| CLI | 成功条件 |
| --- | --- |
| Grok | 进程以零状态退出 |
| OpenCode | 进程以零状态退出，且输出了非空文本（没有结束事件） |
| Cursor、CodeBuddy、Qoder | 出现 `subtype: success`、`is_error` 不为真的 `result` 事件；之后立即结束进程，不等它自行退出 |
| Pi | 出现 `agent_settled`（`agent_end` 不算）。任何一次尝试失败（`error` 事件或 `stopReason: error`）都立即中止回合，不等待 Pi 的自动重试，因为重试会重复已经转发的文本 |

各 CLI 的用量来源：

- Grok：`end` 事件；
- OpenCode：`step_finish`，按步骤累加；
- Cursor / CodeBuddy / Qoder：`result.usage`；
- Pi：assistant 的 `message_end`，按消息累加。

输入用量包含缓存读取与缓存写入的 token，输出用量包含推理 token（CLI 给出时）。只统计未缓存部分会少算用量，租户配额就能被绕过。

文本按以下规则提取：

- Cursor 只把带 `timestamp_ms` 的 assistant 事件当作增量，整条消息不重复输出。
- Qoder 先流式发送的 `text_delta` 与之后的完整 assistant 文本按前缀去重。
- Cursor / CodeBuddy / Qoder 若在 `result` 之前没有任何文本，就使用 `result` 中的文本。

非零退出即使已经输出了部分文本，也按失败处理。进程以零状态退出、却没有给出成功条件时，如果输出中出现了未登录措辞，按 `unauthorized` 处理，否则按协议错误处理。

## 支持范围

支持：

- `system` / `developer` / `user` / `assistant` 文本消息（包括纯文本 `content` 片段）；
- JSON 与 SSE；
- `tool_choice` 为空、`auto` 或 `none`；
- `response_format: text`。

最后一条消息必须是非空的 `user`。

多轮历史以带角色标记的 JSON 文本，作为一次全新 headless 回合的上下文。Agent 不伪造 CLI 会话，也不保存跨请求状态。

`max_tokens` 等正数输出上限作为兼容参数接收，但 CLI 不执行该上限。Gateway 在成功响应中通过 `X-Gateway-Ignored-Parameters` 和 `Warning: 299` 首部明确提示，规则与 [Codex 的输出上限兼容](../codexcli/README.md#harness-与输出上限兼容) 相同。

以下请求会被明确拒绝：

- 工具定义与工具历史；
- 图片、音频、文件输入；
- `temperature`、`top_p`、`seed`、`stop`；
- JSON Schema 输出；
- 强制工具选择；
- 未知扩展字段；
- 嵌入、重排、转录。

## 生命周期与限额

每个请求一个独立进程，并发上限为 `max_concurrent`（默认 32）。

请求总期限与流空闲期限都由注入的 `runtime.Clock` 控制。以下任一情况都会终止整个进程组，并等待回收：

- 客户端断开；
- 流被关闭；
- 超时；
- Agent 关闭。

单行事件与提示词各最多 1 MiB；每个回合的文本最多 1 MiB，进程的全部 stdout 最多 64 MiB。

文本增量逐条同步转发，不预读。消费者读得慢时，转发会阻塞在背压上，但仍响应客户端断开、超时与 Agent 关闭。

逐条转发只对流式输出增量的方言成立。CodeBuddy、Qoder 与 OpenCode 可能只在消息结束时输出整段文本，这期间流上没有任何事件；回复较长时，可能需要调大 `stream_idle_timeout`。

错误只含固定类别，不包含 CLI 输出、提示词或路径：

| 情况 | 错误类别 |
| --- | --- |
| 未登录（stderr、非 JSON 输出或错误事件中出现该 CLI 的未登录措辞） | `unauthorized` |
| 找不到可执行文件 | `connection_error` |
| 回合失败 | `upstream_error` |
| 输出异常或工具事件 | `protocol_error` |

## 平台

只支持 darwin 与 linux：需要进程组来终止 CLI 派生的子进程。在其他系统上，每次启动 CLI 进程都会直接失败，运行时不会就绪。

## 验证

命令行参数与事件格式以 dbx 的适配器为准，本仓库尚未用任何真实 CLI 实机验收。

其中 CodeBuddy 与 Qoder 的 `--tools ""` 和 CodeBuddy 的 `--strict-mcp-config` 是本包在 dbx 参数之外加的收紧项，尚未验证这些 CLI 版本都接受。如果升级后回合一律失败，先用 `--help` 核对这两个参数。

启用后按以下顺序确认：

1. 以 Agent 的同一账户运行该 CLI 的模型列表命令（见「模型发现」），确认已登录且能列出模型。
2. 启动 Agent，Gateway `/v1/models` 应出现上一步的真实模型 ID。
3. 用 `/v1/chat/completions` 分别发送一条非流式和一条 `stream: true` 的纯文本消息。

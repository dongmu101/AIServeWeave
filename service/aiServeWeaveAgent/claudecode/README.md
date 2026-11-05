# Claude Code CLI 运行时

账户订阅用量与余量可在 Agent 本地配置页面查看；查询使用 CLI 实验性的 `get_usage` 控制请求，不执行推理，未提供额度时明确显示暂不可用，详见 [Agent 账户额度说明](../README.md#本地-cli-账户额度)。

Agent 本地 `kind: claude`，标准库实现，无新增第三方依赖。使用同一系统账户的 `claude.ai` 登录，CLI 自行管理认证；不读取、导出或转发 token。发布 `anthropic_messages` 能力，通过 Gateway 的原生 `/v1/messages` 以及兼容的 `/v1/chat/completions`、`/v1/responses` 服务。Chat 与 Responses 在 Gateway 边界转换，仍使用 Messages 隧道；不提供嵌入接口。

模型发现参考 [dbx 的 Claude CLI 适配器](https://github.com/t8y2/dbx/blob/03db4cad8c154a3126bc784f3b2428ba60357511/crates/dbx-ai-provider/src/ai_claude_code_cli.rs)：向隔离的 `claude --print --input-format stream-json --output-format stream-json` 进程发送 `initialize` 控制请求，从响应的 `models[].resolvedModel` 读取真实模型 ID，按真实 ID 去重并保留首次出现的顺序。CLI 的 `value` / `id` 简称及友好名称不作为公开模型名；没有 `resolvedModel` 的条目不发布。此步骤不提交用户提示词、不执行推理、不消耗模型额度。只发布 CLI 实际返回的目录，不使用静态候选列表兜底；CLI 不支持该查询、目录为空或响应畸形时标记未就绪并按既有策略重试。每次查询受 `probe_timeout`（CLI 默认 15 秒）限制，输出最多 1 MiB、目录最多 256 项。

请求只能选择最近一次成功发现的真实模型 ID，传给 CLI 的 `--model` 与请求 ID 完全一致。`default`、`sonnet`、`opus`、`fable`、`haiku` 等简称不进入目录，也不能直接用于请求；同一真实模型经多个简称返回时只发布一次。工具续接保持原会话模型。不能据模型名称推断完整 API 兼容性；未带 `safeguards` 的原版 Claude Code 客户端工具流程目前只验收过 `claude-sonnet-4-6`；Chat 兼容层已验证 `claude-sonnet-5-5` 的文本、流式、多轮历史和工具续接。

## OpenAI Chat Completions 兼容

客户端可保留 OpenAI Chat Completions 类型，Base URL 为 Gateway 地址（通常为 `http://127.0.0.1:8080/v1`），使用控制面签发的 Bearer Key，模型 ID 原样使用目录中的 `claude-sonnet-5-5` 等真实 ID。支持 JSON、SSE、`stream_options.include_usage`、文本、前置 system/developer、function 工具及工具结果续接。Gateway 同样要求身份校验和共享 Redis，工具仍由调用方执行。

普通多轮聊天以带角色标记的 JSON 历史文本作为新 CLI 回合上下文，不伪造 CLI 的 assistant 消息，也不重放历史工具。当前工具链保留原进程，并校验身份、完整历史、全部工具结果和原节点绑定。Chat 将 assistant 文本和工具分别存放，Agent 仅对带内部 `metadata.aisw_chat_completions` 标记的会话按这两个字段校验等价性；文本、工具顺序、ID 和参数仍必须一致。原生 Messages 保持块顺序校验。升级本兼容层后需要同时更新 Gateway 和重启 Agent。

支持 `max_tokens` 或 `max_completion_tokens`（正整数，默认 4096；同时指定必须相同）、`temperature`（0–1）、`reasoning_effort`（见下文「思考内容」）和自动工具选择。兼容 DeepSeek Harness 等 SDK 默认发送的 `store: false`，不持久化保存 Chat 回复；`store: true` 明确返回 400。接受 OpenCode 等客户端的 `tools[].function.strict: false`（或省略/null），保留工具定义与 schema；`strict: true` 要求严格 schema 保证，明确返回 400。不支持图片/音频、强制工具选择、`top_p`、`seed`、`stop`、输出 schema 或未知参数，明确返回 400；已知但不支持的 SDK 字段（如 `parallel_tool_calls`）会在错误中点名，不回显任意字段名或参数值。Chat 请求及转换后的 Messages 请求均最多 32 MiB，响应上限沿用原生 Messages 的 64 MiB；流式逐事件输出，不缓冲完整回合再伪装流式。Codex 的 Chat/Responses 路径独立，不受此转换影响。真实 `claude-sonnet-5-5` 已验证 JSON/SSE 文本、多轮历史和跨 Gateway 工具续接；原版 Claude 客户端验收范围仍见下文。

## OpenAI Responses 兼容

Gateway 先把 Responses 请求还原为 Chat 形式，再走上一节的转换。Agent 收到的仍是带 `metadata.aisw_chat_completions` 标记的 Messages 请求，因此无需为此升级 Agent。同一回合的 assistant 文本和工具调用在 Responses 中是独立的项，Gateway 会合并回一条后再做等价校验。每轮须在 `input` 中携带完整对话，并保持 `store: false`（或省略）；续接时原样回放上一轮输出的调用项，以及对应的 `function_call_output`。参数限制与 Chat 相同，只是字段名换成 Responses 的 `max_output_tokens` 和 `text.format`；另外不支持 `previous_response_id`。与 Chat 不同，未知字段不会被拒绝（与普通 Responses 一致，也不生效）。完整范围见 [Gateway STATUS](../../aiServeWeaveGateway/STATUS.md#claude-code-原生-messages)。目前只做过离线端到端验证，尚未用真实 CLI 联调。

## 启用

确保 Agent 的 PATH 能找到真实 `claude` 可执行文件，以相同账户完成 `claude auth login`。在本地配置中添加并重启 Agent：

```yaml
runtimes:
  - id: claude-local
    kind: claude
```

`base_url` 省略或为空，不配置 API Key、请求头、TLS 或认证文件。设置了 `gateway.allowed_runtimes` 时加入 `claude-local`。Gateway 下发配置不能创建未在本地声明的 Claude ID。配置页面可选择 Claude Code CLI 并填写可选的账户目录（`account_dir`，作为 `CLAUDE_CONFIG_DIR`，见 [Agent 多账户说明](../README.md#同一-cli-的多个账户)）；保存后 Agent 即重新加载运行时，无需重启（见 [运行时热重载](../README.md#运行时热重载)）。

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

客户端工具权限由调用方管理。上述 `--bare` 用于连接 Gateway 的客户端；Agent 后端采用 `--restricted`，保留原生订阅登录读取能力。客户端若继续使用本机订阅或其他认证配置，应先隔离客户端配置，避免与 Gateway Key 混用。此配置验证了单次编程任务的连续工具流程。原生 Messages 的多轮追问目前只有离线测试（假 CLI 进程经真实 mTLS 隧道）覆盖，原版客户端的交互式多轮聊天和完整客户端功能尚未实机验收。

## Claude Code auto 服务端安全检查

带非空 `safeguards` 的原生 `/v1/messages` 请求使用独立路径，支持一个 `dangerous_tool_use` 与对象形式的 `classifier_context`。依据 [Claude Code classifier billing 协议](https://code.claude.com/docs/en/auto-mode-classifier-billing) 和 [Gateway 兼容指南](https://code.claude.com/docs/en/llm-gateway-protocol)，Gateway 经隧道传递 `anthropic-version`、`anthropic-beta`，Agent 由已登录的 Claude Code CLI 向真实上游提交请求，转发上游的 `safeguard_results`。不生成或猜测安全判定；`unsupported`、`unavailable`、缺少判定及未来扩展状态交由客户端按官方规则处理。是否获得新计费资格取决于上游账户、模型及真实返回，升级不代表所有账户都已获得免费 classifier 资格。

此路径保留原始 `system`、结构化 `messages`、工具名、工具 ID、思考历史、缓存及安全策略上下文，不经过旧路径的历史折叠、工具改名或 adaptive 思考转换。原生推理顶层字段采用允许列表，HTTP 地址、任意请求头、凭据、CLI 命令及设置不属于推理 JSON，明确拒绝。原始工具只作为上游定义：Agent CLI 的本地工具及 MCP 注册表为空，工具由调用方执行。完整请求通过权限为 `0600` 的临时 CLI 设置文件加载，进程结束后清理；CLI 继续自行管理本机订阅认证。

原生路径也不运行兼容路径的附件转换、历史渲染、`--effort` / `--thinking-display` 参数映射，或关闭 adaptive、beta 与缓存的环境设置；推理配置直接取自原生请求体。图片、PDF 和文本来源等原生内容由上游校验。Office MIME 与 `video` 是本平台扩展，在此路径不会自动转换；调用方须在生成安全上下文之前准备上游支持的原生内容，不能在策略生成后改写被检查的历史。`model` 按路由选择真实模型，`stream` 在内部设为 `true`，除此之外调用方推理字段保持原始结构。

带非空 `safeguards` 时，Gateway 的 `(high)`、数字思考预算及 `[1m]` / `[2m]` 后缀只用于解析路由模型名，不生成、覆盖或删除 `thinking`；思考设置须由调用方放在请求体中。`null` 或空数组继续采用旧路径的后缀语义。

支持上游阈值压缩的 `compaction_delta`、按需压缩的 `compaction: {"type":"summarize"}` 与 `stop_reason: "compaction"`。JSON 聚合保留摘要、原始签名及 `encrypted_content`，SSE 原样传递；未生成摘要的 `content: null` 保持不变。压缩停止后回收本轮 CLI，后续历史和压缩时机仍由调用方管理。Gateway 对存在非空 `usage.iterations` 的原生响应按全部采样迭代汇总输入/输出 token，用于指标、配额及账本，不改写公开响应的原始 `usage`。

能力拒绝从 CLI 标记的合成 API 错误中提取，只公开固定的 `capability_rejected:` 恢复分类；`context_management` 使用客户端可识别的固定 schema 拒绝文案。覆盖思考类型、签名、对话中途 system、输出格式、effort、上下文长度，以及请求实际携带的被拒 beta；beta 仅允许有界协议名称，不能回显任意上游内容。保留已识别的 400/413/422 状态，未知错误仍使用通用错误；普通模型文本与 CLI 内部恢复成功的中间错误不触发客户端回退。CLI 若已折叠上游原始 `error.type`，Agent 无法还原该类型；这不是任意上游错误的透明透传。

每次请求带完整原始历史并开启独立进程，工具回合结束即停止该进程。后续请求不认领旧 MCP 会话、不依赖 Redis 续接记录，可以跨副本重试；每次仍校验调用方身份和模型权限、执行既有并发及大小限制。原始工具 ID 与判定中的 ID 一致，同名同参数的并行调用也可区分。JSON 聚合保留内容块扩展、判定及上下文管理字段；SSE 保留被 `max_tokens` 截断的不完整工具参数，非流式请求遇不完整参数返回明确的 502。

整个请求最多 32 MiB、响应最多 64 MiB；安全上下文不另设小于请求大小的限制。协议版本最多 128 字节，beta 列表最多 16 KiB，均只接受可打印 ASCII，拒绝换行注入。未带 `safeguards` 的请求继续使用下文的 MCP 桥接、历史校验和一次性续接规则；需要客户端回退判定时仍支持停止序列模拟。

升级时先更新并重启承载 Claude 运行时的 Agent，再重建并重启 Gateway，两端都需要此版本。客户端新建会话，移除旧的 `CLAUDE_CODE_AUTO_MODE_SERVER=0`（可显式设为 `1`），并移除 `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`，以允许客户端发送配套 beta。客户端工具权限仍由调用方管理。

默认测试覆盖真实 mTLS 隧道、两个 Gateway 副本、JSON/SSE、原始历史与工具身份、上游状态的模拟转发、工具截断及旧路径续接保护。Claude Code `2.1.318` 离线实测确认原始 `Bash` 定义不会在 Agent 注册或执行；原版客户端经新 Agent 与真实 `claude-opus-5-5` 上游以两轮原生请求完成一次工具往返，收到两次 `available` 和一次 `evaluated/not_flagged`；[验收记录](../../../docs/acceptance/claude-auto-safeguards-2026-11-04/README.md)区分本机真实调用与双 Gateway 模拟 CLI 测试。生产 open7x.com 的受控长工具链已验证；Agent 主机直连对照、实际编程任务、其他 Claude Code 版本及账户的资格尚未验收。2026-11-05 新增的原生请求契约、原版客户端长工具链及真实订阅对照入口见[原生保留验收记录](../../../docs/acceptance/claude-native-preservation-2026-11-05/README.md)。

## 未带 safeguards 的支持范围

支持以 user 文本、图片、文档或本平台视频附件结尾的单轮或多轮对话、system 文本、调用方定义的工具及其 JSON Schema、文本和附件工具结果（包括 `is_error`）、JSON/SSE、多轮工具往返。工具续接的后续请求必须携带未改动的完整历史和本回合全部 `tool_result`；原始附件、标题及字节也参与历史校验。客户端补入工具 schema 明确声明的默认值视为等价，其他参数修改被拒绝。

多轮追问开启一个新 CLI 会话：Agent 把此前的全部回合（包括已完成的工具调用与结果）渲染成带角色标记的 JSON 文本记录，作为 CLI 的首轮输入。这与 Chat 兼容层的做法一致，不伪造 CLI 的 assistant 消息，也不重放历史工具。历史图片以编号占位标记位置，并作为独立图片块附在文本记录之后，不把 base64 编码当作提示词文本。文本历史以引用形式提供，思考块不进入记录，thinking 也不跨会话保留。

支持 `max_tokens`（必填正整数）、`temperature`（0–1）、`thinking`（见下文「思考内容」）、`tool_choice: {"type":"auto"}`、`output_config.effort`（low/medium/high/xhigh/max）和 `stop_sequences`（见下文「停止序列」）。`metadata` 为调用方注记，不转发给 CLI。不支持缓存控制、独立音频附件、以 assistant 消息结尾的预填历史、强制工具选择、其他采样参数、输出 schema、上下文压缩或未定义的客户端扩展。未知字段严格拒绝，不静默丢弃。

### Messages 文件与视频输入

文件和视频只在原生 `/v1/messages` 路径接入，覆盖首轮、历史、工具结果和孤立回合恢复。支持 UTF-8 文本/代码、PDF、DOC/DOCX、XLS/XLSX、PPT/PPTX、MP4/MOV/WebM。PDF 以 `document` 输入，Office 通过 LibreOffice 转 PDF，视频通过 FFmpeg 采样画面并由本地 whisper.cpp 转录音轨。工具返回的文档使用 Poppler 转为页图和文字。完整请求示例、本地 YAML、限额与未验收范围见 [附件说明](../attachments/README.md)。这不代表 OpenAI Chat/Responses 兼容层或原版客户端 UI 已开放这些格式。

### Messages 图片输入

`/v1/messages` 接受 user 消息及 `tool_result.content` 中的 `image` 块，来源必须为 `{"type":"base64","media_type":"image/png","data":"..."}`，媒体类型支持 JPEG、PNG、GIF、WebP。编码必须有效且非空；实际图片格式、尺寸和模型可接受性由 CLI/上游校验。图片沿用整个原生请求 32 MiB 上限，不额外落盘，也不进入日志。URL 来源明确返回 400，Agent 不抓取调用方提供的地址。

有图的新会话通过 `--input-format stream-json` 发送包含原生 image 块的 user 信封；单条提问保留文本和图片的原始顺序，完整历史及孤立工具回合恢复保留图片编号与对应图像。存活会话的图片工具结果转换为 MCP image 内容（`data`、`mimeType`），文本、图片顺序和 `is_error` 保留。纯图片工具结果也支持。纯文本请求保持原有 CLI 输入方式。OpenAI Chat/Responses 兼容层的图片限制仍按前两节执行。

输入形状依据 [Claude 官方结构化输入文档](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode)。也核对了 [CLIProxyAPI 的图片及工具结果转换](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/internal/translator/claude/openai/chat-completions/claude_openai_request.go#L496)：参考其保留结构化图片内容的方式；该项目的 [Claude 执行器](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/internal/runtime/executor/claude_executor_execute.go#L19) 直接访问 Anthropic HTTP API，本运行时继续由本机 CLI 管理订阅登录与推理。

图片支持需要更新并重启承载该 Claude 运行时的 Agent；Gateway 的现有原生 Messages 隧道无需修改。默认测试覆盖跨两个 Gateway 的 mTLS 首轮、JSON/SSE 工具续接、历史图片、工具返回图片与历史完整性。真实 CLI 看图测试为 `AISW_CLAUDE_LIVE_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestLiveMessagesImages$' -count=1 -timeout=180s -v`，可用 `AISW_CLAUDE_MODEL` 选择模型；此前图片专项验证时缺少可用订阅登录，真实图片理解仍未验收。

Gateway 与 Agent 不设置固定的输出 token 上限，客户端指定的数值原样传给 CLI 的 `CLAUDE_CODE_MAX_OUTPUT_TOKENS`；实际可用上限由 CLI、模型和账户能力决定。请求大小、响应大小与执行超时等资源边界仍然生效。此行为需要同时更新 Gateway 并重启 Agent。

## 思考内容

本节描述未带 `safeguards` 的兼容路径；带安全上下文的请求保留原始思考配置和历史，由上游校验及执行。

请求要求思考时，Agent 以 `MAX_THINKING_TOKENS=<预算>` 启动 CLI，并加 `--thinking-display summarized`，让默认省略思考文本的新模型也给出可读文本；否则保持 `MAX_THINKING_TOKENS=0`。`--thinking-display` 需要较新的 Claude Code（本机 `2.1.311` 已有该参数），旧版本只会在请求要求思考时因参数不识别而失败。

- **原生 Messages**：Agent 只接受 `thinking: {"type":"disabled"}` 或 `{"type":"enabled","budget_tokens":N}`（1024 ≤ N < `max_tokens`，且不带 `temperature`）。Gateway 先把客户端的 `thinking` 当作提示改写成这种形式：`adaptive` 按 `output_config.effort` 换算固定预算（`low` 2048、`medium` 8192、`high` 16384、`xhigh`/`max` 32768，未给或无法识别时取 8192）。这是降级而非原生 adaptive：CLI 以 `CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1` 和固定的 `MAX_THINKING_TOKENS` 运行，模型不再按请求自行决定思考深度；直接发给 Agent 的 `adaptive` 仍返回 400。Claude Code 附带的 `context_management`、`cache_control`、`defer_loading` 工具与对话中途的 `role:"system"` 轮次同样由 Gateway 先整形再转发（见 [Gateway STATUS「Claude Code 原生 Messages」](../../aiServeWeaveGateway/STATUS.md#claude-code-原生-messages)），Agent 本身仍严格拒绝它们，放不进 `max_tokens` 的预算收敛到 `max_tokens - 1`，带 `temperature`、关闭思考或 `max_tokens` ≤ 1024 时去掉该字段；只有格式错误的对象返回 400。开启后，CLI 的 `thinking`（含签名）与 `redacted_thinking` 块按原索引原样转发。
- **Chat**：`reasoning_effort` 换算为思考预算（`none` 为关闭），同样按上面的规则收敛或丢弃；`thinking_delta` 渲染为 `reasoning_content`，签名与 redacted 块没有 Chat 形式，不输出。
- **Responses**：`reasoning` 对象按同样规则转换；思考渲染为 `reasoning` 输出项。

思考历史始终由保留的 CLI 自己持有。客户端回传的 `thinking` / `redacted_thinking` 块（只允许出现在 assistant 回合）以及 Chat 的 `reasoning_content` 被接受后即丢弃：不进入新会话的文本记录，也不参与续接的历史比对，所以回传与否都能续接。未要求思考时，部分新模型仍会生成内部思考块，这些块照旧不对外输出，文本与工具块的公开索引重新连续编号。

Anthropic 的 usage 不单独报告思考 token，思考计入 `output_tokens`。以上行为只有离线测试覆盖，尚未用真实 CLI 验收开启思考的回合。

## 停止序列

本节描述未带 `safeguards` 的兼容路径；带安全上下文的请求由上游处理原始停止序列。

CLI 没有停止序列选项，`stop_sequences` 由桥接在 CLI 流出的文本上模拟（`messages_stop.go`），最多 16 个、每个 1–256 字节。可能是某个序列开头的文本尾部先暂扣，因此跨增量拆开的序列也能识别，序列本身及其之后的内容从不放出。命中时放出命中位置之前的文本并关闭该文本块，回合以 `stop_reason: "stop_sequence"` 和 `stop_sequence` 结束，与 Messages API 一致；随后停止 CLI，会话不保留。只匹配文本块，思考与工具参数不参与。

- `usage.output_tokens` 取 CLI 在该消息结束时报告的值，包含 CLI 越过停止序列后多生成的部分，即实际消耗。
- 命中之后 CLI 若开始工具调用，立即结束回合且不登记该调用（API 本不会产生它），此时没有 CLI 的输出用量可报告。
- Claude Code auto 模式的安全判断请求依赖此功能：它带 `stop_sequences: ["</severity>"]`，不支持时判断失败，Bash 等需要判断的操作会被拦下并提示 "temporarily unavailable"。

Chat Completions 的 `stop` 仍明确返回 400，未接入此模拟。此行为只有离线测试覆盖（含伪造 CLI 的端到端用例），需同时更新 Agent；尚未用真实 CLI 验收。

并行工具只允许可通过名称和规范化参数区分的调用；同轮同名同参数会明确失败，因为公开 MCP 回调没有原生 tool ID，不能可靠消歧。工具仅在调用方执行；Agent 临时目录内无用户项目，内建执行工具、hooks、memory、浏览器与其他 MCP 均关闭。

## 终止原因与故障排查

以下常驻 CLI 和 409 续接说明适用于未带 `safeguards` 的 MCP 路径；带安全上下文的原生请求每轮独立，不使用该续接绑定。

CLI 返回的 `max_tokens`、`stop_sequence`、`refusal` 和 `model_context_window_exceeded` 作为终止响应传给客户端，保留原始 `stop_reason` 与已生成内容；收到完整 `message_stop` 后停止该 CLI，避免 CLI 的自动恢复续写越过调用方的输出边界。`tool_use` 仍保留原进程等待工具结果。自然结束 `end_turn` 继续校验 CLI 最终成功结果和退出状态；成功结果之后的 `prompt_suggestion` 与非初始化 `system` 通知只在本机消费，不进入公开回复，其他尾部事件仍拒绝。完整终止之前的 CLI 失败、缺失输出或非法事件仍明确失败。此修复需要更新并重启 Agent，包含模拟 CLI 子进程的离线回归测试覆盖；尚未验收未带 `safeguards` 的 Opus 5.5 真实工具流程。

失败时 Agent 记录 `Claude Messages round failed`，包含 `runtime_id` 和固定的 `reason` 分类（如 `cli_protocol`、`cli_result`、`cli_exit`、`missing_result`、`unsafe_tools`）；CLI 事件解析器拒绝时额外记录 `cli_event_line` 与固定的 `cli_event_error`（如 `unsupported_stop_reason`、`ambiguous_tool_call`）。已观察到的失败即使遇到调用方断开，也会记录分类。这些字段不包含消息内容、工具参数、CLI 原始输出或任意错误文本。公开错误仍隐去底层细节。

进入生成回合之前拒绝请求时，Agent 另记录 `Claude Messages request rejected`，带 `runtime_id`、错误码和固定的 `reason`。续接历史、配置、归属、忙碌状态、结果数量、额外内容、重复结果及已消费结果分别分类；配置不匹配另带 `config_changes`，只列出 `system`、`tools`、`max_tokens`、`api_version` 等变更字段名，不含值、哈希或身份凭据。`invalid_config` 是运行时错误分类，并不特指 YAML 配置错误。入口拒绝的公开错误也携带本地运行时 ID，便于关联 Gateway 日志。此诊断需更新并重启 Agent，Gateway 无需修改。

Agent 的生产 Messages 入口为每个已接纳回合记录 INFO 级 `Claude Messages round started` 和 `Claude Messages round finished`，共同携带本地随机 `round_id`、`runtime_id` 与 `message_path`。`native_safeguards` 表示本轮确实进入原生路径，`mcp_bridge` 表示旧兼容路径。结束日志包含 `outcome`（`completed` / `failed` / `canceled`）与 `safeguard_status`：`available`、`unsupported`、`unavailable`、`missing`（未收到对应结果）、`unknown`（未来状态）、`invalid`（结果形状异常）、`mixed`（同轮出现不同状态）；兼容路径为 `not_requested`。只观察已投递结果，不改写判定或将 `available` 等同于工具已获准；不记录提示词、历史、工具参数、工具 ID、判定原因或凭据。客户端设为 `1` 后应结合这两个字段检查实际路径及上游支持状态。新增日志需更新并重启 Agent，Gateway 无需修改。

手动用 `go run` 启动时，日志默认写到终端，不会自动生成部署脚本使用的 `agent.log`。可用 `go run . -config /绝对路径/agent.yaml -log-file /绝对路径/agent.log` 留存日志，再查 `Claude Messages request rejected` 和 `Claude Messages round failed`。对齐日志时使用时间戳自身的 UTC 偏移，例如 Agent 的 `21:03+09:00` 与 Gateway 的 `12:03Z` 是同一时刻。

客户端的 `native Messages stream failed` 只能说明流已开始后失败，不能据此区分协议、进程或上游问题。工具结果已经投递或应用情况不确定时，Gateway 会消费本轮续接，重试同一批工具结果可能返回 409 `Messages continuation unavailable`；这个错误与前一次流失败需要分开判断。排查时按同一时区对齐 Agent 与 Gateway 日志，并结合请求 ID、运行时和上述错误分类定位；新建会话可以避开旧续接，但不能修复原始流故障。

## 生命周期与限额

以下等待工具、历史恢复、原节点绑定及每回合 64 个 ID 限制适用于未带 `safeguards` 的 MCP 路径。带安全上下文的原生请求共用运行时并发名额和请求/响应大小限制，每轮结束回收 CLI。

每个运行时的独立 CLI 会话数由 `max_concurrent` 决定：未配置时 8 个，显式配置最多 32 个（负值即不限并发也按 32），所有 Gateway 副本共用该 Agent 限额。每个会话是一个常驻 CLI 进程，同一账户的会话共用订阅额度，调大前确认内存和额度。等待调用方送回工具结果最多 1 小时，CLI 生成阶段无输出最多 10 分钟（与 CLI 自身的 API 请求超时一致；CLI 每输出一行即重新计时，持续输出的长回合不受限），会话总寿命 24 小时；超时的会话被回收后，其续接按孤立回合从文本记录恢复。名额已满时淘汰等待工具结果最久的空闲会话来开启新会话（它之后的续接同样从文本记录恢复），只有全部名额都在生成时才答复会话已满。与 Messages API 一样，消息、工具、内容块、停止序列、每回合工具调用与并行工具调用的数量都不设上限。原生请求最多 32 MiB（与 Messages API 自身的请求上限一致），CLI 单行最多 256 MiB（仅防范进程异常）；Gateway 侧单次响应最多 64 MiB、续接每回合最多 64 个工具调用。流式事件逐条传递，使用同步背压。

新会话（包括下面恢复孤立回合开启的新会话）的首轮可以换节点。Gateway 的候选按隧道空闲槽排序，看不到 Agent 的 CLI 会话名额，所以首个事件到达之前，如果节点答复会话已满（`backpressure`）、运行时正在关闭（`runtime_closed`）、账户额度耗尽（`rate_limited`，附带恢复时长，见 [额度耗尽与限流](../README.md#额度耗尽与限流)）或带可重试标志的错误，调度器会依次尝试下一个候选。请求格式不合法等错误不换节点。续接从不换节点。

Redis 原子保存工具 ID 的完整集合、租户/Key、公共模型及原节点/runtime/实际模型，TTL 1 小时（与 Agent 等待工具的时限一致），不保存消息或工具结果；续接成功后保留已使用标记 24 小时。每次续接重新认证，不允许换节点或自动重试；已应用或结果不确定的工具结果不可重放。Agent 另外在本地记住已投递的工具 ID 24 小时（最多 8192 个，超出丢弃最早的），会话结束后重放同一批已投递的结果仍被拒绝（孤立回合的恢复不属于投递，见下文）。HTTP 断开取消未完成回合；正常完成的工具回合保留进程等待下一请求。Agent 关闭、会话超时及 CLI 退出会清理进程组、MCP 监听和临时文件。

Agent 按最近一轮实际公开的 `tool_use` ID 集合校验结果数量和归属。`pending` 邮箱可暂时保留已投递、但尚待迟到 MCP 回调取走的旧结果；这些旧邮箱不计入新一轮待决工具数量，也不会因开始新一轮而被清空。连续多轮与迟到回调已有跨两个 Gateway、真实 mTLS 隧道和模拟 CLI 的回归覆盖；原来按全部邮箱数量检查会产生的 `continuation_tool_count_mismatch` 已在本地复现并修复，真实长任务仍需更新 Agent 后验收。

`anthropic-beta` 按功能集合比较，顺序、逗号旁空格及重复项不影响续接；[官方 beta 文档](https://platform.claude.com/docs/en/api/beta-headers#multiple-beta-features)支持逗号列表与重复请求头。实际增加或删除功能时，只有归属、完整历史、本轮完整工具 ID 集合以及其他会话配置均匹配，才会终止旧 CLI，使用新 beta 设置开启新 CLI，并从包含已执行工具结果的完整记录恢复；不会把这些结果同时投递给旧 CLI。恢复采用与孤立回合相同的文本记录及媒体输入方式，效果可能略逊于保留原进程。API 版本变化及其他配置/历史不匹配仍明确拒绝，不会把新协议设置静默应用到旧进程。

续接失败按原因区分：

- 孤立回合（Gateway 租约已过期，或 Agent 重启、会话超时、CLI 已退出，没有任何会话还持有这些工具 ID，且这些 ID 未投递过）：不再返回 409，而是开启新会话恢复。新会话把完整历史连同最后的工具结果渲染成文本记录，提示 CLI 从这些结果继续任务、不要重复这些调用；工具已在调用方执行，不会重复执行。新会话看到的是文本记录而不是原进程上下文，效果可能略逊于原会话。租约仍有效时请求回到原节点恢复；租约已过期时按新会话调度到任一节点。恢复不触及存活的 CLI，因此不记录状态：与无状态的 Messages API 一样，以同一批孤立结果结尾的请求可以重复发送——回合失败后的重试，或客户端并发的旁路请求（Claude Code 会复用同一段历史）——各自开启新会话，不会被当作重放拒绝。
- 已消费（重放）或被并发认领：返回 409 `continuation_unavailable`。
- 会话还在，但历史、工具或归属不匹配：返回 400，租约保留，客户端修正后可以重试。
- 仅 beta 功能集合变化、其余匹配：按上述方式在原节点重建 CLI 并恢复，Gateway 无需修改。
- 节点失联：返回 503，租约同样被消费。

以上行为需要同时升级 Gateway 和 Agent。只升级 Gateway 时，旧 Agent 仍拒绝多轮追问，重启后的续接仍是 400；只升级 Agent 时，多轮追问可用，但重启后的续接会被旧 Gateway 报成 503。

## 验证

默认测试使用离线事件和注入时钟，不访问真实模型或 Redis：

```bash
go test ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/messagesession ./common/tunnelwire
```

安装了真实 CLI 时，可执行回环模拟 API 契约测试，不使用订阅认证或模型额度。覆盖原始字段一致性、8 轮请求与客户端压缩历史、Agent 用户级 `CLAUDE.md` 隔离，以及原版客户端直连/经 Agent 的 12 次工具读取和最终结果校验：

```bash
AISW_CLAUDE_CONTRACT_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestNativeCLI.*Contract$' -v -count=1 -timeout=180s
```

在客户电脑上验证现有远端 Gateway/Agent 链路，读取客户端环境或当前 `CLAUDE_CONFIG_DIR/settings.json`（默认 `~/.claude/settings.json`）里的 Base URL、调用方 Key 和模型，不要求客户电脑有订阅登录：

```bash
AISW_CLAUDE_GATEWAY_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestLiveSafeguardsGateway$' -v -count=1 -timeout=10m
```

可用 `AISW_CLAUDE_GATEWAY_URL`、`AISW_CLAUDE_MODEL` 显式选择端点和模型；不在命令行打印 Key。测试通过本机回环观察代理统计原生请求与上游状态，正文和认证头原样发送至选定 Gateway，SSE 即时转发。它验证真实远端链路，不能代替在 Agent 主机直接使用 CLI 的基线。

在 Agent 主机运行同一长工具链的直连/本机 Agent 对照，使用该系统账户的 `claude.ai` 登录，不导出 token：

```bash
AISW_CLAUDE_LIVE_TEST=1 AISW_CLAUDE_MODEL=claude-opus-5-5 go test ./service/aiServeWeaveAgent/claudecode -run '^TestLiveSafeguardsDirectComparison$' -v -count=1 -timeout=20m
```

上述真实测试会消耗服务端订阅额度；只使用回环 MCP 返回随机测试文本，不读写项目或执行命令。校验任务完成且至少 12 轮带原生请求及 `available` 结果，同时统计其他固定状态；重试等额外请求不等于任务轮数。不作为模型智力评分，也不代替自动压缩及实际编程任务的对照验收。直连测试的登录前提适用于 Agent 主机，不适用于仅持调用方 Key 的客户电脑；不能用客户机的认证类型判断远端 Agent 是否已登录。当前执行证据见[验收记录](../../../docs/acceptance/claude-native-preservation-2026-11-05/README.md)。

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

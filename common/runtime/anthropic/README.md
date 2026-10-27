# Anthropic Messages 供应商

本包实现 `kind: anthropic`（Claude 官方 API）和 `kind: anthropic_compatible`（以 Messages 协议提供服务的第三方端点）。

## 为什么经 Chat 适配

Gateway 的原生 `/v1/messages` 通道与 Claude Code CLI 会话绑定，依赖 Redis 续接定位。

本包不走那条通道，而是实现 `Chat` / `ChatStream`：先把协议无关的 `ChatRequest` 转换为 `POST /v1/messages`，再把响应和 SSE 转换回 OpenAI 形状。这样一来，Claude API 的服务路径与其他托管供应商完全相同：

- `/v1/chat/completions`
- 转换到 Chat 的 `/v1/responses`

## 端点与认证

| kind | 默认 base_url | 认证 |
| --- | --- | --- |
| `anthropic` | `https://api.anthropic.com` | `x-api-key` |
| `anthropic_compatible` | 无，必填 | `x-api-key` + `Authorization: Bearer`（兼容端点做法不一，两种都发） |

每个请求都携带 `anthropic-version: 2023-06-01`，可以在 Headers 里用其他值覆盖（请求头名称不区分大小写）。

重定向一律不跟随，以 `protocol_error`（"redirect not followed"）返回：Go 跨主机重定向时会转发 `x-api-key` 这类自定义请求头，跟随重定向就可能把 Key 带到未声明的地址。

`base_url` 末尾带不带 `/v1` 都可以，不会拼出重复的 `/v1`。

声明方式、`api_key_env`、`models`，以及「只能本地声明、`base_url` 必须与本地一致」的规则，都与 [hosted](../hosted/README.md#声明) 相同。

## 请求转换

**system 提示词**

`system` 和 `developer` 消息只能是文本，按顺序以空行连接，作为顶层 `system`。

**工具调用**

- assistant 的 `tool_calls` 转为 `tool_use` 块。参数必须是 JSON 对象，空串按 `{}` 处理。
- `tool` 消息转为 user 角色的 `tool_result` 块。

**相邻同角色消息**

Messages 要求角色交替，所以相邻的同角色消息会被合并。

**图片**

接受以下两种：

- base64 data URL，类型为 png、jpeg、gif、webp；
- http(s) URL。

其他内容片段一律拒绝。

**工具定义**

`tools` 转为 `input_schema`，缺省时使用 `{"type":"object","properties":{}}`。

`tool_choice` 的映射：

| ChatRequest | Messages |
| --- | --- |
| `auto` / `none` | 原样传递 |
| `required` | `any` |
| 指名工具 `{"type":"function","function":{"name":"x"}}`（Gateway 各前门统一传递的形状） | `{"type":"tool","name":"x"}` |
| 其他值 | 拒绝 |

**输出上限与采样参数**

- `max_tokens` 缺省为 4096（Messages 要求必填）。
- `temperature`、`top_p` 和 `stop`（作为 `stop_sequences`）照常传递。部分新模型不接受同时设置 `temperature` 与 `top_p`，这类 400 会原样作为上游错误返回。
- `seed` 和 `text` 以外的 `response_format`（结构化输出）不被支持，拒绝，即使运维通过能力覆盖声明了结构化输出也不会被静默丢弃。

**扩展字段**

`Extra` 可以追加顶层字段（例如 `metadata`），但不能覆盖本包已写入的字段。不建议通过 `Extra` 开启 `thinking`：响应中的 thinking 块会被丢弃，多轮工具调用回传历史时无法重建，上游会拒绝；默认的 4096 输出上限也可能小于 `budget_tokens`。

**错误文本**

错误文本是固定措辞，不回显请求中的角色名、字段名或内容。

## 响应转换

- 文本块拼接为 `content`，`tool_use` 块转为 `tool_calls`，thinking 块丢弃。
- `prompt_tokens` 包含缓存命中和缓存写入的输入 token，与 OpenAI 的口径一致。

`stop_reason` 的映射：

| Messages | OpenAI |
| --- | --- |
| `max_tokens` / `model_context_window_exceeded` | `length` |
| `tool_use` | `tool_calls` |
| `refusal` | `content_filter` |
| 其他 | `stop` |

流式事件的处理：

| 事件 | 处理 |
| --- | --- |
| `message_start` | 角色与输入用量 |
| `content_block_start` / `content_block_delta` | 文本增量与工具调用增量 |
| `message_delta` | 结束原因与用量 |
| `message_stop` | 正常结束 |

另有几条规则：

- 在 `message_stop` 之前断流，报协议错误，不当作正常结束。
- 服务端工具块（如 `server_tool_use`、`mcp_tool_use`）的参数增量被忽略，它们不是客户端工具调用。
- 没有参数的工具调用在 `content_block_stop` 时补发 `{}`，与非流式响应一致。
- 输入用量取自 `message_start`；`message_delta` 携带非零的累计输入用量时以它为准。
- 上游的 `error` 事件报 `upstream_error`，消息中的 Key 会被脱敏。
- 空闲超过 `stream_idle_timeout` 时中止。

## 模型目录与能力

模型目录翻页读取 `GET /v1/models?limit=1000&after_id=…`，最多 16 页。声明了 `models` 时以声明为准，端点没有该路由（404/405）时也接受声明；没有声明时报上游错误，并提示声明 `models`。

| 能力 | `anthropic` | `anthropic_compatible` |
| --- | --- | --- |
| `chat`、`chat_stream`、`tools` | 发布 | 发布 |
| `vision` | 发布 | 不发布（兼容端点不一定支持） |
| 嵌入、转录、重排 | 明确拒绝 | 明确拒绝 |

## 验证

本仓库只用 httptest 模拟的端点验证过转换与 SSE 解析，尚未用真实 Key 实机验收。

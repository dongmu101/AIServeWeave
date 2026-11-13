# Codex CLI 后端接入

账户订阅用量与余量可在 Agent 本地配置页面查看；查询使用 app-server 的 `account/rateLimits/read`，不执行推理，详见 [Agent 账户额度说明](../README.md#本地-cli-账户额度)。

`codexcli.New` 已实现 `runtime.InferenceRuntime`；Agent 通过本机已登录的 `codex app-server` 和 stdio JSON-RPC 提供共享推理。`main.go` 注册 `kind: codex`，仅接受本地 YAML 明确声明的运行时 ID。可执行文件由 Agent 的 PATH 提供，模型请求不能携带路径、命令、URL 或凭据。

登录凭据留在 Agent，客户端工具在各用户自己的电脑执行。Gateway 复用已有鉴权、模型路由、配额和 Chat/ChatStream 隧道，Responses 工具与历史在前门转换成共享 runtime 类型。

## 启用与行为

```yaml
runtimes:
  - id: codex-local
    kind: codex
```

不配置 `base_url`、API Key、Headers 或 TLS。Agent 与 `codex login` 必须使用同一系统账户及原有 CODEX_HOME；声明了 `account_dir` 时改用该目录作为 CODEX_HOME，可在同一台机器上挂多个账户（见 [Agent 多账户说明](../README.md#同一-cli-的多个账户)）。显式运行时白名单需包含 codex-local。模型目录由 app-server 的 `model/list` 提供；Probe/Health/Discover 不调用模型推理，每次都冷启动一个 app-server，受 `probe_timeout`（默认 60 秒）限制；紧接着的下一次检查（Probe 后的 Discover、恢复中的 Health 后的 Discover）在 5 秒内复用上一次成功的结果一次，不再启动第二个进程。app-server 在空的 `sqlite_home` 中启动时，会把 CODEX_HOME 下的整个 `sessions` 目录建一遍索引；会话积累到 GB 级时，这一步要耗数秒 CPU。因此每个 app-server 进程（探测、额度查询、对话与生图）仍使用新建的私有 `sqlite_home`，但启动前先从该账户的「种子」复制一份状态库（`state_*.sqlite` 及其 `-wal`），只需为此后新增的会话建索引。种子只取自成功结束的非推理进程（模型目录探测与额度查询），最多每 5 分钟更新一次；推理进程的状态库含有该请求的会话，用完即删，从不成为种子。memories、goals、queue 与 logs 不复制，始终从空开始，因此不会有内容跨请求。种子位于用户缓存目录的 `AIServeWeave/codex-state/<账户目录摘要>/`（macOS 为 `~/Library/Caches`，Linux 为 `$XDG_CACHE_HOME` 或 `~/.cache`），权限 0700，每个账户目录一份；只在进程退出后复制，先写暂存目录再原子改名发布，被取代的种子保留 1 分钟后删除。首次运行、种子复制失败或缓存目录不可用时，退回从头建索引。超时错误会写明卡在哪一步（如 `no reply to initialize after 60s`）。启动仍然很慢时，可以把旧会话移出 CODEX_HOME，或在运行时声明里调大 `probe_timeout`（见 [Agent README](../README.md#探测期限)）。

每个请求启动独立临时进程和 ephemeral thread，经公开 `thread/inject_items` 保留消息角色、function_call 的 call_id 和 function_call_output 的结果，不把工具结果伪装成用户提示词。历史完整注入后以空 input 启动回合；首次动态工具回调转换成 runtime.ToolCall，返回调用方后终止整个进程组。下一轮重新注入完整历史继续，Agent 不保存跨请求线程映射。

支持文本 Chat、ChatStream、Responses SSE、客户端工具、JSON Schema 输出，以及经 `/v1/images/generations` 的单张图像生成（见下文「图像生成」）。工具声明和历史由 Gateway 统一转换，包含 namespace/custom 工具；additional_tools 的定义合并后在当前生成中立即可用，其历史位置和延迟加载不保留。每次响应最多一个工具调用，不声明并行工具能力。图片/音频/文件输入、嵌入、重排、采样参数、强制工具选择，以及 Chat 回合里的原生托管工具（包括生图工具）均明确拒绝。最大输出 token 参数的兼容行为见下节。沿用 Gateway 的 store/previous_response_id 边界：需配置控制面，否则客户端必须携带完整历史。

## Harness 与输出上限兼容

DeepSeek Harness 可以直接使用 `http://127.0.0.1:8080/v1`、OpenAI Chat Completions 和 Agent 上报的真实模型 ID，无需切换 Responses 或关闭客户端自动添加的输出上限。Codex app-server 不提供最大输出 token 控制，AIServeWeave 因此将正数输出上限作为兼容参数接收，**不执行该上限，也不保证输出或计费 token 数不超过请求值**；零或负数仍返回 400。

Gateway 在 Codex 成功响应（JSON、SSE 或 NDJSON）的首部中明确报告：

```http
X-Gateway-Ignored-Parameters: max_completion_tokens
Warning: 299 AIServeWeave "Codex compatibility mode: output token limit is not enforced"
```

被忽略参数名称使用调用方的实际字段：Chat 的 `max_tokens` / `max_completion_tokens`（同时提供则都列出）、Responses 的 `max_output_tokens`、Anthropic Messages 的 `max_tokens`、Ollama 的 `options.num_predict`。未发送上限时不添加提示。提示取决于实际派发成功的后端，不通过模型名前缀猜测；其他后端继续收到原有上限，不添加 Codex 提示。Agent 同时记录不含请求内容的固定警告；生成的结束原因与用量保持真实后端结果，不伪造 `length` 或截断工具参数。

需要严格输出 token 上限的客户端应选择能够执行该限制的后端。本兼容不放开 `temperature`、`top_p`、`seed`、`stop` 等参数。更新此行为需要同时部署新版 Gateway（HTTP 提示）和 Agent（参数接收）；仅重启旧二进制不会生效。上游限制见 [OpenAI 官方说明](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)。

## 思考内容

Codex 的思考以 **摘要** 形式透出：`item/reasoning/summaryTextDelta` 映射为 `ChatStream` 中独立的 `ReasoningContent` 增量，非流式 `Chat` 聚合进 `ChatMessage.ReasoningContent`，从不混进回复。同一 reasoning 条目的不同摘要段、以及不同 reasoning 条目之间以空行分隔。原始思考链 `item/reasoning/textDelta` 只在开启 `show_raw_agent_reasoning` 时发送，隔离进程从不开启；万一到达也被忽略，避免与摘要交错。`thread/tokenUsage/updated` 的 `reasoningOutputTokens` 写入 `Usage.ReasoningTokens`（它是 `outputTokens` 的一部分）。回复与摘要共用生成文本的 1 MiB 上限。

请求中的 `reasoning`（`ChatRequest.Reasoning`）是提示，映射为 `turn/start` 的 `effort` 与 `summary`：

- app-server 只接受模型声明过的 effort，所以 Agent 在发现时记下 `model/list` 的 `supportedReasoningEfforts`，把请求档位换成最接近的已声明档位（距离相同取较高者）。关闭思考时取已声明的最低档位，模型提供 `none` 时即为 `none`。
- 要求思考却没给档位时不覆盖 effort（沿用模型默认），只设置 `summary: "auto"`；开启思考时都设置 `summary: "auto"`。
- 模型没有声明任何 effort，或最近一次发现中没有出现该模型时，不做任何覆盖，请求照常执行。提示从不导致请求被拒绝。

单帧、注入历史及生成文本各最多 1 MiB，每请求最多 2048 条消息、128 个工具、10000 个 RPC 帧；模型目录最多 256 项/16 页。请求总期限及流空闲期限由 runtime.Clock 控制；流不预读无限事件，关闭会取消并回收进程。所有错误只含固定类别，不输出 RPC、提示词、工具结果或认证信息。

## 图像生成

账户能生图时（`modelProvider/capabilities/read` 返回 `imageGeneration: true`），模型目录额外发布一个图像模型 `codex-image`。它只带 `image_generation` 能力，各项聊天能力显式标为不支持；对话模型则把 `image_generation` 显式标为不支持，所以 Chat 和生图路由永远不会选中对方的模型。Probe/Health/Discover 仍然不调用推理。

`Runtime.GenerateImage` 实现 `runtime.ImageRuntime`：

- 每次请求一个独立的临时进程。只有这个进程不带 `--disable image_generation`，shell、MCP、插件、`view_image` 等其余宿主工具照旧关闭；Chat 进程的参数不变，Chat 回合里出现 `imageGeneration` 条目仍会被拒绝。
- 不注册动态工具，固定的 `baseInstructions` 要求只生成一张图，调用方提示词作为 `turn/start` 的唯一用户输入。出现工具回调、审批请求或未声明的指令文件即失败。
- 收到第一个完成的 `imageGeneration` 条目就停止并回收进程组，一次请求不会产出两张图。图像取自 `result` 的 base64，按魔数判定格式，只接受 PNG/JPEG/WebP/GIF，解码后不超过 32 MiB。
- 只有生图进程的 `imageGeneration` 完成帧可以放宽到 48 MiB（一张 1024² PNG 的 base64 就约 2.2 MB），其余帧仍受 1 MiB 限制。
- Codex 会另存一份到 `CODEX_HOME/generated_images/<thread_id>/`。进程回收后，Agent 删除这个目录。目录由校验过的线程 ID（只允许 `[A-Za-z0-9_-]`）拼出来，不使用 app-server 报回的 `savedPath`，因此不可能删到别处。
- 生图期间 app-server 可能静默二三十秒，所以只受 `RequestTimeout`（默认 5 分钟）和 Gateway 下发的期限约束，不使用流空闲期限。与 Chat 共用并发限额。
- 账户生图额度耗尽（`usageLimitExceeded`）映射为可重试的 `rate_limited`，对话回合的额度耗尽同样如此，并附带额度恢复时长（见 [额度耗尽与限流](../README.md#额度耗尽与限流)）；回合结束仍没有图映射为 `upstream_error`；格式、大小或关联校验失败映射为 `protocol_error`。错误文本不含提示词。

Codex 内置生图工具不允许调用方选择上游图像模型。`codex-cli 0.162.1` 的 `thread/start`、`turn/start` 都没有相关字段，`image_generation.model`、`tools.image_generation.model` 等候选配置键也都被 CLI 判为无法识别。所以这里只发布一个不标版本的 ID，不发布 `gpt-image-2.5-flare` / `gpt-image-2.5-sunburst` 这类 API 模型 ID，也不要在 Gateway 路由里把它们设成指向 `codex-image` 的别名。尺寸、质量、背景、输出格式等参数同理，由 Gateway 按名字拒绝。

## 验证

```bash
go test ./service/aiServeWeaveAgent/codexcli ./service/aiServeWeaveGateway/e2e
go test -race ./service/aiServeWeaveAgent/codexcli ./service/aiServeWeaveGateway/e2e
# 仅查询模型目录，不执行推理
AISW_CODEX_LIVE=1 go test ./service/aiServeWeaveAgent/codexcli -run '^TestLiveModelDiscovery$' -v -count=1
# 其余显式真实测试会使用本机账户额度
AISW_CODEX_LIVE=1 go test ./service/aiServeWeaveAgent/codexcli -run TestLiveCallerToolRoundTrip -v
AISW_CODEX_LIVE=1 go test ./service/aiServeWeaveGateway/e2e -run TestLiveCodexClientThroughTunnel -v
# Harness 原始 Chat 参数与真实 Codex 推理，验证输出上限兼容提示
AISW_CODEX_LIVE=1 go test ./service/aiServeWeaveGateway/e2e -run '^TestLiveCodexOutputLimitCompatibility$' -v -count=1
```

生图没有自动化的真实测试，因为每跑一次都会消耗账户的生图额度。真实验收记录在已删除的设计文档第五节，见提交 b810671 之前历史中的 `docs/superpowers/specs/2026-10-16-codex-image-generation-design.md`。

离线测试覆盖有界 stdio、跨线程/回合拒绝、禁止本机工具与审批、文本 SSE、工具结果续接、Gateway 鉴权、真实回环 mTLS、并发用户隔离、注入时钟期限、并发限额及进程回收。旧实验记录只证明其合成场景。

2026-10-03 本机验收（macOS，`codex-cli 0.159.2`，实际模型 `gpt-6.1-sol`）：

- `TestLiveCallerToolRoundTrip` 通过：适配器返回调用方工具，下一轮模型消费随机工具结果。
- `TestLiveCodexClientThroughTunnel` 通过：原版 CLI → Gateway HTTP/SSE → 真实 mTLS → Agent CLI；随机文件仅位于客户端目录，最终回答包含其内容。测试隔离客户端用户配置和会话环境，保留 `code_mode_host` 来执行客户端工具。
- `go test ./...`、`go vet ./...`、`go build ./...`、格式与 proto 生成一致性检查通过；本次修改涉及的包竞态测试通过。
- 初次全量竞态检查出现两项既有间歇断言失败：`TestManagerDrainAllStopsDispatchAndWaitsForInFlight` 和 `TestSlotOccupancyMetrics`，均在未修改的 HEAD 临时基线上复现。完成本地后端未就绪启动策略后，串行全量普通与竞态复跑均通过；这两项测试及其隧道实现未在本次接入中修改，复跑通过不代表间歇问题已修复。

## 原有合成实验接口

```go
report, err := codexcli.Probe(ctx, codexcli.Config{
    Executable: "/absolute/path/to/codex",
    Timeout:    90 * time.Second,
})
```

`Model` 留空保留本机 CLI 模型配置，显式设置才覆盖；实际模型记录在 `Report.Model`。超时默认 90 秒、最多两分钟。macOS/Linux 支持进程组回收，其他平台当前返回 `ErrProcess`。

运行入口及真实验证记录见 [独立实验命令](../../../experiments/codex-cli-bridge/README.md)。

## 协议与隔离边界

- 使用 `initialize` → `account/read` → `thread/start` → `turn/start`；开启 `experimentalApi` 后接收 `item/tool/call` 并按请求 ID 返回结果。
- 只接受 ChatGPT 登录，不读取、复制或转发 `auth.json`/token；保留原有认证目录环境变量，过滤 API Key 与网关地址覆盖。
- 每次独立进程、0700 临时工作目录、ephemeral thread、只读 sandbox；关闭 shell、MCP/插件/Hook 自动加载等非实验能力。收到未允许的 item 或审批请求即失败，不自动批准。
- 保留本机 Codex 账户目录中原生的 `AGENTS.md` / `AGENTS.override.md`，并报告来源数量；项目和其他来源的指令一律拒绝。账户通用指令会参与实验推理，这不等于空白账户环境。
- 每帧最多 1 MiB、最多 10000 帧，每会话仅允许两个指定工具调用。错误仅暴露固定类别；报告不含模型正文、指令内容、文件路径或凭据。
- 核对线程、turn、工具名、参数与调用 ID；重复或跨会话调用失败。CLI 终态、两个实际回调以及调用方随机结果均满足时才报告通过。
- 取消和任何退出路径都清理 app-server 进程组。默认测试使用假子进程，不访问模型服务。

**工具名与 schema**：`wireToolName` 把客户端工具名映射成 app-server 接受的名字（`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`，且不在保留命名空间里），对话历史中的工具调用按同一规则改写；app-server 回调别名时换回客户端原名再输出。
- app-server 把 `mcp` 与 `mcp__` 前缀（区分大小写）留给自己的 MCP 工具，`dynamicTools` 里出现这类名字时整个回合失败，Gateway 只能看到 `upstream_error`。Claude Code 的 MCP 工具一律命名为 `mcp__<服务>__<工具>`。放得下时别名为 `c_` + 原名。2026-11-03 在 open7x 上实测：`mcp__x__t`、`mcp__t`、`mcp` 失败，`x__t`、`mcp_x_t`、`MCP__x__t`、`xmcp__t` 正常。
- 客户端名字按 `^[A-Za-z0-9_.:-]{1,256}$` 校验。超过 64 字符、或含 app-server 拒绝的字符时，别名为 `c_` + 可读主干 + `_` + 完整名字 SHA-256 的前 8 位十六进制；`mcp__<服务>__<工具>` 的主干保留工具名、优先截短服务名，长服务名下的各个工具对模型仍可区分。不合法字符替换为 `_`。别名与另一工具的真实名字相同时按 `ErrConfig` 拒绝。
- `normalizeToolSchema`：缺失的 schema 视为无参数；缺少或为 null 的 `type` 补成 `"object"`，包含 `"object"` 的类型列表收敛为 `"object"`；其他类型、显式 `null`、非对象 schema 按 `ErrConfig` 拒绝；已合规的 schema 逐字节保留。2026-11-03 实测 `$schema`、`$id`/`$defs`、`\p{L}` 正则、`additionalProperties`、`anyOf`、`propertyNames` 在 app-server 上均正常，因此不改写。

合成 Probe 的限制与正式 Runtime 的范围分别以上文为准；不声明完整 Responses API 对等。`dynamicTools` 是实验接口，CLI 更新后必须重新验证；不能仅凭 schema 出现某字段就假定该版本已实现对应能力。

## 官方依据

- [Codex app-server](https://learn.chatgpt.com/docs/app-server)
- [Codex 配置说明](https://learn.chatgpt.com/docs/config-file/config-reference)

实现核对了本机 `codex-cli 0.159.2` 通过 `generate-json-schema --experimental` 生成的协议。没有采用标记为内部用途的 `thread/resume.history` 拼接历史。

# Codex CLI 后端接入

`codexcli.New` 已实现 `runtime.InferenceRuntime`；Agent 通过本机已登录的 `codex app-server` 和 stdio JSON-RPC 提供共享推理。`main.go` 注册 `kind: codex`，仅接受本地 YAML 明确声明的运行时 ID。可执行文件由 Agent 的 PATH 提供，模型请求不能携带路径、命令、URL 或凭据。

登录凭据留在 Agent，客户端工具在各用户自己的电脑执行。Gateway 复用已有鉴权、模型路由、配额和 Chat/ChatStream 隧道，Responses 工具与历史在前门转换成共享 runtime 类型。

## 启用与行为

```yaml
runtimes:
  - id: codex-local
    kind: codex
```

不配置 `base_url`、API Key、Headers 或 TLS。Agent 与 `codex login` 必须使用同一系统账户及原有 CODEX_HOME；显式运行时白名单需包含 codex-local。模型目录由 app-server 的 `model/list` 提供；Probe/Health/Discover 不调用模型推理，默认探测期限为 15 秒以覆盖 CLI 冷启动。

每个请求启动独立临时进程和 ephemeral thread，经公开 `thread/inject_items` 保留消息角色、function_call 的 call_id 和 function_call_output 的结果，不把工具结果伪装成用户提示词。历史完整注入后以空 input 启动回合；首次动态工具回调转换成 runtime.ToolCall，返回调用方后终止整个进程组。下一轮重新注入完整历史继续，Agent 不保存跨请求线程映射。

支持文本 Chat、ChatStream、Responses SSE、客户端工具及 JSON Schema 输出。工具声明和历史由 Gateway 统一转换，包含 namespace/custom 工具；additional_tools 的定义合并后在当前生成中立即可用，其历史位置和延迟加载不保留。每次响应最多一个工具调用，不声明并行工具能力。图片/音频/文件输入、嵌入、重排、采样/最大输出 token 参数、强制工具选择和原生托管工具均明确拒绝。沿用 Gateway 的 store/previous_response_id 边界：需配置控制面，否则客户端必须携带完整历史。

单帧、注入历史及生成文本各最多 1 MiB，每请求最多 2048 条消息、128 个工具、10000 个 RPC 帧；模型目录最多 256 项/16 页。请求总期限及流空闲期限由 runtime.Clock 控制；流不预读无限事件，关闭会取消并回收进程。所有错误只含固定类别，不输出 RPC、提示词、工具结果或认证信息。

## 验证

```bash
go test ./service/aiServeWeaveAgent/codexcli ./service/aiServeWeaveGateway/e2e
go test -race ./service/aiServeWeaveAgent/codexcli ./service/aiServeWeaveGateway/e2e
# 显式真实测试，会使用本机账户额度
AISW_CODEX_LIVE=1 go test ./service/aiServeWeaveAgent/codexcli -run TestLiveCallerToolRoundTrip -v
AISW_CODEX_LIVE=1 go test ./service/aiServeWeaveGateway/e2e -run TestLiveCodexClientThroughTunnel -v
```

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

合成 Probe 的限制与正式 Runtime 的范围分别以上文为准；不声明完整 Responses API 对等。`dynamicTools` 是实验接口，CLI 更新后必须重新验证；不能仅凭 schema 出现某字段就假定该版本已实现对应能力。

## 官方依据

- [Codex app-server](https://learn.chatgpt.com/docs/app-server)
- [Codex 配置说明](https://learn.chatgpt.com/docs/config-file/config-reference)

实现核对了本机 `codex-cli 0.159.2` 通过 `generate-json-schema --experimental` 生成的协议。没有采用标记为内部用途的 `thread/resume.history` 拼接历史。

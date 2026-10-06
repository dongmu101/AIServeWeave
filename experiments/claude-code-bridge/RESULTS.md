# 正式接入验证记录（2026-10-05）

Go `1.27.1`、macOS arm64、Claude Code `2.1.289`，使用 CLI 原生订阅登录；未提取或转发登录凭据。后端已注册 `kind: claude`，配置页可选择，Agent 与 Gateway 通过新增的原生 Messages 操作连接。上游固定 `claude-sonnet-4-6`，对外发布 `sonnet`，客户端完整模型名通过路由映射。

## 已运行的证据

| 检查 | 结果与范围 |
| --- | --- |
| 真实 Messages 前门 | 单独 HTTP 工具请求/结果往返通过，最终结果包含仅调用方持有的随机值 |
| 原版 CLI 客户端 | 不修改客户端代码，Read 读取临时文件、Edit 追加验证标记、最终回复保留原内容；通过 |
| 双用户完整链路 | 两个原版客户端同时使用不同 Key，经轮流选择的两个 Gateway 副本、共享真实 Redis、真实 Agent mTLS 隧道，分别完成 Read/Edit；最终复跑两用户均成功，总用时约 10.5 秒 |
| 默认离线全链路 | 假 CLI + 真实 mTLS，首轮 Gateway 0、工具续接 Gateway 1，JSON/SSE 都通过；认证后的 `/v1/models` 发布原生模型 |
| Redis 续接 | 两个独立 Redis 客户端：不同租户/Key/模型、缺失结果集合、重复发布、并发认领、回滚、一次性消费及 TTL 检查通过；只操作随机测试 key |
| 归属与历史 | 假事件测试拒绝串用户、改历史/模型/工具、未知/重复结果；两用户同名工具不同结果不串线 |
| CLI 生命周期 | 注入时钟过期、未消费流关闭、完成工具回合后原请求关闭仍可续接、驻留容量最多两个：离线测试通过 |
| 参数默认值 | 原版客户端给 Edit 参数补 schema 声明的默认值时允许续接；修改显式参数或补未声明字段被拒绝 |
| 扩展边界 | thinking、cache_control、未知字段及超限请求明确拒绝；真实客户端采用关闭 thinking/缓存/实验 beta 的限定配置 |

真实联调命令：

```bash
AISW_CLAUDE_LIVE_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run TestLiveMessages -count=1 -v
AISW_MESSAGES_REDIS_ADDR=127.0.0.1:6379 go test ./service/aiServeWeaveGateway/messagesession -count=1 -v
AISW_CLAUDE_LIVE_TEST=1 AISW_MESSAGES_REDIS_ADDR=127.0.0.1:6379 go test ./service/aiServeWeaveGateway/e2e -run TestLiveClaudeClientsThroughTunnel -count=1 -v
```

最初使用浮动 `sonnet` 上游别名时，首轮成功，续接产生未支持的内容块，严格校验中止；重试被已消费续接保护拒绝。固定至已验证的 Sonnet 4.6 后，双用户全链路两次成功。不得把未支持内容块丢掉后继续报告成功。

## 质量检查和剩余边界

`go vet ./...`、`go build ./...` 通过。新增 Claude、Gateway e2e/httpapi、Redis 续接包的 `go test -race` 通过。gofmt 与 `git diff --check` 无输出；使用 protoc `35.0` 重新生成两次，生成文件逐字节一致，没有手工修改 pb.go。

全仓普通测试仍会遇到既有图片测试偶发返回 429：`TestImagesGenerationsURLModeIsDownloadable` 与 `TestImagesGenerationsNoQualifyingArtifactsIsAnError`。用所有修改还原到 HEAD、所有新增 Go 文件隐藏的只读 Go overlay 连跑 30 次，同样复现。全服务 `go test -race -p 1 ./service/...` 的控制面与 Gateway 包通过，但既有 `TestManagerDrainAllStopsDispatchAndWaitsForInFlight` 仍出现排空后 `Idle:1`；旧实现覆盖测试和下方历史记录也出现失败。全仓普通/race 检查不能报告为全部通过，本次不提交代码。

本次证明的是限定客户端配置下的文本编程任务与工具循环，不是完整 Anthropic API 或交互式 Claude Code 兼容。真实等待工具超时/外部进程取消尚未单独全链路验收；thinking、缓存、图片/文件、任意历史、多轮新增提问、同轮同名同参数工具、长稳与跨网络部署均不在已验证支持范围。实际部署的 Agent/Gateway 未自动重启或改写配置。启用说明见 [运行时 README](../../service/aiServeWeaveAgent/claudecode/README.md)。

---

以下保留第一阶段的历史证据，结论只适用于 2026-09-30 的实现。

# CLI/MCP 实验记录（2026-09-30）

## 环境与结论

macOS arm64，Go `1.27.1`，Claude Code `2.1.285`，CLI 原生 `claude.ai` / Max 登录，模型参数 `sonnet`。认证字段只读取是否登录和认证方式，未提取或转发凭据。

**已证明本机已登录 CLI 可以经 MCP 进行连续/并行合成工具调用，并消费模拟调用方提供的结果。尚未证明原版 Claude Code 客户端经 Gateway 的 Messages API 工具往返。**

初始沙箱内 `auth status` 报告未登录；在正常本机权限下重查后为已登录。live 使用后者执行，不能把沙箱结果误判为用户没有登录。

## 真实运行证据

构建命令：

```bash
go build -o /private/tmp/aisw-claude-bridge-probe ./experiments/claude-code-bridge
```

以下命令中 CLI 路径为本机安装的真实可执行文件，每次只输出脱敏摘要：

```bash
/private/tmp/aisw-claude-bridge-probe -live -claude /Users/sky/.nvm/versions/node/v22.23.3/bin/claude -model sonnet -sessions 1 -timeout 90s
/private/tmp/aisw-claude-bridge-probe -live -claude /Users/sky/.nvm/versions/node/v22.23.3/bin/claude -model sonnet -sessions 2 -timeout 90s
/private/tmp/aisw-claude-bridge-probe -live -claude /Users/sky/.nvm/versions/node/v22.23.3/bin/claude -model sonnet -sessions 1 -scenario parallel -timeout 90s
```

| 场景 | 会话 | CLI 事件 | tool start | tool 回合 | message stop | MCP 结果投递 | 随机值出现在最终结果 | 退出码 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 顺序两轮 | probe-1 | 35 | 2 | 2 | 3 | 2 | 是 | 0 |
| 双会话顺序两轮 | probe-1 | 35 | 2 | 2 | 3 | 2 | 是 | 0 |
| 双会话顺序两轮 | probe-2 | 35 | 2 | 2 | 3 | 2 | 是 | 0 |
| 同回合并行 | probe-1 | 31 | 2 | 1 | 2 | 2 | 是 | 0 |

随机值由每个会话的模拟调用方生成，不在初始提示词里，只经第二个工具结果发给 CLI；各会话使用不同值。

并行场景的 broker 投递顺序为 `second`、`first`。两个 HTTP handler 独立写响应，因此该观察不证明 CLI 的实际接收顺序。审查后报告字段由 `result_order` 更名为 `broker_result_order`，并保留此限制。所有运行的 `messages_api_compatibility` 均为 `not_tested`。

这些 live 记录发生在退出清理修复之前；后续进程组清理与输出大小限制的修改通过离线子进程回归验证，没有把旧 live 记录伪装成新的全链路测试。

## 离线测试与审查

已验证：

- 身份不同、未知/重复工具结果拒绝，最多八个待决工具，取消及关闭释放等待。
- MCP 初始化、工具列表、往返、非法 JSON、超大消息、Host/Origin/认证和方法限制。
- CLI 畸形/超大事件、未完成结果、失败结果、意外工具、非零进程退出和 stderr 隐藏。
- 两个并行工具在 broker 中反序投递，绑定各自调用 ID。
- 父子进程握手后取消，确认两者连接关闭；父进程正常/非零退出，确认同组后代被清理。

Go 审查发现 `bytes.Buffer` 匿名嵌入暴露 `ReaderFrom`，可绕过自定义 `Write` 的 64 KiB 限制。先用 `io.Copy` 和真实子进程 stdout 重现失败，再改为具名 buffer 字段，两种回归均通过。

独立代码审查发现父 CLI 自行退出时可能遗留子进程。握手测试先在正常和非零退出两例中失败，再补退出时进程组清理，两例通过。审查也纠正了并行结果顺序的证据范围。修复后的复核无未解决实质问题。

## 对完整设计的覆盖

| 设计验收项 | 当前覆盖 |
| --- | --- |
| V01 登录 CLI 流式回复 | 真实验证通过 |
| V02 自定义工具往返 | 合成 MCP 工具真实通过；尚非远端 Messages 客户端 |
| V03 工具执行位置 | 禁用内建工具、检查初始化工具清单；未验证调用方真实文件读写 |
| V04 连续及并行工具 | 真实通过连续两轮及同回合并行；HTTP 接收逆序未验证 |
| V05 双用户隔离 | broker 身份测试及双 CLI 会话独立随机值通过；真实 Gateway Key 尚未接入 |
| V06 取消与退出 | 离线父子进程取消/退出回归通过；真实工具等待超时未专项联调 |
| V07 重试与历史 | 重复结果离线拒绝通过；完整历史及跨 HTTP 请求续接未实现 |
| V08 原版客户端 | 未实现 Messages 前门，未运行 |
| V09 协议扩展 | 完整 thinking/缓存/工具 schema 兼容未验证 |

生产接入门槛未全部满足。下一步仍需实现实验 Messages 前门和会话续接，并用原版客户端验证 V03、V05、V07–V09；不能把当前程序注册为已兼容 Claude Code 的生产 runtime。

## 仓库质量检查

`go test ./...`、`go build ./...`、`go vet ./...`、新增实验包的 `go test -race` 通过；gofmt 无输出。普通测试在正常本机权限下执行，沙箱禁止回环监听的失败已单独识别。

默认本机 protoc 是 `34.1`，生成后仅版本注释与仓库不同。下载官方 `35.0` 到临时目录，核对 SHA-256 `45444963204757fd3e2fbe304bc1fdadfb488d8556ff099c4cc06575eab88976`，用它重新执行 `go generate ./api/...` 后 `git diff --exit-code -- api/proto` 通过，未手工编辑生成文件或修改系统工具安装。

首次服务全量 race 未通过：`TestManagerDrainAllStopsDispatchAndWaitsForInFlight` 在 `manager_test.go:526/529` 观察到排空后 `Idle:1`，期望 0。该用例在当前目录单独运行 10 次及不含本次变更的 HEAD 临时副本运行 20 次都出现同样失败，确认是基线也存在的问题。并行 race 还出现控制面 `e2e` 和 `internal/logic` 测试进程 `signal: killed`。

随后 `go test -race -p 1 ./service/...` 按包串行重跑，隧道包这次通过，但控制面上述两个包仍被系统终止（约 68 秒和 74 秒）。退出码为 1，不能把全仓 race 报告为通过。按仓库“全部跑通再提交”的要求，本次实现保留在工作区，未创建代码提交。

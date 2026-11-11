# Claude CLI 契约与工具隔离加固

日期：2026-11-11。源码基线 `e53f519`，接续[工具隔离拒绝诊断](../claude-unsafe-tools-2026-11-11/README.md)。本记录区分审查发现、可复现缺陷和原生产故障：原故障只有 `unsafe_tools` 分类，没有再次复现，不能把下列加固宣称为其已证实的根因修复。

## 官方依据与实现规则

核对 [CLI reference](https://code.claude.com/docs/en/cli-reference)、[Agent SDK TypeScript 类型](https://code.claude.com/docs/en/agent-sdk/typescript)、[Tool search 协议](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool) 和官方仓库固定版本 [v2.1.322 CHANGELOG](https://github.com/anthropics/claude-code/blob/v2.1.322/CHANGELOG.md)。Context7 重试仍报鉴权错误，使用官方站点原文继续核验。

| 边界 | 实现规则 |
| --- | --- |
| 调用方工具与本机工具 | 调用方 `tools` 属于上游 API 文档，名称不按 CLI 内建清单过滤；本机注册表另行隔离与校验。原生进程增加 `--disallowedTools "*"`，继续使用空 `--tools`、严格空 MCP 和受限模式；兼容桥接只开放 `aisw_bridge`，不加通配拒绝 |
| 延迟工具与服务器工具 | `defer_loading` 不免除顶层完整定义；`server_tool_use`、`tool_search_tool_result`、`tool_reference` 原样转发，调用方工具仍需已声明且有 ID。不得把服务器工具登记为本机 MCP 回调 |
| 生命周期通知 | `auth_status`、工具进度/摘要、下一步提示及非初始化 system 通知不进入推理输出，可出现在结果前后；成功结果后的额度通知不推翻结果。通知本身不能完成回合 |
| 语义与协议失败 | `conversation_reset` 会改变对话语义，不能作为通知跳过。未知顶层事件、新 init、重复 result、额外模型输出仍拒绝。原有工具声明/ID 与本机注册检查继续保留 |
| 状态码 | 本机设置、启动、协议、工具隔离、失败结果和进程退出按后端失败处理；实际捕获的上游 400/429/529 及重试头保留。响应已开始时使用错误事件，不能再改 HTTP 状态 |

`--allowedTools` 是免询问规则，不能代替移除工具；`--tools` 不限制 MCP。`--bare` 也不能作为全部工具已关闭的保证。没有改成放行任意工具名，也没有根据旧生产日志猜测具体工具。

[Managed MCP 文档](https://code.claude.com/docs/en/managed-mcp) 明确：托管 MCP 文件与严格 MCP 参数冲突时 CLI 拒绝启动。Agent 不删除托管文件、不绕过主机管理员策略；这种主机配置不能靠扩大工具白名单解决。官方公共仓库提供说明、示例和变更记录，不包含完整 CLI 运行时源码，因此需要真实可执行文件契约测试。

## 复现与验证

新增回归先在旧实现中复现：认证通知导致协议失败，成功结果后的工具/额度通知推翻成功结果，本机协议与工具隔离故障误映射为 400，以及原生启动参数缺少显式通配拒绝。工具隔离参数是防护加固，不能据此推断旧进程实际执行过本机工具。

默认测试验证通知前后顺序、隐藏通知载荷、通知不能替代结果、未知事件仍失败、三类工具隔离拒绝及错误透传。另覆盖 `Bash`、`Read`、`Write`、`Agent`、`ToolSearch`、`EndConversation`、未来名称及 MCP 名称；声明它们不会授权本机执行。服务器 ToolSearch 返回的引用与客户端调用保留原字段，不进入本机回调邮箱。

以下检查通过：

```sh
go test ./... -count=1 -timeout=240s
go vet ./...
go build ./...
AISW_CLAUDE_CONTRACT_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestNativeCLI(CallerToolIsolationContract|SafeguardsContract|OriginalClientContract|HistorySizeContract|RejectionContract|CompactionContract|DocumentContract)$' -count=1 -timeout=180s -v
```

真实 CLI `2.1.322` 加回环模拟 API 通过：6 类工具的上游定义、名称、ID、参数原样传递，调用结束并回收 CLI 后没有执行标记文件、没有额外工具轮次请求；8 轮原生字段契约；三条链路各 12 段工具流程；31 MiB 历史、4 MiB safeguards、转义密集文档、PDF、两类压缩和 4 类能力拒绝。所有测试使用合成文档与回环 API，不使用真实订阅、密钥或模型额度。

全服务 `go test -race ./service/... -count=1 -timeout=240s` 中，Claude、Gateway e2e 及其他包通过，但隧道 `TestManagerDrainAllStopsDispatchAndWaitsForInFlight` 失败，断言 drain 返回后仍有空闲槽。当前目录单独重复 20 次有 3 次失败；在隔离目录解包未修改的 `e53f519`，重复 200 次也复现空闲槽残留及取消后退出超时，确认基线已有该问题。未改隧道代码，不将全服务 race 标为通过；该问题需独立跟进。

`gofmt -l ./service ./api` 与 `git diff --check` 通过。在隔离源码副本执行 `go generate ./api/...`，结果与当前仓库仅差 protoc `7.35.1` / `7.34.1` 的版本注释，生成逻辑和协议未改变，工作区生成文件未修改。

## 实际 Agent 主机部署

在实际承载 Claude 的 macOS arm64 AgentDev 主机上，确认目标包源码前像一致后应用补丁，使用该主机的 Go `1.27.4`，绕过远程测试转发器。完整 Claude 包默认测试、新增 6 类工具隔离契约及 8 轮原生契约均实际执行并通过，CLI 为 `2.1.322`，随后原生构建 Agent。

检测活动后端调用数降为零后备份原二进制、替换候选并重启 LaunchAgent。已核对安装后的 SHA-256 与候选一致：`7e0978780e6d46e7bfea39c168459fbe84fe7352444d62de0f1bdbb4692464e1`；进程于台北时间 17:43 启动并完成隧道重连。没有改动 Gateway 或账户配置。新进程的真实订阅长任务尚未验收，不能用隧道已连接代替推理验收。

## 升级与验收边界

现有部署需更新并重启实际承载 Claude 的 Agent。本次不改 Gateway 推理逻辑、隧道协议或配置字段；平台从 Gateway 镜像分发安装包时，需刷新其中的 Agent 制品，已有节点仍需独立升级。

CLI 版本更新前应在目标主机跑门控契约测试，确认实际执行版本与日志路径。真实订阅长任务、托管 MCP 冲突环境、其他 CLI 版本及其他操作系统仍待验收；不保证未来 CLI 的语义或协议变更可以自动兼容。原生产拒绝若复发，继续按新增的固定 `cli_event_error` 与事件行号定位，不记录或回显请求载荷。

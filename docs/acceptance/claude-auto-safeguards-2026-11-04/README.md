# Claude Code auto safeguards 验收记录

日期：2026-11-04。范围为 Claude CLI 原生 Messages 的服务端危险工具判定；不代表完整 Anthropic API 或全部 Claude Code 功能验收。生产 open7x.com 尚未部署本次版本。

## 行为

- 非空 `safeguards` 启用原生无状态路径，保持原始历史、system、工具名称/ID、classifier_context 和思考配置；版本与 beta 通过专用隧道字段传递。
- 只返回真实上游判定，不生成 `available`、`evaluated`、`not_flagged`；缺失或不可用状态由客户端处理。
- Agent CLI 本地工具和 MCP 注册表为空。请求中的原始工具定义只发送给上游，实际工具由调用方执行。
- 每次请求重新启动 CLI，工具回合完成即回收进程。旧的无 safeguards MCP 续接仍保留身份、完整历史、原节点与一次性投递保护。
- JSON/SSE 保留安全结果、原生内容块扩展和上下文管理结果；引用增量按顺序聚合，截断工具参数的 SSE 保持原始终止原因。

## 默认测试

以下检查均已通过。测试使用隔离 CLI 子进程桩，Gateway 测试使用真实 mTLS 隧道、Agent 及两个 Gateway 副本。

```sh
go test ./...
go test -race ./common/runtime ./common/tunnelwire ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/httpapi ./service/aiServeWeaveGateway/e2e
go vet ./...
```

覆盖协议选择器往返及换行注入拒绝、原始策略与历史保留、本地工具禁用、下一轮上下文更新、同参数并行工具、不同判定状态、JSON/SSE、跨副本无状态重试、原生扩展字段、引用增量、截断工具流以及旧路径续接。引用回归在修复前复现 JSON 502 / SSE 错误事件，修复后通过。

## 本机原版 CLI 实验

使用未修改的 Claude Code `2.1.318`，工作目录与客户端配置隔离。未读取或导出账户 token，未修改生产配置。

1. 本地模拟 API 返回原始 `Bash` 工具及判定。CLI 启动工具注册表为空，上游原始工具名及 ID 保持；即使返回写标记文件的命令也未执行。320 KiB 原生历史通过 `0600` 设置文件加载后成功发出两次请求，不受进程初始环境变量大小限制。模拟服务恒定返回工具调用，实验在两轮上限处结束；此项验证传输及隔离，不是实际模型成功验收。
2. 原版客户端 → 新 Agent 的 `NewExperimentalHandler` → 本机已登录订阅上游，模型由 CLI 发现为 `claude-opus-5-5`。客户端唯一工具是回环地址上的模拟 MCP，只返回一次随机测试文本，不执行命令或访问项目文件。客户端完成两轮原生请求，真实上游结果如下：

```text
GUARDED_REQUESTS 2 AVAILABLE_RESULTS 2 VERDICTS [evaluated/not_flagged]
CALLER_DRY_RUN_CALLS 1 FINAL_SUCCESS true TOKEN_RETURNED true
CLIENT_STDERR_BYTES 0 CLIENT_EXIT_OK true
```

第二项验证了原版客户端、安全结果及工具往返的实际相容性；Gateway 至 Agent 的协议链路由上述双副本 mTLS 测试覆盖。未将该本机实验描述为生产 Gateway 实测，也未验证所有账户、系统或模型的计费资格。

## 升级

先更新并重启承载 Claude 的 Agent，再重建并重启 Gateway。客户端移除旧的 `CLAUDE_CODE_AUTO_MODE_SERVER=0`（可设为 `1`）及 `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`，新建会话后核对真实返回。以上针对本记录使用的 `2.1.318`；`2.1.319` 起环境变量规则有变化，以[运行时说明](../../../service/aiServeWeaveAgent/claudecode/README.md#claude-code-auto-服务端安全检查)为准，复验结果见 [2.1.319 验收记录](../claude-code-2.1.319-2026-11-09/README.md)。

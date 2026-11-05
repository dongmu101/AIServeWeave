# Claude 原生请求保留与长工具链验收

日期：2026-11-05。范围为带 `safeguards` 的 Agent CLI 路径、诊断日志、真实 CLI 传输契约及可重复的直连对照。代码支持、模拟上游验证与真实模型验收分别记录。

## 本次改动

- 生产 `Runtime.Messages` 入口记录回合开始/结束、随机 `round_id`、`runtime_id`、`message_path`、结束结果及固定分类的 `safeguard_status`，不记录请求、工具参数、判定原因或凭据。
- 原生路径跳过附件转换、历史折叠、兼容输入大小检查和旧思考/effort/缓存参数映射，直接提交调用方原生推理文档。仍保持 CLI 本地工具/MCP 为空、用户配置隔离、有界请求与临时文件清理。
- 原生附件由上游校验。Office 与 `video` 平台扩展不自动转换；准备原生内容须发生在调用方生成安全上下文之前。
- Gateway 无新增配置或协议变化。上线本次改动需要更新并重启承载 Claude 运行时的 Agent；通过 Gateway 制品分发 Agent 时，还需更新对应分发制品。

## 已执行：真实 CLI 加模拟上游

相关默认回归、race 与静态检查通过：

```bash
go test -race ./common/runtime ./common/tunnelwire ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/httpapi ./service/aiServeWeaveGateway/e2e -count=1
go vet ./common/runtime ./common/tunnelwire ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveGateway/httpapi ./service/aiServeWeaveGateway/e2e
```

CLI：`2.1.318`。接口仅监听回环，使用假 API Key 与隔离账户目录，不调用真实模型。

```bash
AISW_CLAUDE_CONTRACT_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestNativeCLI.*Contract$' -v -count=1 -timeout=180s
```

| 测试 | 核查内容 | 结果 |
| --- | --- | --- |
| `TestNativeCLISafeguardsContract` | 捕获实际 CLI 出站 HTTP，逐字段比对 system、结构化历史、工具 schema/ID、思考与签名、缓存、metadata、context_management、安全上下文和协议头；8 轮，约 360 KiB 首轮历史，第 5 轮由客户端替换为压缩摘要 | 通过 |
| `TestNativeCLIOriginalClientContract/direct` | 原版客户端直接连接模拟 API，顺序读取 12 个随机文本块，提交全部原始 token，并校验最终成功回复 | 通过 |
| `TestNativeCLIOriginalClientContract/through_agent` | 原版客户端经 Agent 的真实 CLI 连接同一模拟 API，执行同样任务；主请求 14 轮均带 safeguards，14 轮均收到模拟 `available` | 通过 |
| `TestNativeCLIOriginalClientContract/gateway` | 原版客户端通过回环 Gateway 观察代理连接模拟 API，验证 `/v1` 基础路径拼接及流式转发；14 轮模拟判定均可用 | 通过 |

前者同时在 Agent 隔离账户目录放入测试 `CLAUDE.md`，确认其内容与 CLI 占位输入均不进入原生出站文档。`model` 使用选定的真实 ID，内部 `stream` 为 `true`；其余调用方推理字段按 JSON 语义相等比较。PDF 内容只验证字节传输，不代表真实 PDF 理解。

长工具链中的上游文本、工具选择与判定均由模拟 API 控制；两端 CLI 是真实程序。这证明传输、客户端工具循环及测试入口可运行，不证明真实模型质量、真实安全判定或自动压缩效果。

## 客户电脑验证真实远端 Gateway

客户机持有 Gateway 调用方 Key，订阅账户在远端 Agent，使用此入口，不要求本机 `claude.ai` 登录：

```bash
AISW_CLAUDE_GATEWAY_TEST=1 go test ./service/aiServeWeaveAgent/claudecode -run '^TestLiveSafeguardsGateway$' -v -count=1 -timeout=10m
```

端点、Key、模型从客户端环境或当前 Claude 配置目录的 `settings.json.env` 读取；只读取必要字段，不修改配置、不打印或复制订阅 token。可显式设置 `AISW_CLAUDE_GATEWAY_URL` 和 `AISW_CLAUDE_MODEL`。本机回环观察代理保留正文、认证头和 beta/version 选择器，并即时转发 SSE，仅统计固定状态。工具任务与下述直连场景相同。

本次客户配置端点为 `api.open7x.com`，模型为 `claude-opus-5-5`；远端订阅登录由 Agent 负责。本机 `auth status` 的认证类型不能代表远端 Agent 状态。

本次该真实入口已通过：顺序读取 12 个块、提交全部原始 token、CLI 最终成功且回复精确匹配。耗时约 93 秒，观察到 16 个非空 safeguards 请求，15 轮收到真实 `available`。其余 1 个请求未被计为 `available`，该次运行未记录其具体分类，不能将此结果描述为每个 HTTP 请求均有可用判定。测试随后补充了固定状态分布统计，便于后续区分缺失、不可用和未知状态。

此项使用现有线上 Gateway 和远端 Agent，不代表本次修改已部署，也不代表已完成 Agent 主机上的直连基线或自动压缩验收。

## Agent 主机上的真实订阅直连对照入口

```bash
claude auth status --json
AISW_CLAUDE_LIVE_TEST=1 AISW_CLAUDE_MODEL=claude-opus-5-5 go test ./service/aiServeWeaveAgent/claudecode -run '^TestLiveSafeguardsDirectComparison$' -v -count=1 -timeout=20m
```

同一模型、同一随机任务、相同的受控工具先直连，再经本机 Agent。每次最多 8 分钟；校验 12 个块的顺序、全部原始 token、工具提交及 CLI 最终成功结果。经 Agent 的场景还要求至少 12 轮原生请求和真实 `available`，并报告固定状态分布。只使用回环 MCP，不读写项目、不执行 shell，不修改用户配置。

本次首先在客户机尝试该直连入口。过滤环境后本机 CLI 报告 `loggedIn: true`、`authMethod: oauth_token`，所以测试在前置认证检查失败，没有提交直连推理。这只说明客户机不适合作为 Agent 主机基线，不能据此认定远端 Agent 缺少订阅登录。没有放宽认证、导出 token 或用假接口替代真实订阅验收。

直连对照尚待在实际 Agent 主机的订阅系统账户执行，记录 CLI/模型版本、两个场景是否完成、工具轮数、判定状态和耗时。此测试通过也只说明受控任务功能相当，不作为通用模型智力结论。

## 生产和实际编程任务验收

生产 open7x.com 已完成上述受控长工具链；真实模型自动压缩、编程效果、Agent 主机直连对照和本次修改部署后的结果仍需单独记录。部署本次 Agent 后：

1. 客户端启用 auto 与服务端检查、允许配套 beta，新建会话。确认 Agent 日志显示 `message_path=native_safeguards`；逐轮检查 `safeguard_status`，不能只看客户端开关。
2. 选择相同真实模型，在两个独立临时项目副本使用相同任务、文件、工具、权限和思考设置。至少覆盖跨文件修复、连续工具结果、长对话追问，以及实际发生一次上下文压缩后的继续执行。
3. 用项目测试、构建结果和明确验收项比较结果，分别记录通过率、错误、轮数及耗时；不要求自然语言逐字相同。重复任务以区分模型随机性与稳定的链路差异。
4. 记录真实 Gateway 路径与版本，避免将本机回环实验记为生产验收。只保存去敏后的结果与固定分类，不保存客户提示词、工具输出或 Key。

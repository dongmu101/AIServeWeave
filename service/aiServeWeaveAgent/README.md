# Agent

## 本地 CLI 账户额度

正常运行的 Agent 在本地配置页面（`-config-ui-addr`）的「推理后端」中显示「CLI 账户额度」。只查询本地配置中已声明的 `kind: codex` / `kind: claude`；同一种 CLI 的多个运行时共用同一系统账户额度，只显示一份。仅配置模式尚未启动运行时，不执行账户查询。

- Codex 使用原生 app-server 的 `account/rateLimits/read`，读取计量桶的主/次窗口、已用比例、窗口时长与重置时间；多桶响应优先于兼容单桶响应。上游未报告重置时间的窗口无法判断何时过期，不显示，其余窗口照常展示。
- Claude Code 使用原生 stream-json 控制请求 `get_usage`（`skip_behaviors: true`），读取 5 小时、7 天及可用的 Sonnet/Opus 周窗口。该接口是 CLI 的实验能力，旧版本或账户权限可能不提供；可缺失窗口不显示，所有窗口都缺失时显示暂不可用，不推算额度。
- 剩余比例为 `max(0, 100 - 已用比例)`，不是可用 token 数或余额金额；平台租户配额、每次推理 token 与 CLI 共享账户的订阅窗口相互独立。
- 页面渲染只读缓存，异步查询每分钟最多一批、两种 CLI 并发执行，共享 15 秒超时。查询不随发起它的页面请求取消，刷新或关闭页面不会把本轮记为失败。点击刷新也遵守一分钟缓存。查询时间和重置时间以 UTC 显示；超过两分钟标记旧数据，窗口到重置时刻后隐藏比例，等待重新查询。
- 未登录、未提供额度、查询失败分别显示提示。失败会清除上一轮额度，避免把旧结果当作当前余量。查询不提交提示词、不执行模型推理，不读取或导出 token、钥匙串秘密；仍由 CLI 自行管理登录，原始输出和错误不进入页面或日志。

Agent 必须与 `codex login` / `claude auth login` 使用相同系统账户和账户目录；页面只监听回环地址。离线验证：

```bash
go test ./service/aiServeWeaveAgent/cliusage ./service/aiServeWeaveAgent/codexcli ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveAgent/configui
```

上游参考：[Codex app-server 账户额度](https://learn.chatgpt.com/docs/app-server#6-rate-limits-chatgpt)、[Claude Code 订阅窗口说明](https://code.claude.com/docs/en/statusline#rate-limit-usage)。真实账户查询与离线解析测试应分别验收，离线通过不代表本机已登录或已获取额度。

## Gemini CLI 图片生成

在本地声明 `kind: gemini-cli`，通过 Gemini CLI 官方 Nano Banana 扩展提供 `gemini-image` 图片模型，复用 Gateway 的 `/v1/images/generations` 与既有图像流隧道。该后端仅支持单张文生图，直接调用扩展的固定 MCP 工具；凭据只从 Agent 本机环境读取。现有 `kind: gemini` 的托管聊天用途不变。

安装、YAML、API 示例、隔离规则与实机验收边界见 [geminicli/README.md](geminicli/README.md)。

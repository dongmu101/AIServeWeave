# Agent

## 本地 CLI 账户额度

正常运行的 Agent 在本地配置页面（`-config-ui-addr`）的「推理后端」中显示「CLI 账户额度」。只查询本地配置中已声明的 `kind: codex` / `kind: claude`，每个运行时一行（名称后附运行时 ID）；声明了不同 `account_dir` 的运行时各自查询自己的账户（见[同一 CLI 的多个账户](#同一-cli-的多个账户)）。仅配置模式尚未启动运行时，不执行账户查询。

- Codex 使用原生 app-server 的 `account/rateLimits/read`，读取计量桶的主/次窗口、已用比例、窗口时长与重置时间；多桶响应优先于兼容单桶响应。上游未报告重置时间的窗口无法判断何时过期，不显示，其余窗口照常展示。
- Claude Code 使用原生 stream-json 控制请求 `get_usage`（`skip_behaviors: true`），读取 5 小时、7 天及可用的 Sonnet/Opus 周窗口。该接口是 CLI 的实验能力，旧版本或账户权限可能不提供；可缺失窗口不显示，所有窗口都缺失时显示暂不可用，不推算额度。
- 剩余比例为 `max(0, 100 - 已用比例)`，不是可用 token 数或余额金额；平台租户配额、每次推理 token 与 CLI 共享账户的订阅窗口相互独立。
- 页面渲染只读缓存，异步查询每分钟最多一批、各运行时并发执行，共享 15 秒超时。查询不随发起它的页面请求取消，刷新或关闭页面不会把本轮记为失败。点击刷新也遵守一分钟缓存。查询时间和重置时间以 UTC 显示；超过两分钟标记旧数据，窗口到重置时刻后隐藏比例，等待重新查询。
- 未登录、未提供额度、查询失败分别显示提示。失败会清除上一轮额度，避免把旧结果当作当前余量。查询不提交提示词、不执行模型推理，不读取或导出 token、钥匙串秘密；仍由 CLI 自行管理登录，原始输出和错误不进入页面或日志。

Agent 必须与 `codex login` / `claude auth login` 使用相同系统账户和账户目录（声明了 `account_dir` 时即该目录）；页面只监听回环地址。隧道上报的额度带运行时 ID，控制面租户模型页按运行时 ID 精确匹配；来自旧版 Agent、不带运行时 ID 的条目按 kind 匹配。离线验证：

```bash
go test ./service/aiServeWeaveAgent/cliusage ./service/aiServeWeaveAgent/codexcli ./service/aiServeWeaveAgent/claudecode ./service/aiServeWeaveAgent/configui
```

上游参考：[Codex app-server 账户额度](https://learn.chatgpt.com/docs/app-server#6-rate-limits-chatgpt)、[Claude Code 订阅窗口说明](https://code.claude.com/docs/en/statusline#rate-limit-usage)。真实账户查询与离线解析测试应分别验收，离线通过不代表本机已登录或已获取额度。

## 同一 CLI 的多个账户

`kind: codex` / `kind: claude` 的运行时可以声明 `account_dir`，让同一台机器以多个订阅账户对外服务。Agent 启动该运行时的 CLI 进程时，把它设为 `CODEX_HOME`（Codex）或 `CLAUDE_CONFIG_DIR`（Claude Code），登录、会话与额度都随目录隔离：

```yaml
runtimes:
  - {id: codex-a, kind: codex}                               # 系统默认登录
  - {id: codex-b, kind: codex, account_dir: ~/.codex-b}      # 第二个账户
  - {id: claude-b, kind: claude, account_dir: ~/.claude-b}
```

- 每个目录先用对应 CLI 登录一次：`CODEX_HOME=~/.codex-b codex login`、`CLAUDE_CONFIG_DIR=~/.claude-b claude auth login`。Agent 不代为登录，也不读取目录里的凭据。
- 路径必须是绝对路径；`~/` 开头会按 Agent 进程的家目录展开。只有 codex 与 claude 两种接受该字段，其他种类声明它会在启动或重新加载时被拒绝。
- `account_dir` 只在本地生效：Gateway 下发的配置既不能设置也不能改动它，注册表总是取本地声明里的值。
- 本地设置页面的推理后端条目中，codex / claude 多出「账户目录」输入框，留空即使用系统默认登录。
- 与其他运行时一样，新 ID 需要加入运行时白名单（显式配置了白名单时）才会被 Gateway 调度。

## 运行时热重载

修改 `-config` 文件的 `runtimes:` 列表后，向 Agent 发送 `SIGHUP`（`kill -HUP <pid>`），或在本地设置页面点「保存」，Agent 会不重启地调和运行时：新增的加入、改动的替换、删掉的移除，CLI 额度监视器同步更新。页面保存后会提示重新加载成功或失败（详情见 Agent 日志）。

- 文件无法读取、存在重复 ID 或任一运行时配置非法时，本次重新加载整体放弃，运行中的运行时不受影响；越过这一关后各运行时独立调和，个别失败只保留该运行时原状。
- 只调和文件声明的运行时；`-ollama-url` 声明的、自动发现的与 Gateway 下发的实例不受影响。
- Gateway 端点、证书、运行时白名单、指标与日志等其余设置仍需重启。重新加载出白名单外的运行时 ID 时，日志会给出警告。
- 没有 `-config` 文件（也没有设置页面的配置路径）时，`SIGHUP` 只记录一条日志，不做任何改动。

## 额度耗尽与限流

本地 CLI 账户的订阅额度用完时，Agent 把失败报告为可重试的 `rate_limited`，并在隧道错误的 `retry_after` 中带上额度恢复所需的时长，Gateway 据此把这个运行时搁置到恢复为止，同一模型的请求转去其他账户或节点：

- Codex：回合以 `usageLimitExceeded` / `rateLimitExceeded` 结束（`turn/completed` 或 `willRetry=false` 的 `error` 通知）时，Agent 趁 app-server 尚未退出，再以 `account/rateLimits/read` 读取窗口，取所有已用满（≥100%）窗口中最晚的重置时刻。`willRetry=true` 的 `error` 通知由 Codex 自行重试，不视为失败。
- Claude Code：stream-json 中 `rate_limit_event` 的 `rate_limit_info.status` 为 `rejected` 时记下 `resetsAt`，随后的错误结果或缺失结果即判为额度耗尽。
- OpenAI 兼容的 HTTP 后端返回 429 时，`Retry-After` 的秒数形式同样透传（日期形式与超过 24 小时的值视为未知）。
- 重置时间未知时 `retry_after` 留空，由 Gateway 使用默认冷却时长。上述事件格式按 CLI 当前版本实现，尚未在真实账户耗尽场景下验收。

## Gemini CLI 图片生成

在本地声明 `kind: gemini-cli`，通过 Gemini CLI 官方 Nano Banana 扩展提供 `gemini-image` 图片模型，复用 Gateway 的 `/v1/images/generations` 与既有图像流隧道。该后端仅支持单张文生图，直接调用扩展的固定 MCP 工具；凭据只从 Agent 本机环境读取。现有 `kind: gemini` 的托管聊天用途不变。

安装、YAML、API 示例、隔离规则与实机验收边界见 [geminicli/README.md](geminicli/README.md)。

## 预编译独立部署

Ubuntu systemd、Apple Silicon macOS LaunchAgent、Windows 11 原生用户计划任务的安装与维护见 [Agent 部署指南](../../deploy/agent/README.md)。包含固定版本在线/本地安装、状态/停止/重载、显式升级/回退与保留身份卸载。目标系统实机验收状态另列，不能由构建通过推定。

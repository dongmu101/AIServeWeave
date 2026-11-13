# Claude Code 2.1.323 兼容复验记录

日期：2026-11-13。源码基线 `e377efd`。范围：核对 Claude Code [`2.1.323`](https://github.com/anthropics/claude-code/blob/v2.1.323/CHANGELOG.md) 的变更是否影响 Agent（`main.go` 与 `claudecode/`）。这次没有修改 Agent 代码，也没有部署。

## 结论

- Agent 代码不需要修改。`main.go` 不拼接 CLI 参数，CLI 调用都在 `claudecode/`，其参数、事件解析与错误分类在 `2.1.323` 下行为不变。
- 全部 7 组 CLI 契约门控测试在 `2.1.323` 下通过，结果与同机 `2.1.322` 一致。
- 只验证了离线契约，真实订阅长任务、Agent 主机实机及生产链路都没有验收。

## 变更逐条核对

Agent 的 CLI 进程固定使用 `--print`、`stream-json`、`--restricted`、空 `--tools`、严格空 MCP、`--setting-sources ""`、`disableAllHooks`、`--no-session-persistence`；原生回合追加 `--disallowedTools "*"`，带 `safeguards` 时使用 `--permission-mode auto`，并经每进程回环代理设置 `HTTPS_PROXY` 与 `NODE_EXTRA_CA_CERTS`。按这些用法核对 changelog 中相关条目：

| 2.1.323 变更 | 对 Agent 的影响 |
| --- | --- |
| `--permission-mode auto` 在 auto 不可用时改为 Manual 模式启动 | 无。safeguards 回合虽传 `auto`，但原生进程已禁用全部本机工具，工具由调用方执行，Agent 也不校验 init 中的权限模式 |
| `--settings` 文件无效且含 `sandbox` 键时拒绝启动；沙盒设置校验失败时 `-p` 停止启动 | 无。Agent 生成的设置只含 `disableAllHooks`、`autoMemoryEnabled` 及原生请求的 `env`，没有 `sandbox` 键 |
| hook 长时间运行时 `-p` / SDK 每 30 秒发送进度心跳 | 无。所有 hook 已关闭，不会产生该事件；未知顶层事件仍按协议失败处理 |
| `--no-session-persistence` 会话退出时删除长工具结果 | 有利，无需改动 |
| 组织设置与策略请求不再使用只由 `ALL_PROXY` 设置的代理 | 无。Agent 给 CLI 设置的是 `HTTPS_PROXY` / `HTTP_PROXY` |
| TLS 检查代理、托管代理与 CA 设置相关修复 | 无。回环代理只解密 `api.anthropic.com`，CA 经 `NODE_EXTRA_CA_CERTS` 注入，契约测试覆盖 |
| 不带流式的请求在可恢复错误上不再直接失败 | 无。Agent 始终使用流式 |
| `-p` 在设置屏蔽所请求模型时于 stderr 警告 | 无。Agent 不设置模型白名单 |
| 新增实验性 Code Mode | 无。需显式开启，Agent 使用空 `--tools` |

其余条目涉及交互界面、插件、Claude in Chrome、云端会话与 IDE，与 Agent 的无头进程无关。

## 离线契约（真实 CLI + 回环模拟上游，不消耗额度）

在仓库根目录、本机已安装待验 CLI 的主机上执行：

```sh
AISW_CLAUDE_PATH=~/.local/share/claude/versions/2.1.323 AISW_CLAUDE_CONTRACT_TEST=1 \
  go test ./service/aiServeWeaveAgent/claudecode -run '^TestNativeCLI' -count=1 -timeout=300s -v
```

`2.1.323` 与 `2.1.322` 各运行一次，均为 `ok`（约 58 秒），测试日志中的 CLI 版本分别为 `2.1.323`、`2.1.322`：

| 测试 | 覆盖内容 | 结果 |
| --- | --- | --- |
| `TestNativeCLIDocumentContract` | PDF 等原生文档传输 | 通过 |
| `TestNativeCLICompactionContract` | 阈值压缩与按需压缩 | 通过 |
| `TestNativeCLIRejectionContract` | `context_management`、思考类型、`prompt_too_long`、被拒 beta 的固定分类 | 通过 |
| `TestNativeCLIHistorySizeContract` | 128 KiB～31 MiB 历史、4 MiB safeguards、转义密集请求 | 通过 |
| `TestNativeCLICallerToolIsolationContract` | `Bash`、`Read`、`Write`、`ToolSearch`、未来名称与 MCP 名称不进入本机执行 | 通过 |
| `TestNativeCLIOriginalClientContract` | 原版客户端直连、经 Agent、经 Gateway 各 12 段工具链；经 Agent 与经 Gateway 各 14 回合判定均为 `available` | 通过 |
| `TestNativeCLISafeguardsContract` | 8 轮原生契约、大历史、调用方压缩、工具 ID、思考、缓存与协议选择器 | 通过 |

运行环境屏蔽了外部网络，CLI 访问 `api.anthropic.com` 的旁路请求均被拒绝，测试仍全部通过，说明门控测试不依赖外网。

## 执行注意

- 门控测试使用执行主机上的 CLI。如果 `go test` 被转到其他主机执行，测得的是那台主机的 CLI 版本，不是待验版本。本次曾误在一台装有旧版 `2.1.296` 的主机上执行，`TestNativeCLIRejectionContract/prompt` 因此失败；换回本机 `2.1.322` / `2.1.323` 后通过，这不属于 `2.1.323` 的回归。核对方法是看 `TestNativeCLISafeguardsContract` 日志中的 `CLI version`。
- `claude` 是 shell 函数或别名时，`go test` 的 `exec.LookPath` 找不到它，所有用例报 `Claude CLI is unavailable`。此时用 `AISW_CLAUDE_PATH` 指向实际可执行文件。

## 待跟进

- Agent 主机升级 CLI 后，在该主机上用其实际的 Go 与 `claude` 复跑上述门控测试。
- 真实订阅长任务（`AISW_CLAUDE_LIVE_TEST=1`）与生产链路在 `2.1.323` 下未验收。

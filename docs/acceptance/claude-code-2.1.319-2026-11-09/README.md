# Claude Code 2.1.319 兼容验收记录

日期：2026-11-09。范围：Claude Code [`2.1.319`](https://github.com/anthropics/claude-code/blob/v2.1.319/CHANGELOG.md) 中与 auto mode 服务端安全检查相关的变化，对本项目 Agent（`claudecode/`）和经 Gateway 接入的客户端有什么影响。这次没有修改 Agent 代码，也没有部署到生产。

## 结论

- Agent 代码不需要修改。现有的离线契约测试在 `2.1.319` 下全部通过。
- 需要更新的是客户端的环境变量说明，已同步到[运行时说明](../../../service/aiServeWeaveAgent/claudecode/README.md#claude-code-auto-服务端安全检查)。
- 真实订阅下，经 Agent 跑长工具链的验收没有通过。原因是上游对该任务给出 cyber `refusal`，客户端随后改用回退模型重试，被单模型的回环入口拒绝。换成 `2.1.318` 失败方式完全相同，所以不是 `2.1.319` 引入的回归。详见下文「真实订阅」。

## 离线契约（真实 CLI + 回环模拟上游，不消耗额度）

```sh
AISW_CLAUDE_CONTRACT_TEST=1 go test -count=1 -v \
  -run 'TestNativeCLISafeguardsContract|TestNativeCLIOriginalClientContract' ./service/aiServeWeaveAgent/claudecode/
```

两项均通过：8 轮原生契约（大历史、调用方压缩、工具 ID、思考、缓存、协议选择器）；原版客户端在直连、经 Agent、经 Gateway 三条链路上各完成 12 段工具链，经 Agent 的 14 个回合全部为 `available`。

## 2.1.319 行为实测（临时测试，未入库）

客户端是本机未修改的 `2.1.319`，以 `--print --permission-mode auto` 运行，带 `DISABLE_TELEMETRY=1`，`ANTHROPIC_BASE_URL` 指向回环模拟上游或回环 Agent。模拟上游对一个 MCP 工具返回 `tool_use`，并按场景返回不同的 `safeguard_results`。

| 场景 | 客户端发送 safeguards / `dangerous-tool-use` beta | 工具是否执行 | 退出码 |
|---|---|---|---|
| 判定 `available`，`AUTO_MODE_SERVER=1` 或未设置 | 是 | 执行 | 0 |
| 判定缺失或 `unavailable`，`=1` 或未设置 | 是 | 拒绝，不退回内置分类器 | `stream-json` 0；`json`/`text` 1 |
| 判定缺失，`=0` | 否；另发 2 次内置分类器请求 | 由内置分类器决定 | 0 |
| `=1` + `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1` | 是 | 执行 | 0 |
| 只设 `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1` | 否；退回内置分类器 | 由内置分类器决定 | 0 |
| 经 Agent，判定为 `available` / `unavailable` / 缺失 | 是 | 分别为执行 / 拒绝 / 拒绝 | 0；bridge 均返回 200 |

判定缺失时，`json`/`text` 输出下客户端的 stderr 会写明「requests go through <网关>, which doesn't pass it through」，同时提示可以改用其他权限模式。Agent 内部的 CLI 使用 `stream-json`，并在工具回合主动停止，因此 changelog 中「`claude -p` 被拒时退出码改为 1」这一条不影响 Agent。

## 真实订阅

```sh
AISW_CLAUDE_LIVE_TEST=1 go test -count=1 -v -run TestLiveSafeguardsDirectComparison ./service/aiServeWeaveAgent/claudecode/
```

- `direct` 通过：`claude-opus-5-5`，12 段工具链，耗时 54 秒。
- `through_agent` 失败，可以稳定复现。临时诊断抓取了客户端与回环 Agent 之间的请求和响应：
  1. 前两轮由真实上游返回 `stop_reason: "refusal"`，`stop_details.category: "cyber"`，同时附带 `safeguard_results` 为 `available`。
  2. 客户端插入合成提示「Your response above was stopped by a safety classifier…」，随后改用 `claude-opus-4-8`（`max_tokens` 64000）重试，部分重试请求不带 `safeguards`。
  3. 测试使用的回环入口（`messagesBridge.ServeHTTP`）只接受配置的那一个模型，因此对这些重试一律返回 400。客户端最终以退出码 1 结束，工具一次都没有调用。
- 用 `2.1.318`（`AISW_CLAUDE_PATH=~/.local/share/claude/versions/2.1.318`）复跑，结果完全相同。
- 使用较短提示词的两轮工具任务，经 Agent 在真实上游完成；加上自定义 `--system-prompt` 后同样完成。

## 待跟进

- 同一个任务，直连时上游不拒绝，经 Agent 时稳定拒绝。上游实际收到的两份请求有什么差别，这次没有抓取对照，需要借助原生路径的上游代理去比对。
- 上游 refusal 后客户端会换回退模型重试。生产 Gateway 按模型选择路由，如果没有可服务 `claude-opus-4-8` 的节点，这类回合会直接失败。这一点只根据本次回环实验推断，生产环境尚未验证。
- 经 Gateway 的真实链路（`TestLiveSafeguardsGateway`），以及 `2.1.319` 客户端连接旧版 Agent/Gateway 时被拒绝的场景，都还没有实测。

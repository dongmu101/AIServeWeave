# Codex CLI 实验记录（2026-09-30）

## 范围与环境

macOS arm64，Go 1.27.1，`codex-cli 0.159.2`，本机 `codex login status` 为 ChatGPT 登录。未指定 `-model`，实际线程返回本机默认 `gpt-6-astra`。每条线程采用独立进程和临时工作目录，保留现有 Codex 账户目录的通用指令，来源数量为 1。

这次证明的是 **Codex app-server 与宿主之间的合成动态工具往返**。原版客户端、Gateway Responses 前门、Agent 运行时注册及隧道端到端尚未接通；报告的 `responses_api_compatibility` 均为 `not_tested`。

## 真实验证

```bash
go build -o /private/tmp/aisw-codex-bridge-probe ./experiments/codex-cli-bridge
/private/tmp/aisw-codex-bridge-probe -live -codex /Users/sky/.nvm/versions/node/v22.23.3/bin/codex -sessions 1 -timeout 90s
/private/tmp/aisw-codex-bridge-probe -live -codex /Users/sky/.nvm/versions/node/v22.23.3/bin/codex -sessions 2 -timeout 90s
```

| 运行 | 会话 | RPC 帧数 | 实际工具调用 | 随机值被消费 | turn 终态 | 退出码 |
| --- | --- | --- | --- | --- | --- | --- |
| 单会话 | 1 | 56 | 2 | 是 | completed | 0 |
| 双会话 | 1 | 51 | 2 | 是 | completed | 0 |
| 双会话 | 2 | 53 | 2 | 是 | completed | 0 |

随机值由每个实验宿主独立生成，只通过第二次工具结果返回，不包含在初始提示词中。两条同时运行的会话通过独立 stdio 通道接收自己的值。这不等于已经验证 Gateway 租户 Key 隔离。

## 定位过程与配置约束

1. 最初的“零指令文件”假设与本机 CLI 不符，线程创建报告了一份账户通用指令。实现改为只接受现有 Codex 账户目录中的 `AGENTS.md` / `AGENTS.override.md`，拒绝项目及未知来源，报告中保留数量。
2. 最初禁用了 `code_mode_host`。模型能够完成回合，但连续试验都没有工具回调，因此验证器正确返回失败，没有把普通回复当作成功。
3. 命名空间形式的动态工具在本机版本被 `thread/start` 拒绝，未采用。移除显式空 `environments` 覆盖后，单独关闭 `code_mode_host` 仍然得到 0 次工具调用。
4. 保留 `code_mode_host` 默认工具宿主后，同一模型的两次动态工具回调成功。Shell、插件、Hook 等具体执行能力仍设置为禁用；读取到不允许的 item 或审批请求仍会终止实验。

因此当前版本不能用“关闭工具宿主”替代“关闭具体工具”。本机生成 schema 和官方文档只能用于选择和核对协议；某字段存在不代表已在当前版本及配置下实际可用。

## 离线测试与审查

已覆盖真实假子进程的初始化、认证失败、RPC 错误、跨线程/turn、重复调用、意外工具、未知 item、错误结果、超大/畸形输出、取消及指令来源检查。测试不访问模型服务。

运行中取消测试先等待父 app-server 和其后代各自通过 Unix socket 握手，再取消并验证两条连接 EOF 和 `context.Canceled`；没有用 sleep 推进时间。

Go 审查发现本机 schema 的子代理 item 是 `collabAgentToolCall`，原来的拒绝列表拼写漏拦。现使用明确的允许列表，未知类型也拒绝；新增回归先失败后通过。Go 复核与独立代码审查未发现其他明确阻断缺陷。

实验包与命令的单测、race 和 vet 已通过。完整仓库门禁结果在本次实施计划中记录；本文件不将旧的全仓测试记录当作本次通过证明。

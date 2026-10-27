# Codex CLI 桥接实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在 Agent 下增加 Codex CLI 的隔离 app-server 验证入口，作为与 Claude 并列的后端接入前置工作。

**Architecture:** Agent 的 `codexcli` 包以 stdio 管理本机 `codex app-server`；通过官方动态工具 RPC 让宿主返回工具结果。独立实验命令验证本机订阅下的两轮工具往返和双会话隔离。生产 Gateway 的 Responses 兼容和运行时注册仍需另行完成，当前不虚报支持。

**Tech Stack:** Go 1.27 标准库，Codex CLI 0.159.2 的本机生成 schema，不增加 SDK/第三方依赖。

**Spec:** 延续 `docs/superpowers/specs/2026-09-30-claude-code-cli-bridge-design.md` 的凭据、工具执行位置及验收原则。本次改用 Codex 官方 app-server 的 `initialize`、`account/read`、`thread/start`、`turn/start`、`item/tool/call`，不依赖 Claude 的 JSON 事件形状。

## Global Constraints

- 用户已要求增加 Codex 接入，沿用当前会话内实现方式，不重复请求开始授权。
- 用户本机当前 `codex login status` 为 ChatGPT 登录；不读取/复制 auth.json，不改 CODEX_HOME。
- 使用临时工作目录、ephemeral thread、只读 sandbox，禁用 shell、MCP/插件/Hook 自动加载等非实验工具；意外工具或授权请求直接失败。
- 保留现有账户目录中的通用 AGENTS 指令并报告数量；项目和未知来源一律拒绝。保留默认 code_mode_host 工具宿主，不能关闭它来替代关闭具体执行工具。
- 输入输出均有界：单条 1 MiB、总计最多 10000 帧；每个实验会话只允许两个指定的合成工具调用；进程超时最多两分钟。
- 默认测试全部离线，不使用真实 sleep；live 显式启用；错误不含模型文本或凭据。
- 本次未指定模型，默认保留本机 Codex 的模型配置，显式 `-model` 才覆盖；报告记录 thread/start 返回的模型名。

## Review Focus

- RPC ID/线程/turn 混淆、重复工具调用必须失败，不接受其他会话结果。
- 认证失败、RPC error、失败 turn、输出截断和超限不能变成成功。
- 异常工具或审批请求不能自动放行。
- 取消、正常返回、解析失败均回收 app-server 进程组。
- 真实测试结果只能证明合成动态工具往返，不代表 Responses API 兼容或 Gateway 已接通。

## Task 1：Agent 原生 app-server 驱动

Files: `service/aiServeWeaveAgent/codexcli/{probe.go,protocol.go,process_unix.go,process_other.go,probe_test.go,main_test.go}`。

Interfaces: `Probe(ctx context.Context, cfg Config) (Report, error)`；Config 只含本机 Executable、Model 和 Timeout。宿主合成工具固定为 `aisw_probe_echo`，不是通用命令代理。

- [x] 用假 app-server 子进程先写成功往返、认证失败、RPC error、跨线程/turn、重复调用、意外原生工具、超大消息、失败 turn、退出和取消测试，观察 RED。
- [x] 实现有界 stdio 协议状态机；只返回非敏感报告，不保存模型输出。
- [x] 运行包测试及 race，通过后才能进入 live。

## Task 2：独立验证命令和真实结果

Files: `experiments/codex-cli-bridge/{main.go,main_test.go,README.md}`；Agent README 新增 Codex 实验入口；增加实验结果文档。

- [x] 默认只显示帮助；`-live` 才调用 Probe；会话数限 1–2，超时限两分钟。
- [x] 本机运行一次顺序两轮及一次双会话实验，记录实际模型、帧数、工具次数和随机结果校验，不记录正文。
- [ ] Go 与独立代码审查，处理实质缺陷；执行仓库门禁，保留基线失败证据，失败时不提交。

## 依据与执行记录

- [官方 app-server 文档](https://learn.chatgpt.com/docs/app-server)：动态工具要求 experimentalApi，工具结果由客户端回传。
- [官方配置说明](https://learn.chatgpt.com/docs/config-file/config-reference)：用于核实工具、Hook、项目指令和沙箱开关。
- 本机 `codex app-server generate-json-schema --experimental` 输出存于临时目录，仅作核对，不作为仓库生成契约或依赖提交。
- `thread/resume.history` 的本机 schema 标记为 FOR CODEX CLOUD / DO NOT USE，因此不使用它伪造兼容历史；完整 Gateway 接入需另验证公开 `thread/inject_items` 与客户端 Responses 历史语义。
- Ruling: 本机原生账户通用指令属于操作者的 CLI 配置，只允许该目录中两种已知 AGENTS 文件；不接受项目或未知来源。报告记录数量，不读取或输出内容。
- Ruling: 本机命名空间动态工具请求被拒绝，保留已实际验证的裸函数声明。禁用 code_mode_host 会使动态工具不可见，恢复默认宿主后单/双会话均通过，详见实验 RESULTS.md。
- 审查：item 允许列表修复子代理类型漏拦；加入父子进程握手后的取消回归；Go 和独立审查复核通过。
- 本次门禁：`go test ./...`、`go build ./...`、`go vet ./...`、两个新增包的 race 通过；gofmt 无输出；临时 protoc 35.0 生成结果与仓库一致。未重跑前次已知失败的全服务 race，因此不声称全门禁通过，也未创建本次代码提交。

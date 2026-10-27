# Claude CLI 桥接验证器实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付可重复运行、默认离线的验证器，以证据决定是否继续生产接入。

**Architecture:** 独立 Go 命令启动受认证的回环 MCP 端点，CLI 只获得一个合成工具；工具通过有界 broker 交给模拟调用方返回结果。CLI 输出逐行检查，只输出不含 Prompt、凭据或模型正文的报告。验证器不注册生产 runtime。

**Tech Stack:** Go 1.27 标准库、现有 Claude CLI，零新增模块依赖。

**Spec:** `docs/superpowers/specs/2026-09-30-claude-code-cli-bridge-design.md`。

## Global Constraints

- 用户已明确要求开始实现，本计划在当前会话内直接执行；不再次询问执行方式。
- 保持当前工作区 `dev`，新增实验目录与文档，不改变服务代码；开始时工作区干净。
- 默认测试不依赖登录和网络；真实 CLI 只由显式 `-live` 开关启用。
- 最大两个会话，每会话最多八个待决工具，JSON 事件最多 1 MiB；超过预算明确失败。
- CLI 凭据由其自身读取，不能抽取凭据、改钥匙串或自动登录；不能使用不读订阅的 `--bare`。
- 工具内容只使用合成测试数据；不加载仓库项目，不执行调用方命令或文件操作。
- 注释双语；不以 `time.Sleep` 推进测试；泄漏检查集中在 TestMain。

## Review Focus

- 其他身份及重复工具结果：拒绝，不能第二次推进。
- 待决工具取消/超限：有界退出并释放额度。
- 本地 MCP 被浏览器或未认证进程请求：Origin/Host/token 检查先于分派。
- CLI 输出、stderr 及畸形 JSON：不泄漏正文，超限失败，不因 `is_error` 或非零退出误报通过。
- CLI 退出或超时：回收整个进程组与 HTTP 服务；成功文本不能代替工具回合证据。

## Task 1：有界工具 broker

Files: `experiments/claude-code-bridge/broker.go`、`broker_test.go`、`main_test.go`。

Interfaces: `newBroker(owner principal) *broker`；`call(ctx, name, args) (toolResult, error)`；`resolve(owner, id, result) error`；`close()`；`calls` 只在本进程内消费。

- [x] 先写往返、身份拒绝、重复结果、八个待决上限、取消和关闭测试，观察缺少实现导致失败。
- [x] 实现有界 map/channel 与一次性投递；统一常量错误，不回显输入。
- [x] `go test ./experiments/claude-code-bridge -run TestBroker -count=1` 通过。

## Task 2：受控 MCP 与 CLI 输出检查

Files: `mcp.go`、`mcp_test.go`、`events.go`、`events_test.go`。

Interfaces: `mcpHandler(broker, token, host) http.Handler`；`inspectEvents(io.Reader, expected string) (eventSummary, error)`。

- [x] MCP 请求用 httptest Recorder 离线验证初始化、工具列表、工具往返、认证、Origin、Host、方法、消息尺寸与非法 JSON。
- [x] 输出 fixture 验证工具事件与回合边界、成功 result、失败 result、无 result、畸形/超大事件；不存正文。
- [x] 实现后运行对应测试并确认通过。

## Task 3：可执行 live 驱动及报告

Files: `main.go`、`driver.go`、`client.go`、`process.go`、`process_unix.go`、`process_other.go`、对应测试、`live_test.go`、`README.md`、`RESULTS.md`。

Interfaces: `runCLI(ctx, executable, args, dir, env, stdin, expected) (eventSummary, error)`；`runProbe(ctx, program, model, session, scenario) (probeReport, error)`；命令 `go run ./experiments/claude-code-bridge -live -claude /absolute/path -model sonnet`。

- [x] 假子进程测试退出码、取消、超大输出、stderr 隐藏；参数测试证明关闭内建工具、严格 MCP、隔离工作目录。
- [x] 默认执行只打印使用说明；live 启动前检查 `auth status --json`，未登录输出固定错误并退出。
- [x] MCP 配置文件置于 0700 临时目录，以 0600 写入；不把 token 放 CLI 参数或报告中。
- [x] 真实运行只报告协议事件计数和通过/失败，不输出原始事件；结果不等同 V01–V09 全部通过。
- [ ] 默认套件、race、vet、build 及仓库要求的质量门禁通过。
- [x] 尝试 live；缺少认证则如实记录阻塞，不能自动改为 API Key 或将模拟通过算真实通过。
- [x] 完成独立 Go/安全代码审查，修复实质问题，同步设计状态和 Agent README。

## 执行记录与裁决

- 2026-09-30：直接执行 CLI 的 `auth status --json` 返回 `loggedIn=false, authMethod=none`；只提取四项非敏感字段，未读取凭据。已询问用户实测目标机器，离线实现继续。
- Ruling: 本次先实现能独立证明 CLI/MCP 工具往返的基础验证器；完整 Messages 仿真、跨请求续接与原版客户端 V08 以这个前置实验成功为门槛 — 当前无可用登录，直接设计完整协议会把未验证假设写成生产功能 — 代价是本次若认证仍不可用，整体接入仍未完成。
- Ruling: 验证器的回环 MCP 服务是实验进程内部通信，不对外提供推理入口；固定回环地址、每次随机认证、拒绝 Origin — 不改变 Agent 出站建连约束。
- 2026-09-30：正常本机权限下 CLI 报告已登录 Max；沙箱的未登录是执行权限差异。真实顺序两轮、两个隔离会话及同回合并行工具均通过，详见 `experiments/claude-code-bridge/RESULTS.md`。
- Ruling: 参考 DBX 固定提交 `9192af28aefb434fb90d27dad78831961ce0f1e8` 的 CLI/MCP 模式；不采用其将完整历史拼成文本且省略旧工具调用的回放方式 — 那不能满足原版客户端代理的协议要求。
- 审查修复：具名 buffer 阻止 ReaderFrom 绕过认证输出上限；父 CLI 正常/非零退出都清理其进程组；均有先失败后通过的回归。并行顺序字段限定为 broker 投递顺序。
- 质量门禁：普通全量测试、构建、vet、实验 race 和匹配 protoc 的生成一致性通过。服务全量 race 出现基线隧道排空失败（HEAD 临时副本也复现）及控制面测试进程被系统终止；该复选项保留未完成，不提交代码或声称全绿。

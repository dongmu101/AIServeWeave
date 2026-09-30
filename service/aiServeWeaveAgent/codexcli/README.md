# Codex CLI 接入验证

`codexcli.Probe` 管理本机已登录的 `codex app-server`，通过 stdio JSON-RPC 验证动态工具调用与结果回传。它是 Agent 的实验接入包，尚未实现 `runtime.InferenceRuntime`，也没有注册到 `main.go` 的后端工厂；当前不能用 `runtimes.kind: codex` 启用共享推理。

目标与 Claude 接入相同：登录凭据留在 Agent，客户端工具在各用户自己的电脑执行。当前工具结果由本进程内的合成调用方提供，完整 Gateway Responses 前门与隧道往返尚未接入。

## 程序化接口

```go
report, err := codexcli.Probe(ctx, codexcli.Config{
    Executable: "/absolute/path/to/codex",
    Timeout:    90 * time.Second,
})
```

`Model` 留空保留本机 CLI 模型配置，显式设置才覆盖；实际模型记录在 `Report.Model`。超时默认 90 秒、最多两分钟。macOS/Linux 支持进程组回收，其他平台当前返回 `ErrProcess`。

运行入口及真实验证记录见 [独立实验命令](../../../experiments/codex-cli-bridge/README.md)。

## 协议与隔离边界

- 使用 `initialize` → `account/read` → `thread/start` → `turn/start`；开启 `experimentalApi` 后接收 `item/tool/call` 并按请求 ID 返回结果。
- 只接受 ChatGPT 登录，不读取、复制或转发 `auth.json`/token；保留原有认证目录环境变量，过滤 API Key 与网关地址覆盖。
- 每次独立进程、0700 临时工作目录、ephemeral thread、只读 sandbox；关闭 shell、MCP/插件/Hook 自动加载等非实验能力。收到未允许的 item 或审批请求即失败，不自动批准。
- 保留本机 Codex 账户目录中原生的 `AGENTS.md` / `AGENTS.override.md`，并报告来源数量；项目和其他来源的指令一律拒绝。账户通用指令会参与实验推理，这不等于空白账户环境。
- 每帧最多 1 MiB、最多 10000 帧，每会话仅允许两个指定工具调用。错误仅暴露固定类别；报告不含模型正文、指令内容、文件路径或凭据。
- 核对线程、turn、工具名、参数与调用 ID；重复或跨会话调用失败。CLI 终态、两个实际回调以及调用方随机结果均满足时才报告通过。
- 取消和任何退出路径都清理 app-server 进程组。默认测试使用假子进程，不访问模型服务。

这些约束针对实验适配层，不构成原版 Codex 客户端或完整 Responses API 的兼容声明。`dynamicTools` 是实验接口，CLI 更新后必须重新验证；不能仅凭 schema 出现某字段就假定该版本已实现对应能力。

## 官方依据

- [Codex app-server](https://learn.chatgpt.com/docs/app-server)
- [Codex 配置说明](https://learn.chatgpt.com/docs/config-file/config-reference)

实现核对了本机 `codex-cli 0.159.2` 通过 `generate-json-schema --experimental` 生成的协议。没有采用标记为内部用途的 `thread/resume.history` 拼接历史。

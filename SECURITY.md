# 安全策略

## 报告漏洞

**不要**通过公开 GitHub issue 报告安全漏洞——这会在修复发布前把细节暴露给潜在攻击者。

请使用 GitHub 的 Private Vulnerability Reporting 私密提交：

1. 打开仓库 [github.com/dongmu101/AIServeWeave](https://github.com/dongmu101/AIServeWeave) 的 **Security** 标签页。
2. 点击 **Report a vulnerability**，按提示填写。

报告中请尽量包含：

- 漏洞描述与影响范围（涉及哪个服务：Agent / Gateway / Registry / ControlPlane / Console）。
- 复现步骤或概念验证（PoC）。
- 已知的触发条件（例如需要特定配置、特定权限）。

## 响应流程

维护者会在收到报告后尽快确认收悉，并评估影响与修复优先级。修复发布后会在变更记录中说明，具体披露时间视漏洞严重程度与修复复杂度而定，会与报告者协商后再公开细节。

## 安全设计

安全能力应从第一版开始建设：

- 用户 API Key 只保存不可逆哈希
- Agent 使用短期注册令牌换取节点证书
- Agent 与 Registry、Gateway 和 Tunnel 服务之间使用 mTLS
- 推理后端密钥加密保存
- 用户、租户、模型和节点权限隔离
- 所有管理操作写入审计日志
- 限制 Agent 可以访问和代理的目标地址
- 校验后端 URL，防止 SSRF
- 限制请求体大小、上下文长度、超时和并发数
- 校验 ComfyUI 工作流节点和公开输入，禁止未经授权的任意节点执行
- 隔离 ComfyUI 自定义节点及其外部网络访问权限
- 对上传文件执行类型、大小、文件名和恶意内容检查
- 日志对 Authorization、Cookie 和密钥进行脱敏
- 默认不保存 prompt 和生成内容，只记录必要元数据

## 已知安全边界

项目的安全设计原则见 [AGENTS.md「安全红线」](AGENTS.md#安全红线)，包括：

- API Key、自定义鉴权头、完整 Prompt、工作流 JSON 不落日志。
- Agent 只主动出站建连，从不监听公网端口；不做通用 HTTP 代理。
- `runtime_id` 必须命中 Agent 本地白名单才执行。

当前已知的、经过评估的权衡（非漏洞，但影响攻击面）记录在各服务 README 与根 [STATUS.md](STATUS.md) 中，例如 Console 服务端持有未收窄的租户 Gateway API Key（见 Console STATUS 的 C26）。这类已知权衡不需要通过本渠道重复报告，但如果你发现了超出既有文档描述的新风险，欢迎按上述流程提交。

## 支持的版本

项目当前处于持续开发阶段，尚未有稳定版本发布（见 [STATUS.md](STATUS.md) 的里程碑规划）。安全修复只针对 `main` 分支的最新代码，不维护历史版本的回溯补丁。

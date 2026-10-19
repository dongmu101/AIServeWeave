# Agent

## Gemini CLI 图片生成

在本地声明 `kind: gemini-cli`，通过 Gemini CLI 官方 Nano Banana 扩展提供 `gemini-image` 图片模型，复用 Gateway 的 `/v1/images/generations` 与既有图像流隧道。该后端仅支持单张文生图，直接调用扩展的固定 MCP 工具；凭据只从 Agent 本机环境读取。现有 `kind: gemini` 的托管聊天用途不变。

安装、YAML、API 示例、隔离规则与实机验收边界见 [geminicli/README.md](geminicli/README.md)。

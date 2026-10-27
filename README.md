# AIServeWeave

简体中文 | [English](README.en.md)

把分散在 Mac、内网工作站和 GPU 服务器上的推理服务编织成一个统一入口：一套 OpenAI / Anthropic 兼容 API，背后自动调度到 Ollama、vLLM、ComfyUI 等任意节点。

节点不需要公网地址——Agent 主动出站，经 mTLS 隧道接入平台。

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/go-1.27-00ADD8.svg)

## 特性

- **兼容 API**：OpenAI Chat Completions / Responses（含工具循环）/ Embeddings / Images，Anthropic Messages，Ollama 原生 API，统一支持 SSE。
- **多种后端**：Ollama、vLLM、SGLang、TensorRT-LLM、TokenSpeed、ComfyUI；托管供应商（OpenAI、Claude、Gemini、DeepSeek、Qwen 等）；本机 CLI（Codex、Claude Code、Gemini CLI 等）。
- **NAT 穿透**：Agent 只出站、不监听端口；运行时白名单保证 Gateway 失守也无法让节点访问未声明地址。
- **调度与路由**：逻辑模型别名、节点标签、优先级与权重、健康过滤、熔断、首 token 前重试、前缀亲和。
- **ComfyUI 工作流**：受控模板、异步 Job、SSE 进度、取消、产物持久化到对象存储。
- **多租户**：API Key、配额与限流（可选 Redis 跨副本）、审计、可吊销会话。
- **管理控制台**：机群、模型路由版本发布与回滚、模板、Job 历史、指标曲线。

## 架构

```text
Console ──► ControlPlane ──► MySQL / Redis
                │  配置、Key 吊销、Job 历史
                ▼
Client ──► Gateway ──► 对象存储
  (OpenAI /    │
  Anthropic)   │ gRPC 双向流 + mTLS（Agent 主动出站）
               ▼
             Agent ──► Ollama / vLLM / ComfyUI / ...

Registry ◄── Agent 注册与证书续期、Gateway 副本名册
```

| 组件 | 职责 |
| --- | --- |
| [Gateway](service/aiServeWeaveGateway/README.md) | 兼容 API 前门、鉴权与配额、调度、隧道终结 |
| [Agent](service/aiServeWeaveAgent/README.md) | 部署在算力节点，发现本地后端并经隧道执行请求 |
| [Registry](service/aiServeWeaveRegistry/README.md) | 节点身份（一次性令牌换证书）与 Gateway 副本发现 |
| [ControlPlane](service/aiServeWeaveControlPlane/README.md) | 租户、用户、Key、配额、路由与模板发布、审计、Job 历史 |
| [Console](service/aiServeWeaveConsole/README.md) | Next.js 管理界面 |

完整设计见 [docs/architecture.md](docs/architecture.md)。

## 快速开始

需要 Docker Compose v2；本机运行 Agent 需要 Go 1.27。

```bash
cd deploy
cp .env.example .env    # 填写密钥与首个账户，见 deploy/README.md
docker compose up -d --build
```

打开 <http://127.0.0.1:3000> 登录并创建 API Key，然后按 [接一个 Agent](deploy/README.md#接一个-agent) 把本机 Ollama 接进来：

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $AISW_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "qwen3:8b", "messages": [{"role": "user", "content": "hello"}], "stream": true}'
```

任何 OpenAI / Anthropic SDK 把 Base URL 指向 `http://127.0.0.1:8080/v1` 即可使用。

## API

| 端点 | 说明 |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat，支持工具调用 |
| `POST /v1/responses` | OpenAI Responses，可接入 Codex CLI |
| `POST /v1/embeddings` · `GET /v1/models` | |
| `POST /v1/images/generations` | 映射到 ComfyUI 模板或 CLI 生图后端 |
| `POST /v1/messages` | Anthropic Messages（文本 + 图片） |
| `POST /api/chat` · `/api/generate` · `/api/embeddings` | Ollama 原生推理端点 |
| `POST /v1/workflows/{id}/runs` · `/v1/jobs/{id}/...` | 工作流 Job：提交、状态、事件、取消、产物 |

不支持的参数按名字拒绝，不静默丢弃。各端点的完整限制见 [Gateway STATUS](service/aiServeWeaveGateway/STATUS.md)。

## 文档

- [架构设计](docs/architecture.md)：协议兼容、ComfyUI、调度、数据模型、安全、可观测性
- [部署](deploy/README.md) · [数据库升级与恢复](deploy/database-recovery.md)
- [隧道协议](service/aiServeWeaveAgent/tunnel/README.md)
- [开发进度与路线图](STATUS.md) · [变更日志](CHANGELOG.md)

## 参与贡献

欢迎 issue 与 PR。请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 与 [AGENTS.md](AGENTS.md)（代码约定与质量门禁）；安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。

## 许可证

[Apache License 2.0](LICENSE)

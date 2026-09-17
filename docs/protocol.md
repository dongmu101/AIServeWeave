# 协议兼容与调度

本文描述外部协议如何收敛成内部统一请求、逻辑模型与实际部署的抽象关系，以及调度器的选择流程。包含尚未实现的目标能力，当前接口范围以 [Gateway README](../service/aiServeWeaveGateway/README.md) 为准。

## 协议兼容

外部协议只存在于系统边界。所有请求先转换成内部统一请求，再进入调度器；选定节点后，再由后端适配器转换成目标服务协议。

```text
OpenAI Request ────┐
Anthropic Request ─┼─► Protocol Adapter ─► Canonical Request
Ollama Request ────┘                         │
                                             ▼
                                         Scheduler
                                             │
                                             ▼
                                      Backend Adapter
                                 ┌────────┬──────────┐
                                 ▼        ▼          ▼
                               vLLM     Ollama     Other
```

内部请求可以抽象为：

```go
type InferRequest struct {
	RequestID string
	TenantID  string
	Model     string
	Messages  []Message
	Tools     []Tool
	Sampling  SamplingOptions
	Stream    bool
	Metadata  map[string]string
}
```

流式和非流式结果统一成事件：

```go
type InferEvent struct {
	Type  EventType
	Delta *ContentDelta
	Usage *Usage
	Error *InferError
}
```

已实现的基础兼容 API（参数与限制见 [Gateway README](../service/aiServeWeaveGateway/README.md)）：

- `POST /v1/chat/completions`（含 SSE）
- `POST /v1/responses`（含 SSE）
- `POST /v1/embeddings`
- `GET /v1/models`

规划中的扩展协议范围：

- Anthropic `POST /v1/messages`
- Ollama 原生 API
- 音频转录和翻译
- rerank
- OpenAI-compatible 图像生成

ComfyUI 工作流和异步任务 API 已实现，见 [ComfyUI 任务 API](comfyui.md#comfyui-任务-api)。

vLLM 已提供 Chat Completions、Responses、Embeddings 和音频等多种 OpenAI-compatible API；Ollama 也提供部分 OpenAI API 兼容能力。因此，第一版以 OpenAI 协议作为主要对外协议和后端协议，可以减少适配成本。

- [vLLM OpenAI-Compatible Server](https://docs.vllm.ai/en/latest/serving/openai_compatible_server/)
- [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)

兼容不代表所有参数和行为完全相同。AIServeWeave 必须维护每个实际部署的能力矩阵：

```text
DeploymentCapability
├── chat
├── responses
├── embeddings
├── image_generation
├── workflow_execution
├── vision
├── audio
├── video
├── tools
├── parallel_tool_calls
├── structured_output
├── reasoning
├── max_context_length
└── streaming
```

如果目标部署不支持请求所需能力，调度器应选择其他部署或返回明确错误，不能静默丢弃参数。

## 模型与部署抽象

客户端使用逻辑模型名，而不是直接指定节点上的真实模型：

```text
客户端请求：model = "qwen-coder"

qwen-coder
├── mac-mini-01 / Ollama / qwen3-coder:30b
├── gpu-server-01 / vLLM / Qwen/Qwen3-Coder
└── gpu-server-02 / vLLM / Qwen/Qwen3-Coder-FP8
```

核心对象：

| 对象 | 含义 |
| --- | --- |
| Node | Mac、工作站或 GPU 服务器 |
| Backend | 节点上的推理服务，如 Ollama、vLLM 或 ComfyUI |
| Deployment | 某个 Backend 中运行的实际模型实例 |
| Model | 暴露给客户端的逻辑模型 |
| Route | Model 到一个或多个 Deployment 的选择规则 |

这种抽象允许在不影响客户端的情况下更换底层模型、量化版本、节点或推理框架。

路由目标携带真实模型名、节点选择器、优先级与权重；管理配置的期望状态与节点上报的能力快照分别保存。持久化 Backend/Deployment 时，应明确其稳定身份和生命周期，不能直接把瞬时连接对象当作实体。

**节点标签是偏好，不是权限。** Agent 自报标签只用于调度筛选。租户是否可以访问某模型、模板或节点池，必须由受信任的授权配置决定。

## 调度流程

```text
接收请求
  → 验证租户、API Key 和配额
  → 解析逻辑模型
  → 找到候选部署
  → 过滤离线、维护和熔断节点
  → 过滤能力不匹配节点
  → 检查租户和节点访问权限
  → 按优先级、负载、延迟和成本评分
  → 选择目标部署
  → 转发请求
  → 记录结果和用量
```

调度策略的规划范围：

- 加权轮询
- 最少正在执行请求
- 节点优先级
- 节点标签路由，例如 `region=local`
- 租户绑定节点或节点池
- 会话亲和，以便复用 KV Cache
- 连续失败熔断和自动恢复

流式请求只有在返回第一个 token 之前可以安全重试。一旦已经向客户端发送内容，不应自动切换节点，否则可能产生重复或不连续的输出。

ComfyUI 的调度另有额外约束，见 [ComfyUI 调度](comfyui.md#comfyui-调度)。

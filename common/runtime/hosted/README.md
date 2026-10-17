# 托管供应商（OpenAI 兼容）

本包实现以下种类：

- `kind: openai`
- `kind: gemini`
- `kind: deepseek`
- `kind: kimi`
- `kind: qwen`
- `kind: zhipu`
- `kind: minimax`
- `kind: openai_compatible`

这些供应商都提供 OpenAI 兼容的 `chat/completions`，因此共用 `internal/oaibase` 的客户端、并发上限、能力门禁和流式转换。本包只补充每家供应商的差异。Claude 官方 API 走 Messages 协议，见 [anthropic](../anthropic/README.md)。

dbx 界面上的「Custom」对应 `openai_compatible`；如果端点是 Messages 协议，对应 `anthropic_compatible`。

## 端点

`base_url` 是带版本号的根路径，请求路径在它后面直接拼 `/models` 和 `/chat/completions`，不再额外加 `/v1`。留空时使用下表的官方端点：

| kind | 默认 base_url | 说明 |
| --- | --- | --- |
| `openai` | `https://api.openai.com/v1` | 输出上限以 `max_completion_tokens` 发送 |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta/openai` | 使用 Google 的 OpenAI 兼容端点（dbx 用的是原生 API）；模型 ID 去掉 `models/` 前缀 |
| `deepseek` | `https://api.deepseek.com/v1` | |
| `kimi` | `https://api.moonshot.cn/v1` | 国际站可改为 `https://api.moonshot.ai/v1` |
| `qwen` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | |
| `zhipu` | `https://open.bigmodel.cn/api/paas/v4` | 国际站可改为 `https://api.z.ai/api/paas/v4` |
| `minimax` | `https://api.minimaxi.com/v1` | 国际站可改为 `https://api.minimax.io/v1`；输出上限以 `max_completion_tokens` 发送 |
| `openai_compatible` | 无，必填 | |

Key 以 `Authorization: Bearer` 发送。

## 声明

在 Agent 本地配置中声明：

```yaml
runtimes:
  - id: openai
    kind: openai
    api_key_env: OPENAI_API_KEY      # 只存环境变量名
  - id: my-gateway
    kind: openai_compatible
    base_url: https://llm.example.com/v1
    api_key_env: MY_GATEWAY_KEY
    models: qwen3-coder glm-4.6      # 可选，空格或逗号分隔
```

### Key 只留在 Agent 本机

Key 只在 Agent 启动时从 `api_key_env` 指定的环境变量读取，不写进配置文件，也不过隧道。环境变量没有设置时 Key 为空：供应商会以 `unauthorized` 拒绝，Agent 本身照常启动。

这些 ID 只能在本地声明，Gateway 下发的配置不能新建它们。

Gateway 下发同一个 ID 时，`base_url` 归一化后必须与本地声明一致，否则拒绝创建。端点、Key、Headers 与 TLS 始终取本地值，所以即使 Gateway 被攻破，也无法把本机 Key 发到其他地址。

## 模型目录

模型目录由 `GET {base_url}/models` 读取。读取后：

- 保持供应商的顺序；
- 去掉重复项；
- 按 dbx 的规则去掉非聊天模型：
  - OpenAI：嵌入、图像、语音、审核、实时等；
  - Gemini：嵌入、图像、Live、TTS 等；
  - Qwen：语音、图像、OCR、重排等。

声明了 `models` 时，以声明为准。端点没有模型列表路由（404/405）时也接受声明，Probe 会以 `IdentityVerified=false` 标出这一点。认证失败（401/403）从不被掩盖。

列表为空时，Discover 会给出警告，提示声明 `models`。

## 能力

发布的能力：

- `chat`
- `chat_stream`
- `tools`

视觉与结构化输出取决于具体模型，在通过 `capability_overrides` 声明之前保持未知。嵌入、转录、重排同样只在运维声明后放行。

## 验证

本仓库只用 httptest 模拟的端点验证过请求形状，尚未用真实 Key 实机验收。

启用后：

1. 确认 Gateway `/v1/models` 出现预期的模型；
2. 用 `/v1/chat/completions` 分别发送一条非流式消息和一条流式消息。

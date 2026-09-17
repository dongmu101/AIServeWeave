
# AIServeWeave

AIServeWeave 是一个分布式 AI 推理节点管理平台，为本地 Mac、内网设备和 NVIDIA GPU 服务器上的推理服务提供统一接入、管理、调度和对外 API。

项目目标是构建一套“AI 推理控制平面 + 兼容 API 网关”，让应用只对接一个稳定入口，而底层可以运行 Ollama、vLLM、ComfyUI 或其他 AI 推理服务。

## 项目目标

- 集中管理本地设备、内网设备和 GPU 服务器等推理节点
- 统一接入 Ollama、vLLM、ComfyUI 和其他推理后端
- 管理 ComfyUI 工作流、异步生成任务及图片、视频、音频等产物
- 对外提供 OpenAI、Anthropic 等兼容 API
- 支持普通响应和 SSE 流式响应
- 根据模型能力、节点状态、负载和策略自动调度请求
- 支持 API Key、租户、配额、限流、审计和用量统计
- 让没有公网入口的节点通过 Agent 主动连接平台

## 当前能力与规划边界

当前实现支持 OpenAI Chat Completions、Responses（含 SSE，不支持 `store` / `previous_response_id`）、Embeddings、Models，以及经 Agent Tunnel 执行的受控工作流 Job API。控制面已提供租户、用户与平台运维生命周期、Redis 可吊销会话、API Key、审计、配额与 MySQL Job 历史，并以 generation 长轮询向 Gateway 推送 Key 吊销失效；Console 已接入这些管理页面及只读机群、模板目录、Job 取消与产物预览/下载。模型路由已支持控制面版本发布、Gateway 热切换/生效查询与 Console 编辑回滚（P02）；文件配置模式保留。接口限制以服务 README 为准。

[文档索引](#文档)中的架构与设计文档包含目标能力：Anthropic/Ollama 原生 API、Managed 部署、对象存储、资源采集、模板/部署配置发布与告警仍属规划；Direct 的可交付范围待 A04 核实。Registry 已有令牌管理与节点禁用，控制面/Console 已接入节点审批、禁用/启用、维护与平台运维会话（P01）。历史记录可查不保证文件在原节点离线或 Gateway 重启后仍可下载。

开发进度、优先级、依赖和验收统一见 [STATUS.md](STATUS.md)。

## 快速开始

用 Docker Compose 在一台机器上跑起整套服务：Console、依赖服务、Registry、控制面与 Gateway。需要 **Docker Engine 24+ 与 Compose v2**（`docker compose version` 能打印版本号），2 核 4G 内存足够试用，并且宿主机端口 `3000`、`8080`、`8443`、`9090`、`8090`、`9443`、`5432`、`6379` 空闲。

### 1. 启动

```bash
git clone https://github.com/dongmu101/AIServeWeave.git
cd AIServeWeave/deploy
cp .env.example .env

# 生成四个独立密钥并写回 .env
for var in AISW_ACCESS_SECRET AISW_INTERNAL_TOKEN AISW_BOOTSTRAP_TOKEN; do
  sed -i.bak "s#^${var}=#${var}=$(openssl rand -base64 32)#" .env
done
sed -i.bak "s#^AISW_CONSOLE_SESSION_SECRET=#AISW_CONSOLE_SESSION_SECRET=$(openssl rand -base64 48)#" .env
rm -f .env.bak

# 预置一个能登录 Console 的 owner 账号（密码只在这里打印一次，存好）
sed -i.bak "s#^AISW_DEFAULT_OWNER_EMAIL=#AISW_DEFAULT_OWNER_EMAIL=owner@example.com#" .env
sed -i.bak "s#^AISW_DEFAULT_OWNER_PASSWORD=#AISW_DEFAULT_OWNER_PASSWORD=$(openssl rand -base64 18)#" .env
rm -f .env.bak
grep '^AISW_DEFAULT_OWNER_' .env

docker compose up -d --build
```

首次构建三个 Go 服务镜像 + Console 镜像需要几分钟。`docker compose ps` 应显示八个服务，其中 `registry-init` 与 `bootstrap` 跑完退出为 `Exited (0)` 是预期状态。

起来之后：

| 端点 | 地址 |
| --- | --- |
| Console 管理控制台 | `http://127.0.0.1:3000` |
| OpenAI 兼容 API | `http://127.0.0.1:8080/v1/...` |
| 控制面 Admin API | `http://127.0.0.1:8090/admin/v1/...` |
| Gateway 指标 | `http://127.0.0.1:9090/metrics` |

全部只绑宿主机回环。要对外提供服务，前面必须先有一层带鉴权的入口。

### 2. 登录 Console

浏览器打开 `http://127.0.0.1:3000`，用上一步 `.env` 里那个 owner 邮箱和密码登录，即可管理用户、API Key 和配额。

### 3. 签发 API Key 并调一次

```bash
cd deploy

SESSION_TOKEN=$(curl -sS -X POST http://127.0.0.1:8090/admin/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"owner@example.com","password":"<上一步生成的密码>"}' | jq -r .token)

# 明文只在这一次响应里出现，之后任何接口都读不回它
API_KEY=$(curl -sS -X POST http://127.0.0.1:8090/admin/v1/apikeys \
  -H "Authorization: Bearer $SESSION_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"first-key","ttl_seconds":0}' | jq -r .key)

curl http://127.0.0.1:8080/v1/models -H "Authorization: Bearer $API_KEY"
```

还没有 Agent 连接时 `/v1/models` 返回 `{"object":"list","data":[]}`——空列表是预期结果，说明鉴权链路已经通了，只是还没有推理节点。

### 4. 接一台推理节点

在有 Ollama、vLLM 或 ComfyUI 的机器上运行 Agent，它会主动出站连接平台，不需要公网入口。完整步骤见 [deploy/README.md「接一个 Agent」](deploy/README.md#接一个-agent)。

**完整的部署说明、证书链、两份控制面配置、常见问题与已知限制见 [deploy/README.md](deploy/README.md)。**

## 文档

| 文档 | 内容 |
| --- | --- |
| [deploy/README.md](deploy/README.md) | 部署与运维：Compose 编排、证书链、Console 配置、接 Agent、常见问题 |
| [docs/architecture.md](docs/architecture.md) | 架构设计：控制面/数据面/节点面分层、四个服务的职责、代码结构、核心业务链路 |
| [docs/protocol.md](docs/protocol.md) | 协议兼容与调度：外部协议收敛、能力矩阵、逻辑模型与部署抽象、调度流程 |
| [docs/comfyui.md](docs/comfyui.md) | ComfyUI 接入与部署：External/Managed 两级接入、工作流模板、Job API、文件与产物 |
| [docs/data-model.md](docs/data-model.md) | 逻辑数据模型与实体关系 |
| [docs/observability.md](docs/observability.md) | 指标、追踪与标签基数约束 |
| [SECURITY.md](SECURITY.md) | 安全设计原则、已知安全边界与漏洞报告流程 |
| [STATUS.md](STATUS.md) | 开发里程碑、任务顺序、未完成项与验收记录 |
| [AGENTS.md](AGENTS.md) | 开发约定：依赖边界、编码规范、质量门禁 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 贡献流程 |

各服务的接口细节与运行限制见服务自己的 README：
[Gateway](service/aiServeWeaveGateway/README.md)、
[ControlPlane](service/aiServeWeaveControlPlane/README.md)、
[Registry](service/aiServeWeaveRegistry/README.md)、
[Agent](service/aiServeWeaveAgent/README.md)（[隧道协议](service/aiServeWeaveAgent/tunnel/README.md)）、
[Console](service/aiServeWeaveConsole/README.md)。

## 许可

见 [LICENSE](LICENSE)。

# Docker Compose 部署

本目录部署单机 AIServeWeave：MySQL、Redis、Registry、ControlPlane、Gateway 与 Console。
Agent 在推理节点宿主机上运行，连接本机 Ollama / vLLM / ComfyUI。

下文默认保留 `.env.example` 中的端口。除明确标注外，命令均在仓库的 `deploy` 目录执行。

启用 `ComfyEditor.DistDir` 的部署，须先在仓库根目录使用
`scripts/fetch-comfyui-frontend.sh --localized <空目录>` 构建对应八语言制品，
再将目录挂载给控制面并升级服务。工具链与校验规则见
[本地化构建说明](../scripts/comfyui-localization/README.md)。已配置目录缺少
`aisw-i18n-manifest.json`、配置版本或逐文件摘要不符时，控制面启动失败；
不要将原始 release zip 与本地化制品混用。未配置 `DistDir` 时编辑器继续关闭。

## 环境要求

- Docker Engine 24+、Docker Compose v2 或更新版本，使用 `docker compose` 命令。
- 首次构建可访问镜像仓库、Go 模块源、npm registry 与 Google Fonts。
- 宿主机安装 `openssl`、`curl`；从源码运行 Agent 时需要 Go 1.27。
- 下表端口空闲；冲突时在 `.env` 中设置对应变量。

| 用途 | 默认端口 | 覆盖变量 |
| --- | --- | --- |
| Console | 3000 | `CONSOLE_PORT` |
| Gateway API / 隧道 / 指标 | 8080 / 8443 / 9090 | `GATEWAY_PORT` / `GATEWAY_TUNNEL_PORT` / `GATEWAY_METRICS_PORT` |
| ControlPlane | 8090 | `CONTROLPLANE_PORT` |
| Registry / 指标 | 9443 / 9091 | `REGISTRY_PORT` / `REGISTRY_METRICS_PORT` |
| MySQL / Redis | 3306 / 6379 | `MYSQL_PORT` / `REDIS_PORT` |

## 起步

### 1. 创建配置

首次部署在仓库根目录执行；已有 `.env` 时直接编辑原文件，不覆盖已有密钥：

```bash
cd deploy
umask 077
test -e .env || cp .env.example .env
chmod 600 .env
```

执行以下命令填充八个独立密钥的空值，已有值保持不变：

```bash
for var in AISW_ACCESS_SECRET AISW_INTERNAL_TOKEN AISW_BOOTSTRAP_TOKEN AISW_REGISTRY_ADMIN_TOKEN AISW_GATEWAY_ADMIN_TOKEN AISW_GATEWAY_MODEL_PULL_TOKEN AISW_GATEWAY_COMFYUI_MANAGED_TOKEN; do
  sed -i.bak "s#^${var}=\$#${var}=$(openssl rand -base64 32)#" .env
done
sed -i.bak "s#^AISW_CONSOLE_SESSION_SECRET=\$#AISW_CONSOLE_SESSION_SECRET=$(openssl rand -base64 48)#" .env
rm -f .env.bak
```

编辑 `.env`，填写首次登录账户和独立的数据库密码：

```dotenv
AISW_DEFAULT_TENANT_NAME=Default
AISW_DEFAULT_OWNER_EMAIL=owner@example.com
AISW_DEFAULT_OWNER_PASSWORD=<随机密码>
MYSQL_PASSWORD=<独立随机密码>
MYSQL_ROOT_PASSWORD=<另一独立随机密码>
```

每个密码可分别用 `openssl rand -hex 24` 生成，替换上面的占位符并保存到密码管理器。
当前 bootstrap 手工拼接 JSON，自动建号使用的名称和密码应避免双引号、反斜杠与换行。

需要管理机群时，再填写独立的平台运维账户：

```dotenv
AISW_DEFAULT_OPERATOR_EMAIL=ops@example.com
AISW_DEFAULT_OPERATOR_PASSWORD=<独立随机密码>
AISW_DEFAULT_OPERATOR_NAME=Admin
```

租户账户与平台运维账户各自登录。对应邮箱留空则跳过建号；再次执行 bootstrap
不会修改已有账户或重置密码。数据库密码变量只在新数据卷初始化时创建数据库账户，
已有数据库改密需同步修改数据库内的账户密码与 `.env`。

### 2. 构建与启动

```bash
docker compose config --quiet
docker compose up -d --build
docker compose ps --all
docker compose logs --tail=50 bootstrap
```

`mysql`、`redis`、`console` 应最终显示 `healthy`；`registry`、`controlplane`、`gateway`
应持续运行。`registry-init` 与 `bootstrap` 成功结束后显示 `Exited (0)`，属于正常状态。
`bootstrap` 日志确认建号成功后，再登录 Console。

Compose 的 `service_started` 只保证容器已启动，Console 健康检查也只检查登录页；
两者都不证明控制面业务可用。启动后通过实际登录与下面的 API 请求验证。
参见 [Compose 启动顺序](https://docs.docker.com/compose/how-tos/startup-order/)。

### 3. 登录与验证 API

打开 `http://127.0.0.1:3000`，使用上面配置的租户邮箱和密码登录，在 API Key 页面创建
并保存 Key。明文仅在创建时显示一次。

在终端将 `AISW_API_KEY` 设置为刚创建的 Key，再执行：

```bash
curl --fail-with-body --connect-timeout 5 --max-time 15 \
  http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer $AISW_API_KEY"
unset AISW_API_KEY
```

没有 Agent 时模型列表为空。接入带模型的 Agent 后再次查询，应能看到模型目录。

租户控制台的“AI 服务与模型”（`/console/models`）展示 **Agent 连接上来后上报的 AI 服务和模型**，
包括 Agent 在线/离线状态、服务类型、健康状态、后端版本、模型 ID 与能力；不展示 Gateway
本机发现结果。租户页面无需平台运维登录，详细内部地址仅在运维页面展示。

平台运维 `/operator/models` 还保留独立的 Gateway 本机发现区域。Compose 默认通过
`host.docker.internal:11434` 和 `host.docker.internal:8000` 读取宿主机 Ollama/vLLM 清单，
无需 Agent 即可显示服务地址、健康状态、版本与模型；这份发现清单不会直接加入推理调度。
可在 `deploy/.env` 设置 `GATEWAY_LOCAL_OLLAMA_URL` / `GATEWAY_LOCAL_VLLM_URL` 为容器
可达地址，设为空字符串跳过该类型。Docker Desktop 提供 `host.docker.internal`；其他
Docker 环境需自行配置该名称或覆盖地址。通过 Gateway API 调用模型仍按下一节接入 Agent。

## 接一个 Agent

以下示例适用于 Agent 与 Compose 位于同一台机器，且本机 Ollama 已在 `11434` 端口运行。
每个节点使用不同的 `node-id`，示例中的 `local-ollama-01` 需按实际节点修改。

先在 `deploy` 目录执行，为该节点签发有效十五分钟的一次性绑定令牌并复制公共 CA：

```bash
umask 077
mkdir -p ../data/agent
docker compose exec -T registry /usr/local/bin/service \
  -data-dir=/data -admin-token-file=/data/admin-token \
  -registry-addr=127.0.0.1:9443 \
  -mint-token -bind-node-id=local-ollama-01 > ../data/agent/bootstrap-token
docker compose cp registry:/data/ca/ca-cert.pem ../data/agent/ca.pem
```

绑定令牌代表运维对该节点的授权，可直接注册；未绑定令牌需要先审批节点。
随后切回仓库根目录运行 Agent：

```bash
cd ..
go run ./service/aiServeWeaveAgent \
  -node-id=local-ollama-01 \
  -registry=127.0.0.1:9443 -ca-file=./data/agent/ca.pem \
  -bootstrap-token-file=./data/agent/bootstrap-token \
  -cert-file=./data/agent/cert.pem -key-file=./data/agent/key.pem \
  -gateway=127.0.0.1:8443 \
  -ollama-url=http://127.0.0.1:11434 \
  -metrics-addr=127.0.0.1:9092 \
  -labels=region=local
```

Agent 指标使用 `9092`，避免与宿主机上的 Registry 指标端口冲突。注册成功后令牌文件由
Agent 删除；重启沿用节点证书和私钥。长期运行的本地 YAML 配置见
[Agent README](../service/aiServeWeaveAgent/README.md)。

## 日常维护

返回 `deploy` 目录后执行以下命令：

| 操作 | 命令 |
| --- | --- |
| 查看全部容器与一次性任务 | `docker compose ps --all` |
| 查看某服务日志 | `docker compose logs --tail=100 <service>` |
| 修改 `.env` 后应用配置 | `docker compose up -d` |
| 修改挂载的 `controlplane.yaml` 后加载 | `docker compose restart controlplane` |
| 重建单个业务服务 | `docker compose up -d --no-deps --build <service>` |
| 重跑账户初始化 | `docker compose up -d --no-deps --force-recreate bootstrap` |
| 停止并保留容器与数据 | `docker compose stop -t 45` |
| 移除容器并保留数据卷 | `docker compose down -t 45` |

Compose 会检测服务配置或镜像变化并重建容器，`restart` 不会重新读取 `.env`；
只有需要强制重建时才加 `--force-recreate`。
参见 [docker compose up](https://docs.docker.com/reference/cli/docker/compose/up/)。

数据库、Redis 会话状态和 Registry 身份材料分别持久化在 `mysql-data`、`redis-data`、
`registry-data`。长期部署固定目录及 Compose 项目名，避免换目录后连接到新的空数据卷。
升级前备份配置、数据库与 Registry 身份材料；数据库升级与恢复步骤见
[数据库升级与恢复](database-recovery.md)。不要将删除数据卷作为常规故障处理方式。

### 证书链是怎么闭合的

Registry 首次生成 CA；`registry-init` 用同一个 CA 为 Gateway 签发服务端证书。
Agent 用公共 `ca-cert.pem` 验证 Registry 与 Gateway，Gateway 用该 CA 验证节点证书。

### 服务端证书有效期与旧部署升级

Registry 默认自签证书和 Gateway 服务端证书最长有效十年，且不超过 CA 的剩余有效期。
CA 首次生成时有效十年；Agent 节点证书有效三十天并自动续期。

已有的三十天服务端证书需重新签发。在维护窗口执行以下命令，保留原 CA：

```bash
docker compose build registry registry-init gateway
docker compose up -d --no-deps --force-recreate registry
docker compose run --rm --no-deps registry-init
docker compose up -d --no-deps --force-recreate -t 45 gateway
```

Gateway 重建会断开现有隧道，Agent 自动重连，无需重新注册。
自行提供的证书文件按签发方的有效期更新。证书细节见
[Registry STATUS](../service/aiServeWeaveRegistry/STATUS.md)。

## 对外部署与已知限制

- 默认所有宿主机端口只绑定 `127.0.0.1`。Console 和 Gateway HTTP API 对外服务时通过
  HTTPS 反向代理接入；设置 `AISW_CONSOLE_COOKIE_SECURE=true`，并让代理保留外部 `Host`。
- 远程 Agent 还需调整 Registry 与隧道端口的宿主机绑定、证书 SAN 和
  `GATEWAY_ADVERTISE_ADDR`。仅修改通告地址不能使回环端口对远程节点可达；
  修改 `GATEWAY_TUNNEL_PORT` 时也需同步通告地址中的端口。
- 当前为单 Registry、单 Gateway 编排。多副本方案见对应服务 README。
- 默认数据库为 MySQL。`postgres` profile 只额外启动 PostgreSQL，不切换控制面数据源；
  Job 持久化目前仅支持 MySQL。
- `controlplane.yaml` 是容器配置。长期部署在独立迁移完成后关闭 `Database.AutoMigrate`，
  按 [数据库升级与恢复](database-recovery.md) 操作。
- Redis 是会话的权威状态，已启用 AOF 和数据卷；不可用时管理请求返回 `503`。
  不要恢复早于吊销或改密操作的 Redis 快照。

历史指标为可选项：在 `controlplane.yaml` 添加以下配置，再重启控制面。
Fleet、模型拉取和 ComfyUI Managed 转发已在默认配置中接好。

```yaml
MetricsHistory:
  GatewayAddrs: ["http://gateway:9090"]
  RegistryAddr: "http://registry:9091"
```

## 常见问题

| 现象 | 处理 |
| --- | --- |
| `config --quiet` 提示 `set it in deploy/.env` | 补齐八个密钥；旧部署保留已有值，只补缺失项 |
| 端口已被占用 | 修改 `.env` 对应端口后执行 `docker compose up -d` |
| `bootstrap` 为 `Exited (1)` | 查看其日志；检查邮箱、密码和控制面状态，修正后单独重跑 |
| Console 可打开但登录失败 | 查看 `controlplane` 日志及 `bootstrap` 创建结果；确认使用对应账户的登录入口 |
| Registry 数据目录权限错误 | 核对卷挂载与属主，镜像运行 UID 为 `10001`；先备份，再修复目录权限 |
| Agent 注册失败 | 检查绑定令牌的节点 ID、有效期和 CA；令牌已用或过期时重新签发 |
| Agent 连接失败或证书主机名不匹配 | 同时检查端口绑定、通告地址及证书 SAN |

### 第一个租户与第一个 key

主流程使用 bootstrap 建号、Console 创建 Key。选择手动建号时，留空相应
`AISW_DEFAULT_*_EMAIL`，在 `deploy` 目录执行下面的租户创建请求。
示例适用于本指南生成的 `.env`；自行编辑的值需使用兼容 shell 的引号格式。

```bash
source .env
curl --fail-with-body --connect-timeout 5 --max-time 15 \
  -X POST http://127.0.0.1:8090/admin/v1/tenants \
  -H "Authorization: Bearer $AISW_BOOTSTRAP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Default","owner_email":"owner@example.com","owner_password":"replace-with-a-random-password"}'
```

手动创建平台运维使用 `POST /admin/v1/platform/operators`，同样携带 bootstrap token，
JSON 字段为 `email`、`password`、`name`。其余接口见
[ControlPlane STATUS](../service/aiServeWeaveControlPlane/STATUS.md)。

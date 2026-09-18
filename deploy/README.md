# deploy

用 Docker Compose 把一套 AIServeWeave 跑起来：Console、依赖服务、Registry、控制面与 Gateway。
本文档按「从零到能在 Console 里登录、能用 API Key 调通一次推理」的顺序写，每一步给出
可复制粘贴的命令，以及命令没有按预期工作时该往哪看。

## 环境要求

- **Docker Engine 24+ 与 Compose v2**（老式的 `docker-compose`（带连字符）分支不支持
  `service_completed_successfully` 依赖条件，会在 `registry-init` 那一步失败或忽略依赖顺序）。
- **能访问镜像仓库与包源**：首次 `--build` 要拉取 `mysql`/`redis` 镜像、下载 Go 与
  Node.js 依赖，离线环境需先自建镜像或配置代理/镜像源。
- **下列宿主机端口空闲**：`3000`（Console）、`8080`/`8443`/`9090`（Gateway）、`8090`
  （控制面）、`9443`（Registry）、`3306`（MySQL）、`6379`（Redis）。改端口见
  `.env.example` 里的 `*_PORT` 变量，不用改 `docker-compose.yaml`。
- 一台机器跑全部服务，2 核 4G 内存足够试用；生产规模见下方「已知限制」。

## 起步

```bash
cd deploy
cp .env.example .env

# 生成八个独立密钥并写回 .env
for var in AISW_ACCESS_SECRET AISW_INTERNAL_TOKEN AISW_BOOTSTRAP_TOKEN AISW_REGISTRY_ADMIN_TOKEN AISW_GATEWAY_ADMIN_TOKEN AISW_GATEWAY_MODEL_PULL_TOKEN AISW_GATEWAY_COMFYUI_MANAGED_TOKEN; do
  sed -i.bak "s#^${var}=#${var}=$(openssl rand -base64 32)#" .env
done
sed -i.bak "s#^AISW_CONSOLE_SESSION_SECRET=#AISW_CONSOLE_SESSION_SECRET=$(openssl rand -base64 48)#" .env
rm -f .env.bak

# 可选：填了这两行，起来后就有一个能登录 Console 的账号，不用再手动建租户
# ——见下面「第一个租户与第一个 key」。邮箱按自己的改；密码随机生成，记得存好，
# 这条命令不会再打印第二次。
sed -i.bak "s#^AISW_DEFAULT_OWNER_EMAIL=#AISW_DEFAULT_OWNER_EMAIL=owner@example.com#" .env
sed -i.bak "s#^AISW_DEFAULT_OWNER_PASSWORD=#AISW_DEFAULT_OWNER_PASSWORD=$(openssl rand -base64 18)#" .env
rm -f .env.bak
grep '^AISW_DEFAULT_OWNER_' .env

docker compose up -d --build
```

首次构建需要几分钟，之后 `up -d` 只重建改过的部分。**先做下一节的检查再用
Console**——`controlplane` 没有 healthcheck，Compose 显示「已启动」不代表已能接受请求。

### 确认起来了

```bash
docker compose ps
```

默认启动的八个服务（`mysql`、`redis`、`registry-init`、`registry`、`controlplane`、
`bootstrap`、`console`、`gateway`）都应处于 `running`/`healthy`；`registry-init` 与
`bootstrap` 跑完显示 `Exited (0)` 是正常的，`postgres` 需要 `--profile postgres` 才会启动。
任何服务反复重启，先看日志：

```bash
docker compose logs --tail=100 <service>
```

再确认控制面已经能接受请求（只要能连上就会返回，不代表凭据正确）：

```bash
until curl -s -o /dev/null http://127.0.0.1:8090/admin/v1/auth/login; do
  echo "等待 controlplane..."; sleep 1
done
echo "controlplane 已就绪"
```

起来之后：

| 端点 | 地址 |
| --- | --- |
| Console 管理控制台 | `http://127.0.0.1:3000` |
| OpenAI 兼容 API | `http://127.0.0.1:8080/v1/...` |
| 控制面 Admin API | `http://127.0.0.1:8090/admin/v1/...` |
| Gateway 指标 | `http://127.0.0.1:9090/metrics` |
| Registry 指标（P08） | `http://127.0.0.1:9091/metrics` |
| Registry（Agent 用） | `127.0.0.1:9443` |
| Gateway 隧道（Agent 用） | `127.0.0.1:8443` |

全部只绑宿主机回环。要对外提供服务，前面必须先有一层带鉴权的入口。

**启用历史指标采集（P08，可选）。** 默认不采集。要让控制面为 Console
`/operator/metrics` 采集历史时序，在 `controlplane.yaml` 里加：

```yaml
MetricsHistory:
  GatewayAddrs: ["http://gateway:9090"]
  RegistryAddr: "http://registry:9091"
```

字段与默认值见 [控制面 README「指标与历史监控」](../service/aiServeWeaveControlPlane/README.md#指标与历史监控p08)。

### Redis 会话持久化（P05）

控制面会话以 Redis 为权威状态，不是可选缓存：Redis 不可用时管理请求返回 `503`，不会
退回只验 JWT。Compose 已配置具名卷 `redis-data` 与 AOF 持久化。丢失 Redis 数据会安全地
让所有人重新登录；不要恢复早于安全操作的旧快照。生产集群的复制、故障切换与备份 RPO
仍须按 Redis 拓扑自行验证。

## Console

`console` 默认随整套服务启动，使用独立的 Node.js 24 多阶段镜像。首次构建需要访问镜像
仓库、npm registry 和 Google Fonts。

从已有部署升级时，在 `deploy/.env` 中新增独立的 `AISW_CONSOLE_SESSION_SECRET`（至少
32 字符，不要复用控制面的密钥）。所有 Console 副本需用相同密钥；更换会使已有登录
Cookie 失效。

| 配置 | 默认值 / 要求 |
| --- | --- |
| `AISW_CONSOLE_SESSION_SECRET` | 必填，独立随机密钥，未配置时 Compose 拒绝启动 |
| `CONSOLE_PORT` | 宿主机端口，默认 `3000`，只绑定 `127.0.0.1` |
| `AISW_CONSOLE_COOKIE_SECURE` | 本地 HTTP 默认 `false`；HTTPS 部署设为 `true` |
| `AISW_CONSOLE_CONTROL_PLANE_URL` | Compose 内固定为 `http://controlplane:8090`，不受宿主机 `CONTROLPLANE_PORT` 影响 |

浏览器访问 Console，由其服务端转发 Admin API 请求，不直连控制面。按下文创建首个租户和
owner 后用该邮箱密码登录；Compose 不提供默认账号，也不会自动创建租户。

仅重建 Console（在 `deploy` 目录执行）：

```bash
docker compose up -d --build console
docker compose ps console
docker compose logs --tail=100 console
```

健康检查只验证登录页可响应，不代表控制面/数据库已就绪。工作流与运维聚合页面需要额外
配置 Gateway 管理端点和控制面 `Fleet`，见
[控制面 README](../service/aiServeWeaveControlPlane/README.md) 与
[Console README](../service/aiServeWeaveConsole/README.md)。

部署到 HTTPS 反向代理之后时，设置 `AISW_CONSOLE_COOKIE_SECURE=true` 并重建 Console
容器；代理须原样透传外部 `Host`（含非标准端口），否则同源检查会拒绝写操作。此编排不
附带 TLS 终结服务。

## 证书链是怎么闭合的

改证书相关配置前先看这张图：

```text
registry-init（跑一次就退出）
  └─ Registry 生成自己的 CA            → registry-data:/data/ca/ca-cert.pem
  └─ 用那个 CA 签一张服务端证书         → registry-data:/data/gateway-certs/
                                             ↓
gateway 只读挂载同一个 volume
  -tls-cert / -tls-key  ← gateway-certs/    （隧道监听器出示它）
  -client-ca            ← ca/ca-cert.pem    （校验 Agent 的节点证书）
  -registry-ca          ← ca/ca-cert.pem    （校验 Registry 的服务端证书）
                                             ↓
Agent（宿主机）
  -ca-file ← 同一份 ca-cert.pem              （校验 Gateway 与 Registry）
  节点证书 ← Registry 用 bootstrap token 签发
```

**Gateway 证书必须由 Registry 的 CA 签发**：Agent 只信任这一个根，其他签发方的证书
每一个 Agent 都会拒绝。

Registry 的 RPC 只签发**节点**证书，监听器证书需要用 `-issue-server-cert`：

```bash
aiserveweave-registry -data-dir /data -issue-server-cert \
  -tls-host gateway,127.0.0.1 -out-dir /data/gateway-certs
```

与 `-mint-token` 一样，这是部署时执行一次的运维动作。

## 接一个 Agent

Agent **不在** compose 里——它要连本机的 Ollama / vLLM / ComfyUI，装进容器反而多一层
网络。

```bash
# 取一个 bootstrap token 与 CA
docker compose exec -T registry /usr/local/bin/service -data-dir /data -mint-token > /tmp/aisw-token
docker compose cp registry:/data/ca/ca-cert.pem /tmp/aisw-ca.pem

go run ./service/aiServeWeaveAgent \
  -registry 127.0.0.1:9443 -ca-file /tmp/aisw-ca.pem \
  -bootstrap-token-file /tmp/aisw-token \
  -cert-file ./data/agent/cert.pem -key-file ./data/agent/key.pem \
  -gateway 127.0.0.1:8443 \
  -ollama-url http://127.0.0.1:11434 \
  -labels region=local,gpu=none
```

`-labels` 是 Gateway 路由规则据以选节点的东西（见 Gateway README 的「模型别名与节点标签」）。

## 第一个租户与第一个 key

**若「起步」时填了 `AISW_DEFAULT_OWNER_EMAIL`**，`bootstrap` 已自动完成第 1 步，直接
用那个邮箱密码登录 `http://127.0.0.1:3000` 或跳到第 2 步拿 API Key 即可
（`docker compose logs bootstrap` 查看创建结果）。

**没填，或想手动控制这一步**：下面四步走完「建租户 → 登录 → 签发 key → 调一次
API」，需要 `curl` 和 `jq`（没有 `jq` 见每一步后面的备用做法）。

**1. 用 BootstrapToken 建第一个租户和它的 owner。** 该 token 只对这一个端点有效，且
只在「还没有任何用户能登录」时起作用：

```bash
source .env
curl -sS -X POST http://127.0.0.1:8090/admin/v1/tenants \
  -H "Authorization: Bearer $AISW_BOOTSTRAP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Acme","owner_email":"owner@example.com","owner_password":"a-long-enough-password"}'
```

`201` 与返回的租户/owner 信息即为成功；`401` 说明 `$AISW_BOOTSTRAP_TOKEN` 与 `.env`
没对上（常见原因见下面「常见问题」）。

**2. 用 owner 的邮箱密码登录，拿会话 token**（有效期 12 小时，见
`deploy/controlplane.yaml`）：

```bash
SESSION_TOKEN=$(curl -sS -X POST http://127.0.0.1:8090/admin/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"owner@example.com","password":"a-long-enough-password"}' | jq -r .token)
# 没有 jq：把上面 curl 的完整输出粘出来，手动复制 "token" 字段的值，
# 后面用 SESSION_TOKEN=<那段值> 手工赋值即可。
echo "$SESSION_TOKEN"   # 应该是一长串 JWT，不是空的
```

**3. 签发一个 API Key。** 明文只在这一次响应里出现，之后任何接口都读不回它，务必存好：

```bash
curl -sS -X POST http://127.0.0.1:8090/admin/v1/apikeys \
  -H "Authorization: Bearer $SESSION_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"first-key","ttl_seconds":0}' | jq -r .key | tee /tmp/aisw-api-key.txt
```

**4. 用这个 key 调一次 Gateway。** 没有 Agent 连接时 `/v1/models` 返回空列表——这是
预期结果，说明鉴权链路已通，只是还没有推理节点；接上「接一个 Agent」的 Agent 后会
列出它的模型：

```bash
curl http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer $(cat /tmp/aisw-api-key.txt)"
```

同一个 owner 邮箱密码也能直接登录 Console，在页面上管理用户、Key 和配额，不必再走
`curl`。

### 第一个平台运维账号

「平台运维」是独立于租户/owner 的另一套角色（登录后能看机群、发布管理等运维页面），
默认同样不会自动创建。

**若「起步」时填了 `AISW_DEFAULT_OPERATOR_EMAIL`**，`bootstrap` 已自动建好，直接用该
邮箱密码登录即可（`docker compose logs bootstrap` 查看结果）。

**没填**：用同一个 `$AISW_BOOTSTRAP_TOKEN` 手动建一个（`createPlatformOperator` 不
接受其他凭据，见控制面 README「平台运维身份」一节）：

```bash
source .env
curl -sS -X POST http://127.0.0.1:8090/admin/v1/platform/operators \
  -H "Authorization: Bearer $AISW_BOOTSTRAP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"email":"ops@example.com","password":"a-long-enough-password","name":"Ops"}'
```

登录走的是另一个端点 `POST /admin/v1/platform/auth/login`，与租户 owner 的
`/admin/v1/auth/login` 不通用。

## 两份控制面配置

`deploy/controlplane.yaml` 使用容器主机名与 MySQL 9.7；
`service/aiServeWeaveControlPlane/etc/controlplane.yaml` 是独立的宿主机配置。
Compose 默认选择 MySQL，因为 `jobs` / `job_artifacts` 持久化目前仅支持 MySQL。
`MYSQL_USER`、`MYSQL_PASSWORD`、`MYSQL_DATABASE` 从 `.env` 注入控制面。

从 PostgreSQL 部署切换时，原 `postgres-data` 卷保留，但账户、Key 与其他数据不会自动迁移。
新 MySQL 数据库可由 `bootstrap` 使用 `.env` 中的默认账户配置初始化；随后需重新登录
Console，并按需重新创建 API Key。不要使用 `down -v`，它会删除两边的数据卷。

三个密钥在两份文件里都写成 `${VAR}`，未注入时控制面的 `Validate` 会因长度不足拒绝
启动——而不是带着一个提交在仓库里、人人可见的占位符继续跑。

## 镜像

三个 Go 服务共用根目录的 `Dockerfile`，由 `SERVICE` build-arg 选择二进制：

```bash
docker build --build-arg SERVICE=aiServeWeaveGateway -t aisw-gateway .
```

Console 使用 [独立 Dockerfile](../service/aiServeWeaveConsole/Dockerfile)，构建上下文是
Console 目录。从仓库根目录单独构建：

```bash
docker build -t aisw-console ./service/aiServeWeaveConsole
```

## 常见问题

按症状排列，都是这套编排本身会遇到的情况，不是代码缺陷。

- **`controlplane`/`gateway`/`console`/`bootstrap` 反复 `Restarting` 或直接
  `Exited (1)`，日志里有一行 `set it in deploy/.env`。** 检查 `deploy/.env`（不是
  `.env.example`）里四个密钥是否都已填上；改完 `.env` 后必须让容器重新读取环境，仅
  保存文件不够：

  ```bash
  docker compose up -d --force-recreate controlplane console gateway bootstrap
  ```

- **`registry-init` 报错退出，日志是 `registry: ca: cannot create /data/ca:
  mkdir /data/ca: permission denied`。** 已在 `Dockerfile` 里修复，但**只对全新的卷
  生效**——若此前失败过，`registry-data` 卷可能已以 root 属主存在。删掉该卷重来
  （Registry 的 CA 会重新生成）：

  ```bash
  docker compose down
  docker volume rm deploy_registry-data   # 卷名前缀取决于目录名，用 docker volume ls 核对
  docker compose up -d --build
  ```

- **改了 `.env` 里的端口/密钥，`docker compose up -d` 却好像什么也没变。**
  Compose 只在容器配置发生变化时才重建，需要显式 `--force-recreate`（如上一条）。
  确认哪些变量真正生效：`docker compose config`。

- **`Bind for 127.0.0.1:8080 failed: port is already allocated`（或其他端口）。**
  宿主机上已有别的进程占用该端口。改 `deploy/.env` 里对应的 `*_PORT`（见「环境要求」）
  后 `docker compose up -d` 即可，不用改 `docker-compose.yaml`。

- **`registry-init` 状态是 `Exited (0)`，被当成了故障。** 这是预期行为：它只在首次
  启动时签发一次证书就退出（见「证书链是怎么闭合的」），`Exited (0)` 是成功。

- **`postgres` 启动失败，日志提示数据目录不兼容/版本不对。** 通常是 `postgres-data`
  卷里留着旧版本数据文件。这套编排的数据只是本地试用数据，换个干净的卷最省事：

  ```bash
  docker compose down
  docker volume rm deploy_postgres-data   # 卷名前缀取决于目录名，用 docker volume ls 核对
  docker compose up -d
  ```

- **创建租户时 `POST /admin/v1/tenants` 返回 `401`。** `$AISW_BOOTSTRAP_TOKEN` 与
  `controlplane` 容器里实际生效的值对不上——多半是 `.env` 改了但没重建容器（见第一
  条），或者 `source .env` 的目录不是 `deploy/`。

- **`bootstrap` 服务是 `Exited (1)`。** `docker compose logs bootstrap` 看具体
  原因：`HTTP 400` 多半是 `AISW_DEFAULT_OWNER_EMAIL` 格式不对或密码为空；
  `did not accept connections within 120s` 说明控制面本身没起来，先查它的日志。
  改好 `.env` 后单独重跑即可，不用重启整套编排：

  ```bash
  docker compose up -d --force-recreate bootstrap
  ```

  失败不影响 Console、Gateway 等其他服务，走一遍「第一个租户与第一个 key」的手动
  步骤即可。

- **Console 页面打得开，但登录后一直转圈或提示无法连接。** Console 对
  `controlplane` 的依赖条件是「已启动」而不是「已就绪」，两者之间有短暂窗口。等几秒
  重试；持续失败看 `docker compose logs controlplane`。

- **想彻底清空重来。** 这会连证书一起删掉，之后 Agent 也要用新证书重新接入：

  ```bash
  docker compose down -v   # -v 连具名卷（数据库、Redis、Registry 的 CA/证书）一起删
  ```

- **Agent 连不上 Gateway 的隧道端口。** 若 Agent 与这套 Compose 不在同一台机器、或
  前面有 NAT/负载均衡，必须显式设置 `deploy/.env` 里的 `GATEWAY_ADVERTISE_ADDR` 为
  Agent 真正能拨通的地址，见 `.env.example` 里的注释。

- **改了任意服务的源码，`docker compose up -d` 却还是旧行为。** 这套编排从源码构建
  镜像，重新构建才会反映改动：

  ```bash
  docker compose up -d --build <service>
  ```

## 已知限制

- **单 Registry 实例。** bootstrap token 的一次性校验靠本地文件加内存锁，只在单进程时成立（Registry README 已记录）。这套编排里 `registry` 没有副本。
- **本地开启 `Database.AutoMigrate`。** P07 已将实现换成固定版本 SQL；生产先执行 `-migrate up`，再关掉该开关，以只校验 schema 的模式启动。升级、dirty 处置和恢复演练见 [数据库升级与恢复](database-recovery.md)。
- **Gateway 单副本。** 多副本需要每个副本一张自己的证书（`-tls-host` 覆盖各自的地址）与各自的 `-replica-id`，名册由 Registry 负责；限流已经通过 `-redis-addr` 跨副本共享。
- **没有 TLS 终结。** Console 的 3000 与 Gateway 的 8080 都是明文 HTTP，只绑回环。对外提供服务时在反向代理终结 TLS，并启用 Console Secure Cookie。

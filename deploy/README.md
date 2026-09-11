# deploy

用 Docker Compose 把一套 AIServeWeave 跑起来：Console、依赖服务、Registry、控制面与 Gateway。
本文档按「从零到能在 Console 里登录、能用 API Key 调通一次推理」的顺序写，每一步给出
可复制粘贴的命令，以及命令没有按预期工作时该往哪看。

## 环境要求

- **Docker Engine 24+ 与 Compose v2**（`docker compose version` 能打印版本号；老式的
  `docker-compose`（带连字符）分支不支持本编排用到的 `service_completed_successfully`
  依赖条件，会在 `registry-init` 那一步失败或直接忽略依赖顺序）。
- **能访问镜像仓库与包源**：首次 `--build` 要拉取 `postgres`/`redis` 官方镜像、
  下载 Go 依赖与 Node.js 依赖，离线环境需要先自建镜像或配置好代理/镜像源。
- **下列宿主机端口空闲**：`3000`（Console）、`8080`/`8443`/`9090`（Gateway）、`8090`
  （控制面）、`9443`（Registry）、`5432`（Postgres）、`6379`（Redis）。改端口见
  `.env.example` 里的 `*_PORT` 变量，不用去改 `docker-compose.yaml`。
- 一台机器跑全部服务，2 核 4G 内存足够试用；生产规模见下方「已知限制」。

## 起步

```bash
cd deploy
cp .env.example .env

# 生成四个独立密钥并写回 .env——手抄 openssl 的输出很容易抄漏一个字符或填错变量名，
# 这一步直接把结果写进对应的行。之后可以用 $EDITOR .env 检查，但不再需要手填。
for var in AISW_ACCESS_SECRET AISW_INTERNAL_TOKEN AISW_BOOTSTRAP_TOKEN; do
  sed -i.bak "s#^${var}=#${var}=$(openssl rand -base64 32)#" .env
done
sed -i.bak "s#^AISW_CONSOLE_SESSION_SECRET=#AISW_CONSOLE_SESSION_SECRET=$(openssl rand -base64 48)#" .env
rm -f .env.bak

# 可选：填了这两行，起来之后就有一个能登录 Console 的账号，不用再手动建租户
# ——见下面「第一个租户与第一个 key」。不填也完全可以，那一节手动做同一件事。
# 邮箱按自己的改；密码这里随机生成一个，记得存到密码管理器，这条命令不会再打印第二次。
sed -i.bak "s#^AISW_DEFAULT_OWNER_EMAIL=#AISW_DEFAULT_OWNER_EMAIL=owner@example.com#" .env
sed -i.bak "s#^AISW_DEFAULT_OWNER_PASSWORD=#AISW_DEFAULT_OWNER_PASSWORD=$(openssl rand -base64 18)#" .env
rm -f .env.bak
grep '^AISW_DEFAULT_OWNER_' .env

docker compose up -d --build
```

首次构建三个 Go 服务镜像 + Console 镜像，视网络情况需要几分钟；之后再次 `up -d` 只会
重建改过的部分。**不要跳过下一节的检查就直接去用 Console**——`postgres`/`redis` 有
`healthcheck`，但 `controlplane` 本身没有，Compose 认为它「已启动」不代表它已经能接受
请求，`bootstrap` 那一步的自动建号也要等它就绪才会成功。

### 确认起来了

```bash
docker compose ps
```

默认启动的八个服务（`postgres`、`redis`、`registry-init`、`registry`、`controlplane`、
`bootstrap`、`console`、`gateway` —— `registry-init` 与 `bootstrap` 跑完都应显示
`Exited (0)`，这是预期状态，不是失败；`mysql` 需要 `--profile mysql` 才会启动，默认不
在其中）都应处于 `running`/`healthy`，没有 `restarting` 或 `Exit` 非 0 的。任何一个反复
重启，先看它的日志：

```bash
docker compose logs --tail=100 <service>
```

再确认控制面已经能接受请求（下面这条只要能连上就会返回，不代表凭据正确）：

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

**启用历史指标采集（P08）。** Gateway 与 Registry 的 `-metrics-addr` 在容器内都绑 `0.0.0.0`，因此控制面容器已经能以 `http://gateway:9090`、`http://registry:9091` 在 compose 内部网络上抓到它们——宿主机回环映射只影响从宿主机之外访问，不影响容器间访问。要让控制面开始采集历史时序（供 Console `/operator/metrics` 使用），在 `controlplane.yaml` 里加一段 `MetricsHistory`（默认未配置，采集器不会启动）：

```yaml
MetricsHistory:
  GatewayAddrs: ["http://gateway:9090"]
  RegistryAddr: "http://registry:9091"
```

字段与默认值见 [控制面 README「指标与历史监控」](../service/aiServeWeaveControlPlane/README.md#指标与历史监控p08)。

### Redis 会话持久化（P05）

控制面会话以 Redis 为权威状态，因此 Redis 已不是可选缓存。Compose 使用具名卷 `redis-data`、AOF `appendonly yes` 与 `appendfsync always`；控制面只在会话创建/撤销已由 Redis 确认后返回成功。Redis 暂时不可用时管理请求返回 `503`，不会退回只验 JWT。丢失 Redis 数据会安全地让所有人重新登录；不要恢复一份早于安全操作的旧快照，否则可能恢复旧会话记录。生产集群的复制、故障切换与备份 RPO 仍须由部署方按 Redis 拓扑验证。

## Console

`console` 默认随整套服务启动，使用独立的 Node.js 24 多阶段镜像。构建时按 Console 的
`packageManager` 安装 pnpm，再使用锁文件安装依赖；运行镜像只保留 Next.js standalone
服务端和静态资源，以非 root 用户运行。首次构建需要访问镜像仓库、npm registry 和
Google Fonts（当前页面使用 `next/font/google`）。

从已有部署升级时，在 `deploy/.env` 中新增独立的 `AISW_CONSOLE_SESSION_SECRET`，至少
32 字符；不要复用控制面的密钥。密钥仅在容器运行时注入，不作为镜像构建参数。
所有 Console 副本需使用相同的密封密钥；更换它会使已有登录 Cookie 失效。

| 配置 | 默认值 / 要求 |
| --- | --- |
| `AISW_CONSOLE_SESSION_SECRET` | 必填，独立随机密钥，未配置时 Compose 拒绝启动 |
| `CONSOLE_PORT` | 宿主机端口，默认 `3000`，只绑定 `127.0.0.1` |
| `AISW_CONSOLE_COOKIE_SECURE` | 本地 HTTP 默认 `false`；HTTPS 部署设为 `true` |
| `AISW_CONSOLE_CONTROL_PLANE_URL` | Compose 内固定为 `http://controlplane:8090`，不受宿主机 `CONTROLPLANE_PORT` 影响 |

浏览器访问 Console，由 Console 服务端转发 Admin API 请求；浏览器不直连控制面，也不持有
控制面的会话令牌。按下文创建首个租户和 owner 后，用该 owner 的邮箱和密码登录。
Compose 不提供默认账号，也不会自动创建租户。

仅重建 Console（在 `deploy` 目录执行）：

```bash
docker compose up -d --build console
docker compose ps console
docker compose logs --tail=100 console
```

健康检查只验证登录页可响应，不代表控制面、数据库或用户登录已就绪。当前编排未配置
Gateway 的管理读取端点与控制面的 `Fleet`，所以工作流/运行聚合和运维页面需要额外配置，
见 [控制面 README](../service/aiServeWeaveControlPlane/README.md) 与
[Console README](../service/aiServeWeaveConsole/README.md)；添加 Console 不会自动启用运维权限。

部署到 HTTPS 反向代理之后时，将 `AISW_CONSOLE_COOKIE_SECURE=true` 并重新创建 Console
容器；代理必须原样透传外部 `Host`（含非标准端口），否则登录、退出等写操作的同源检查会拒绝请求。
此编排不附带 TLS 终结服务。

## 证书链是怎么闭合的

这是这套编排里唯一不显然的部分，值得先读懂再改：

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

**为什么 Gateway 的证书必须由 Registry 的 CA 签发**：Agent 只信任一个根——`tunnel/identity.go` 的 `TLSConfig` 把 `-ca-file` 加载的 Registry CA 作为 `RootCAs`。任何其他签发方的证书，每一个 Agent 都会拒绝。

Registry 通过 RPC 签发的是**节点**证书（ClientAuth + `aiserveweave://node/<id>` SAN），那不是监听器该出示的东西，所以有了 `-issue-server-cert` 这条 CLI：

```bash
aiserveweave-registry -data-dir /data -issue-server-cert \
  -tls-host gateway,127.0.0.1 -out-dir /data/gateway-certs
```

与 `-mint-token` 一样，它是部署时执行一次的运维动作，执行者本来就对 CA 有文件系统访问权。

## 接一个 Agent

Agent **不在** compose 里：它的工作是连本机上已经跑着的 Ollama / vLLM / ComfyUI，装进容器只会在它与那些后端之间多一层网络。

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

**如果「起步」那一步填了 `AISW_DEFAULT_OWNER_EMAIL`**，`bootstrap` 服务已经自动做完了
下面第 1 步——用那个邮箱和密码直接登录 `http://127.0.0.1:3000` 或跳到第 2 步拿 API
Key 即可，不用再手动 `curl` 一次；`docker compose logs bootstrap` 能看到它是创建成功
还是已经存在。

**没填，或者想手动控制这一步**：到这里为止还没有任何租户、用户或 API Key——Compose
默认不会自动创建。下面四步一次走完「建租户 → 登录 → 签发 key → 用它调一次 API」，
全程只需要 `curl` 和 `jq`（没有 `jq` 见每一步后面的备用做法）。

**1. 用 BootstrapToken 建第一个租户和它的 owner。** 这个 token 只对这一个端点有效，
只在「还没有任何用户能登录」的这一次起作用（`bootstrap` 服务内部调的就是这一个端点）：

```bash
source .env
curl -sS -X POST http://127.0.0.1:8090/admin/v1/tenants \
  -H "Authorization: Bearer $AISW_BOOTSTRAP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Acme","owner_email":"owner@example.com","owner_password":"a-long-enough-password"}'
```

返回 `201` 与刚创建的租户、owner 信息就是成功；`401` 说明 `$AISW_BOOTSTRAP_TOKEN`
没对上 `.env`（常见原因：改过 `.env` 但没有 `docker compose up -d` 重建
`controlplane`，见下面「常见问题」）。

**2. 用 owner 的邮箱密码登录，拿会话 token。** 这一步返回的 `token` 就是 Console 登录
后浏览器持有的那种会话令牌，`deploy/controlplane.yaml` 里配的有效期是 12 小时：

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

**4. 用这个 key 调一次 Gateway。** 在还没有 Agent 连接的情况下，`/v1/models` 会返回
`{"object":"list","data":[]}`——空列表是预期结果，说明鉴权链路（Console/控制面签发的
key → Gateway 校验）整个通了，只是还没有推理节点；接上「接一个 Agent」一节的 Agent 后
这里就会列出它的模型：

```bash
curl http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer $(cat /tmp/aisw-api-key.txt)"
```

同一个 owner 邮箱密码也能直接登录 `http://127.0.0.1:3000` 的 Console，在页面上管理
用户、Key 和配额，不必再走 `curl`。

## 两份控制面配置

`deploy/controlplane.yaml` 与 `service/aiServeWeaveControlPlane/etc/controlplane.yaml` 只差三行——监听主机、数据库 DSN、Redis 地址，一份用容器主机名、一份用回环。

分开是刻意的：go-zero 的 `conf.UseEnv` 展开 `${VAR}` 但**不支持** `${VAR:-default}`（不支持时整个表达式变成空串，而不是退回默认值）。要用一份模板同时服务两种场景，就得给「本来有显然本地默认值」的东西也强加一个环境变量。

**三个密钥两份文件里都没有取值**，都写成 `${VAR}`。未注入时展开为空串，而控制面的 `Validate` 要求至少 32 字符——因此忘记注入的部署会带着一条可操作的报错启动失败，而不是带着一个提交在仓库里、人人可见的占位符继续跑。

## 镜像

三个 Go 服务共用根目录的 `Dockerfile`，由 `SERVICE` build-arg 选择二进制：

```bash
docker build --build-arg SERVICE=aiServeWeaveGateway -t aisw-gateway .
```

三个二进制共用一个 module、一套依赖与同一条构建命令，因此三个 Dockerfile 就是同一份配方的三份拷贝——等哪天有人只在其中两个里升了 Go 版本，它们就开始漂移了。

Console 使用 [独立 Dockerfile](../service/aiServeWeaveConsole/Dockerfile)，构建上下文是
Console 目录。从仓库根目录单独构建：

```bash
docker build -t aisw-console ./service/aiServeWeaveConsole
```

## 常见问题

按症状排列，都是这套编排本身会遇到的情况，不是代码缺陷。

- **`docker compose up -d` 起来后，`controlplane`/`gateway`/`console`/`bootstrap`
  反复 `Restarting` 或直接 `Exited (1)`，日志里有一行 `set it in deploy/.env`。**
  这几个密钥是 `${VAR:?...}` 形式：容器里读到空值就直接拒绝启动，而不是带着一个不安全的
  默认值继续跑——这是设计成这样的，见下面「两份控制面配置」。检查 `deploy/.env`（不是
  `.env.example`）里四个密钥是否都已填上；改完 `.env` 之后必须让容器重新读取环境，
  仅仅保存文件不够：

  ```bash
  docker compose up -d --force-recreate controlplane console gateway bootstrap
  ```

- **`registry-init` 报错退出，日志是 `registry: ca: cannot create /data/ca:
  mkdir /data/ca: permission denied`。** 这是本仓库早前版本的一个真实 bug：Registry
  以非 root 用户运行，而一个全新的具名卷首次挂载到镜像里一个此前不存在的路径上时，落地
  属主是 root，非 root 进程写不进去。已在 `Dockerfile` 里修（构建镜像时预先建好 `/data`
  并 `chown` 给运行用户，好让 Docker 用这个属主去初始化新卷）——但**只对全新的卷生效**：
  一个此前失败过的部署，`registry-data` 卷可能已经以 root 属主存在，重新构建镜像并不会
  修正一个已经存在的卷。这种情况下删掉那个卷再重来（Registry 的 CA 会重新生成，见下面
  「想彻底清空重来」）：

  ```bash
  docker compose down
  docker volume rm deploy_registry-data   # 卷名前缀取决于目录名，用 docker volume ls 核对
  docker compose up -d --build
  ```

- **改了 `.env` 里的端口/密钥，`docker compose up -d` 却好像什么也没变。**
  Compose 只在容器配置（镜像、环境变量、命令等）发生变化时才重建它；有时候需要显式
  `--force-recreate`，如上一条。确认哪些环境变量真正生效了：
  `docker compose config` 会打印出所有变量替换之后的最终配置。

- **`Bind for 127.0.0.1:8080 failed: port is already allocated`（或其他端口）。**
  宿主机上已经有别的进程占用该端口，常见于同时跑着这个仓库的另一套编排，或本机自己的
  Postgres/Redis。改 `deploy/.env` 里对应的 `*_PORT`（见「环境要求」的端口列表）后
  `docker compose up -d` 即可，不用改 `docker-compose.yaml`。

- **`registry-init` 状态是 `Exited (0)`，被当成了故障。** 这是预期行为：它只在首次启动
  时签发一次证书就退出（见「证书链是怎么闭合的」），`docker compose ps` 里显示 `Exited
  (0)` 是成功，不是 `0` 以外的退出码才需要关心。

- **`postgres` 启动失败，日志提示数据目录不兼容 / 版本不对。** 通常是这台机器上曾用旧版
  本仓库（或别的项目）起过 Postgres，`postgres-data` 这个具名卷里留着旧版本的数据文件。
  这套编排的数据只是本地试用数据，直接换个干净的卷最省事：

  ```bash
  docker compose down
  docker volume rm deploy_postgres-data   # 卷名前缀取决于目录名，用 docker volume ls 核对
  docker compose up -d
  ```

- **创建租户时 `POST /admin/v1/tenants` 返回 `401`。** `$AISW_BOOTSTRAP_TOKEN` 与
  `controlplane` 容器里实际生效的值对不上——多半是 `.env` 改了但没重建容器（见上面第一
  条），或者 `source .env` 的目录不是 `deploy/`。

- **`bootstrap` 服务是 `Exited (1)`。** `docker compose logs bootstrap` 看具体原因：
  `tenant creation failed with HTTP 400` 多半是 `AISW_DEFAULT_OWNER_EMAIL` 格式不对
  或密码为空；`controlplane did not accept connections within 120s` 说明控制面本身
  没起来，先查它的日志。`bootstrap` 是一次性容器（`restart: "no"`），改好 `.env` 后
  单独重跑它即可，不用重启整套编排：

  ```bash
  docker compose up -d --force-recreate bootstrap
  ```

  它跑失败不影响 Console、Gateway 等其他服务——只是还得走一遍「第一个租户与第一个
  key」的手动步骤。

- **Console 页面打得开，但登录后一直转圈或提示无法连接。** Console 对 `controlplane`
  的 `depends_on` 条件是「已启动」而不是「已就绪」（控制面没有单独的健康检查端点），
  两者之间有一个短暂窗口。等几秒重试；如果持续失败，`docker compose logs
  controlplane` 里应该有更具体的错误。

- **想彻底清空重来。** 这会连证书一起删掉，之后 Agent 也要用新证书重新接入：

  ```bash
  docker compose down -v   # -v 连具名卷（数据库、Redis、Registry 的 CA/证书）一起删
  ```

- **Agent 连不上 Gateway 的隧道端口。** 若 Agent 与这套 Compose 不在同一台机器、或前面
  有 NAT/负载均衡，必须显式设置 `deploy/.env` 里的 `GATEWAY_ADVERTISE_ADDR` 为 Agent
  真正能拨通的地址——它默认值只在同机场景下成立，见 `.env.example` 里的注释。

- **改了任意服务的源码，`docker compose up -d` 却还是旧行为。** 这套编排从源码构建镜像，
  重新构建才会反映改动：

  ```bash
  docker compose up -d --build <service>
  ```

## 已知限制

- **单 Registry 实例。** bootstrap token 的一次性校验靠本地文件加内存锁，只在单进程时成立（Registry README 已记录）。这套编排里 `registry` 没有副本。
- **本地开启 `Database.AutoMigrate`。** P07 已将实现换成固定版本 SQL；生产先执行 `-migrate up`，再关掉该开关，以只校验 schema 的模式启动。升级、dirty 处置和恢复演练见 [数据库升级与恢复](database-recovery.md)。
- **Gateway 单副本。** 多副本需要每个副本一张自己的证书（`-tls-host` 覆盖各自的地址）与各自的 `-replica-id`，名册由 Registry 负责；限流已经通过 `-redis-addr` 跨副本共享。
- **没有 TLS 终结。** Console 的 3000 与 Gateway 的 8080 都是明文 HTTP，只绑回环。对外提供服务时在反向代理终结 TLS，并启用 Console Secure Cookie。

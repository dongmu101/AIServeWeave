# deploy

用 Docker Compose 把一套 AIServeWeave 跑起来：依赖服务、Registry、控制面与 Gateway。

## 起步

```bash
cd deploy
cp .env.example .env
$EDITOR .env          # 三个密钥，各自用 openssl rand -base64 32 生成
docker compose up -d
```

起来之后：

| 端点 | 地址 |
| --- | --- |
| OpenAI 兼容 API | `http://127.0.0.1:8080/v1/...` |
| 控制面 Admin API | `http://127.0.0.1:8090/admin/v1/...` |
| Gateway 指标 | `http://127.0.0.1:9090/metrics` |
| Registry（Agent 用） | `127.0.0.1:9443` |
| Gateway 隧道（Agent 用） | `127.0.0.1:8443` |

全部只绑宿主机回环。要对外提供服务，前面必须先有一层带鉴权的入口。

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

```bash
source .env
curl -X POST http://127.0.0.1:8090/admin/v1/tenants \
  -H "Authorization: Bearer $AISW_BOOTSTRAP_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Acme","owner_email":"owner@example.com","owner_password":"a-long-enough-password"}'

# 登录拿会话 token，再签发 API Key，然后：
curl http://127.0.0.1:8080/v1/models -H "Authorization: Bearer <api-key>"
```

## 两份控制面配置

`deploy/controlplane.yaml` 与 `service/aiServeWeaveControlPlane/etc/controlplane.yaml` 只差三行——监听主机、数据库 DSN、Redis 地址，一份用容器主机名、一份用回环。

分开是刻意的：go-zero 的 `conf.UseEnv` 展开 `${VAR}` 但**不支持** `${VAR:-default}`（不支持时整个表达式变成空串，而不是退回默认值）。要用一份模板同时服务两种场景，就得给「本来有显然本地默认值」的东西也强加一个环境变量。

**三个密钥两份文件里都没有取值**，都写成 `${VAR}`。未注入时展开为空串，而控制面的 `Validate` 要求至少 32 字符——因此忘记注入的部署会带着一条可操作的报错启动失败，而不是带着一个提交在仓库里、人人可见的占位符继续跑。

## 镜像

一个 `Dockerfile`，由 `SERVICE` build-arg 选择二进制：

```bash
docker build --build-arg SERVICE=aiServeWeaveGateway -t aisw-gateway .
```

三个二进制共用一个 module、一套依赖与同一条构建命令，因此三个 Dockerfile 就是同一份配方的三份拷贝——等哪天有人只在其中两个里升了 Go 版本，它们就开始漂移了。

## 已知限制

- **单 Registry 实例。** bootstrap token 的一次性校验靠本地文件加内存锁，只在单进程时成立（Registry README 已记录）。这套编排里 `registry` 没有副本。
- **`AutoMigrate` 开着。** 本地起步够用；生产部署应关掉它并改用带版本的迁移。
- **Gateway 单副本。** 多副本需要每个副本一张自己的证书（`-tls-host` 覆盖各自的地址）与各自的 `-replica-id`，名册由 Registry 负责；限流已经通过 `-redis-addr` 跨副本共享。
- **没有 TLS 终结。** 对外的 8080 是明文 HTTP，只绑回环。要暴露出去，前面加一层反向代理并在那里终结 TLS。

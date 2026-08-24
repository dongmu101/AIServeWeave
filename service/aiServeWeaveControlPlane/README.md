# aiserveweave-controlplane

控制面的 Admin API：Console 背后的租户、用户、API Key 与审计线索，以及供 Gateway 校验 API Key 的内部端点。

**当前进度：第二阶段「用户、租户、API Key 和配额」的前三项已落地，配额未做。** 这个二进制现在能创建租户、让用户登录、签发与吊销 API Key、记录管理操作审计，并且 Gateway 已经改成对着它校验 key —— `-api-keys` 明文列表退化为无控制面时的回退路径。

| 目录 | 状态 | 内容 |
| --- | --- | --- |
| `internal/model/` | 已实现 | 四张表的 gorm 映射：`tenants`、`users`、`api_keys`、`audit_logs` |
| `internal/store/` | 已实现 | 四个窄接口 + `gormstore/`（PostgreSQL / MySQL）+ `memstore/`（测试用内存实现） |
| `internal/logic/` | 已实现 | 业务层：权限、审计、key 生命周期。不依赖 HTTP，也不依赖数据库 |
| `internal/token/` | 已实现 | 会话令牌的签发与校验（HS256，golang-jwt/v5） |
| `internal/cache/` | 已实现 | key 校验的 Redis 缓存，吊销时主动失效 |
| `internal/handler/` | 已实现 | go-zero rest 路由、认证中间件、JSON 翻译 |
| `internal/svc/` | 已实现 | 启动时装配：数据库、缓存、签发器 |
| `e2e/` | 已实现 | 真实 HTTP + 真实 JWT + Gateway 真实客户端的闭环测试 |

`common/apikey` 是 key 的格式与哈希算法，Gateway 与本服务共用一份 —— 那是两者之间的契约，不是本服务的内部实现。

## 为什么这个服务用 go-zero，而数据面不用

这是一次有意的分叉，不是不一致：

- **控制面是 CRUD**：路由、参数解析、配置加载、中间件链，go-zero 的 `rest` 把这些做完了。
- **数据面不是**：Gateway 与 Agent 之间是自定义的双向流隧道 + 预热槽池，`zrpc` 的服务发现与按 endpoint 熔断都对不上，而 OpenAI 兼容的 SSE 前门要绕开框架的响应封装才能逐帧 flush。

具体到用了什么、没用什么：

- **只用 `rest`，不用 `zrpc`** —— README 写明「初期模块化单体」，控制面内部不需要 RPC。
- **不用 goctl 生成代码** —— `.api` DSL 会成为对同一批结构体的第二份描述，与仓库「一份契约只有一个来源」冲突，且重新生成会覆盖双语 doc comment。goctl 换来的路由样板就是 `internal/handler/routes.go` 那一页。
- **不用 go-zero 内置的 JWT 中间件** —— 它绑定 `jwt/v4`，而签发端用 `jwt/v5`；它还把每个 claim 以裸字符串为键塞进 context。`internal/token` 加 `requireSession` 一共不到两百行，换来自定义 context key 与单一 jwt 版本。产出的仍是普通 HS256 JWT，客户端看不出区别。
- **用 gorm，不用 go-zero 的 `sqlx`/`sqlc`** —— 按用户选型。
- **用 go-redis，不套 go-zero 的 `core/stores/redis`** —— 那层的主要增值是它自己的 metrics/trace 钩子，而本仓库已有 `common/metrics`，套上去等于引入第二套指标口径。

## 凭据是怎么存的

三种凭据，三种处理方式，区别是有意的：

| 凭据 | 存储形式 | 为什么 |
| --- | --- | --- |
| 用户密码 | bcrypt | 人自己选的，一定活在某人已有的字典里，慢哈希是对的 |
| API Key | SHA-256 | 256 位均匀随机，不存在字典；而校验在每次推理请求上发生，逐请求 bcrypt 等于给每个 token 垫几十毫秒 |
| 共享密钥（Internal/Bootstrap） | 配置文件明文 | 它们是部署配置而非用户凭据；比较走常数时间，长度不足 32 字符时启动直接失败 |

**API Key 的明文只在创建响应里出现一次**，之后没有任何读取路径能重建它。列表只给 `display`（前缀 + 8 个字符），足以区分、不足以重建。

**Gateway 发给本服务的是哈希，不是 key** —— Gateway 自己算 SHA-256，因此用户的凭据从不进入本服务的内存、请求日志或两者之间的抓包。

## 吊销的生效路径

```text
DELETE /admin/v1/apikeys/:id
  → 数据库置为 revoked（先）
  → 删除 Redis 缓存条目（后）
  → Gateway 进程内缓存仍持有，最多 -key-cache-ttl（默认 30s）
```

顺序是「先写库再清缓存」而不是反过来：先清缓存会留下一个窗口，期间一次并发校验会用一行仍然 active 的记录把缓存重新填上。

**Gateway 那 30 秒是本设计已知的代价。** 它换来的是校验不必每个请求一次 HTTP 往返。要把它压到零，需要控制面向 Gateway 推送失效（反向通道），那是独立的一步，见下面「下一步」。

## 数据库

PostgreSQL 与 MySQL 都支持，由 `Database.Driver` 选择，PostgreSQL 是首要目标。

当前这四张表只用标量列，两种引擎表达一致，因此双支持的代价很低。**这在某个 JSON 列落地的那天就不再成立** —— 后续二十张表里的 `workflow_templates`、`deployment_revisions`、`job_events` 都要存 JSON，JSONB 的索引能力是 MySQL JSON 比不了的。到那一步应当重新评估是否继续双支持，而不是悄悄糊过去。

MySQL 的 DSN 必须带 `parseTime=True`，否则每个 `time.Time` 列都会扫描失败。

**迁移目前用 gorm 的 `AutoMigrate`，默认关闭。** 它无法表达回滚、不会删列、不留执行记录。在本服务只有四张表且没有生产数据期间够用；一旦其中任何一条不再成立，这里就换成带版本的 SQL 文件。

## 本地起一套

```bash
# 数据库与缓存
docker compose -f deploy/docker-compose.yaml up -d postgres redis

# 三个密钥，各自独立
openssl rand -base64 32   # Auth.AccessSecret
openssl rand -base64 32   # InternalToken
openssl rand -base64 32   # BootstrapToken

# 填进 etc/controlplane.yaml 后
go run ./service/aiServeWeaveControlPlane -f service/aiServeWeaveControlPlane/etc/controlplane.yaml
```

创建第一个租户（`BootstrapToken` 是这个操作唯一的凭据，因为此时还没有可登录的用户）：

```bash
curl -X POST http://127.0.0.1:8090/admin/v1/tenants \
  -H "Authorization: Bearer $BOOTSTRAP_TOKEN" \
  -d '{"name":"Acme","owner_email":"owner@example.com","owner_password":"a-long-enough-password"}'
```

登录、签发 key，然后让 Gateway 用上它：

```bash
go run ./service/aiServeWeaveGateway \
  -control-plane-addr http://127.0.0.1:8090 \
  -addr 127.0.0.1:8080
# InternalToken 通过 AISW_CONTROL_PLANE_TOKEN 传，不要用 flag —— flag 在 ps 里可见
```

## 路由与守卫

三组守卫就是本服务全部的授权面，都在 `internal/handler/routes.go` 一屏之内：

| 守卫 | 路由 |
| --- | --- |
| 公开 | `POST /admin/v1/auth/login` |
| 会话（JWT） | `/admin/v1/users`、`/admin/v1/apikeys`、`/admin/v1/audit` |
| BootstrapToken | `POST /admin/v1/tenants` |
| InternalToken | `POST /internal/v1/apikeys/verify` |

角色：`owner` 可以做任何事（含增删用户）；`admin` 可以管理 key 与只读一切；`member` 只能管理自己创建的 key。**跨租户与越权一律返回 404 而不是 403** —— 能分辨「存在但不属于你」与「不存在」的调用方，可以据此枚举出别人的 id。

## 已知缺口

1. **审计写入不在事务里。** 动作成功而审计写入失败时，动作保留、记录丢失（只进日志）。修法是把两者放进同一个事务，它要等本层先拥有事务。
2. **配额与速率限制没做。** 第二阶段清单里「配额、并发限制和速率限制」那一项，`Identity` 已经在 Gateway 的请求 context 上了，是它的接入点。
3. **Gateway↔控制面用共享密钥，不是 mTLS。** Gateway 本就在集群自有网络内访问控制面；要更强隔离时再评估。
4. **X-Forwarded-For 不被采信。** 审计记录的是 `RemoteAddr`。要采信该头，必须与「配置一份可信代理清单」一并改动。
5. **go-zero 自己的指标没接进 `common/metrics`。** 本服务目前没有 `/metrics` 端点。

## 下一步

1. **配额与限流**，接在 `Identity` 上。
2. **吊销推送**，把 Gateway 那 30 秒窗口压到零。
3. **带版本的 SQL 迁移**，替换 `AutoMigrate`。
4. **`/metrics` 端点**，把本服务接进 `common/metrics`。

## 质量门禁

```bash
gofmt -l ./service
go vet ./service/aiServeWeaveControlPlane/...
go test -race ./service/aiServeWeaveControlPlane/...
```

`e2e` 包起一个真实的 go-zero 服务、真实的 JWT 会话，并用 Gateway 真实的 `controlplaneclient` 打完整闭环。它不需要数据库：store 是接口，测试用内存实现，因此默认的 `go test ./...` 不依赖任何外部服务。`gormstore` 对真实引擎的验证是单独的事。

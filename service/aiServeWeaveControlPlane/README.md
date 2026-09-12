# aiserveweave-controlplane

控制面的 Admin API：Console 背后的租户、用户、API Key 与审计线索，以及供 Gateway 校验 API Key 的内部端点。

**当前进度：租户、用户生命周期、Redis 可吊销会话、API Key、配额、发布与只读聚合均已落地。** 这个二进制现在能创建租户、管理租户用户与平台运维、立即撤销 JWT 会话、签发与吊销 API Key、记录管理操作审计，并且 Gateway 已经改成对着它校验 key —— `-api-keys` 明文列表退化为无控制面时的回退路径。

| 目录 | 状态 | 内容 |
| --- | --- | --- |
| `internal/model/` | 已实现 | 五张基础表的 gorm 映射：`tenants`、`users`、`platform_operators`、`api_keys`、`audit_logs`。租户配额是 `tenants` 上的三个标量列；Job 与发布相关的版本化表见对应章节 |
| `internal/store/` | 已实现 | 六个窄接口（含 `Jobs`、`JobArtifacts`）+ `gormstore/`（PostgreSQL / MySQL；`jobs`/`job_artifacts` 走独立的带版本迁移，仅 MySQL）+ `memstore/`（测试用内存实现） |
| `internal/logic/` | 已实现 | 业务层：权限、审计、key 生命周期。不依赖 HTTP，也不依赖数据库 |
| `internal/token/` | 已实现 | 会话令牌的签发与校验（HS256，强制 `session_id`） |
| `internal/session/` | 已实现 | Redis 权威会话、每账户 20 个上限、单个/批量撤销与生命周期登录门；内存实现供默认测试 |
| `internal/cache/` | 已实现 | key 校验的 Redis 代际缓存，吊销以常数时间令整代失效 |
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
| 用户密码 | bcrypt（长密码先取 SHA-256 十六进制摘要） | 人自己选的，一定活在某人已有的字典里，慢哈希是对的 |
| API Key | SHA-256 | 256 位均匀随机，不存在字典；而校验在每次推理请求上发生，逐请求 bcrypt 等于给每个 token 垫几十毫秒 |
| JWT 会话 | JWT 只带随机 `session_id`，服务端状态在 Redis | 每次管理请求同时验签与查会话，才能立即撤销 |
| 共享密钥（Internal/Bootstrap） | 配置文件明文 | 它们是部署配置而非用户凭据；比较走常数时间，长度不足 32 字符时启动直接失败 |

初始密码不限制最小长度、最大密码长度或字符组成，也允许空字符串；创建租户与创建用户采用相同规则，登录按原样校验，不裁剪空格。HTTP 请求体仍有 64 KiB 上限。

既有账号继续使用原 bcrypt 哈希。新密码超过 bcrypt 的 72 字节边界时，先对完整 UTF-8 内容取 SHA-256 十六进制摘要，再做 bcrypt，并以 `bcrypt-sha256:` 标记存储格式；登录根据该标记选择校验方式，不截断长密码。此格式仍保存在已有 `password_hash` 列，无需更改表结构。旧版控制面无法校验新增长密码格式，部署了此类账号后不能直接回退旧二进制。

**API Key 的明文只在创建响应里出现一次**，之后没有任何读取路径能重建它。列表只给 `display`（前缀 + 8 个字符），足以区分、不足以重建。

**Gateway 发给本服务的是哈希，不是 key** —— Gateway 自己算 SHA-256，因此用户的凭据从不进入本服务的内存、请求日志或两者之间的抓包。

## 吊销的生效路径

```text
DELETE /admin/v1/apikeys/:id
  → 同一数据库事务：置为 revoked + 递增 outbox generation
  → outbox 发送者持有行锁，Redis Lua 原子推进并发布校验 generation
  → 成功后确认 delivered_generation，失败保留待发送状态
  → Gateway 长轮询收到新 generation，1s 内清空进程内正向缓存
```

顺序是「先写库再推进 generation」。缓存 miss 会连同当前 generation 一起读取；回填时 Lua 只允许仍匹配该代际的写入，因此一次在吊销前开始的数据库读取，无法在吊销后把旧正向结果填进新代际。

`GET /internal/v1/apikeys/revocations/watch?after=<generation>` 与校验端点共用 `InternalToken`。处理器先确认 Redis Pub/Sub 订阅，再读当前 generation，并最多等待 2 秒；Pub/Sub 只负责即时唤醒，持久 generation 才负责断线、漏通知、重连和控制面副本切换后的补偿。Gateway 侧 5 秒检测静默断线；通知不健康时清空并停用本地缓存，逐请求调用本服务，无法验证就保持既有 503 语义。

P07 使用 `key_revocation_outbox` 的一行合并待发通知：Key 吊销或用户禁用与 `generation + 1` 同事务提交；请求随后同步尝试发布，后台启动时立即补发并每秒重试，单次尝试最多 5 秒。发送者锁定该行，Redis 成功后才确认 `delivered_generation`。提交后通知前崩溃会留下持久待发状态，重启或其他控制面副本继续发送；发布后确认前崩溃可能重复通知，语义为至少一次，重复清缓存无害。该行不保存凭据或哈希，存储容量恒定。数据库与 Redis 故障期间仍依赖 TTL 作为纵深防御，不承诺零秒失效或已鉴权推理中断。

## 数据库

PostgreSQL 与 MySQL 都支持，由 `Database.Driver` 选择，PostgreSQL 是首要目标——但这仅对 `tenants`/`users`/`platform_operators`/`api_keys`/`audit_logs` 五张基础表成立。`jobs`/`job_artifacts` 两张新表是 STATUS.md 对 Job 持久化的既有决定，只支持 MySQL 9.7/InnoDB，见下方「Job 持久化契约」一节。

当前这五张基础表只用标量列，两种引擎表达一致，因此双支持的代价很低。**这在某个 JSON 列落地的那天就不再成立** —— 后续表里的 JSON 数据仍应按各自版本化迁移与查询需求评估，而不是悄悄糊过去。

MySQL 的 DSN 必须带 `parseTime=True`，否则每个 `time.Time` 列都会扫描失败。

**P07 已用固定版本 SQL 替换基础表 AutoMigrate。** `migrations/base/{postgres,mysql}` 包含基础五表、历史配额/登录时间增量、索引与吊销 outbox；所有命名空间共用专用连接锁、校验和与 dirty 状态检查。已有路由、模板与 Job 的 SQL 和版本号保持不变，旧账本升级时补记校验和。MySQL 失败需显式 `resume`，不会伪装成 DDL 可回滚。

`-migrate up|status|resume` 使用 Database 配置独立执行，不要求 Redis 或会话密钥。默认服务启动只校验版本、映射列、声明索引和 outbox；缺失、未知、不兼容或 dirty schema 均拒绝服务。旧配置 `Database.AutoMigrate: true` 仍兼容，但它也只执行已内嵌的版本 SQL；生产先用迁移账号执行命令，再以 `AutoMigrate: false` 和受限运行账号启动。

升级顺序、失败恢复、备份命令、恢复后的安全状态核对与可复现双引擎演练见 [数据库升级与恢复](../../deploy/database-recovery.md)。无破坏性 down 迁移；数据库恢复也不等于 Registry/Redis/对象存储的全平台灾备。

## 本地起一套

```bash
# 数据库与缓存
docker compose -f deploy/docker-compose.yaml up -d postgres redis

# 三个密钥，各自独立生成后 export —— 配置文件里写的是 ${VAR}，没有任何取值
export AISW_ACCESS_SECRET=$(openssl rand -base64 32)
export AISW_INTERNAL_TOKEN=$(openssl rand -base64 32)
export AISW_BOOTSTRAP_TOKEN=$(openssl rand -base64 32)

go run ./service/aiServeWeaveControlPlane -f service/aiServeWeaveControlPlane/etc/controlplane.yaml
```

密钥从环境变量注入（`conf.Load` 带 `conf.UseEnv()`）。**未注入时展开为空串，而 `Validate` 要求至少 32 字符，因此会带着一条可操作的报错启动失败**——这比此前那个 44 字符的 `REPLACE-ME-...` 占位符要好：那个长度足以通过校验，服务可以带着一个仓库里人人可见的密钥跑起来。

注意 `conf.UseEnv` 展开 `${VAR}` 但**不支持** `${VAR:-default}`——写成后者时整个表达式变成空串，而不是退回默认值。整套编排见 [deploy/README.md](../../deploy/README.md)。

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

这几组守卫就是本服务全部的授权面，都在 `internal/handler/routes.go` 一屏之内：

| 守卫 | 路由 |
| --- | --- |
| 公开 | `POST /admin/v1/auth/login`、`POST /admin/v1/platform/auth/login` |
| 会话（JWT，租户） | `/admin/v1/users`、`/admin/v1/apikeys`、`/admin/v1/audit`、`/admin/v1/tenants/current`、`/admin/v1/tenants/limits`、`/admin/v1/workflows`、`/admin/v1/jobs*` |
| 会话（JWT，平台运维，STATUS.md 的 P01） | `/operator/v1/*`（机群清单只读 + 节点写路径），见「机群清单」与「节点写路径」两节 |
| BootstrapToken | `POST /admin/v1/tenants`、`POST /admin/v1/platform/operators`（P01 引导创建平台运维账户，复用同一把密钥，理由见「平台运维身份」一节） |
| InternalToken | `POST /internal/v1/apikeys/verify`、`GET /internal/v1/apikeys/revocations/watch`、`/internal/v1/jobs*`（STATUS.md 的 P06/J04/J06，见「吊销的生效路径」与「Job 持久化契约」） |

租户会话（`requireSession`）与平台会话（`requirePlatformSession`）虽然共用同一个 `token.Issuer`，却互相拒绝对方的令牌——见「平台运维身份」一节 `Claims.TenantID` 哨兵值的说明；一次路由配置失误不会让某个会话跨界生效。

`GET /admin/v1/tenants/current` 返回调用方自己所属的租户及其配额，任何已登录角色都可读；`PUT /admin/v1/tenants/limits` 设置该配额，仅 owner 与 admin 可写。两者的请求里都没有租户 id：租户来自会话，因此管理员无法通过改请求体把它指向别人的租户。读写权限刻意不对称——member 无法调高限制，但一个正在被限流的 member 需要看得到是哪条限制在起作用；而能调高自己租户限制的角色，绕过限制最省事的办法就是调高它。

### 角色能做什么

以代码为准（`logic.Service` 的各方法），不是概括：

| 操作 | owner | admin | member |
| --- | --- | --- | --- |
| 创建用户 | 可以 | 否（403） | 否（403） |
| 读用户列表 | 可以 | 可以 | 可以 |
| 重置他人密码、改角色、禁用/启用、撤销他人会话 | 可以（不能作用于自己） | 否（403） | 否（403） |
| 修改自己密码、撤销自己全部会话、退出当前会话 | 可以 | 可以 | 可以 |
| 创建 API Key | 可以 | 可以 | 否（403） |
| 吊销 API Key | 本租户任意 | 本租户任意 | 仅自己创建的（其余 404） |
| 读 Key 列表 | 可以 | 可以 | 可以（本租户全部的展示信息） |
| 读配额 | 可以 | 可以 | 可以 |
| 写配额 | 可以 | 可以 | 否（403） |
| 读审计 | 可以 | 可以 | 可以 |

**角色不允许的操作返回 403，资源不存在或跨租户返回 404。** 两者不是同一件事：403 说的是「你这个角色不能做这件事」，404 说的是「没有这个东西，或者它不属于你」。跨租户、不存在的 id、以及 member 吊销他人 key，都收敛到 404 —— 能分辨「存在但不属于你」与「不存在」的调用方，可以据此枚举出别人的 id。

**吊销不是幂等的。** `RevokeAPIKey` 匹配的是一行仍处于 active 的记录，因此对一个已被吊销的 key 再吊销一次会得到 404，与「别人租户的 key」「从不存在的 id」同码。调用方无法区分这三者，正确的应对是重新读取列表。

### 列表分页

`/admin/v1/users`、`/admin/v1/apikeys`、`/admin/v1/audit` 都返回信封而不是裸数组：

```json
{ "items": [ ... ], "next_cursor": "eyJ..." }
```

- `next_cursor` 缺席即表示这是最后一页。**没有总数**：每页都统计整张表是最先变慢的那种查询，而总数在渲染出来时本就已经过期。
- 分页是 keyset 而不是 offset，游标基于 `(created_at, id)`，排序固定为两者的倒序。这些列表在被读取的同时也在被写入（审计尤其如此），用 offset 会随着新行插入而跳过并重复一些行，且悄无声息。
- `limit` 默认 50、上限 200，超过按上限截断而不是报错；非数字按默认值处理。游标无法解码时返回 400。
- 筛选参数：用户接受 `role` 与 `q`（匹配 email 或姓名）；Key 接受 `status` 与 `q`（匹配名称或 display，**绝不匹配哈希**）；审计接受 `action`、`actor_id`、`since`、`until`（RFC 3339，含左端不含右端，颠倒或为空的窗口返回 400）。
- 游标未签名，这是刻意的：它指出的位置属于一份调用方本来就有权读取的列表，且每个查询依然按租户限定范围。改动它至多把读取者移到自己租户的另一些行上。

## 机群清单（运维视图）

`GET /operator/v1/nodes` 与 `GET /operator/v1/models` 返回整个机群的节点与模型部署。它们**只在配置了 `Fleet` 时才挂载**——没有配置的部署是根本没有这两条路由，而不是有两条回答「未配置」的路由。

```yaml
Fleet:
  Gateways: ["http://gateway-1:8091", "http://gateway-2:8091"]
  GatewayToken: "${AISW_GATEWAY_ADMIN_TOKEN}"   # 与各 Gateway 的同名变量一致
  Timeout: 3s
```

**为什么不放在租户会话组里。** 节点是所有租户共用的基础设施：任何租户的请求都可能被路由到任何节点，而节点身上没有租户维度可供过滤。放进租户会话组，就意味着每个租户的管理员都能读到整个机群的节点 ID、标签、GPU 型号与已加载模型。因此它用自己的路径前缀，无论将来租户角色如何调整都够不到。

**守卫是 `requirePlatformSession`，不是共享密钥（STATUS.md 的 P01）。** 此前这里还有第三把共享密钥 `Fleet.OperatorToken`（`InternalToken` 授权 Gateway 问 key，复用它会让任何 Gateway 也能读到整个机群，因此另起一把）；P01 引入平台运维身份后，这把密钥已移除——「谁能读机群」现在由一名登录的平台运维决定，且能记入审计，不再是「谁拿到了这份密钥」。升级到本版本的部署需要改为先创建平台运维账户（见下）。

**聚合是局部的，且明说这一点。** 每个 Gateway 副本只知道连到它自己身上的节点，所以「有哪些节点」有 N 个局部答案、没有权威答案。本服务向全部副本并发发问并合并结果：

- 同一个 Agent 连到多个副本时只出现一次；展示的视图取自「认为它在线」的那份，同等条件下取心跳更新的那份，而 `replicas` 保留所有报告过它的副本。
- 响应里有三个时间与状态字段：`collected_at`（本服务发问的时刻）、每个副本各自的 `generated_at`（它查看自己节点表的时刻），以及 `partial`。**某个副本没作答不会让列表悄悄变短**——它会成为 `replicas` 里一条具名的失败，错误取自封闭集合 `unreachable` / `timeout` / `unauthorized` / `malformed`，绝不透传传输层文本（那会点出内部网络的主机与端口，而这份文档正在前往浏览器）。
- 模型目录由同一次读取推导，不额外往返：一个机群的第二个视图若单独再读一次，两者就会彼此矛盾。目录里的是**后端上报的模型 id**，不是调用方可用的名字——别名由独立路由版本 API 管理（P02）；机群模型目录仍只表示后端观测，不能代替路由期望状态。

## 节点写路径：审批、禁用、维护（P01）

节点身份的账本权威在 Registry（`internal/identitystore`），本服务不复制一份，只做转发加审计：

```yaml
Registry:
  Addr: "registry:9090"
  CACertFile: "/etc/aiserveweave/registry-ca.pem"   # 留空则信任宿主机根证书库，自签 CA 通常需要设置
  AdminToken: "${AISW_REGISTRY_ADMIN_TOKEN}"        # 与 Registry 的 -admin-token-file 一致
  Timeout: 5s
```

`Registry` 独立于 `Fleet` 配置——审批/禁用/维护不依赖任何 Gateway 读取路径，只在 `Registry.Addr`/`AdminToken` 都配置时才挂载：

| 端点 | 对应 Registry `TokenAdmin` 方法 |
| --- | --- |
| `GET /operator/v1/nodes/states` | `ListNodeStates` |
| `POST /operator/v1/nodes/:id/approve` | `ApproveNode` |
| `POST /operator/v1/nodes/:id/disable` | `DisableNode` |
| `POST /operator/v1/nodes/:id/enable` | `EnableNode` |
| `POST /operator/v1/nodes/:id/maintenance` | `SetMaintenance` |
| `DELETE /operator/v1/nodes/:id/maintenance` | `ClearMaintenance` |

全部由 `requirePlatformSession` 守卫，且只在 Registry 调用成功后才写一条 `audit_logs`（`TenantID=model.PlatformScope`，`ActorID` 是平台运维的 id）——失败的调用不留痕迹，理由与 `Service.audit` 的既有约定相同：一个没发生的动作不该被记成发生过。`internal/registryclient` 是本服务第一个说 gRPC 的包，鉴权方式（纯 TLS + metadata 里的 Bearer admin token）照抄 Registry 自己 CLI 客户端已经在用的写法，不引入 mTLS。

## 平台运维身份（P01）

平台运维与租户用户是两张分开的表（`platform_operators`，不是 `TenantID` 留空的 `users`）与两条分开的登录入口：

- `POST /admin/v1/platform/operators`：引导创建账户，复用与 `POST /admin/v1/tenants` 相同的 `BootstrapToken`——两者都是背后尚无已登录用户的操作，没有理由再引入一把含义相同的密钥。
- `POST /admin/v1/platform/auth/login`：签发一个会话令牌，`Claims.TenantID` 固定为哨兵值 `"platform"`（`model.PlatformScope`）、`Claims.Role` 固定为 `"platform_operator"`。之所以能用同一个 `token.Issuer`、不必新起一套签发器：真实租户 id 永远以 `NewID(PrefixTenant)` 生成、必定带 `tnt_` 前缀，字面量 `"platform"` 永不会与之相撞，因此这两个字符串已经足够把两种会话彼此分开，也彼此隔离——`requireSession` 会拒绝一个携带 `PlatformScope` 的令牌，`requirePlatformSession` 只接受它，双向都不允许对方蒙混过关。

## 用户与会话生命周期（P05）

Redis 现在是会话的权威来源而不是可选缓存：JWT 必须带随机 `sid`，每个受保护请求既验 JWT，也要求 Redis 中存在身份、租户/范围和角色完全一致的记录。旧版无 `sid` JWT 在升级时统一失效。每账户最多 20 个会话，第 21 个挤掉最早到期者；退出只撤销当前会话，改密、重置密码、改角色与禁用会先关闭短期登录门并撤销全部会话，再写数据库，避免并发登录穿过变更窗口。Redis 不可用时登录与管理请求 fail closed 为 `503`，不降级成只验 JWT。

租户 owner 可管理其他用户；不能禁用/降级自己，数据库事务与行锁保证并发操作也不会留下零个有效 owner。禁用用户在同一数据库事务里把其全部 active API Key 置为 revoked；重新启用不会恢复 Key。密码与角色变化不影响 Key。Key 校验缓存以 generation 命名，吊销推进并发布 generation；一个早先开始的数据库读取只能尝试写回旧代际，不能在吊销后重新填脏新缓存，Gateway 也通过同一 generation 清空进程内正向缓存。

平台运维可在始终保留一名有效运维的前提下创建和管理其他运维；账户管理不依赖 Fleet 或 Registry 配置。BootstrapToken 入口保留作首次创建与带外恢复。

租户端点：`DELETE /admin/v1/auth/session`、`POST /admin/v1/auth/{password,sessions/revoke}`、`PUT /admin/v1/users/:id/{password,role}`、`POST /admin/v1/users/:id/{disable,enable}` 与 `POST /admin/v1/users/:id/sessions/revoke`。平台端点为对应的 `/operator/v1/auth/*` 与 `/operator/v1/operators*`，并包含分页列表和会话内创建。

## 工作流菜单与运行（租户）

配置了 `Fleet` 之后，除运维端点外还会挂载两条**会话守卫**的租户路由：

| 端点 | 内容 |
| --- | --- |
| `GET /admin/v1/workflows` | 可提交的工作流模板与输入声明；按 P03 的租户可见范围过滤 |
| `GET /admin/v1/jobs` | 本租户当前的运行 |

它们挂在这里而不是常规会话组，只是因为需要一条已配置的 Gateway 读取路径——没有它，本服务根本看不到任何模板或 job，而一条回答「未配置」的路由比没有路由更糟。

**租户响应里没有基础设施身份。** 聚合过程会拿到副本 id 与配置的 endpoint（内部主机名与端口），这两样在返回租户之前一律清空：模板不带 `replicas`、job 不带 `replica`、响应不带逐副本状态列表。留下来的是 `partial` 与 `truncated`——那是租户能据以行动的部分。运维面 `GET /operator/v1/workflows` 返回同一份目录且保留副本信息，因为「发布推到了哪几个副本」正是运维要问的。e2e 测试 `TestTenantWorkflowAndJobViewsCarryNoInfrastructureIdentity` 守着这条线。

**同一模板在副本间可能不同。** 各副本从自己的文件配置或控制面同步（P03）加载模板，发布推到一半是正常状态。合并因此不选出胜者：目录记录注册了它的副本，并用 `divergent` 说明它们是否一致——只有部分副本拥有的模板同样算不一致，因为落在其余副本上的请求会得到 404。比较的是调用方可观察的部分（描述、输入/输出声明、依赖、版本与可见范围）；图不离开 Gateway，因此「同一个 id 下图不同」是本视图看不见的一种不一致。

**租户可见范围过滤（P03）只在这条聚合菜单上生效，是便利视图不是安全边界。** `listWorkflows` 在清空副本身份之前，先按会话租户 id 与每个模板的 `VisibleTenantIDs` 过滤——空列表即对所有租户可见。真正的授权边界在 Gateway 自己的数据面：`POST /v1/workflows/{workflow_id}/runs` 对不在允许列表上的租户返回与「模板不存在」相同的 404，与这份菜单是否一致无关；`GET /operator/v1/workflows`（运维面）不做此过滤，因为运维需要看到完整可见范围本身。

**`/admin/v1/jobs` 是实时视图，不是历史。** Gateway 的 job 表在进程内存、有上限、每副本各自持有：运行会随副本重启消失、被上限挤出，且从不跨副本可见。因此它能回答「现在在跑什么」，回答不了「上周跑过什么」——回答后者的是 `GET /admin/v1/jobs/history` 与 `GET /admin/v1/jobs/history/:id`（STATUS.md 的 J07，见下面「Job 持久化契约」一节的「已实现的持久化历史查询」小节），两者直接读 `jobs` 表，与 `Fleet` 是否配置无关，因此不挂在这两条实时端点旁边，而在常规会话组里无条件挂载。

## Job 持久化契约（J01–J08 实现与边界）

本节是 [STATUS.md](../../STATUS.md) J01 的交付物：定义 Gateway 内存 job 表之外那份持久化记录的写入时机、失败语义与状态机，供 J03（建表）、J04（内部 API）、J05（故障窗口）、J06（重启恢复）、J07（历史查询）、J08（真实 MySQL 验证）落地时对齐，不是它们的替代。以下到「与后续任务的关系」为止是 J01 的契约本身，只定义、不引入数据库代码；「已实现的存储层」「已实现的内部 API 与 Gateway 客户端」「已接入持久化」「已实现的重启恢复」「已实现的持久化历史查询」「真实 MySQL 9.7 上的集成与故障验证」六小节分别记录 J03、J04、J05、J06、J07、J08 在这份契约上落地了什么。

### 为什么是两个事实，不是一次写入

一次工作流提交实际发生两件独立的事：

1. **后端已接收**——Gateway 把图交给 ComfyUI（或其他工作流后端），换回 `run_id`。这件事发生在 `service/aiServeWeaveGateway/httpapi/jobs.go` 的 `sched.SubmitWorkflow` 里，早于本节讨论的任何持久化。
2. **控制面已记录**——Gateway 把这次运行的元数据写进控制面的 `jobs` 表。

这两件事的失败模式不同，因此不能合并成一次写入去谈"成功/失败"：后端接收失败，这次提交从未发生，调用方应该收到错误，无需谈持久化；后端接收成功但控制面记录失败，运行已经在物理世界发生，只是暂时没有历史记录——这是完全不同的故障，需要不同的语义。

### 三态：未提交 / 已确认 / 提交结果未知

| 状态 | 含义 | 触发条件 |
| --- | --- | --- |
| **未提交** | 后端从未接收这次运行 | `SubmitWorkflow` 本身失败；不产生 job，不写控制面，调用方直接收到错误 |
| **已确认** | 后端已接收，且控制面已成功记录 | `SubmitWorkflow` 成功，且随后对控制面的 create-job 调用在超时内收到确认 |
| **提交结果未知** | 后端已接收，但控制面这次写入的结果不明 | create-job 调用超时或连接中断——不知道控制面到底收没收到，重复发送有把同一次运行记两遍的风险，放弃发送则记录彻底丢失 |

"提交结果未知"不是第四个 job 状态，不出现在 `runtime.WorkflowState` 或对外的 `queued/running/succeeded/failed/cancelled` 词汇表里——它是持久化记录这一层的元状态，用一个独立的 `durability` 字段承载（见下）。job 本身该是什么运行状态，仍由后端汇报决定，与这次记录有没有落库是两条正交的轴。

**设计与实现差异（R04 核对）：** 下述公开 `durability` 字段仍是设计，当前 Gateway 的 `jobJSON`/`renderJob` 未输出它。J03–J08 的存储、同步和页面实现已落地，但不代表 J01 每项设计均已交付。恢复器只读非终态 Job，产物下载只查副本内存映射；历史记录存在不等于任意副本可下载原产物 ID。

### 何时向客户端确认持久化受理

**客户端拿到 202 与 `job_id` 的时机，是"后端已接收"达成的那一刻，不等待"控制面已记录"。** 现状（`jobs.go:73-155`）已经是这样，本设计延续而非改变它：

- 若改成等控制面确认后才回 202，控制面就从"旁路记录"变成推理请求路径上的同步依赖——控制面一次抖动，所有工作流提交都会失败，这正是要避免的耦合。
- 对外 job 视图上补一个 `durability` 字段，取值 `memory_only`（目前只在发起提交的那个 Gateway 副本内存里，控制面尚未确认收到）或 `persisted`（控制面已确认落库，任一副本、重启后都能查到）。这个字段让 Console 与调用方能判断"如果现在这个 Gateway 副本挂了，这条记录还找得到吗"，而不必猜测控制面写入的内部时序。
- `durability` 从 `memory_only` 变成 `persisted` 是单向的：一旦确认落库就不会退回去，即使后续对该 job 的状态更新写入失败——那只影响状态是否最新，不影响这条记录本身是否存在。

### 状态更新：幂等、单调，拒绝无条件覆盖

Gateway 内存 store（`jobstore.go` 的 `update`）会在状态或错误摘要变化时递增 `ObservedSeq`，但没有持久化层的终态保护：重复或乱序观测仍可能覆盖本地状态。这对纯内存、单副本内的临时记录可以接受，但持久化记录要跨副本、跨重启共享，必须补上内存表没有的两条约束：

1. **终态不可被覆盖。** job 一旦落 `succeeded`/`failed`/`cancelled`，后续任何状态更新写入对该 job 都是空操作（返回成功，但不改任何列），不产生新的 `updated_at`，也不覆盖 `error_summary`。这保证了"谁先把终态写进去，谁的结果就是最终结果"，不依赖写入方到达的顺序。
2. **非终态更新按 `observed_seq` 单调递增,而不是按墙钟时间。** 多副本、多次重连的场景下，各方时钟不可信；`observed_seq` 由发起写入的 Gateway 副本在自己观测到状态变化时自增并带上，控制面只接受 `observed_seq` 严格大于当前记录值的更新，更旧的直接丢弃。重复事件（同一 `observed_seq`）视为幂等空操作，不报错、不产生第二条历史记录。

这两条与现有内存 store 的行为不一致，是刻意的：内存表服务于"单副本内的实时轮询"，持久化表服务于"跨副本、跨重启的最终一致历史"，两者的一致性要求不同，J04 实现内部 API 时用这两条校验，不能照搬内存 store 的覆盖逻辑。

### 数据库故障不拖垮普通推理链路

推理请求的成功与否，不能依赖控制面这次写入是否成功。具体到每个操作：

| 操作 | 控制面不可达时的行为 |
| --- | --- |
| 提交（create-job） | 推理请求**不失败**。已经拿到的 `run_id` 和 job 视图照常返回给调用方；这次记录标记为"提交结果未知"，交给 J05 的补写机制处理，不能在请求路径上重试到用户等不及 |
| 状态更新（update-job） | 不影响正在进行的推理。轮询与 SSE 事件流继续由 Gateway 内存/节点直连回答（现状不变），只是这次状态变化暂时没有写进历史 |
| 取消 / 产物访问 | 完全不经过控制面——取消直连节点，产物直连节点转发，现状已经如此，且应当保持，不应该在这两条路径上新增对控制面的依赖 |

也就是说，本节定义的整条持久化链路是**推理请求的旁路记录，不是前置关卡**：create-job / update-job 调用必须带独立的超时与熔断，且失败不得向上抛成推理请求的 5xx。这条原则是 J01 的验收核心，J02～J06 的实现只要违反它（比如让推理路径同步等待控制面写入），就是偏离了本设计。

### 持久化记录需要、但对外 job 视图不暴露的字段

延续 Gateway README「工作流 Job」一节已经确立的口径——job 视图刻意不含运行位置：`node_id`、`runtime_id`、后端 `run_id` 不进入任何面向调用方或 Console 的响应。但持久化记录本身必须存这些字段，否则 J06（重启恢复）与取消/产物访问在副本重启后无从谈起。也就是说"存储层需要"和"对外可见"是两层独立的决定，J03 建表时两者都要满足，不能因为对外视图不显示就干脆不存。

### 已实现的存储层（J03）

`internal/model/job.go` 定义 `Job`、`JobArtifact` 两张表，`internal/store/store.go` 的 `Jobs`、`JobArtifacts` 接口是 logic 层使用的窄接口，`memstore`（测试用）与 `gormstore`（生产）各有一份实现，`internal/store/gormstore/jobmigrate.go` 是建表本身。几处对齐上面契约的地方：

- **`Job` 只存路由绑定，不存判断。** `NodeID`、`RuntimeID`、`BackendRunID` 三列就是「持久化记录需要、但对外不暴露的字段」一节点名的东西；本表本身不产出任何 HTTP 响应，字段是否对外可见是 J04 的事，这里只保证需要的都在。
- **`UpdateJobState` 是契约里「终态不可覆盖 + `observed_seq` 单调」的唯一实现入口。** `gormstore` 版本把两个条件一起写进一条 `UPDATE ... WHERE state NOT IN (...) AND observed_seq < ?` 的 `WHERE` 子句，由数据库自己的行锁裁定谁先落地，不是本进程里的先读后写再比较；`RowsAffected=0` 时才补一次存在性查询，只用来分清「job 不存在」（`ErrNotFound`）与「job 存在但这次更新陈旧或已终态」（`applied=false, err=nil`）——契约明确后者必须是无声的幂等成功，不能与前者共用一个错误。`memstore` 版本用一次锁内的读改写实现相同的判定，供 logic 层测试。
- **建表用固定版本 SQL。** `MigrateJobs` 与基础/路由/模板迁移共用 P07 执行器，文件名仍记录在 `schema_migrations_jobs`，并新增 checksum/dirty 元数据。MySQL DDL 隐式提交：执行前持久置 dirty，全部成功后才确认完成。已有建表、索引和产物存储字段的增量步骤支持检查后重放，故障处置见上方「数据库」与部署恢复说明。
- **`jobs`/`job_artifacts` 目前只支持 MySQL。** 这是 STATUS.md 对 Job 持久化目标数据库的既有决定（MySQL 9.7/InnoDB）。对 PostgreSQL 调用 `MigrateJobs` 返回明确错误；完整迁移命令只在 MySQL 上包含 Job 命名空间，不扩大其支持范围。
- **索引对应验收目标「按租户与时间/状态建立查询索引」。** `jobs` 表有 `(tenant_id, created_at, id)`（供 `ListJobs` 的 keyset 分页与时间窗筛选）与 `(tenant_id, state)`（供按状态筛选）两个复合索引；`job_artifacts` 按 `job_id` 与 `tenant_id` 分别建索引。
- **真实 MySQL 上的验证由 J08 完成，见下方「Job 持久化契约」小节。** 与 `gormstore` 里其余基础表的既有测试划分一致（业务规则在 `memstore` 上测，SQL 本身对着真实引擎测），这里为 `pendingJobMigrations` 的顺序与跳过逻辑写了不依赖数据库的单元测试；迁移 SQL 本身、并发更新、跨租户隔离与故障行为在真实 MySQL 9.7 上的验证见 `internal/store/gormstore/mysql_live_test.go`。

### 已实现的内部 API 与 Gateway 客户端（J04）

`internal/handler` 新增五个端点，与 `/internal/v1/apikeys/verify` 共用 `InternalToken` 这同一把共享密钥守卫（见上面「路由与守卫」）：

| 端点 | 对应契约动作 |
| --- | --- |
| `POST /internal/v1/jobs` | 「已确认」：记录一次提交 |
| `GET /internal/v1/jobs/:id?tenant_id=…` | 读取一个 job 当前的持久化行 |
| `PATCH /internal/v1/jobs/:id/state` | 状态更新，走 J03 的 `UpdateJobState`（终态不可覆盖 + `observed_seq` 单调） |
| `POST /internal/v1/jobs/:id/artifacts` | 记录一个产物 |
| `GET /internal/v1/jobs/:id/artifacts?tenant_id=…` | 列举一个 job 的产物 |

几处对齐上面契约、且不是随手做出的选择：

- **鉴权是「Gateway 这个服务是谁」，不是「哪个租户的会话」。** `tenant_id` 在 `POST`/`PATCH` 里是请求体字段、在 `GET` 里是查询参数，而不是从会话推导——这条内部通道上没有会话，`tenant_id` 是 Gateway 对自己调用方所做的断言，与 `verifyKey` 对哈希的信任边界相同。
- **幂等写入在 `internal/logic/jobs.go` 里落实，而不是要求调用方自己去重。** `CreateJob` 遇到重复 id 时不返回冲突，而是读回并返回已有的那一行——但仅当那一行确实属于同一租户；不同租户抢占同一个 id 时仍然是真实的 `ErrConflict`（409）。`CreateJobArtifact` 同理，且更简单：不重新读取，直接把调用方本就知道的内容当作已生效返回，因为重试者发来的字段本该与它第一次发的相同。
- **`PATCH .../state` 返回的 job 与 `applied` 是两件独立的事。** 一次陈旧或已在终态之后到达的更新，`applied=false` 且 `err=nil`——这是 J01 明确要求的「无声成功」，不是需要调用方特殊处理的错误路径；返回体里的 job 永远是数据库当前那一行，即便这次调用没能改动它，调用方也能看到真正落地的是什么。
- **错误与日志不携带凭据、Prompt 或工作流 JSON。** 这五个端点的请求体/响应体只有 id、状态词汇、时间戳与路由标识（node/runtime/backend run id），没有字段能装下这些东西；`respondErr` 沿用既有的粗粒度错误映射（`ErrNotFound`→404、`ErrConflict`→409、`ErrInvalidInput`→400、其余→500 且不回显原始错误文本），与本服务其余端点一致。

Gateway 侧的客户端是 `service/aiServeWeaveGateway/controlplaneclient/jobs.go` 的 `JobsClient`，与既有的 `Verifier` 分属两个类型而不是合并成一个：`Verifier` 坐在每次推理请求上、必须靠缓存摊薄延迟；`JobsClient` 的调用是 Gateway 内存 job 表的一条旁路，绝不能被推理响应等待，混进 `Verifier` 会让人在不该等待的地方顺手写出一次阻塞调用。`JobsClient` 把契约的「提交结果未知」态显式命名为 `ErrOutcomeUnknown`——包住超时、连接失败、本 Gateway 自己的 token 被拒绝，或任何未识别的状态码——`ErrConflict`/`ErrNotFound`/`ErrInvalidRequest` 才是控制面给出的确定答案。

### 已接入持久化（J05）

`JobsClient` 现在有真正的调用方：`service/aiServeWeaveGateway/httpapi/jobpersist.go` 的 `jobPersister`，一个与 `jobSyncer`（J02）同构的后台循环，把 Gateway 内存 job 表持续追平到控制面。它覆盖 J05 点名的那个故障窗口——「后端已接收但映射尚未落库」——以及随之而来的两条要求：

- **结果未知时不盲目重提。** `jobPersister` 结构体本身没有 `scheduler` 依赖，架构上就不可能把工作流重新提交给节点或铸造新 job id；一次持久化调用失败后，唯一的动作是就同一个 job id 与同一份路由绑定再问一次控制面。这把「后端提交」与「持久化写入」锁定成两个绝不会被混淆的故障域——含糊的持久化结果，绝不会变成生成第二次没人要的 ComfyUI 运行的理由。
- **补写有容量上限，且不靠承诺「不丢」蒙混过去。** 每一轮由 `dueForPersist` 限定考虑多少个 job、一个信号量限定并发、`PersistCallTimeout` 限定单次调用时长；连续失败按 `persistFailures` 翻倍退避（独立于 `jobSyncer` 自己的 `syncFailures`——控制面不可达与节点不可达是两个不相关的故障域，合用一套退避会让一处故障压制住另一处本该继续的重试）。**这是尽力而为的旁路，不是可靠队列**：重试状态就存在 `jobStore` 自己的记账里，进程重启即丢、且受 `DefaultMaxJobs` 逐出上限约束——一个还没来得及持久化就被逐出的 job，这次机会就没有了。这一点如实写在 Gateway README 里，不是等真出问题才被发现的缺口。

哪个 job 需要写、写多少，由 Gateway 一侧的 `job.ObservedSeq`（在 `jobStore.update()` 里只在 State 或 ErrorSummary 真正变化时才自增）与 `job.persisted`/`persistedSeq`（记录控制面已确认到哪）驱动，而不是本服务这边猜测。`dueForPersist` 刻意不排除终态 job：一次运行的最终状态恰恰是最不该丢失的记录，也是 `jobSyncer` 自己的轮询在 job 到达终态那一刻起就不再覆盖的情形——若持久化也照抄「跳过终态」这条规则，终态记录就会永远没有第二次机会补写。

三处观测点各自在应用完本地状态后非阻塞地提醒（`nudge`）持久化器：`submitRun`（提交）、`jobStatus`（前台轮询）、`jobevents.go` 的 SSE 终态写入。提醒不是等待——202、轮询响应、SSE 帧都在持久化调用真正发生之前就已经发给调用方，这正是「数据库故障不拖垮普通推理链路」在代码里的样子。

### 已实现的重启恢复（J06）

`internal/handler` 新增第六个内部 Job 端点，同样由 `InternalToken` 守卫：`GET /internal/v1/jobs/active?node_id=…&runtime_id=…`。它是这六个端点里唯一不按租户限定范围的——`internal/logic/jobs.go` 的 `ListActiveJobsForRoute` 直接转发到 `store.Jobs` 同名方法——理由与 `GetAPIKeyByHash` 不按租户限定范围相同：一个重启后正在恢复的 Gateway 副本，知道的是此刻连接到自己的是哪些节点与 runtime，不知道是哪些租户把工作提交到了它们身上，因此恢复必须能在没有租户可供限定范围的情况下发问「我欠这个路由绑定什么」。响应上限 `store.MaxActiveJobsForRoute`（500，非终态、按 `created_at` 最旧优先）不是分页——一个真实并发运行数超过它的路由需要一次容量方面的讨论，而不是把常量调大；超出的记录仍保存在数据库，但没有分页游标；只有更早的记录退出非终态集合后才可能进入下一轮结果，不能保证下一轮就覆盖到。

**路由顺序是刻意的。** `/internal/v1/jobs/active` 在 `routes.go` 里注册在 `/internal/v1/jobs/:id` 之前，依赖 go-zero 的路由器在同一深度上优先选择字面路径段而非参数段——`e2e/jobs_e2e_test.go` 的 `TestListActiveJobsForRouteRoutesAheadOfTheParameterizedGetJobRoute` 把这条行为钉住，不带 `tenant_id` 调用 `/jobs/active` 若落进了 `getJob`（`:id` 捕获成字面量 `"active"`），会得到那个 400 而不是本端点的 200，测试据此分辨两者。

Gateway 侧的消费者是 `httpapi/jobrecover.go` 的 `jobRecoverer`，与 `jobPersister`（J05）、`jobSyncer`（J02）同构的第三个后台循环：周期性地就 `scheduler.WorkflowCapableCandidates()` 报告的每一个当前已连接节点/runtime 发问，把本副本尚不知道的非终态 job 用 `jobStore.recoverIfMissing` 补回内存表——精确找回 `job.Candidate`（`NodeID`/`RuntimeID`）与 `job.RunID`（`BackendRunID`），取消、产物访问与状态查询所需要的正是这份路由绑定，且从不序列化任何连接对象：Gateway 的 `NodeRuntime` 本就在每次调用时按 `(nodeID, runtimeID)` 重新解析节点，恢复回来的 `Candidate` 不过是它一直以来的那两个字符串。恢复时会把 `ObservedSeq`/`persisted`/`persistedSeq` 播种为控制面已有的值而不是从零开始——否则 `jobPersister` 对一个刚恢复的 job 做出的头几次真实观测，会因本地序号"看起来更旧"而被这里的 `observed_seq` 单调门槛无声拒绝。

**恢复的执行权刻意不是排他的。** 一个节点/runtime 可能同时连接到不止一个 Gateway 副本（STATUS.md 的 P2 就提到这一点），此设计不为它们选出一个"负责"的副本，也没有认领或租约机制。多个副本各自独立地同步、持久化同一个 job，在构造上就是安全的：本节前面「状态更新：幂等、单调，拒绝无条件覆盖」定义的 `observed_seq` 门槛，无需协调即可化解并发写入——这与它已经化解单个副本上一次前台轮询与一次后台同步的竞争，是同一条机制。**一个再也没有重新连接到任何副本的节点不被当作失败处理**：没有任何东西会为一个够不着的 job 主动编造终态，它的持久化记录只会停在最后观测到的状态，与 Gateway README 一贯的立场一致。

### 已实现的产物保留期清理（P04）

`internal/handler` 新增两个内部端点，同样由 `InternalToken` 守卫，供 Gateway 一侧新增的后台清理循环使用：

| 端点 | 作用 |
| --- | --- |
| `GET /internal/v1/job-artifacts/expired?type=…&before=…`（`before` 为 RFC3339 时间戳） | 列举某一产物类型里 `created_at` 早于 `before` 的行,按最旧优先排序,单次最多 `store.MaxExpiredJobArtifacts`（200）条 |
| `DELETE /internal/v1/jobs/:id/artifacts/:artifact_id` | 删除一条产物元数据行 |

- **列表按类型、不按租户限定范围。** 与 `/internal/v1/jobs/active`（J06）同一个理由:清理循环要回答的是「这个类型里有哪些行老到该清了」,不是「某个租户名下有哪些产物」——它本就该扫过所有租户。响应体里的 `ExpiredJobArtifact` 携带 `StorageKey`,这是唯一一个把该字段序列化出去的响应:`internal/types.JobArtifactResponse`（J04）刻意不带它,因为那是租户可见的响应;这里的调用方是 Gateway 自己的清理循环,需要这个键才能去对象存储里删掉对应的字节。
- **删除是幂等的。** `logic.Service.DeleteJobArtifact` 对已经不存在的行返回成功而不是 404——清理循环的重试路径与「这一行本来就没了」在效果上没有区别,让调用方去区分这两种情况没有意义,`gormstore`/`memstore` 两个实现都遵循这条约定。
- **顺序由调用方保证,不是这两个端点自己的事。** 「先删对象存储里的字节,再删这行元数据」是 Gateway 侧 `artifactCleaner`（见 Gateway README）的职责,不是控制面能替它保证的——控制面这边的 `DeleteJobArtifact` 单看是一次单表删除,不知道也不需要知道调用方是不是先做完了另一件事。这与 J05 的旁路持久化是同一条分工原则:控制面只管自己这一步的正确性,跨系统的顺序保证留给发起调用的那一侧。
- **保留期时长本身不在控制面。** 「output 类型保留多久、temp/预览类型保留多久」是 Gateway 侧的配置（`ArtifactRetention`/`ArtifactPreviewRetention`）,这两个端点只回答「给定一个截止时刻,哪些行落在它之前」——控制面不对「什么算过期」有主张,只是按调用方给的 `before` 参数机械筛选,这与 J01 里「Job 持久化契约不定义调度策略,只定义状态如何被观测」是同一条边界。

### 已实现的持久化历史查询与 Console 接入（J07）

`internal/handler` 在常规会话组（不依赖 `Fleet` 配置）新增两个端点：`GET /admin/v1/jobs/history`（按 `state`、`workflow_id`、`since`、`until` 筛选，keyset 分页，参数与校验规则复用 `/admin/v1/audit` 已有的 `listQuery`/`timeParam`）与 `GET /admin/v1/jobs/history/:id`。两者都直接读 `jobs` 表，因此与前一节 `/admin/v1/jobs`（Fleet 实时视图）互补而非替代：一个回答「现在在跑什么」，一个回答「上周跑过什么」，即便对方所需的 Gateway 读取路径完全没有配置。

- **渲染剥离路由绑定。** `types.JobHistoryResponse` 不含 `NodeID`/`RuntimeID`/`BackendRunID`——那是内部 API 的 `JobResponse` 才携带的东西，租户没有理由知道自己的请求由哪个节点或哪次后端运行服务，这与实时视图已经清空副本身份是同一个原则。
- **`updated_at` 依旧是最后观测时刻，不是实时值。** 持久化记录本身就是一份「最后观测」的快照（见上面「Job 持久化契约」开篇几节），历史查询继承这条语义而不重新发明一套；`terminal_at` 是唯一一个一旦写入就不再移动的时间戳，可用来分辨「已经结束」与「仍在进行、只是暂时没人问起」。
- **未知的状态筛选被拒绝，不是筛出空集。** `logic.Service.ListJobs` 校验 `state` 落在 `model.Job` 的封闭词汇表内，理由与 `ListUsers` 拒绝未知角色筛选相同：一个畸形筛选悄悄读成「没有这类 job」，是对一个问题给出另一个问题的答案。

**取消与授权产物访问已在 Console 一侧实现。** 上面「数据库故障不拖垮普通推理链路」一节那句「取消/产物访问完全不经过控制面……不应该在这两条路径上新增对控制面的依赖」，回答的是一个更窄的问题：J02～J06 的持久化后台写入链路，不该把已经存在的「客户端持自己的 API Key 直连 Gateway」这条取消/产物路径也拉扯进控制面。它没有讨论过、也回答不了 Console 这个结构上不可能持有任何租户 API Key 的会话客户端，要如何把一次已登录的租户会话兑现成一次对 Gateway 数据面有权限的调用——这是它俩之间真正要解决的问题。

采用的方案是 **Console 服务端直接持有一把该租户的 Gateway API Key**（owner/admin 用已有的「创建 API Key」功能生成，粘贴进 Console 一个新的设置页由 Console 加密存储在会话 cookie 里），不做 scope 收紧——这把 Key 与租户自己创建的 Key 权限完全相同，Console 自己的会话/角色体系（owner/admin/member）是唯一的授权判断点，判断通过后直接用这把 Key 调 Gateway 的 `/v1/jobs/{id}/cancel` 与 `/v1/jobs/{id}/artifacts`。**Gateway 与控制面都没有为此新增任何代码**——这是选择这个方案而不是按次签发限定域 token 的主要原因。已知的代价：这把 Key 一旦被拿到，能做的不只是取消/读产物，也能拿去跑推理、烧配额；爆炸半径仍局限在单个租户内，但比加了 scope 字段的版本更大。这是当前阶段（没有真实租户在生产环境运行）刻意接受的权衡，不是遗漏，真有生产租户之后应当重新评估是否要收紧。实现见 Console 的 `AGENTS.md`「与后端的边界」一节记录的这条明文例外，以及 `lib/server/gateway.ts`、`app/api/gateway-key/`、`app/api/gateway/`、`app/console/settings/`；同时接在 Console 的实时 Job 视图（`app/console/jobs`）与持久化历史详情页（`app/console/jobs/history/[id]`）上。

**持久化历史列表页、详情页与产物预览已经落地。** `app/console/jobs/history`（列表，走上面两个新端点的分页与筛选）与 `app/console/jobs/history/[id]`（详情）已经接上；详情页能内联预览图片/视频产物，靠的是产物本身仍经由上面那把 Gateway API Key 从 Gateway 数据面流式取回，本服务只提供产物 id 列表。**这份 id 列表此前是空的**：J04 建好了 `CreateJobArtifact` 写入 API，但直到这次修复前，Gateway 从未调用它——`listArtifacts` 铸造的公开产物 id 只留在 Gateway 自己的内存里，从未上报到本服务的 `job_artifacts` 表。修复是在 `jobPersister`（J05 的同一个后台循环）里新增一条并行的旁路：`jobStore` 记录每个 job 尚未确认的产物（`pendingArtifacts`），`jobPersister.persistArtifacts` 按批调用 `CreateJobArtifact` 上报，失败的产物留在待确认列表里等下一轮重试，成功的立即移除——与 job 状态本身的持久化重试是同一套纪律，只是粒度更细（逐产物而不是逐 job）。`getJobHistory` 现在会带上 `ListJobArtifacts` 的结果渲染进 `JobHistoryResponse.Artifacts`（仅详情端点，列表端点不逐行多发一次查询）。

### 真实 MySQL 9.7 上的集成与故障验证（J08）

前面几节的每一条设计断言——迁移可重复、并发更新按 `observed_seq` 单调裁定、跨租户读写互相拒绝、数据库不可达时快速失败而不是挂起——到这里为止都只在 `memstore` 或不依赖数据库的单元测试上验证过。J08 把同一批断言对着真实 `mysql:9.7` 引擎重新跑一遍，测试文件与其余对外部后端的验证遵循同一条约定（见 `common/runtime/ollama` 的 `live_test.go`）：按需启用、默认跳过。

- **`internal/store/gormstore/mysql_live_test.go`** 覆盖 store 层：`MigrateJobs` 的可重复性（第二次调用在已是最新的 schema 上什么都不应用）；`CreateJob` 对同租户重复 id 的幂等与跨租户抢占同一 id 的真实冲突（经由 MySQL 自身的主键唯一性，通过 gorm 的 `TranslateError` 转译）；`UpdateJobState` 面对二十个并发写入者时，由真实行锁（`UPDATE ... WHERE observed_seq < ?`）而不是本进程里的任何协调，裁定出恰好落在最高序号上的结果，且终态之后的更新不会被更低序号的并发写入超车；`ListActiveJobsForRoute` 跨租户聚合、排除终态；以及一个指向不可达数据库的 `Store` 在 context 超时内快速返回错误而不是无限期挂起。
- **`e2e/mysql_live_test.go`** 覆盖 J06/J05 点名的、单进程内假件在结构上就答不出的两个问题——「Gateway 重启」与「多副本查询」：用两个完全独立的 `controlplaneclient.JobsClient`（互不知道对方存在，只共享同一个真实数据库）模拟"副本 1 创建、副本 2（重启后）用 `ListActiveJobsForRoute` 找回路由绑定"，以及"两个副本并发上报状态，真实 MySQL 的 `observed_seq` 门槛而非任何协调机制裁定谁的观测留下"；另有一个"提交结果未知"场景：同一个 `CreateJob` 请求发送两次，用 `ListActiveJobsForRoute` 直接清点真实表里这个路由下该 job id 出现了几次，确认幂等重试没有留下第二行。
- **默认 `go test ./...` 不受影响。** 两个文件都以 `AISW_MYSQL_TEST_DSN` 环境变量门控，未设置时每个用例 `t.Skip`，不需要真实数据库、也不需要 Docker；本次改动已用真实 `mysql:9.7` 容器（Docker）连同 `-race` 跑通过全部用例。

### 与后续任务的关系

J01～J08 已有定义、建表、内部 API、后台写入、非终态恢复、历史查询、Console 页面与 MySQL 验证记录；公开持久化标记和完整数据面恢复仍未交付，见本节开头与下方已知缺口。另外，旁路写入有以下丢失窗口：

- 一个 job 在被 J05 的持久化器追上之前就被 Gateway 内存表逐出，这条记录永久丢失——不是靠扩大内存表解决，而是接受这一权衡：内存表的有界性是「任何一跳都不得无界缓冲」的红线，持久化没赶上逐出速度的窗口期损失，比无界的内存表更可接受。产物的持久化（`jobPersister.persistArtifacts`）继承的是同一张内存表，因此同一条权衡也适用于它：一个还没来得及上报就被逐出的 job，其产物记录同样永久丢失。

## 已知缺口

1. **审计写入不在事务里。** 动作成功而审计写入失败时，动作保留、记录丢失（只进日志）。修法是把两者放进同一个事务，它要等本层先拥有事务。
2. **配额是每租户的，不是每 key 的。** 同一租户的多个 key 共用一份额度。按 key 计费需要 `api_keys` 上再加三列与一次额外的表达式，等有人真的需要「给某个集成单独限速」时再做。
3. **Gateway↔控制面用共享密钥，不是 mTLS。** Gateway 本就在集群自有网络内访问控制面；要更强隔离时再评估。
4. **X-Forwarded-For 不被采信。** 审计记录的是 `RemoteAddr`。要采信该头，必须与「配置一份可信代理清单」一并改动。
5. **平台运维身份已落地，但归因止步于本服务。** STATUS.md 的 P01 引入了 `platform_operators` 表与 `requirePlatformSession`，`/operator/v1/*` 与节点写路径都由平台运维的会话守卫，本服务的 `audit_logs`（`TenantID=model.PlatformScope`）记着是哪个 operator 做了什么。但这份归因传到 Registry 就断了：本服务用同一把共享的 `Registry.AdminToken` 调用 `TokenAdmin`，Registry 自己分不清这次调用背后是哪个 operator（见 Registry README 对应的已知限制）。
6. **节点的审批、禁用与维护现在有了持久化与下发路径（P01）。** `/operator/v1/nodes/:id/{approve,disable,enable,maintenance}` 与 `GET /operator/v1/nodes/states` 把这些操作转发给 Registry 的 `TokenAdmin`（节点身份账本的权威来源仍在 Registry，本服务不复制一份），仅在配置了 `Registry`（见下）时挂载。路由配置的版本、发布、回滚及逐副本确认已通过独立 P02 API 落地，见下方「模型路由发布」。
7. **Job 状态与事件历史有不同边界。** Gateway 后台同步已实现，持久化历史保存最后观测快照；节点不可达时保留旧状态并退避。尚无持久化事件时间线，历史记录也不能证明后端此刻可达。
8. **历史元数据不保证数据面访问可恢复。** 历史列表、详情与 Console 取消/产物入口已接入，见 J07；但 Gateway 仅恢复非终态 Job，产物下载仍依赖副本内存映射。终态 Job 与旧产物 ID 在重启/切换副本后的访问，以及原节点离线后的文件可用性，仍需补齐。
9. **没有请求检索与告警。** 指标与历史时序已由 P08 落地（见下方「指标与历史监控」）；请求检索与告警仍需要可检索的日志存储与规则/通知管理，属于 P09。

## 下一步

1. **请求检索与告警（P09）**：需要可检索的脱敏请求元数据存储，以及告警规则与通知管理，对应 Console C28/C29。

## 质量门禁

```bash
gofmt -l ./service
go vet ./service/aiServeWeaveControlPlane/...
go test -race ./service/aiServeWeaveControlPlane/...
```

`e2e` 包起一个真实的 go-zero 服务、真实的 JWT 会话，并用 Gateway 真实的 `controlplaneclient` 打完整闭环。它不需要数据库：store 是接口，测试用内存实现，因此默认的 `go test ./...` 不依赖任何外部服务。`gormstore` 对真实引擎的验证是单独的事。

## 平台运维审计查询（P01 Console 阶段三）

`GET /operator/v1/audit` 由 `requirePlatformSession` 守卫，固定读取 `model.PlatformScope` 的审计记录，不接受调用方指定租户范围。查询参数与租户审计相同：`limit`、`cursor`、`action`、`actor_id`、`since`、`until`，响应为 `{items, next_cursor}`；无需 Fleet 或 Registry 配置。租户 JWT 不可调用此端点，平台 JWT 也不可调用租户审计端点。覆盖测试见 `e2e/platform_audit_test.go`。

Console 已用独立平台会话接入 `/operator/*`，不再使用共享的 Console 运维 token 或邮箱名单。节点状态的传播仍是最终一致，审计仍沿用已有非事务写入边界。

## 模型路由发布（P02）

平台运维管理整套路由，草稿留在 Console 页面，发布后成为不可变快照。共享结构和校验唯一源为 `common/modelroute`，包括别名、真实模型、节点选择器、priority 与 weight。别名与真实模型不可为空，目标权重非负，优先级和权重须为 JavaScript 安全整数；别名唯一，每版最多 1000 个别名、每别名 100 个目标，规范 JSON 数组最多 1 MiB（请求/快照额外预留 64 KiB 元数据）。没有任何后端凭据、任意 URL 或推理内容字段。

| 接口 | 守卫与行为 |
| --- | --- |
| `GET /operator/v1/routes` | 平台会话；当前 Snapshot，未发布时 revision 为 0、routes 为空数组 |
| `POST /operator/v1/routes/validate` | 平台会话；`{routes}`，只校验并返回 `{valid:true,digest}` |
| `POST /operator/v1/routes/publish` | 平台会话；`{expected_revision,routes}`，成功 201 返回完整快照，版本冲突 409 |
| `POST /operator/v1/routes/rollback` | 平台会话；`{expected_revision,revision}`，复制历史内容为新版本，记录 rollback_of，成功 201 |
| `GET /operator/v1/routes/history` | 平台会话；before 版本游标、limit 1–50，倒序元数据 `{items,next_before?}`，不返回每版正文 |
| `GET /operator/v1/routes/revisions/:revision` | 平台会话；单个不可变历史快照 |
| `GET /operator/v1/routes/status` | 平台会话；期望版本与每个配置 Gateway 的实时观测，不依赖缓存 ACK |
| `GET /internal/v1/routes/current` | InternalToken；供 Gateway 拉取，尚未发布返回 404 |

发布和回滚在同一个数据库事务里完成当前版本 CAS、不可变历史插入与平台审计（`routes.publish` / `routes.rollback`）。并发使用同一 expected_revision 只有一个胜出；审计失败不会留下已经切换的当前版本。回滚不改写旧版本，也不降低版本号。历史最多 1000 版，达到容量时拒绝新发布/回滚（409），不自动删除历史；需要更多容量时另行设计保留策略，不能绕过上限手工改 active 指针。

`route_revisions` 存正文和版本元数据，`route_actives` 是 ID=1 的当前版本指针。JSON 作为 PostgreSQL TEXT / MySQL MEDIUMTEXT 保存，不使用方言相关 JSON 查询。迁移位于 `internal/store/gormstore/migrations/routes/{postgres,mysql}`，`MigrateRoutes` 在 `Database.AutoMigrate` 显式启用时调用，独立记录 `schema_migrations_routes`。P07 已把基础/路由/模板/Job 接入同一迁移执行器，保留各自 SQL 和账本；增加校验和、锁和 dirty 状态，MySQL 失败后必须显式 resume，Job 的 MySQL-only 范围不变。

生效查询最多并发 8 个 Gateway，每个调用受超时与 16 KiB 响应上限约束；结果包含所有 `Fleet.Gateways` 端点，缺失或失败不会被丢弃。只有非空端点集合、唯一副本身份、controlplane 模式、完全匹配的版本和摘要、无同步错误且时间有效，complete 才为 true；生成时间偏差超过一分钟标为 stale。读取期间发生新的发布会将 complete 降为 false。未列入 Fleet 的副本不在确认范围内，配置管理员须保证名册完整。

验证：`TestLiveRoutes` 使用 `AISW_POSTGRES_TEST_DSN` / `AISW_MYSQL_TEST_DSN` 按需启用，在独立 PostgreSQL 17 与 MySQL 9.7 上以 race 验证重复迁移、20 个并发首次发布、原子审计失败回滚、历史容量及大于 64 KiB 的版本回滚；默认测试不需要外部数据库。HTTP 测试覆盖守卫隔离、验证/发布/回滚、分页及大小限制。

## 工作流模板发布契约（P03）

平台运维创建、发布与回滚工作流模板，草稿留在 Console 页面，发布后成为不可变版本。与 P02 路由的核心差异只有一处：路由是单一全局表，模板是**多份各自独立版本化的文档**，因此每个方法都以 `template_id` 为键，而不是单一的 `id=1` 单例指针。共享结构和校验唯一源为 `common/workflowtemplate`，包括输入（节点/字段/类型/范围）、输出（节点/种类）、依赖（自定义节点/模型及其版本）与图（API Format ComfyUI 工作流）；限额为每模板最多 100 个输入、20 个输出、100 个自定义节点依赖、100 个模型依赖，图不超过 4 MiB，整份发布文档不超过 4 MiB + 256 KiB，平台最多发布 500 个不同模板 id，单个模板最多保留 1000 个历史版本。

**图从不进入任何目录响应。** 与 Job 的完整 Prompt、API Key 同属安全红线内的东西：`/operator/v1/workflow-templates`（列表）与 Fleet 聚合出的 `/admin/v1/workflows`、`/operator/v1/workflows` 均不携带图，只有 `GET /operator/v1/workflow-templates/:id`、`GET .../revisions/:revision`（平台会话，编辑/对比用）与 `GET /internal/v1/workflow-templates/current`（InternalToken，供 Gateway 同步）这两类端点会带图，因为调用方分别是模板的作者与本就需要执行它的 Gateway。

| 接口 | 守卫与行为 |
| --- | --- |
| `GET /operator/v1/workflow-templates` | 平台会话；每个模板当前头版本的列表，不含图 |
| `GET /operator/v1/workflow-templates/:id` | 平台会话；当前完整快照（含图）；从未发布过的 id 返回 404 |
| `POST /operator/v1/workflow-templates/:id/validate` | 平台会话；`{content, visible_tenant_ids}`，只校验并返回 `{valid:true,digest}` |
| `POST /operator/v1/workflow-templates/:id/publish` | 平台会话；`{expected_revision, content, visible_tenant_ids}`，成功 201 返回完整快照，版本冲突 409；`expected_revision=0` 且该 id 从未发布过即为"创建" |
| `POST /operator/v1/workflow-templates/:id/rollback` | 平台会话；`{expected_revision, revision}`，复制历史内容与可见范围为新版本，记录 rollback_of，成功 201 |
| `GET /operator/v1/workflow-templates/:id/history` | 平台会话；before 版本游标、limit 1–50，倒序元数据 `{items,next_before?}`，不返回每版正文 |
| `GET /operator/v1/workflow-templates/:id/revisions/:revision` | 平台会话；单个不可变历史快照（含图） |
| `GET /operator/v1/workflow-templates/status` | 平台会话；期望整包（数量+摘要）与每个配置 Gateway 的实时观测，不依赖缓存 ACK |
| `GET /internal/v1/workflow-templates/current` | InternalToken；供 Gateway 拉取全部模板当前头版本（含图），供 workflowsync 构建整包 |

发布和回滚在同一个数据库事务里完成该模板指针的 CAS、不可变历史插入与平台审计（`workflow_templates.publish` / `workflow_templates.rollback`）。**首次发布（创建）没有可预先播种的单例行**——这是与 P02 路由唯一的结构性差异：一次真实 MySQL 并发测试就抓到过由此产生的一类真实缺陷：许多事务并发对同一个从未见过的模板 id 做普通 `INSERT`，不是简单地败于重复键，而是在 InnoDB 默认隔离级别下因两阶段的"发现不存在、再插入"彼此交错成一个锁等待环，被 MySQL 判为 1213 死锁杀掉，而不是 `translate()` 认识的重复键错误。修复是把"确保指针行存在"从普通 INSERT 换成 `INSERT ... ON CONFLICT DO NOTHING` 式的 upsert，让它成为单条语句而不是两步——详见 `internal/store/gormstore/workflowtemplates.go` 的 `PublishWorkflowTemplateRevision` 文档注释。回滚不改写旧版本，也不降低版本号；单模板历史达到 1000 版容量时拒绝新发布/回滚（409）。创建新模板（`expected_revision=0` 且该 id 此前不存在）额外检查平台已发布的不同模板总数，达到 500 时拒绝；重新发布一个已存在的模板不受此上限影响。

`workflow_template_revisions` 以 `(template_id, revision)` 复合主键存正文（`description`/`inputs_json`/`outputs_json`/`dependencies_json`/`visible_tenants_json`/`graph_json` 六列）和版本元数据，`workflow_template_actives` 以 `template_id` 为主键、每个模板一行指针（无需预先播种，首次发布时惰性创建）。JSON 各自作为 PostgreSQL TEXT / MySQL MEDIUMTEXT 保存,不使用方言相关 JSON 查询。迁移位于 `internal/store/gormstore/migrations/workflowtemplates/{postgres,mysql}`，`MigrateWorkflowTemplates` 在 `Database.AutoMigrate` 显式启用时调用，独立记录 `schema_migrations_workflow_templates`，与 `MigrateRoutes` 同一时机、同一模式。

**租户可见范围随版本一起不可变。** `VisibleTenantIDs` 是发布请求的一部分，落在同一份不可变历史行里；回滚到旧版本时，恢复的是那个版本自己的可见范围，而不只是它的图——这是刻意的简化：可见范围变更走的是与内容变更同一条发布/审计路径，不另设一条无审计轨迹的"仅改可见范围"旁路。空列表表示对所有租户可见；租户可见范围过滤只发生在 Gateway 的运行提交路径（`POST /v1/workflows/{workflow_id}/runs`）与本服务聚合出的租户菜单（`/admin/v1/workflows`）两处，各自独立生效，互不依赖。

**依赖检查是结构性声明校验，不是能力核对。** `Dependencies{CustomNodes, Models}` 只在发布时检查非空、去重与数量上限，从不与 Fleet 里任何已连接节点实际上报的已装列表交叉核对——因为目前没有节点上报这类信息，接入那条边界属于超出本轮范围的新协议设计，如实记在这里而不是假装已经做到。`Outputs` 同理，只结构性校验声明的节点存在于图中，不核实运行后是否真的产出了声明种类的产物。

生效查询（`/operator/v1/workflow-templates/status`）与 P02 路由的 `/operator/v1/routes/status` 同构：最多并发 8 个 Gateway，每个调用受超时与 16 KiB 响应上限约束；结果包含所有 `Fleet.Gateways` 端点，缺失或失败不会被丢弃。只有非空端点集合、唯一副本身份、controlplane 模式、完全匹配的模板数量和整包摘要、无同步错误且时间有效，complete 才为 true。读取期间发生新的发布会将 complete 降为 false。

验证：`TestLiveWorkflowTemplates` 使用 `AISW_POSTGRES_TEST_DSN` / `AISW_MYSQL_TEST_DSN` 按需启用，在独立 PostgreSQL 17 与 MySQL 9.7 上以 race 验证重复迁移、20 个并发首次创建（含上述死锁修复的回归验证）、两个独立模板 id 各自独立版本化、原子审计失败回滚与历史容量；默认测试不需要外部数据库，本轮已在真实 MySQL 9.7 与 PostgreSQL 17 上分别跑通验证过。HTTP 测试（`e2e/workflowtemplates_test.go`）覆盖守卫隔离、验证/发布/回滚、独立模板互不干扰、`/status` 与 `/:id` 路径不互相遮蔽、分页及大小限制；租户菜单可见范围过滤借用 `e2e/fleet_test.go` 已有的桩 Gateway 基础设施验证——过滤发生在 Fleet 聚合出的菜单上，与本服务自己的模板存储是两回事（见上方"图从不进入任何目录响应"一段的端点划分）。

## 指标与历史监控（P08）

本服务接入 `common/metrics`，与 Gateway/Registry 同构：`-metrics-addr`（默认 `127.0.0.1:9090`，与 REST 监听端口分开，留空则关闭）上的 `GET /metrics` 以 Prometheus 文本格式导出 `internal/metrics.Descriptions()` 声明的目录。`internal/handler/instrumented.go` 的 `instrumented(...)` 包裹每一条 `routes.go` 里注册的路由，以路由自身的静态路径模板（如 `/admin/v1/jobs/history/:id`）而不是原始请求路径作为 `route` 标签——因此无论实际出现过多少个不同的 id，标签始终有界。另记录吊销 outbox 的滞后量（`controlplane_revocation_outbox_lag`，每 5 秒刷新一次 `generation - delivered_generation`）与 Fleet/Registry 客户端调用的成功/失败计数。

| 指标 | 标签 | 说明 |
| --- | --- | --- |
| `controlplane_http_requests_total` | `route,status` | 按路由模板与状态码分类的请求数 |
| `controlplane_http_request_duration_seconds` | `route` | 请求耗时 |
| `controlplane_http_inflight_requests` | `route` | 按路由模板的在途请求数（简化实现：并发同路由请求互相覆盖为 1，只回答"此刻是否有在途请求"） |
| `controlplane_revocation_outbox_lag` | — | 吊销 outbox 的 `generation - delivered_generation` |
| `controlplane_fleet_calls_total` | `result` | `ctx.Fleet.*` 调用，`result` 为 `success`\|`error`（不还原 Fleet 自己更细的逐副本错误分类，避免猜出一个本包不拥有的标签取值） |
| `controlplane_registry_client_calls_total` | `result` | 经 `ctx.Logic` 到达 Registry `TokenAdmin` 的调用（节点审批/禁用/启用/维护/列出状态），取值同上 |

**历史时序复用本服务已有的关系库，不是独立 TSDB。** `internal/metricshistory` 是一个可选的后台采集器，仅在 `MetricsHistory` 配置时启动（`Enabled()` 判定与 `Fleet`/`Registry` 同一约定）：

```yaml
MetricsHistory:
  GatewayAddrs: ["http://gateway-1:9090", "http://gateway-2:9090"]  # 各 Gateway 的 -metrics-addr，与 Fleet.Gateways(adminapi 端口)不同
  RegistryAddr: "http://registry:9091"                              # Registry 的 -metrics-addr
  Interval: 5m     # 既是抓取周期，也是落库粒度：计数器/量表本就是累计值，窗口收尾抓一次即是代表值
  Retention: 2160h  # 90 天，默认值；超期由独立的保留期清理协程每天清理一次
```

采集器定时向每个来源的标准 Prometheus 文本 `/metrics` 发 HTTP GET（复用现有 `-metrics-addr`，不新增内部协议或鉴权机制——与 README「可观测性」一节"指标端点应处于受控网络"的立场一致，边界由网络位置而非端点自身鉴权负责），用 `common/metrics.ParseExposition`（现有 Prometheus 文本写入器的逆操作，零新依赖）解析，按 `internal/metricshistory` 的封闭标签白名单聚合——例如 `tunnel_server_slots_total` 只保留 `class`/`state`，跨全部 `node_id` 求和，因此历史数据的基数不随机群规模增长——写入固定版本 SQL 迁移落地的 `metrics_history_points` 表（`metric, labels, bucket_at, value`，`(metric, labels, bucket_at)` 唯一索引支持幂等 upsert）。部署上，Gateway/Registry 的 `-metrics-addr` 需要从默认回环改绑到本服务可达的内部网络接口，见 [deploy/README.md](../../deploy/README.md)。

`GET /operator/v1/metrics/history?since=&until=` 由 `requirePlatformSession` 守卫，`since`/`until` 为必填的 RFC 3339 时间戳，按 `internal/logic.HistoryMetricNames` 这份封闭指标名单查询，按 `(metric, labels)` 分组为 Console ECharts 面板需要的多条时间序列返回。供 Console C27（`/operator/metrics`）使用，是平台运维视角，不按租户拆分——现有 Gateway 指标按设计不带 `tenant_id` 标签。

## 请求日志检索（P09/C28）

`request_logs` 保存 Gateway 已通过鉴权的 chat/responses/embeddings/models 四个前门端点的每一次请求，供 Console C28（`/console/requests`、`/operator/requests`）检索。与 `metrics_history_points` 同构：都是「高频追加、按时间清理」的时序数据，因此同样走 PostgreSQL/MySQL 双支持的固定版本 SQL 迁移（`gormstore/requestlogmigrate.go`），而不跟随 `jobs` 那种带状态机的 MySQL-only 先例。

```sql
request_logs(
  id           VARCHAR(64) PRIMARY KEY,  -- Gateway 侧 common/reqid 铸造的 request_id，不是代理键
  tenant_id    VARCHAR(32) NOT NULL,
  key_display  VARCHAR(64) NOT NULL,     -- apikey.Display 的展示形式，不是完整 key 或哈希
  endpoint     VARCHAR(16) NOT NULL,     -- 封闭枚举：chat/responses/embeddings/models
  status_code  SMALLINT NOT NULL,
  outcome      VARCHAR(24) NOT NULL,     -- 封闭枚举，Gateway 侧从状态码纯推导，不存自由文本错误
  duration_ms  BIGINT NOT NULL,
  created_at   TIMESTAMP NOT NULL  -- PostgreSQL: TIMESTAMPTZ；MySQL: DATETIME(6)
);
-- 索引比照 jobs 表按 (tenant_id, created_at) 与 (tenant_id, status) 的既有模式：
CREATE INDEX idx_request_logs_tenant_created         ON request_logs (tenant_id, created_at, id);
CREATE INDEX idx_request_logs_tenant_outcome_created ON request_logs (tenant_id, outcome, created_at, id);
```

`POST /internal/v1/requestlogs`（`requireSharedSecret(ctx.Config.InternalToken, ...)` 守卫，与 J04 的 Job 内部 API 同一信任级别）接受一批记录，每条自带 `tenant_id`——因为一个 Gateway 副本同时服务多个租户，一个批次可能跨租户。**写入按主键唯一约束加 `clause.OnConflict{DoNothing: true}` 天然幂等**（PostgreSQL 译为 `ON CONFLICT DO NOTHING`，MySQL 译为等价的忽略冲突语义），不先读已有行确认——调用方（Gateway 后台推送器）不关心单条写入结果，只关心整批调用有没有网络层失败，失败则本地丢弃、不重试，与 Gateway README「请求日志中间件与推送」描述的行为对应。批内任何缺少必填字段（`request_id`/`tenant_id`/`endpoint`）的记录被跳过而不是让整批失败，响应用 `accepted` 计数报告实际写入条数。控制面**不校验** `tenant_id` 是否存在——与 Job 内部 API 相同的信任边界。

两个只读检索端点共享 `since`/`until`（必填 RFC 3339 时间戳）、`status`（可选，按 `outcome` 精确匹配）、`request_id`（可选，精确匹配）与 `cursor`/`limit`（复用 `jobs`/`audit_logs` 既有的 keyset 分页）：

- `GET /admin/v1/requests`——`requireSession` 守卫，自动按调用者的 `tenant_id` 过滤，租户无法指定别的 `tenant_id`，响应省略 `tenant_id` 字段。
- `GET /operator/v1/requests`——`requirePlatformSession` 守卫，可选 `tenant_id` 查询参数做跨租户过滤，不传则返回全部租户，响应保留 `tenant_id` 字段。

**保留期默认 30 天**（`config.DefaultRequestLogRetention`，`Config.RequestLogRetention` 可覆盖）——比 `metrics_history` 的 90 天短，因为这是逐请求明细而非 5 分钟聚合桶，同等时间窗口下行数级别不同。`internal/requestlogretention` 是一个独立的后台协程，每 24 小时运行一次，按创建时间批量删除过期行；与 `MetricsHistory` 需要显式配置才启动不同，这个清理协程**只要表已迁移就无条件运行**——填充这张表的内部推送 API 只要设置了 `InternalToken` 就已经挂载，不存在一个独立的「是否配置了」的问题。

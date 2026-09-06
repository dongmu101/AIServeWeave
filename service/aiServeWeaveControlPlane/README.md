# aiserveweave-controlplane

控制面的 Admin API：Console 背后的租户、用户、API Key 与审计线索，以及供 Gateway 校验 API Key 的内部端点。

**当前进度：第二阶段「用户、租户、API Key 和配额」四项均已落地，列表已改为游标分页与服务端筛选，并新增了只读的机群清单、工作流目录与实时运行聚合。** 这个二进制现在能创建租户、让用户登录、签发与吊销 API Key、记录管理操作审计，并且 Gateway 已经改成对着它校验 key —— `-api-keys` 明文列表退化为无控制面时的回退路径。

| 目录 | 状态 | 内容 |
| --- | --- | --- |
| `internal/model/` | 已实现 | 四张表的 gorm 映射：`tenants`、`users`、`api_keys`、`audit_logs`。租户配额是 `tenants` 上的三个标量列，不是单独一张表：每个租户恰好一组，而一对一的表会给那条位于推理请求路径上的查询平添一次 join。另有 `jobs`、`job_artifacts` 两张表（`job.go`），是 Job 持久化契约的存储层落地，详见下方「Job 持久化契约」一节 |
| `internal/store/` | 已实现 | 六个窄接口（含 `Jobs`、`JobArtifacts`）+ `gormstore/`（PostgreSQL / MySQL；`jobs`/`job_artifacts` 走独立的带版本迁移，仅 MySQL）+ `memstore/`（测试用内存实现） |
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
| 用户密码 | bcrypt（长密码先取 SHA-256 十六进制摘要） | 人自己选的，一定活在某人已有的字典里，慢哈希是对的 |
| API Key | SHA-256 | 256 位均匀随机，不存在字典；而校验在每次推理请求上发生，逐请求 bcrypt 等于给每个 token 垫几十毫秒 |
| 共享密钥（Internal/Bootstrap） | 配置文件明文 | 它们是部署配置而非用户凭据；比较走常数时间，长度不足 32 字符时启动直接失败 |

初始密码不限制最小长度、最大密码长度或字符组成，也允许空字符串；创建租户与创建用户采用相同规则，登录按原样校验，不裁剪空格。HTTP 请求体仍有 64 KiB 上限。

既有账号继续使用原 bcrypt 哈希。新密码超过 bcrypt 的 72 字节边界时，先对完整 UTF-8 内容取 SHA-256 十六进制摘要，再做 bcrypt，并以 `bcrypt-sha256:` 标记存储格式；登录根据该标记选择校验方式，不截断长密码。此格式仍保存在已有 `password_hash` 列，无需更改表结构。旧版控制面无法校验新增长密码格式，部署了此类账号后不能直接回退旧二进制。

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

PostgreSQL 与 MySQL 都支持，由 `Database.Driver` 选择，PostgreSQL 是首要目标——但这仅对 `tenants`/`users`/`api_keys`/`audit_logs` 四张老表成立。`jobs`/`job_artifacts` 两张新表是 STATUS.md 对 Job 持久化的既有决定，只支持 MySQL 9.7/InnoDB，见下方「Job 持久化契约」一节。

当前这四张老表只用标量列，两种引擎表达一致，因此双支持的代价很低。**这在某个 JSON 列落地的那天就不再成立** —— 后续二十张表里的 `workflow_templates`、`deployment_revisions`、`job_events` 都要存 JSON，JSONB 的索引能力是 MySQL JSON 比不了的。到那一步应当重新评估是否继续双支持，而不是悄悄糊过去。

MySQL 的 DSN 必须带 `parseTime=True`，否则每个 `time.Time` 列都会扫描失败。

**这四张老表的迁移用 gorm 的 `AutoMigrate`，默认关闭。** 它无法表达回滚、不会删列、不留执行记录。在本服务只有这四张表且没有生产数据期间够用；一旦其中任何一条不再成立，这里就换成带版本的 SQL 文件。`jobs`/`job_artifacts` 已经先一步换了：它们的验收目标明确要求「迁移可重复执行且有版本记录」，因此用的是独立的带版本 SQL 迁移，而不是 `AutoMigrate`，见下方「Job 持久化契约」一节。

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

三组守卫就是本服务全部的授权面，都在 `internal/handler/routes.go` 一屏之内：

| 守卫 | 路由 |
| --- | --- |
| 公开 | `POST /admin/v1/auth/login` |
| 会话（JWT） | `/admin/v1/users`、`/admin/v1/apikeys`、`/admin/v1/audit`、`/admin/v1/tenants/current`、`/admin/v1/tenants/limits` |
| BootstrapToken | `POST /admin/v1/tenants` |
| InternalToken | `POST /internal/v1/apikeys/verify`、`/internal/v1/jobs*`（STATUS.md 的 J04/J06，见「Job 持久化契约」一节的「已实现的内部 API」与「已实现的重启恢复」小节） |

`GET /admin/v1/tenants/current` 返回调用方自己所属的租户及其配额，任何已登录角色都可读；`PUT /admin/v1/tenants/limits` 设置该配额，仅 owner 与 admin 可写。两者的请求里都没有租户 id：租户来自会话，因此管理员无法通过改请求体把它指向别人的租户。读写权限刻意不对称——member 无法调高限制，但一个正在被限流的 member 需要看得到是哪条限制在起作用；而能调高自己租户限制的角色，绕过限制最省事的办法就是调高它。

### 角色能做什么

以代码为准（`logic.Service` 的各方法），不是概括：

| 操作 | owner | admin | member |
| --- | --- | --- | --- |
| 创建用户 | 可以 | 否（403） | 否（403） |
| 读用户列表 | 可以 | 可以 | 可以 |
| 编辑 / 删除 / 禁用 / 改密用户 | **无接口** | 无接口 | 无接口 |
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
  OperatorToken: "${AISW_OPERATOR_TOKEN}"       # 调用方向本服务出示的
  Timeout: 3s
```

**为什么不放在会话守卫的 Admin API 上。** 节点是所有租户共用的基础设施：任何租户的请求都可能被路由到任何节点，而节点身上没有租户维度可供过滤。放进会话组，就意味着每个租户的管理员都能读到整个机群的节点 ID、标签、GPU 型号与已加载模型。因此它用自己的路径前缀与自己的密钥，无论将来角色如何调整，租户会话都够不到。

`OperatorToken` 是第三个密钥而不是复用 `InternalToken`：两者授权的方向相反——`InternalToken` 让 Gateway 来问本服务某个 key，复用它就等于任何 Gateway 也能读到整个机群。

**聚合是局部的，且明说这一点。** 每个 Gateway 副本只知道连到它自己身上的节点，所以「有哪些节点」有 N 个局部答案、没有权威答案。本服务向全部副本并发发问并合并结果：

- 同一个 Agent 连到多个副本时只出现一次；展示的视图取自「认为它在线」的那份，同等条件下取心跳更新的那份，而 `replicas` 保留所有报告过它的副本。
- 响应里有三个时间与状态字段：`collected_at`（本服务发问的时刻）、每个副本各自的 `generated_at`（它查看自己节点表的时刻），以及 `partial`。**某个副本没作答不会让列表悄悄变短**——它会成为 `replicas` 里一条具名的失败，错误取自封闭集合 `unreachable` / `timeout` / `unauthorized` / `malformed`，绝不透传传输层文本（那会点出内部网络的主机与端口，而这份文档正在前往浏览器）。
- 模型目录由同一次读取推导，不额外往返：一个机群的第二个视图若单独再读一次，两者就会彼此矛盾。目录里的是**后端上报的模型 id**，不是调用方可用的名字——别名在 Gateway 的路由表里，那是 Gateway 的文件配置，本服务不持有它。

## 工作流菜单与运行（租户）

配置了 `Fleet` 之后，除运维端点外还会挂载两条**会话守卫**的租户路由：

| 端点 | 内容 |
| --- | --- |
| `GET /admin/v1/workflows` | 可提交的工作流模板与输入声明 |
| `GET /admin/v1/jobs` | 本租户当前的运行 |

它们挂在这里而不是常规会话组，只是因为需要一条已配置的 Gateway 读取路径——没有它，本服务根本看不到任何模板或 job，而一条回答「未配置」的路由比没有路由更糟。

**租户响应里没有基础设施身份。** 聚合过程会拿到副本 id 与配置的 endpoint（内部主机名与端口），这两样在返回租户之前一律清空：模板不带 `replicas`、job 不带 `replica`、响应不带逐副本状态列表。留下来的是 `partial` 与 `truncated`——那是租户能据以行动的部分。运维面 `GET /operator/v1/workflows` 返回同一份目录且保留副本信息，因为「发布推到了哪几个副本」正是运维要问的。e2e 测试 `TestTenantWorkflowAndJobViewsCarryNoInfrastructureIdentity` 守着这条线。

**同一模板在副本间可能不同。** 各副本从自己的文件配置加载模板，发布推到一半是正常状态。合并因此不选出胜者：目录记录注册了它的副本，并用 `divergent` 说明它们是否一致——只有部分副本拥有的模板同样算不一致，因为落在其余副本上的请求会得到 404。比较的是调用方可观察的部分（描述与输入声明）；图不离开 Gateway，因此「同一个 id 下图不同」是本视图看不见的一种不一致。

**`/admin/v1/jobs` 是实时视图，不是历史。** Gateway 的 job 表在进程内存、有上限、每副本各自持有：运行会随副本重启消失、被上限挤出，且从不跨副本可见。因此它能回答「现在在跑什么」，回答不了「上周跑过什么」——回答后者的是 `GET /admin/v1/jobs/history` 与 `GET /admin/v1/jobs/history/:id`（STATUS.md 的 J07，见下面「Job 持久化契约」一节的「已实现的持久化历史查询」小节），两者直接读 `jobs` 表，与 `Fleet` 是否配置无关，因此不挂在这两条实时端点旁边，而在常规会话组里无条件挂载。

## Job 持久化契约（J01-J08 已完成，取消与产物访问未做）

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

### 何时向客户端确认持久化受理

**客户端拿到 202 与 `job_id` 的时机，是"后端已接收"达成的那一刻，不等待"控制面已记录"。** 现状（`jobs.go:73-155`）已经是这样，本设计延续而非改变它：

- 若改成等控制面确认后才回 202，控制面就从"旁路记录"变成推理请求路径上的同步依赖——控制面一次抖动，所有工作流提交都会失败，这正是要避免的耦合。
- 对外 job 视图上补一个 `durability` 字段，取值 `memory_only`（目前只在发起提交的那个 Gateway 副本内存里，控制面尚未确认收到）或 `persisted`（控制面已确认落库，任一副本、重启后都能查到）。这个字段让 Console 与调用方能判断"如果现在这个 Gateway 副本挂了，这条记录还找得到吗"，而不必猜测控制面写入的内部时序。
- `durability` 从 `memory_only` 变成 `persisted` 是单向的：一旦确认落库就不会退回去，即使后续对该 job 的状态更新写入失败——那只影响状态是否最新，不影响这条记录本身是否存在。

### 状态更新：幂等、单调，拒绝无条件覆盖

Gateway 现有内存 store（`jobstore.go:228-240` 的 `update`）是无条件覆盖式写入：同一 job 收到两次终态事件，或事件乱序到达，后者直接覆盖前者，没有版本号或状态机校验。这对纯内存、单副本内的临时记录可以接受，但持久化记录要跨副本、跨重启共享，必须补上内存表没有的两条约束：

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

`internal/model/job.go` 定义 `Job`、`JobArtifact` 两张表，`internal/store/store.go` 的 `Jobs`、`JobArtifacts` 接口是 logic 层将来会依赖的窄接口，`memstore`（测试用）与 `gormstore`（生产）各有一份实现，`internal/store/gormstore/jobmigrate.go` 是建表本身。几处对齐上面契约的地方：

- **`Job` 只存路由绑定，不存判断。** `NodeID`、`RuntimeID`、`BackendRunID` 三列就是「持久化记录需要、但对外不暴露的字段」一节点名的东西；本表本身不产出任何 HTTP 响应，字段是否对外可见是 J04 的事，这里只保证需要的都在。
- **`UpdateJobState` 是契约里「终态不可覆盖 + `observed_seq` 单调」的唯一实现入口。** `gormstore` 版本把两个条件一起写进一条 `UPDATE ... WHERE state NOT IN (...) AND observed_seq < ?` 的 `WHERE` 子句，由数据库自己的行锁裁定谁先落地，不是本进程里的先读后写再比较；`RowsAffected=0` 时才补一次存在性查询，只用来分清「job 不存在」（`ErrNotFound`）与「job 存在但这次更新陈旧或已终态」（`applied=false, err=nil`）——契约明确后者必须是无声的幂等成功，不能与前者共用一个错误。`memstore` 版本用一次锁内的读改写实现相同的判定，供 logic 层测试。
- **建表用带版本的 SQL，不是 `Store.Migrate` 的 `AutoMigrate`。** `MigrateJobs` 独立于四张老表的迁移之外，理由是验收目标本身写明「迁移可重复执行且有版本记录」——`AutoMigrate` 恰恰两者都不提供。迁移文件在 `internal/store/gormstore/migrations/jobs/`，按文件名顺序执行，每个文件在自己的事务里执行并把文件名记入 `schema_migrations_jobs` 表，因此一次执行到一半的失败不会被误记为已完成，重复调用在 schema 已是最新时是空操作。
- **`jobs`/`job_artifacts` 目前只支持 MySQL。** 这是 STATUS.md 对 Job 持久化目标数据库的既有决定（MySQL 9.7/InnoDB），不是本次任务顺手做出的选择；对 PostgreSQL 部署调用 `MigrateJobs` 直接返回明确错误，而不是尝试用跨方言的 SQL 或悄悄跳过。`internal/svc/servicecontext.go` 把它接进现有的 `AutoMigrate` 开关：配置了 `AutoMigrate` 且驱动是 MySQL 时，启动会依次跑完四表迁移与这两张新表的迁移。
- **索引对应验收目标「按租户与时间/状态建立查询索引」。** `jobs` 表有 `(tenant_id, created_at, id)`（供 `ListJobs` 的 keyset 分页与时间窗筛选）与 `(tenant_id, state)`（供按状态筛选）两个复合索引；`job_artifacts` 按 `job_id` 与 `tenant_id` 分别建索引。
- **真实 MySQL 上的验证由 J08 完成，见下方「Job 持久化契约」小节。** 与 `gormstore` 里其余四张表的既有测试划分一致（业务规则在 `memstore` 上测，SQL 本身对着真实引擎测），这里为 `pendingJobMigrations` 的顺序与跳过逻辑写了不依赖数据库的单元测试；迁移 SQL 本身、并发更新、跨租户隔离与故障行为在真实 MySQL 9.7 上的验证见 `internal/store/gormstore/mysql_live_test.go`。

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

`internal/handler` 新增第六个内部 Job 端点，同样由 `InternalToken` 守卫：`GET /internal/v1/jobs/active?node_id=…&runtime_id=…`。它是这五个端点里唯一不按租户限定范围的——`internal/logic/jobs.go` 的 `ListActiveJobsForRoute` 直接转发到 `store.Jobs` 同名方法——理由与 `GetAPIKeyByHash` 不按租户限定范围相同：一个重启后正在恢复的 Gateway 副本，知道的是此刻连接到自己的是哪些节点与 runtime，不知道是哪些租户把工作提交到了它们身上，因此恢复必须能在没有租户可供限定范围的情况下发问「我欠这个路由绑定什么」。响应上限 `store.MaxActiveJobsForRoute`（500，非终态、按 `created_at` 最旧优先）不是分页——一个真实并发运行数超过它的路由需要一次容量方面的讨论，而不是把常量调大；超出的部分不会永久丢失，只是要等副本下一轮扫描才被注意到。

**路由顺序是刻意的。** `/internal/v1/jobs/active` 在 `routes.go` 里注册在 `/internal/v1/jobs/:id` 之前，依赖 go-zero 的路由器在同一深度上优先选择字面路径段而非参数段——`e2e/jobs_e2e_test.go` 的 `TestListActiveJobsForRouteRoutesAheadOfTheParameterizedGetJobRoute` 把这条行为钉住，不带 `tenant_id` 调用 `/jobs/active` 若落进了 `getJob`（`:id` 捕获成字面量 `"active"`），会得到那个 400 而不是本端点的 200，测试据此分辨两者。

Gateway 侧的消费者是 `httpapi/jobrecover.go` 的 `jobRecoverer`，与 `jobPersister`（J05）、`jobSyncer`（J02）同构的第三个后台循环：周期性地就 `scheduler.WorkflowCapableCandidates()` 报告的每一个当前已连接节点/runtime 发问，把本副本尚不知道的非终态 job 用 `jobStore.recoverIfMissing` 补回内存表——精确找回 `job.Candidate`（`NodeID`/`RuntimeID`）与 `job.RunID`（`BackendRunID`），取消、产物访问与状态查询所需要的正是这份路由绑定，且从不序列化任何连接对象：Gateway 的 `NodeRuntime` 本就在每次调用时按 `(nodeID, runtimeID)` 重新解析节点，恢复回来的 `Candidate` 不过是它一直以来的那两个字符串。恢复时会把 `ObservedSeq`/`persisted`/`persistedSeq` 播种为控制面已有的值而不是从零开始——否则 `jobPersister` 对一个刚恢复的 job 做出的头几次真实观测，会因本地序号"看起来更旧"而被这里的 `observed_seq` 单调门槛无声拒绝。

**恢复的执行权刻意不是排他的。** 一个节点/runtime 可能同时连接到不止一个 Gateway 副本（STATUS.md 的 P2 就提到这一点），此设计不为它们选出一个"负责"的副本，也没有认领或租约机制。多个副本各自独立地同步、持久化同一个 job，在构造上就是安全的：本节前面「状态更新：幂等、单调，拒绝无条件覆盖」定义的 `observed_seq` 门槛，无需协调即可化解并发写入——这与它已经化解单个副本上一次前台轮询与一次后台同步的竞争，是同一条机制。**一个再也没有重新连接到任何副本的节点不被当作失败处理**：没有任何东西会为一个够不着的 job 主动编造终态，它的持久化记录只会停在最后观测到的状态，与 Gateway README 一贯的立场一致。

### 已实现的持久化历史查询（J07 的只读部分）

`internal/handler` 在常规会话组（不依赖 `Fleet` 配置）新增两个端点：`GET /admin/v1/jobs/history`（按 `state`、`workflow_id`、`since`、`until` 筛选，keyset 分页，参数与校验规则复用 `/admin/v1/audit` 已有的 `listQuery`/`timeParam`）与 `GET /admin/v1/jobs/history/:id`。两者都直接读 `jobs` 表，因此与前一节 `/admin/v1/jobs`（Fleet 实时视图）互补而非替代：一个回答「现在在跑什么」，一个回答「上周跑过什么」，即便对方所需的 Gateway 读取路径完全没有配置。

- **渲染剥离路由绑定。** `types.JobHistoryResponse` 不含 `NodeID`/`RuntimeID`/`BackendRunID`——那是内部 API 的 `JobResponse` 才携带的东西，租户没有理由知道自己的请求由哪个节点或哪次后端运行服务，这与实时视图已经清空副本身份是同一个原则。
- **`updated_at` 依旧是最后观测时刻，不是实时值。** 持久化记录本身就是一份「最后观测」的快照（见上面「Job 持久化契约」开篇几节），历史查询继承这条语义而不重新发明一套；`terminal_at` 是唯一一个一旦写入就不再移动的时间戳，可用来分辨「已经结束」与「仍在进行、只是暂时没人问起」。
- **未知的状态筛选被拒绝，不是筛出空集。** `logic.Service.ListJobs` 校验 `state` 落在 `model.Job` 的封闭词汇表内，理由与 `ListUsers` 拒绝未知角色筛选相同：一个畸形筛选悄悄读成「没有这类 job」，是对一个问题给出另一个问题的答案。

**取消与授权产物访问明确未做，设计仍是一片空白，不是被哪条既有决策堵死的。** 上面「数据库故障不拖垮普通推理链路」一节那句「取消/产物访问完全不经过控制面……不应该在这两条路径上新增对控制面的依赖」，回答的是一个更窄的问题：J02～J06 的持久化后台写入链路，不该把已经存在的「客户端持自己的 API Key 直连 Gateway」这条取消/产物路径也拉扯进控制面。它没有讨论过、也回答不了这里真正的问题——Console 是一个结构上不可能持有任何租户 API Key 的会话客户端，它要如何把一次已登录的租户会话，兑现成一次对 Gateway 数据面有权限的调用。这是一个独立的、目前完全开放的设计问题，留给后续任务专门讨论，不预设答案，也不受那条针对持久化写入路径的窄决策约束。

### 真实 MySQL 9.7 上的集成与故障验证（J08）

前面几节的每一条设计断言——迁移可重复、并发更新按 `observed_seq` 单调裁定、跨租户读写互相拒绝、数据库不可达时快速失败而不是挂起——到这里为止都只在 `memstore` 或不依赖数据库的单元测试上验证过。J08 把同一批断言对着真实 `mysql:9.7` 引擎重新跑一遍，测试文件与其余对外部后端的验证遵循同一条约定（见 `common/runtime/ollama` 的 `live_test.go`）：按需启用、默认跳过。

- **`internal/store/gormstore/mysql_live_test.go`** 覆盖 store 层：`MigrateJobs` 的可重复性（第二次调用在已是最新的 schema 上什么都不应用）；`CreateJob` 对同租户重复 id 的幂等与跨租户抢占同一 id 的真实冲突（经由 MySQL 自身的主键唯一性，通过 gorm 的 `TranslateError` 转译）；`UpdateJobState` 面对二十个并发写入者时，由真实行锁（`UPDATE ... WHERE observed_seq < ?`）而不是本进程里的任何协调，裁定出恰好落在最高序号上的结果，且终态之后的更新不会被更低序号的并发写入超车；`ListActiveJobsForRoute` 跨租户聚合、排除终态；以及一个指向不可达数据库的 `Store` 在 context 超时内快速返回错误而不是无限期挂起。
- **`e2e/mysql_live_test.go`** 覆盖 J06/J05 点名的、单进程内假件在结构上就答不出的两个问题——「Gateway 重启」与「多副本查询」：用两个完全独立的 `controlplaneclient.JobsClient`（互不知道对方存在，只共享同一个真实数据库）模拟"副本 1 创建、副本 2（重启后）用 `ListActiveJobsForRoute` 找回路由绑定"，以及"两个副本并发上报状态，真实 MySQL 的 `observed_seq` 门槛而非任何协调机制裁定谁的观测留下"；另有一个"提交结果未知"场景：同一个 `CreateJob` 请求发送两次，用 `ListActiveJobsForRoute` 直接清点真实表里这个路由下该 job id 出现了几次，确认幂等重试没有留下第二行。
- **默认 `go test ./...` 不受影响。** 两个文件都以 `AISW_MYSQL_TEST_DSN` 环境变量门控，未设置时每个用例 `t.Skip`，不需要真实数据库、也不需要 Docker；本次改动已用真实 `mysql:9.7` 容器（Docker）连同 `-race` 跑通过全部用例。

### 与后续任务的关系

Job 持久化契约到这里，J01～J08 均已完成（J07 只完成只读历史部分）：定义、建表、内部 API、接入写入、重启恢复、持久化历史查询、真实 MySQL 验证。留下两处已知的、如实记录而非蒙混过去的边界：

- 一个 job 在被 J05 的持久化器追上之前就被 Gateway 内存表逐出，这条记录永久丢失——不是靠扩大内存表解决，而是接受这一权衡：内存表的有界性是「任何一跳都不得无界缓冲」的红线，持久化没赶上逐出速度的窗口期损失，比无界的内存表更可接受。
- Job 历史的取消与授权产物访问需要一条全新的「会话到租户级 Gateway 调用权限」的链路，尚未设计，更未实现，Console 侧也尚未接入持久化历史的两个新端点——均见上面「已实现的持久化历史查询」一节的说明，留给 Console STATUS 的 C26 任务。

## 已知缺口

1. **审计写入不在事务里。** 动作成功而审计写入失败时，动作保留、记录丢失（只进日志）。修法是把两者放进同一个事务，它要等本层先拥有事务。
2. **配额是每租户的，不是每 key 的。** 同一租户的多个 key 共用一份额度。按 key 计费需要 `api_keys` 上再加三列与一次额外的表达式，等有人真的需要「给某个集成单独限速」时再做。
3. **Gateway↔控制面用共享密钥，不是 mTLS。** Gateway 本就在集群自有网络内访问控制面；要更强隔离时再评估。
4. **X-Forwarded-For 不被采信。** 审计记录的是 `RemoteAddr`。要采信该头，必须与「配置一份可信代理清单」一并改动。
5. **go-zero 自己的指标没接进 `common/metrics`。** 本服务目前没有 `/metrics` 端点。
6. **没有平台运维身份。** 机群清单由共享密钥守卫，背后没有用户，因此本服务无法记录「是谁读的」，也无法把运维权限授予某个具体的人。当前是由 Console 侧的名单决定谁能使用那个密钥（见 Console 的 `lib/server/operator.ts`），这是一处缺口而不是设计。真正的解法是在角色模型里引入平台级身份，那时机群端点可以改为会话守卫并进入审计。
7. **机群清单只读。** 节点的审批、禁用与维护状态需要持久化与下发路径，路由配置的版本、发布与回滚需要把那张表从 Gateway 的文件搬进本服务。两者都还没做。
8. **Job 状态不会自行推进这一条已在 Gateway 侧补上。** `/admin/v1/jobs` 返回的 `state` 仍是 Gateway 最后观测到的状态，但现在即使提交方停止轮询、也不挂着事件流，Gateway 自己的后台同步器（`httpapi/jobsync.go`，见 [Gateway README 工作流 Job 一节](../aiServeWeaveGateway/README.md#工作流-job)第九条）也会代为继续观测，因此运行仍会走向终态，只是本服务这次聚合到的仍是某一时刻的快照。真正的缺口收窄到「没有 Job 历史」（见下一条）：状态会推进,但推进的记录仍只存在于 Gateway 内存里，本服务读到的是聚合时的截面，不是可回放的时间线。
9. **可查询的 Job 历史已经建好，但 Console 还没有页面接它，取消与产物访问仍然没有。** `GET /admin/v1/jobs/history` 与 `GET /admin/v1/jobs/history/:id`（J07，见上面「Job 持久化契约」一节的「已实现的持久化历史查询」小节）直接读 `jobs` 表，按租户、时间、状态、工作流分页，副本重启不再让它消失——`/admin/v1/jobs`（实时聚合）与它是两条并存的路径，回答的是两个不同的问题，不是谁取代谁。缺的是 Console 侧的页面（`app/console/jobs` 目前只渲染实时视图，尚未接这两个新端点）与取消/产物访问：后两者需要一条全新的「会话到租户级 Gateway 调用权限」链路，目前设计仍是空白，尚未讨论出方案（见上面同一小节的说明）。产物本身仍由 Gateway 数据面用租户的 API Key 提供，本服务不在那条路径上，控制台目前只能列出产物 id。
10. **没有指标、请求检索与告警。** 这三项需要时序库与可检索的日志存储，仓库里都没有；Gateway 各副本的 Prometheus 文本导出不等于历史曲线。

## 下一步

1. **吊销推送**，把 Gateway 那 30 秒窗口压到零。
2. **带版本的 SQL 迁移**，替换 `AutoMigrate`。
3. **`/metrics` 端点**，把本服务接进 `common/metrics`。

## 质量门禁

```bash
gofmt -l ./service
go vet ./service/aiServeWeaveControlPlane/...
go test -race ./service/aiServeWeaveControlPlane/...
```

`e2e` 包起一个真实的 go-zero 服务、真实的 JWT 会话，并用 Gateway 真实的 `controlplaneclient` 打完整闭环。它不需要数据库：store 是接口，测试用内存实现，因此默认的 `go test ./...` 不依赖任何外部服务。`gormstore` 对真实引擎的验证是单独的事。

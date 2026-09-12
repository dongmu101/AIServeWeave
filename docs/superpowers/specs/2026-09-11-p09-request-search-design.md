# P09a 请求与错误检索(C28)

本轮执行 STATUS.md 的 P09 前半部分:补 Console C28「按时间、状态、request ID 查询脱敏元数据」。范围不含 C29(告警,见同目录 `2026-09-11-p09-alerting-design.md`,本轮仅设计不实施)。本轮**实施**本文档描述的全部内容。

## 已确认的范围决策

- **只覆盖 OpenAI 前门四个端点**(chat/responses/embeddings/models)的每次请求。工作流 Job 提交/状态已有独立的持久化历史(J07 的 `jobs` 表与 `/admin/v1/jobs/history`),不重复建设。
- **只记录已通过鉴权的请求**。鉴权失败(无效/已吊销 Key)的尝试不进入新表——这类尝试目前已有 `withLogging` 结构化日志与 Gateway 指标计数覆盖;强行给这些请求分配 `tenant_id` 无从谈起,会把新表从"租户内表"变成需要 `tenant_id` 可空的特殊表,不值得为此增加复杂度。
- **可见范围两者都要**:租户在 `/console` 下只能查自己的记录,平台运维在 `/operator` 下可跨租户查询,两者复用同一张表、同一套查询参数,只是会话守卫与 `tenant_id` 过滤方式不同。
- **上报走异步有界批量**,不做同步单条调用。前门请求量级可能远高于 Job 提交频率,同步调用控制面会员每请求增加一次内部 HTTP 往返的延迟,且控制面故障会直接拖累推理链路——这违反 J01 确立的"数据库故障不拖垮普通推理链路"原则。
- **数据库双支持 PostgreSQL/MySQL**,跟随 P08 `metrics_history` 的先例,不跟随 `jobs`/`job_artifacts` 的 MySQL-only 先例——请求记录表在数据形态上是"高频追加、按时间清理"的时序表,与 `metrics_history` 同构,而不是 `jobs` 那种带状态机的强持久化实体。
- **错误信息用封闭分类枚举,不存自由文本**。表不是 Prometheus 标签,但依然要避免任意错误文本(可能混入片段化 prompt 或内部实现细节)进入可检索存储,与仓库现有"标签基数纪律"的精神一致。

## Gateway 采集链路

### 中间件位置

新增 `service/aiServeWeaveGateway/httpapi/requestlog.go`,提供一个中间件,插入现有链路:

```
observe → withLogging → auth.middleware → requestlog.middleware → rateLimit → mux
```

放在 `auth.middleware` **之后**,使得该中间件被调用时 `Identity{TenantID, KeyID}` 已经解析并挂在请求的 `context.Context` 上(`IdentityFrom(ctx)` 可直接读取),不需要引入跨中间件共享指针的机制。中间件包裹 `rateLimit` 与 `mux`,因此能捕获限流拒绝、业务 handler 成功或失败的最终状态码。

### 范围过滤

中间件只对四个已知路径前缀生效(chat/responses/embeddings/models),按前缀映射为封闭的 `Endpoint` 枚举;其余路径(工作流 Job、产物下载等)直接跳过,不产生记录、不占用缓冲区容量。

### 状态码到 outcome 的映射

复用 `withLogging` 已有的状态码捕获响应写入器包装模式,拿到最终 HTTP 状态码后,按固定表映射为封闭的 `Outcome` 枚举,不读取、不拼接任何业务错误文本:

| 状态码 | Outcome |
| --- | --- |
| 2xx | `ok` |
| 400 | `invalid_request` |
| 401 | `unauthorized` |
| 403 | `forbidden` |
| 404 | `not_found` |
| 429 | `rate_limited` |
| 500 | `internal` |
| 502/503/504 | `upstream_unavailable` |
| 其他(含客户端提前断开、状态码未写出) | `error` |

该映射是纯函数,不需要触碰 `chat.go`/`responses.go`/`embeddings.go`/`models.go` 任何一个业务 handler——这是本设计刻意选择的最小改动面。

### 记录字段

| 字段 | 说明 |
| --- | --- |
| `RequestID` | `common/reqid` 铸造的 id,作为主键,与 `Job.ID` 的既有约定相同:Gateway 铸造的 id 本身全局唯一,直接做主键,重复上报天然被主键唯一性拒绝,不需要额外的幂等键 |
| `TenantID` | 来自 `Identity.TenantID` |
| `KeyDisplay` | `apikey.Display(key)` 的返回值(前缀 + 明文前 8 位),不存完整 key 或其哈希 |
| `Endpoint` | 封闭枚举:`chat`/`responses`/`embeddings`/`models` |
| `StatusCode` | 原始 HTTP 状态码,用于精确排障 |
| `Outcome` | 上表映射出的封闭分类 |
| `DurationMS` | 从中间件进入到响应完成(含 SSE 流式场景下的完整连接时长)的毫秒数 |
| `CreatedAt` | 请求开始时间 |

不记录:请求体、响应体、模型名、node_id、Prompt 片段、鉴权头。模型名不在本轮字段内——STATUS.md 对 C28 的验收口径只要求"按时间、状态、request ID"检索,加入模型名需要触碰四个业务 handler 才能在请求早期确定,不符合 YAGNI;真要按模型排障,现有 `chat.go` 的 `logTTFT` 结构化日志已覆盖。

### 上报缓冲与批量推送

Gateway 新增一个有界 channel(容量与批大小为新增配置项,默认参考 J02 同类背压参数的量级:缓冲容量 10000 条,单批最多 500 条,flush 间隔 5 秒,先到者先触发)。请求完成后中间件把记录塞入 channel;后台协程消费并攒批,定时或攒够即调用新的内部 API。

- **channel 满时丢弃新记录**,同时对一个封闭的 `runtime.Metrics` 计数器自增(如 `gateway_requestlog_dropped_total`,无高基数标签),不阻塞正在处理的推理请求——这类记录是诊断性数据,允许极端负载下的少量丢失,不做无界缓冲(遵循 AGENTS.md 安全红线"任何一跳都不得无界缓冲")。
- Gateway 优雅停止时对现有缓冲做一次尽力而为的 flush,不保证清空,不新增独立的持久化队列。

### 内部推送 API

新增 `POST /internal/v1/requestlogs`,挂在控制面 `internal/handler/routes.go`,鉴权方式与 J04 的 Job 内部 API 一致:`requireSharedSecret(ctx.Config.InternalToken, handler)`。请求体是一批记录(每条携带自己的 `tenant_id`,因为一个 Gateway 副本同时服务多个租户,一个批次可能跨租户):

```json
{
  "records": [
    {
      "request_id": "…",
      "tenant_id": "tnt_…",
      "key_display": "aisw-abcd1234…",
      "endpoint": "chat",
      "status_code": 200,
      "outcome": "ok",
      "duration_ms": 842,
      "created_at": "2026-09-11T08:00:00Z"
    }
  ]
}
```

控制面侧写入用 `INSERT ... ON CONFLICT (request_id) DO NOTHING`(PostgreSQL)/`INSERT IGNORE`(MySQL)按主键天然幂等,不逐条读回确认——与 `CreateJob` 的"冲突即认为已存在"思路一致,但这里连读回都不需要,因为调用方(Gateway 后台协程)不关心单条写入结果,只关心整批调用有没有网络层失败(失败则本地丢弃,不重试,避免请求记录的重试造成无界的本地积压)。响应只回传批内接受/冲突计数,供 Gateway 端的可观测指标使用,不影响推理链路。

控制面**不校验** `tenant_id` 是否存在——Gateway 是与 Job 内部 API 相同信任级别的内部调用方,已经在自己的认证链路上确认过该请求属于哪个租户。

Gateway 侧封装:`controlplaneclient` 新增 `RequestLogsClient`,与既有 `JobsClient` 同构。

## 存储:新表 + 固定版本迁移

新表 `request_logs`,走 P07 落地的固定版本 SQL 迁移账本模式(参照 `metricshistorymigrate.go`,新增 `requestlogmigrate.go`,`migrations/request_logs/{postgres,mysql}/0001_request_logs.sql`)。

```
request_logs(
  request_id   VARCHAR(64) PRIMARY KEY,
  tenant_id    VARCHAR(32) NOT NULL,
  key_display  VARCHAR(64) NOT NULL,
  endpoint     VARCHAR(16) NOT NULL,
  status_code  SMALLINT NOT NULL,
  outcome      VARCHAR(24) NOT NULL,
  duration_ms  INT NOT NULL,
  created_at   TIMESTAMP NOT NULL,
  INDEX/idx (tenant_id, created_at),
  INDEX/idx (tenant_id, outcome, created_at)
)
```

索引设计比照 `jobs` 表按 `(tenant_id, created_at)` 与 `(tenant_id, status)` 建索引的既有模式。

保留期默认 **30 天**(比 `metrics_history` 的 90 天短,因为这是逐请求明细而非 5 分钟聚合桶,同等时间窗口下行数级别不同),复用 P08 `metricshistory/retention.go`、P04 `artifactCleaner` 已经确立的"定期清理协程"模式,新增独立协程按创建时间批量删除过期行,保留期数字后续可按容量验证结果调整,记入控制面 README。

## 查询 API

复用现成的 keyset 游标分页设施:`store.ListQuery{Limit, Cursor}`、`store.Page[T]`、`gormstore.readPage`,与 `jobs`/`audit_logs` 的既有分页实现同构。

- `GET /admin/v1/requests`:`requireSession` 守卫(租户会话),自动按调用者的 `tenant_id` 过滤,租户无法指定别的 `tenant_id`。
- `GET /operator/v1/requests`:`requirePlatformSession` 守卫(平台会话),可选 `tenant_id` 查询参数做跨租户过滤;不传则返回全部租户。

两个端点共享同一套过滤参数:

| 参数 | 说明 |
| --- | --- |
| `since`/`until` | 时间窗口,校验规则复用现有 `timeParam` |
| `status` | 可选,按 `Outcome` 枚举精确匹配 |
| `request_id` | 可选,精确匹配(用于"这一条请求发生了什么"的排障场景) |
| `cursor`/`limit` | 复用现有分页参数,默认值与上限与 `jobs`/`audit_logs` 一致,不新增新的分页参数格式 |

响应字段是 `request_logs` 表字段的直接映射(租户视角省略 `tenant_id` 展示,因为已隐含;运维视角保留)。

## Console

- `/console/requests`:租户自助检索页,复用 `app/console/jobs/history` 已有的列表/分页/筛选 UI 模式,新增一条 `upstream-routes.ts` 转发白名单条目并补测试。
- `/operator/requests`:平台运维检索页,挂在既有 `/operator` 导航下,额外多一个 `tenant_id` 筛选框。

两个页面均是只读列表,不提供导出、不提供详情跳转(字段本身已经是完整的一行,不需要单独的详情页)。

## 验证

默认测试不依赖真实网络、真实数据库或真实时钟:

- 状态码→`Outcome` 的映射表用表驱动测试覆盖全部分支。
- 中间件的 channel 满时丢弃行为、批量攒批触发条件(时间/条数)用注入的 `runtime.Clock` 与假 channel 容量覆盖,不用真实 `time.Sleep`。
- 控制面批量写入的幂等性(重复 `request_id` 冲突不报错、不覆盖)用 `memstore` 覆盖业务逻辑分支,真实引擎的唯一约束行为在 PostgreSQL/MySQL 的 live test 上验证。
- 分页(keyset 游标)复用 `jobs`/`audit_logs` 已验证过的 `readPage` 实现,只需为新表补充等价的表驱动用例。
- Console 单元测试覆盖两个新端点的响应解析、空态与筛选参数拼接。

真实环境验证:真实 Gateway 产生一批 chat/responses/embeddings/models 请求(含成功、限流、上游错误等不同 outcome),确认异步批量上报后控制面表内可查;真实 PostgreSQL 与 MySQL 各跑一遍迁移与保留期清理;Console 浏览器验收 `/console/requests`、`/operator/requests` 的筛选与分页交互。

最终跑 AGENTS.md 全量 Go 质量门禁(`gofmt`/`go vet`/`go build`/`go generate ./api/...`/`go test ./...`/`go test -race ./service/...`)与 Console 门禁(`lint`/`typecheck`/`test`/`build`),更新 STATUS.md、ControlPlane/Gateway README 与 Console STATUS 的 C28 条目、CHANGELOG。

## 约束

- 不记录完整 Prompt、鉴权头、完整 API Key、工作流 JSON——全部走本文档描述的封闭字段,不新增自由文本字段。
- 跨隧道的字段转换规则不受影响,本设计完全在 Gateway 的 HTTP 前门与控制面之间,不涉及 `common/tunnelwire`。
- 新写与修改的注释英文在前、中文紧随;导出标识符有 doc comment。
- 不引入新的第三方依赖:批量推送用标准库 `net/http`(`controlplaneclient` 现有约定),有界 channel 用标准库 `chan`。
- 不为 C29(告警)预先搭建任何跨表关联能力——`request_logs` 与 `metrics_history`、`alert_rules`/`alert_instances` 之间没有外键或联合查询设计,两个子系统各自独立可验证。

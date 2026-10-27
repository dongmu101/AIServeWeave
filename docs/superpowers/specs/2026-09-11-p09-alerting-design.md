# P09b 告警(C29)

本文档设计 STATUS.md P09 的后半部分:Console C29「告警列表、规则与处理状态」。C28(见同目录 `2026-09-11-p09-request-search-design.md`)已经落地,本文档现进入实施阶段(实施计划见 `docs/superpowers/plans/`)。范围不含真实邮件/IM 通知集成、不含跨 `request_logs` 表的告警规则。

## 已确认的范围决策

- **规则数据源仅限 `metrics_history`**(P08 已落地的 5 分钟粒度聚合表:请求量、成功率、延迟分位、token 用量、容量)。不引入新的指标采集链路,不跨 `request_logs` 表关联——C28 与 C29 存储链路完全独立,这是两个子系统按仓库一贯做法("先设计后实施、每个子项目独立 spec")拆分之后的直接结果,避免相互产生实现依赖。
- **告警管理是平台运维视角,不分租户**——与 P08 对 C27(指标)已经做出的判断一致:现有指标不带 `tenant_id` 维度,做按租户告警需要一整套独立的按租户记账机制,超出本轮范围。
- **判定逻辑全部在后端**,浏览器只做只读展示与"确认处理"操作,不在前端计算或缓存判定结果——对应 STATUS.md 原文"不在浏览器实现唯一的告警判定"。
- **通知只做平台内告警列表 + 通用 Webhook 回调**,不内置邮件/IM SDK。具体接入邮件、钉钉、Slack 等留给运维自己在 Webhook 网关侧完成,不增加本仓库的第三方依赖。
- **不做持久投递队列**,Webhook 发送失败走有界重试,最终失败只记录计数与状态,不承诺送达——与仓库对 P06 通知超时、P07 outbox 的既有取舍(有界重试而非无限承诺)保持同一立场,但规模更小:这里连接的是运维自建的下游,而非租户可感知的鉴权路径,可靠性要求相应更低。

## 数据模型

两张新表,走 P07 固定版本 SQL 迁移账本模式,**PostgreSQL/MySQL 双支持**(与 `metrics_history`、`request_logs` 一致——这两张表本身也是低频写入的运维配置数据,双支持成本更低)。

### `alert_rules`

| 字段 | 说明 |
| --- | --- |
| `ID` | `model.NewID("alr_")` |
| `Name` | 运维自定义的规则名称,展示用 |
| `Metric` | 封闭枚举:`request_rate`(请求量)、`success_rate`(成功率)、`latency_p95`(延迟 P95)、`token_usage`(token 用量)、`capacity`(容量)——不是 `metrics_history` 原始行的 `metric` 列值,而是评估循环从原始行派生出的统一语义,见下方「派生指标计算」一节;不允许自由文本指标名 |
| `Operator` | 封闭枚举:`lt`/`lte`/`gt`/`gte` |
| `Threshold` | `float64`,与 `Metric` 单位一致(如成功率用 0~1 之间的比例) |
| `ConsecutiveBuckets` | 连续命中多少个 5 分钟桶才判定触发,默认 1,防止单个抖动桶造成误报 |
| `WebhookURL` | 可选,空值表示这条规则不发通知,只在告警列表里出现 |
| `Enabled` | 是否参与评估,禁用的规则保留历史 `alert_instances` 但不再产生新的判定 |
| `CreatedAt`/`UpdatedAt` | 常规审计字段 |

### `alert_instances`

| 字段 | 说明 |
| --- | --- |
| `ID` | `model.NewID("ali_")` |
| `RuleID` | 外键到 `alert_rules.ID` |
| `Status` | 封闭枚举:`firing` → `acknowledged`(运维手动)→ `resolved`(系统自动或手动均可到达);`resolved` 是终态,与仓库里 Job 终态不可逆的既有约定一致——条件后续再次触发会创建一条新的 `alert_instances` 行,不重开旧行 |
| `ValueAtFire` | 触发那一刻的指标值,用于展示"当时是多少" |
| `FirstFiredAt` | 首次判定命中的时间 |
| `LastEvaluatedAt` | 最近一次评估循环确认条件仍成立的时间,用于展示告警是否还在持续 |
| `ResolvedAt` | 可空,进入 `resolved` 的时间 |
| `AcknowledgedBy` | 可空,平台运维账户 id |
| `AcknowledgedAt` | 可空 |
| `NotifyStatus` | 封闭枚举:`pending`/`sent`/`failed`/`skipped`(规则未配置 `WebhookURL` 时为 `skipped`) |
| `NotifyAttempts` | 已尝试的 Webhook 发送次数 |

## 评估循环

控制面新增一个后台协程(与 `metricshistory.Collector` 同级,新增 `internal/alertengine` 包),固定间隔(默认与 `metrics_history` 的采集粒度对齐,60 秒一次)执行:

1. 读取所有 `Enabled=true` 的规则。
2. 对每条规则,按 `Metric` 调用下方「派生指标计算」得到最近 `ConsecutiveBuckets` 个 5 分钟桶的派生值序列。
3. 若全部桶都满足 `Operator`/`Threshold` 条件:
   - 若该规则当前没有处于 `firing`/`acknowledged` 状态的实例,创建一条新的 `alert_instances`(`Status=firing`),并把它加入待通知队列。
   - 若已经存在(`firing`/`acknowledged`),只更新 `LastEvaluatedAt`,**不重复创建、不重复通知**——这是刻意的去抖动设计,防止同一次故障产生通知风暴。
4. 若条件不再满足,且存在处于 `firing`/`acknowledged` 的实例,将其转为 `resolved`(`ResolvedAt=now`),如该规则配置了 `WebhookURL`,额外发一次"resolved"事件。

评估循环的单次执行体(`RunOnce`)用注入的 `runtime.Clock` 驱动测试,不依赖真实时间;循环本身(`Run`)按 `metricshistory.Retention.Run`/`Collector.Run` 的既有先例用 `time.NewTicker` 驱动,只测 `RunOnce`、不测 `Run` 的定时器分支——与仓库里控制面后台清理/采集协程的既有测试范围一致。

**是否需要"未配置就不启动"的开关:不需要。** 与 `metricshistory` 依赖外部 Gateway/Registry 地址(`MetricsHistoryConf.Enabled()`)不同,告警评估循环只读同一个数据库里已经存在的 `metrics_history` 表,没有需要外部配置才能工作的前提条件。因此告警评估循环与 P09a 的请求日志保留期清理协程一样,**在服务启动时无条件运行**,只有评估间隔(默认 60 秒)一个可调参数;`/operator/v1/alert-rules`、`/operator/v1/alerts` 等路由同样无条件挂载,不引入"配置了才挂路由"这种目前控制面路由层没有先例的写法。

### 派生指标计算

`metrics_history` 落库的是 P08 采集器原始 Prometheus 指标名的逐桶行(`gateway_http_requests_total`、`gateway_http_request_duration_seconds_bucket/_sum/_count`、`gateway_tokens_total`、`tunnel_server_slots_total`,见 `internal/logic/metricshistory.go` 的 `HistoryMetricNames`),不是"成功率""P95"这类语义化数值——这两项目前只有 Console 前端 TypeScript(`lib/console/metrics-charts.ts` 的 `deltaByBucket`/`approxP95`)算过,后端 Go 从未实现。评估循环新增一个纯函数集合,把 `ListRollup` 返回的原始行折算成 `alert_rules.Metric` 的五个语义值:

- `request_rate`:直接读 `gateway_http_requests_total` 各 `status` 标签值之和,按桶计数(计数器,需要与上一桶做差得到"这个桶内发生了多少次",逻辑对应 Console 前端 `deltaByBucket` 处理计数器重置/单调递增的方式,在 Go 侧照此语义重新实现,而不是简单相减,以正确处理副本重启导致的计数器归零)。
- `success_rate`:同一批 `gateway_http_requests_total` 行按 `status` 标签分子分母相除(`status` 前两位为 `2` 的计数之和 ÷ 全部计数之和)。
- `latency_p95`:对窗口内的每一个 `bucket_at`,取该时刻全部 `le` 标签的 `gateway_http_request_duration_seconds_bucket` 累计计数,按 `le` 升序找到第一个"累计计数 ≥ 该时刻总计数(即最大 `le` 桶的计数)的 95%"的桶,取其 `le` 值作为该时刻的近似 P95——这是仓库里 Console 前端 `approxP95` 已经验证过的近似算法(不做桶内线性插值,足够判断阈值触发,不追求精确分位数),在 Go 侧按同样语义重新实现(算法逻辑照搬,不是共享代码——前端 TypeScript 与后端 Go 之间没有共享运行时)。
- `token_usage`:`gateway_tokens_total` 按桶计数器差值求和(不分 `direction` 标签,取 prompt+completion 合计)。
- `capacity`:直接读 `tunnel_server_slots_total`(量表,不需要差值)。

计数器差值计算需要跨桶读取(计算第 N 个桶的速率需要第 N-1 个桶的原始累计值),因此评估循环每次取 `ConsecutiveBuckets + 1` 个桶用于差分,只对外暴露 `ConsecutiveBuckets` 个派生值。

## 通知

`WebhookURL` 非空时,状态转入 `firing`(新建实例)或 `resolved`(该实例状态更新)都会各触发一次 POST,payload 固定 JSON schema:

```json
{
  "alert_id": "ali_…",
  "rule_id": "alr_…",
  "rule_name": "…",
  "metric": "success_rate",
  "status": "firing",
  "value": 0.83,
  "threshold": 0.95,
  "fired_at": "2026-09-11T08:00:00Z",
  "resolved_at": null
}
```

发送用标准库 `net/http`,有界重试(固定 3 次、指数退避、总耗时上限数秒),全部失败后把 `NotifyStatus` 置为 `failed`、`NotifyAttempts` 记录已尝试次数,不再重试、不入持久队列——运维可以在告警列表里看到发送失败,自行核实下游 Webhook 网关是否正常。

## API

全部挂 `requirePlatformSession`(平台会话守卫,与 C27/`operator` 系列端点一致),写操作复用现有 `audit_logs` 记录(`TenantID=model.PlatformScope`,与 P01 节点写路径的审计记法一致):

- `POST /operator/v1/alert-rules`、`GET /operator/v1/alert-rules`、`GET /operator/v1/alert-rules/:id`、`PATCH /operator/v1/alert-rules/:id`、`DELETE /operator/v1/alert-rules/:id`——规则 CRUD。
- `GET /operator/v1/alerts`——告警实例列表,支持按 `status`、时间窗口分页(复用 `store.ListQuery`/`Page[T]`)。
- `POST /operator/v1/alerts/:id/acknowledge`——手动确认处理,`firing` → `acknowledged`;对已经是 `resolved` 的实例返回明确的冲突错误,不允许倒退状态。

## Console

- `/operator/alert-rules`:规则列表 + 创建/编辑/启停表单,复用 C24(路由管理)、C22(节点管理)已经确立的表单/列表 UI 模式。
- `/operator/alerts`:告警实例列表,展示当前状态、触发时间、最近确认时间、通知发送状态;`firing` 状态的实例上提供"确认处理"按钮调用上面的 `acknowledge` 端点。

两个页面都挂在既有 `/operator` 导航下,与 `/operator/metrics`(C27)相邻,復用同一个平台会话守卫与页面布局约定。

## 验证

- 评估循环的判定逻辑(阈值比较、连续桶计数、去抖动、自动 resolve)用注入的假 `metrics_history` 读取接口与 `runtime.Clock` 做表驱动测试,不依赖真实数据库或真实时钟。
- Webhook 发送的重试与失败记录用假 HTTP 服务器覆盖成功/超时/5xx 分支。
- 规则/实例的 CRUD 与分页复用 `jobs`/`audit_logs`/`request_logs` 已验证过的 `readPage` 与会话守卫模式,补等价表驱动用例。
- 真实环境验证:真实 PostgreSQL/MySQL 各跑一遍迁移;人工把某个规则阈值设到必然触发的水平,确认 `alert_instances` 按预期创建、Webhook 收到预期 payload、条件恢复后自动转 `resolved`;Console 浏览器验收规则表单与告警列表交互。
- 最终跑 AGENTS.md 全量 Go 质量门禁与 Console 门禁,更新 STATUS.md、ControlPlane README、Console STATUS 的 C29 条目、CHANGELOG。

## 约束

- 不引入 Prometheus 告警管理器(Alertmanager)等新部署组件——判定逻辑复用已有的 `metrics_history` 表与控制面自身的后台协程模式,与 P08"不引入新部署组件"的立场一致。
- 不新增第三方依赖:Webhook 发送用标准库 `net/http`。
- 新写代码注释英文在前、中文紧随;导出标识符有 doc comment。
- 不为按租户拆分的告警预留字段或接口——如未来需要,是一个需要独立评估的新决策,不在本设计范围内假设。

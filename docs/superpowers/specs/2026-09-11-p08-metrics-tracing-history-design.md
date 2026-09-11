# P08 指标、追踪与历史监控

本轮执行 STATUS.md 的 P08：Registry 与控制面接入指标、历史时序查询、跨服务 trace、补 Console C27。范围不含 C28/C29(P09)、Registry↔Gateway mTLS 加固、OpenTelemetry/第三方 APM 接入。

## 已确认的范围决策

- **历史时序复用现有关系库**，不引入 Prometheus/VictoriaMetrics/InfluxDB 等新部署组件。
- **跨服务 trace 只做结构化日志增强**（Scheduler/Tunnel/Agent 关键节点补 `request_id` 起止日志），不新增存储、不给 Console 加查看入口——这与 C28(请求检索)属于 P09 的边界一致。
- **C27 是平台运维视角**，挂在 `/operator` 下，不做按租户拆分——现有 Gateway 指标按设计不带 `tenant_id` 标签，做租户拆分需要一整套独立于 Prometheus 的按租户记账机制，超出本轮范围。
- **C27 前端本轮一并交付**，不拆成独立任务。

## Registry 接入指标

新增 `-metrics-addr` HTTP 监听器，与 Gateway 的 `-metrics-addr` 完全同构：复用 `common/metrics.Registry` 与现有 `exposition.go`，默认只绑回环、留空则关闭，暴露标准 Prometheus 文本 `GET /metrics`。默认端口需要与 `-addr`（Registry 的 gRPC 监听端口，默认 `:9090`）不同，实现时选定。Registry 目前是纯 gRPC 服务，这是它第一个 HTTP 监听器。

`internal/registryserver` 新增自己的指标目录（`Descriptions()`），在 `Register`、`RenewCertificate`、`Join`、`MintToken`、`RevokeToken`、`DisableNode`、`EnableNode`、`ApproveNode`、`SetMaintenance`、`ClearMaintenance` 这些既有 RPC 方法里打点：按方法分的调用计数与结果分布（`result` 取自各方法已有的封闭错误分类，例如 `Register` 的 new/reconnect/conflict）、节点状态变更计数（`action` 取值封闭：approve/disable/enable/maintenance_set/maintenance_clear）。**不对 `node_id` 加标签**——Registry 的身份账本包含历史上出现过的所有节点，不像 Gateway 的 `+node_id` 标签只覆盖"当前连接"这个有界集合，按节点粒度排查走结构化日志而非指标。最终指标表随实现写入 Registry README，格式参照 Gateway README「指标」一节。

## 控制面接入指标

接入 `common/metrics.Registry`，新增独立的 `-metrics-addr`（与 go-zero 的 REST 监听器分开，理由与 `adminapi` 跟推理 API 分开监听器相同：运维读取面与业务服务面不共享监听器）。打点范围：

- 各路由组的请求量与耗时，标签用 `routes.go` 里已经存在的封闭端点名，不用原始 path。
- 会话/登录操作结果、Key 操作计数、审计写入计数。
- Fleet 聚合与 Registry 客户端调用的成功/失败/耗时（复用现有 `unreachable`/`timeout`/`unauthorized`/`malformed` 错误分类做 `result` 标签）。
- 吊销 outbox 的 `generation - delivered_generation` 滞后量（gauge），把 P07 引入的这张单行表变成可观测的量。

标签基数纪律与 Gateway README 现有约定一致：不放 `tenant_id`、`user_id`、自由文本错误信息。

## 历史时序：采集、存储与查询

### 采集机制：复用 Prometheus 文本导出，不新增内部协议

控制面新增一个采集包 `internal/metricshistory`，定时（默认 60s，可配置）向 Gateway 各副本与 Registry 的 `/metrics` 发 HTTP GET，解析后按一份封闭的指标名单过滤、跨副本聚合（计数器求和；直方图按桶累加计数、和与总数），写入历史表。

读取的是已有的标准 Prometheus 文本端点，不新增 JSON 快照端点或新的鉴权机制——这与 README 已经写明的立场一致：「指标端点应处于受控网络」，边界由网络位置负责，与 `Fleet.Gateways`（adminapi）现在的信任模型相同。部署上，Gateway/Registry 的 `-metrics-addr` 需要从只绑回环改为绑定控制面可达的内部网络接口（如 docker compose 内部网络），文档需要更新这一条部署要求。

为此给 `common/metrics` 新增一个只读的 `ParseExposition(io.Reader) ([]Sample, error)`，是 `exposition.go` 现有写入器的逆操作——解析的是我们自己生成的确定性格式，不是接一个第三方 Prometheus 解析库，**零新依赖**。`Sample` 携带指标名、标签、类型，以及计数器/量表的值或直方图的累积桶计数、总和、总次数。

配置新增：`Fleet` 补一个 Gateway 侧指标地址列表（与现有 `Fleet.Gateways` 分开，因为 `-metrics-addr` 是与 adminapi 不同的第三个端口）；新增 `MetricsHistory` 配置块，字段包括 Registry 指标地址、采集间隔、汇总粒度、保留期。均为可选配置，未配置则不启用采集，`/operator/v1/metrics/history` 端点也不挂载——沿用 `Fleet`/`Registry` 现有的"没配置就没有这条路由"的约定。

### 存储：新表 + 固定版本迁移

新表走 P07 落地的固定版本 SQL 迁移账本模式（参照 `jobmigrate.go`/`routemigrate.go`，新增 `metricshistorymigrate.go`），不用 AutoMigrate。汇总粒度采用 5 分钟一桶（比原始抓取间隔更粗，用于控制行数），默认保留 90 天；这两个数字在实现与容量验证阶段可调整，最终值记入控制面 README。保留期清理复用 P04 已建立的定期清理任务模式。

存储的是聚合后的数值（每个指标名 × 标签组合 × 5 分钟桶一行），不存原始抓取样本；直方图存累积桶计数、总和、总次数，分位数在查询时由这些量推导，不在写入时预计算。

### 查询：`/operator/v1/metrics/history`

`GET /operator/v1/metrics/history`，`requirePlatformSession` 守卫，参数为时间窗口（`since`/`until`，校验规则复用 `listQuery`/`timeParam`）与可选的指标名筛选。响应按查询窗口聚合返回请求量、成功率、延迟分位、token 用量、容量（复用 `tunnel_server_slots_total`）的时间序列，明确区分"该窗口无流量"（返回零值序列）与"该窗口无数据"（采集器尚未运行到、或数据已过保留期，返回显式标记而非静默拉直曲线）。

## 轻量级 trace：结构化日志增强

在 Scheduler 派发决策、Tunnel 分发/完成、Agent 后端调用这几个关键节点补齐带 `request_id` 的结构化起止日志（span 语义：一条开始事件、一条结束事件，携带耗时），落地仍是日志而非独立存储。不新增查询接口，运维通过外部日志工具（grep/ELK 等）按 `request_id` 拼接——这与 Gateway README「下一步」里"把 request_id 接成 span 是独立一步"的描述一致，只是这一步刻意选择最轻的实现。

## Console C27：总览与指标

新增 `/operator/metrics` 页面，复用平台会话守卫与 C21-C24 相同的运维视角（不是租户页面）。前端用已安装的 `echarts-for-react` + `dataZoom` 渲染请求量、成功率、延迟、token 用量、容量五类曲线，走 Console 现有的只读分页/白名单转发约定（新增一条 Admin API 调用 = 往 `upstream-routes.ts` 加一行并补测试）。图表能区分"无流量"和"数据不可用"两种空态，展示查询窗口与数据新鲜度（沿用 STATUS.md 对 C27 验收标准已经写明的这条）。

## 验证

默认测试不依赖真实网络、Prometheus 或时钟：`ParseExposition` 用固定文本用例覆盖；`metricshistory` 采集器的聚合逻辑用注入的 HTTP 客户端与 `runtime.Clock` 覆盖；Console 单元测试覆盖响应解析与空态区分。真实环境验证：真实 Registry/Gateway 起 `-metrics-addr`，控制面采集器抓取并聚合，PostgreSQL/MySQL 两个引擎各跑一遍迁移与历史查询；Console 浏览器验收 `/operator/metrics` 页面曲线渲染、`dataZoom` 交互、空态提示。最终跑 AGENTS.md 全量 Go 门禁与 Console 门禁（`lint`/`typecheck`/`test`/`build`），更新 STATUS、根 README「可观测性」一节、Gateway/Registry/控制面 README 与 CHANGELOG。

## 约束

- 不引入 Prometheus/VictoriaMetrics/InfluxDB 等新部署组件，不引入第三方 Prometheus 解析库。
- 标签基数纪律与现有约定一致：不放 `tenant_id`、`user_id`、`node_id`（Registry 侧）、自由文本错误信息、模型名、请求路径。
- 新写与修改的注释英文在前、中文紧随；导出标识符有 doc comment。
- 不用真实 `time.Sleep` 推进测试时间；不记录凭据、哈希、Prompt 或工作流 JSON。
- Trace 结构化日志不新增独立存储，不为 P08 之外的检索场景（C28）预先搭基础设施。

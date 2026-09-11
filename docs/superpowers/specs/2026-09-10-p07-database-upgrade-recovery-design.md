# P07 数据库升级与恢复

本轮执行 STATUS.md 的 P07，同时完成控制面 README 指定的吊销 outbox。沿用当前工作区的 P05/P06 实现，不提交或覆盖这些既有改动。

## 迁移与升级

基础五表改用内嵌的 PostgreSQL/MySQL SQL，保留 GORM 作为查询映射。新增基础表迁移账本，并为已有迁移账本补齐 SHA-256 和 dirty；所有命名空间共用同一执行器，记录版本与完成时间。已应用文件不可修改；未知版本、校验和不符和 dirty 状态均阻止服务启动。迁移占用专用连接并持有数据库 advisory lock，避免多个控制面副本同时改表。

支持空库与本仓库既有 AutoMigrate 数据库：CREATE IF NOT EXISTS 保留数据，显式补齐已发布的配额与 last_login_at 增量列，校验基础列和索引。拒绝不兼容的人工改表，不猜测修复数据。Job、路由与工作流模板仍保留自己的已发布版本和存储范围；Job 仍仅支持 MySQL。

PostgreSQL 的一个迁移与完成记录在同一事务内；MySQL 在 DDL 前持久记录 dirty，失败后需要运维检查并显式 resume，不能宣称 DDL 可回滚。当前增量步骤须可重复执行。默认启动只检查 schema；旧 Database.AutoMigrate 开关保留兼容，含义改为执行固定版本迁移。提供独立 up/status/resume 命令，不要求 Redis 或会话密钥，只使用数据库配置。

## 吊销 outbox

使用 `key_revocation_outbox` 的一行合并待发送通知：`id=1`、`generation` 与 `delivered_generation`，均为 BIGINT。每次 Key 吊销或用户禁用，与业务变更同事务递增 generation。通知只需要让全部正向缓存失效，所以无需保存 Key 哈希或无界事件队列。

发送者锁定该行，发现 generation 超过 delivered_generation 后执行现有 Redis INCR + PUBLISH，成功才确认 delivered_generation。Redis 失败或进程崩溃留下待发送状态；发布后确认前崩溃可重复通知，语义为至少一次。请求完成时同步尝试发送；后台每秒重试，使用注入的 runtime.Clock，单次工作有超时，关闭等待协程退出。多个副本共用行锁，避免丢失并发吊销。

## 备份、恢复与边界

使用引擎原生 pg_dump/pg_restore 与 mysqldump/mysql，先恢复到独立空库，检查迁移状态与业务数据，再切换连接。文档包含停止写入、备份、失败处置、恢复、校验和应用回退边界。迁移不提供破坏性 down；回退通过恢复已验证备份或前向修复。

数据库恢复不等于全平台灾备：Redis 会话不得从旧安全快照复活，恢复后需重置会话/校验缓存并重启 Gateway；Registry CA、对象文件和引用一致性仍属于 A02。不得把演练时长写成生产 RPO/RTO 承诺。

## 验证

默认测试不依赖网络或真实数据库。真实 PostgreSQL 18 与 MySQL 9.7 在隔离测试库覆盖：空库、含已有租户/用户/Key/审计的数据升级、重复迁移、并发迁移、未知/篡改/dirty 账本、失败重试、outbox 原子回滚和补发、原生备份恢复后的数据与迁移一致性。最终运行 AGENTS.md 的全量 Go 门禁，更新 STATUS、控制面 README、部署说明和 CHANGELOG。

## 约束

- 不新增第三方依赖，不改变数据面依赖边界或公共 HTTP/proto 契约。
- 新写与修改的注释英文在前、中文紧随；导出标识符有 doc comment。
- 不用真实 time.Sleep 推进测试时间，不记录凭据、哈希、Prompt 或工作流 JSON。
- 只在 P07 独立数据库和容器中进行故障注入与恢复，不修改已有服务数据。

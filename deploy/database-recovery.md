# 控制面数据库升级与恢复（P07）

适用于 PostgreSQL 与 MySQL 的控制面数据库。Job 两表仍仅支持 MySQL；Registry 身份材料、对象存储文件与跨系统灾备属于 A02，不由关系库备份代替。

## 升级

1. 停止控制面所有副本与 Gateway 写入，记录旧二进制版本和配置，并按下文备份。当前版本尚未承诺混合版本滚动升级。
2. 用目标版本二进制和数据库配置执行迁移。命令只读取 `Database`，不要求 Redis、JWT 或内部令牌，也不启动 HTTP：

   ```sh
   aiserveweave-controlplane -f /secure/controlplane.yaml -migrate status
   aiserveweave-controlplane -f /secure/controlplane.yaml -migrate up
   aiserveweave-controlplane -f /secure/controlplane.yaml -migrate status
   ```

3. `status` 输出 JSON 版本列表；全部已应用且结构检查通过才退出 0。空库、旧账本、pending/dirty 或缺失结构退出非零，适合作为部署门禁。检查通过后以 `Database.AutoMigrate: false` 启动控制面，再恢复 Gateway。

基础表、路由、工作流模板和 MySQL Job 的账本分别是 `schema_migrations_base`、`schema_migrations_routes`、`schema_migrations_workflow_templates`、`schema_migrations_jobs`。每条记录包含文件名 `id`、SQL 的 SHA-256 `checksum`、`dirty` 和 `applied_at`；dirty 行的时间表示本次尝试开始，完成后替换为完成时间。不要编辑已发布 SQL 或手工伪造完成记录。

P07 会接管既有 AutoMigrate 基础表，补齐缺失的配额和 `last_login_at` 列、索引及 outbox；已有账号、摘要、吊销状态与配额保留。旧版本的 Job/发布账本仍保留版本名，首次升级为其补记校验和。这是对已有可信账本的接管，不能反向证明旧版曾执行的 SQL 未被人工修改。已缺失或不兼容的列、主键/声明索引与 MySQL 非 InnoDB 表会使结构检查失败。

`Database.AutoMigrate` 保留配置兼容，默认 false；true 现在也只执行固定版本 SQL。Compose 的本地起步配置仍为 true。生产使用独立迁移命令，使运行账号无需 DDL 权限。

## 失败处理

- **锁竞争**：另一个迁移进程持有同库会话锁时立即失败；等其退出后重试。DDL 与锁使用同一个专用连接，整个迁移调用最多五分钟。连接终止会释放数据库会话锁。
- **PostgreSQL 失败**：单个迁移和完成记录在同一事务内；失败不记录该版本。修复失败原因后重新 `up`。此前已完成版本保留。
- **MySQL 失败或进程崩溃**：先持久写 dirty，再执行 DDL，最后确认完成。MySQL DDL 会隐式提交，不能依赖 ROLLBACK 撤销已执行语句。检查失败版本与实际表结构，修复重复邮箱、权限或不兼容改表后，执行 `-migrate resume`。它只重放未完成版本；本版 CREATE、增量列与索引步骤能检查并跳过已存在部分。[MySQL 隐式提交说明](https://dev.mysql.com/doc/refman/9.7/en/implicit-commit.html)
- **旧账本元数据升级中断**：若已加 checksum/dirty 列但尚未补记历史校验和，普通 `up` 拒绝空校验和；核对旧版本和结构后使用 `resume` 完成接管。基础账本的空校验和不允许这样修复。
- **未知版本、校验和不符、历史缺口**：`up` 和 `resume` 都拒绝。找回匹配的二进制/SQL，或恢复经过验证的备份；不要通过清账本绕过。
- **恢复或升级失败**：保持入口关闭；MySQL 部分恢复的目标库应废弃，重新建空库再恢复。没有破坏性的 `down` 命令。应用回退采用前向修复或恢复旧库与匹配二进制；旧版本不认识 P05 新密码/会话格式，也不会写 P07 outbox，不能在新库上直接混跑。

## 原生备份与恢复

以下命令在安装了对应版本客户端的运维环境运行。连接凭据放在受限权限的 PostgreSQL service/passfile 或 MySQL option file 中，不放在命令参数或日志里。备份文件包含密码摘要、Key 哈希、内部路由和工作流正文，应按数据库同等级保护。数据库备份使用流式文件输出。

PostgreSQL：在 `~/.pg_service.conf` 中配置 `aisw_source` 和指向**独立空目标库**的 `aisw_restore`，凭据由 `.pgpass` 提供；使用与服务端匹配的客户端版本。

```sh
umask 077
pg_dump --dbname='service=aisw_source' --format=custom --no-owner --no-acl \
  --file=controlplane.dump
sha256sum controlplane.dump > controlplane.dump.sha256
sha256sum -c controlplane.dump.sha256
pg_restore --dbname='service=aisw_restore' --exit-on-error --single-transaction \
  --no-owner --no-acl controlplane.dump
```

备份期间不要执行迁移或其他 DDL。`pg_restore --single-transaction` 让本次恢复整体成功或回滚；大型数据库需按实际锁/空间容量另行制定恢复策略。[pg_dump](https://www.postgresql.org/docs/18/app-pgdump.html)、[pg_restore](https://www.postgresql.org/docs/18/app-pgrestore.html)

MySQL：分别准备 `/secure/source.cnf` 与 `/secure/restore.cnf` 的 `[client]` 连接配置，目标 `aisw_restore` 必须事先建为空库。

```sh
umask 077
mysqldump --defaults-extra-file=/secure/source.cnf \
  --single-transaction --quick --no-tablespaces --set-gtid-purged=OFF \
  --skip-add-drop-table aiserveweave > controlplane.sql
sha256sum controlplane.sql > controlplane.sql.sha256
sha256sum -c controlplane.sql.sha256
mysql --defaults-extra-file=/secure/restore.cnf aisw_restore < controlplane.sql
```

所有业务表必须为 InnoDB，备份期间禁止 DDL。保留退出码，失败的文件不算备份；恢复不使用 `--force`，遇错即停止。MySQL 的 DDL 无法把整个恢复包在一个可回滚事务里。[mysqldump](https://dev.mysql.com/doc/refman/9.7/en/mysqldump.html)

## 恢复后的检查与切换

1. 将目标版本控制面配置指向恢复库，运行 `-migrate status`。如果恢复的是旧版本备份，按上面的升级流程执行 `up` 再检查。
2. 对照备份清单核验各表行数和关键记录：租户及配额、用户/平台运维的角色和禁用状态、Key 吊销时间、审计、路由与模板版本/当前指针、MySQL Job 和产物元数据。产物元数据还需对照对象存储字节，P07 演练不保证文件存在。
3. 检查 `SELECT generation, delivered_generation FROM key_revocation_outbox WHERE id=1`。待发送的 generation 必须保留，控制面启动后会自动补发；不要把 delivered_generation 人工调成 generation。
4. 数据库回退会恢复备份时的安全状态，备份之后的 Key 吊销、禁用与改密必须依据独立保留的安全记录重放或主动重新吊销。仅清缓存不能弥补这类数据回退。
5. 为恢复后的控制面使用全新的、专用 Redis 数据库/实例并重启全部 Gateway，确保旧会话和正向缓存不会复活。不要向共享 Redis 执行 FLUSHALL，也不要把早于安全操作的 Redis 快照重新上线。路由/模板缓存与对象引用按恢复版本核对。
6. 在关闭外部流量时检查登录、当前权限、已吊销 Key 拒绝、版本目录和 Job 历史；确认 outbox 已发送、所有 Gateway 已重新同步，再开放入口。保留旧库作受控回退证据，不覆盖原库。

本流程没有给出未经容量演练的生产 RPO/RTO。备份后的变更量、重放安全操作和对象存储恢复都影响真实恢复目标。

## 可复现验收

`gormstore` 的 P07 测试使用 `AISW_POSTGRES_TEST_DSN` / `AISW_MYSQL_TEST_DSN`，每个新迁移/outbox/恢复用例创建随机数据库并在结束时删除它；测试账号需要 CREATE/DROP DATABASE。请始终使用独立测试实例。PostgreSQL DSN 使用 URL 格式，MySQL 带 `parseTime=true`。

原生恢复测试另外使用 `AISW_POSTGRES_TEST_CONTAINER` / `AISW_MYSQL_TEST_CONTAINER`，通过 `docker exec` 调用对应容器内原生工具；`AISW_REDIS_TEST_ADDR` 应指向独立 Redis，超时测试会暂停其写入两秒。生产装配测试验证启动补发和真实 Redis 超时。

```sh
go test -race ./service/aiServeWeaveControlPlane/internal/store/gormstore \
  -run 'TestLive(BaseMigration|DatabaseUpgrade|Migration|ConcurrentMigration|Revocation|NativeDatabase|ServiceStartup|Schema)' \
  -count=1 -v
```

未设置环境变量时默认跳过真实引擎测试。2026-09-11 的本地演练在 PostgreSQL 18、MySQL 9.7 和 Redis 8 专用容器完成；备份恢复逐表比较业务行与迁移账本，验证恢复后的 schema 检查及未发送吊销补发。完整平台灾备仍按 A02 单独验收。

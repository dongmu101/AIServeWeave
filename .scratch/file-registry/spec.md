# 文件登记表 files

状态：已交付代码，默认测试通过（2026-11-27）。live 与 R2 验证情况见 Gateway STATUS「文件登记表 files」。

## 目标

平台写进对象存储的每个文件都在控制面的 `files` 表里有一行：
- 清理只看这张表，不再按前缀列举对象存储。
- 每个文件都能查到它属于哪个业务对象。

## 决定

- **范围**：所有写进对象存储的文件，即 job 产物、Studio 运行输入（上传与复用副本）、Studio 推理生图。部署存储与租户存储都登记。
- **业务关联**：一行一个归属，用 `biz_type` + `biz_id` 表示：
  - `job` + job id：`kind` 为产物类型 `output`、`temp` 等。
  - `studio_run` + 运行 id：`kind` 为 `upload`、`copy` 或 `image`。
  - 复用输入本来就复制成新文件，因此一对一够用。
- **id**：使用文件自己的公开 id。job 产物用 artifact id，Studio 文件用 `sart_…` id。这样 Gateway 重试同一个产物时，登记是幂等的 upsert，对象 key 不变，不会留下两行指向同一个 key。
- **job_artifacts**：保留为业务记录，新增 `file_id`（MySQL 专用 jobs 迁移 0008）。字节写进存储的产物，`file_id` 等于它的 id。
  - 原有的 job_artifacts 保留期清理只处理 `file_id = ''` 的行，即仅有元数据的行和升级前写入的行。
  - 其余产物随文件一起清理：先删对象，再删 job_artifacts 行，最后删 files 行。
- **写入顺序**：先登记 `pending`，再 Put，再登记 `ready`，同时写入大小、sha256 与内容类型。
  - 任一步失败都会尽力删除对象和行。
  - 删不掉时，`pending` 行由清理在 24 小时后回收。因此崩溃或补偿失败都不会留下无人知道的对象。
  - 登记失败时写入失败，与「读不到租户设置时不写」一致。
- **清理**（Gateway `fileCleaner`，替代 `studioCleaner` 与 `tenantStorageCleaner` 的前缀扫描）：
  - 部署存储：job `output` 按 `-artifact-retention`，job `temp` 按 `-artifact-preview-retention`，`studio_run` 按 `-studio-artifact-retention`。规则与以前相同，其他类型的 job 产物仍不清理。
  - 租户存储：按租户的 `retention_days`，0 表示不删。
  - `pending` 超过 24 小时：不论存储与保留期，一律删除。
  - 租户已不再有自有存储的行只删记录，旧存储里的文件按既有约定留在原处。
  - 没有部署存储时，部署存储的行保持不动。
- **List**：`objectstore.Backend.List` 不再被生产代码使用，本期保留，不在这次删除。
- **没有新 flag**：设置了 `-control-plane-addr` 即登记与按表清理。

## 表结构（base 0022，MySQL 与 PostgreSQL）

| 列 | 类型 | 说明 |
|---|---|---|
| id | VARCHAR(64) | 文件标识（产物 id 或 Studio 文件 id）；主键为 `(id, created_at)` |
| tenant_id | VARCHAR(40) | 所属租户 |
| storage | VARCHAR(16) | `deployment` 或 `tenant` |
| object_key | VARCHAR(1024) | 在该存储中的 key |
| biz_type / biz_id | VARCHAR(32) / VARCHAR(64) | 归属的业务对象 |
| kind | VARCHAR(32) | 业务内的类别 |
| filename | VARCHAR(512) | 展示用文件名（超长时 Gateway 按 UTF-8 截断） |
| content_type | VARCHAR(255) | 媒体类型 |
| size_bytes | BIGINT | 字节数（ready 后） |
| sha256 | VARCHAR(64) | 摘要（ready 后）；写入过程中 body 被跳读时为空（宁可未知，不给错值） |
| status | VARCHAR(16) | `pending` 或 `ready` |
| created_at / updated_at | 时间 | 登记时间 / 最后更新时间 |

索引：
- `(storage, biz_type, kind, created_at)`：部署存储规则。
- `(tenant_id, storage, created_at)`：租户规则。
- `(status, created_at)`：pending 回收。
- `(biz_type, biz_id)`：业务查询。

索引只写在迁移 SQL 里，不写在模型标签上，原因是 PostgreSQL 上复合索引的启动检查有既有问题，见 STATUS。

分区：
- 按 `created_at` 按月 RANGE 分区，预建 2 个月，与其他增长表共用 `partitionmaint` 维护。MySQL 为 `PARTITION BY RANGE COLUMNS(created_at)`，带 `p_hist` 与 `p_max` 兜底；PostgreSQL 为声明式分区，带 `files_hist` 与 `files_default`。
- 主键因此为 `(id, created_at)`。表永不整段删除分区：行的删除只由 Gateway 的清理逐个执行，旧分区在行删空后留空。
- 登记不使用事务和锁定读：按 id 更新，读回原 `created_at`；不存在时插入，与并发插入冲突时再更新一次。重新登记沿用首次登记的 `created_at`，行不会换分区。MySQL 上锁定读一个不存在的 id 会加间隙锁，并发登记会因此死锁（1213），所以没有用 `SELECT … FOR UPDATE`。

## 内部接口（InternalToken）

- `PUT /internal/v1/files/:id`：`{tenant_id, storage, object_key, biz_type, biz_id, kind, filename, content_type}`，登记或重新登记为 `pending`。
- `POST /internal/v1/files/:id/complete`：`{size_bytes, sha256, content_type}`，标记为 `ready`；行不存在时返回 404。
- `DELETE /internal/v1/files/:id`：幂等，返回 204。
- `GET /internal/v1/files`：
  - 参数：`before`（必填，RFC3339）、`storage`、`tenant_id`、`biz_type`、`kind`、`status`、`limit`（上限 500）。
  - 按 `created_at, id` 升序返回 `{items}`。
- 旧版控制面对这些路径回 404：Gateway 把登记的 404 当作「不支持」，照常写入、不登记，产物也不带 `file_id`；列举的 404 当作空列表；删除的 404 当作已删除。
- `POST /internal/v1/jobs/:id/artifacts` 新增可选字段 `file_id`。Gateway 只在值非空时发送，旧版控制面因此不会因未知字段拒绝请求。

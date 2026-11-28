-- AIServeWeave ControlPlane schema (MySQL 9.7 / InnoDB)
-- AIServeWeave 控制面表结构（MySQL 9.7 / InnoDB）
--
-- Generated from the migration ledgers' empty table definitions and the versioned migrations under
-- service/aiServeWeaveControlPlane/internal/store/gormstore/migrations/ in the
-- order migrationNamespaces runs them. Those files remain the source of truth;
-- regenerate this file when a migration is added with
-- `UPDATE_INIT_SQL=1 go test ./service/aiServeWeaveControlPlane/internal/store/gormstore -run TestInitSQLMatchesMigrations`;
-- the default test run fails when it is stale.
-- 由空迁移账本的表定义和 service/aiServeWeaveControlPlane/internal/store/gormstore/migrations/
-- 下的内嵌版本迁移按 migrationNamespaces 的执行顺序拼接而成。权威来源仍是内嵌迁移，
-- 新增迁移时用上面的命令重新生成本文件；它过期时默认测试会失败。
--
-- MySQL is used because jobs/job_artifacts exist only on MySQL; on PostgreSQL
-- every other table has an equivalent file under migrations/<group>/postgres/.
-- 选用 MySQL 是因为 jobs/job_artifacts 只支持 MySQL；PostgreSQL 下其余各表在
-- migrations/<分组>/postgres/ 下有对应文件。
--
-- This script is a reference for reviewing the schema, not a deployment path.
-- It does not fill the schema_migrations_* ledgers, so the ControlPlane refuses
-- a database built only from it: CheckSchema rejects every version as pending,
-- and AutoMigrate re-runs DDL that is not idempotent. Initialise a real
-- database with `-migrate up`.
-- 本脚本用于查阅表结构，不是部署途径。它不写 schema_migrations_* 版本账本，
-- 只用它建出的库控制面会拒绝启动：CheckSchema 会把所有版本视为待迁移，
-- AutoMigrate 则会重跑不幂等的 DDL。真实数据库请用 `-migrate up` 初始化。

-- ============================================================
-- base
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_base (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- base/mysql/0001_create_tenants.sql
CREATE TABLE IF NOT EXISTS tenants (
    id VARCHAR(32) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL,
    created_at DATETIME(3),
    updated_at DATETIME(3)
) ENGINE=InnoDB;

-- base/mysql/0002_create_users.sql
CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(32) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    email VARCHAR(255) NOT NULL,
    password_hash VARCHAR(120) NOT NULL,
    name VARCHAR(128),
    role VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL,
    created_at DATETIME(3),
    updated_at DATETIME(3)
) ENGINE=InnoDB;

-- base/mysql/0003_create_platform_operators.sql
CREATE TABLE IF NOT EXISTS platform_operators (
    id VARCHAR(32) PRIMARY KEY,
    email VARCHAR(255) NOT NULL,
    password_hash VARCHAR(120) NOT NULL,
    name VARCHAR(128),
    status VARCHAR(16) NOT NULL,
    created_at DATETIME(3),
    updated_at DATETIME(3)
) ENGINE=InnoDB;

-- base/mysql/0004_create_api_keys.sql
CREATE TABLE IF NOT EXISTS api_keys (
    id VARCHAR(32) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    created_by VARCHAR(32) NOT NULL,
    name VARCHAR(128) NOT NULL,
    hash VARCHAR(64) NOT NULL,
    display VARCHAR(32) NOT NULL,
    status VARCHAR(16) NOT NULL,
    expires_at DATETIME(3),
    last_used_at DATETIME(3),
    revoked_at DATETIME(3),
    created_at DATETIME(3),
    updated_at DATETIME(3)
) ENGINE=InnoDB;

-- base/mysql/0005_create_audit_logs.sql
CREATE TABLE IF NOT EXISTS audit_logs (
    id VARCHAR(32) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    actor_id VARCHAR(32),
    action VARCHAR(64) NOT NULL,
    target VARCHAR(64),
    detail VARCHAR(512),
    ip VARCHAR(64),
    created_at DATETIME(3)
) ENGINE=InnoDB;

-- base/mysql/0006_tenant_quota.sql
ALTER TABLE tenants ADD COLUMN requests_per_minute BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tenants ADD COLUMN tokens_per_minute BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tenants ADD COLUMN max_concurrent BIGINT NOT NULL DEFAULT 0;

-- base/mysql/0007_last_login.sql
ALTER TABLE users ADD COLUMN last_login_at DATETIME(3);
ALTER TABLE platform_operators ADD COLUMN last_login_at DATETIME(3);

-- base/mysql/0008_base_indexes.sql
CREATE INDEX idx_tenants_status ON tenants (status);
CREATE INDEX idx_users_tenant_id ON users (tenant_id);
CREATE UNIQUE INDEX idx_users_email ON users (email);
CREATE INDEX idx_users_status ON users (status);
CREATE UNIQUE INDEX idx_platform_operators_email ON platform_operators (email);
CREATE INDEX idx_platform_operators_status ON platform_operators (status);
CREATE INDEX idx_api_keys_tenant_id ON api_keys (tenant_id);
CREATE UNIQUE INDEX idx_api_keys_hash ON api_keys (hash);
CREATE INDEX idx_api_keys_status ON api_keys (status);
CREATE INDEX idx_api_keys_expires_at ON api_keys (expires_at);
CREATE INDEX idx_audit_logs_tenant_id ON audit_logs (tenant_id);
CREATE INDEX idx_audit_logs_actor_id ON audit_logs (actor_id);
CREATE INDEX idx_audit_logs_action ON audit_logs (action);
CREATE INDEX idx_audit_logs_created_at ON audit_logs (created_at);

-- base/mysql/0009_key_revocation_outbox.sql
CREATE TABLE IF NOT EXISTS key_revocation_outbox (
    id BIGINT PRIMARY KEY,
    generation BIGINT NOT NULL,
    delivered_generation BIGINT NOT NULL
) ENGINE=InnoDB;
INSERT IGNORE INTO key_revocation_outbox (id, generation, delivered_generation) VALUES (1, 0, 0);

-- base/mysql/0010_model_access.sql
CREATE TABLE IF NOT EXISTS model_access (
    scope VARCHAR(64) PRIMARY KEY,
    models_json MEDIUMTEXT NOT NULL,
    updated_by VARCHAR(32) NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;

-- base/mysql/0011_node_access.sql
CREATE TABLE IF NOT EXISTS node_access (
    node_id VARCHAR(128) PRIMARY KEY,
    tenants_json MEDIUMTEXT NOT NULL,
    updated_by VARCHAR(32) NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;

-- base/mysql/0012_tenant_nodes.sql
CREATE TABLE IF NOT EXISTS tenant_nodes (
    node_id VARCHAR(128) PRIMARY KEY,
    tenant_id VARCHAR(32) NOT NULL,
    created_by VARCHAR(32) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    enroll_by DATETIME(6) NOT NULL,
    revoked_at DATETIME(6) NULL,
    revoked_by VARCHAR(32) NOT NULL DEFAULT '',
    INDEX idx_tenant_nodes_tenant_created (tenant_id, created_at)
) ENGINE=InnoDB;

-- base/mysql/0013_agent_install_codes.sql
CREATE TABLE IF NOT EXISTS agent_install_codes (
    code_hash VARCHAR(64) PRIMARY KEY,
    node_id VARCHAR(128) NOT NULL,
    tenant_id VARCHAR(32) NOT NULL,
    registry_addr VARCHAR(255) NOT NULL,
    gateway_addr VARCHAR(255) NOT NULL,
    created_by VARCHAR(32) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    redeemed_at DATETIME(6) NULL
) ENGINE=InnoDB;

-- base/mysql/0014_node_providers.sql
CREATE TABLE IF NOT EXISTS node_providers (
    node_id VARCHAR(128) NOT NULL,
    runtime_id VARCHAR(64) NOT NULL,
    scope VARCHAR(32) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    base_url VARCHAR(2048) NOT NULL,
    models_json MEDIUMTEXT NOT NULL,
    key_ciphertext TEXT NOT NULL,
    key_last4 VARCHAR(8) NOT NULL,
    updated_by VARCHAR(32) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (node_id, runtime_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS node_provider_versions (
    node_id VARCHAR(128) PRIMARY KEY,
    version BIGINT NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;

-- base/mysql/0015_node_sharing.sql
ALTER TABLE node_access ADD COLUMN mode VARCHAR(16) NOT NULL DEFAULT 'dedicated';

-- base/mysql/0016_list_pagination_indexes.sql
CREATE INDEX idx_users_tenant_created ON users (tenant_id, created_at, id);
CREATE INDEX idx_api_keys_tenant_created ON api_keys (tenant_id, created_at, id);
CREATE INDEX idx_audit_logs_tenant_created ON audit_logs (tenant_id, created_at, id);

-- base/mysql/0017_partition_audit_logs.sql
UPDATE audit_logs SET created_at = UTC_TIMESTAMP(3) WHERE created_at IS NULL;
ALTER TABLE audit_logs
 MODIFY created_at DATETIME(3) NOT NULL,
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, created_at)
 PARTITION BY RANGE COLUMNS(created_at) (PARTITION p_max VALUES LESS THAN (MAXVALUE));

-- base/mysql/0018_widen_id_columns.sql
ALTER TABLE tenants
 MODIFY id VARCHAR(40) NOT NULL;
ALTER TABLE users
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL;
ALTER TABLE platform_operators
 MODIFY id VARCHAR(40) NOT NULL;
ALTER TABLE api_keys
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY created_by VARCHAR(40) NOT NULL;
ALTER TABLE audit_logs
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY actor_id VARCHAR(40) NULL;
ALTER TABLE model_access
 MODIFY updated_by VARCHAR(40) NOT NULL;
ALTER TABLE node_access
 MODIFY updated_by VARCHAR(40) NOT NULL;
ALTER TABLE tenant_nodes
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY created_by VARCHAR(40) NOT NULL,
 MODIFY revoked_by VARCHAR(40) NOT NULL DEFAULT '';
ALTER TABLE agent_install_codes
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY created_by VARCHAR(40) NOT NULL;
ALTER TABLE node_providers
 MODIFY updated_by VARCHAR(40) NOT NULL;

-- base/mysql/0019_node_billing.sql
CREATE TABLE IF NOT EXISTS node_billing (
    node_id VARCHAR(128) PRIMARY KEY,
    updated_by VARCHAR(40) NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;

-- base/mysql/0020_table_and_column_comments.sql
ALTER TABLE tenants
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '租户标识',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '租户名称',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '租户状态',
    MODIFY COLUMN created_at DATETIME(3) COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(3) COMMENT '最后更新时间',
    MODIFY COLUMN requests_per_minute BIGINT NOT NULL DEFAULT 0 COMMENT '每分钟请求数上限，0 表示不限',
    MODIFY COLUMN tokens_per_minute BIGINT NOT NULL DEFAULT 0 COMMENT '每分钟 Token 数上限，0 表示不限',
    MODIFY COLUMN max_concurrent BIGINT NOT NULL DEFAULT 0 COMMENT '并发请求数上限，0 表示不限',
    COMMENT = '商户租户与请求配额', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE users
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '租户成员标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN email VARCHAR(255) NOT NULL COMMENT '登录邮箱',
    MODIFY COLUMN password_hash VARCHAR(120) NOT NULL COMMENT '登录密码摘要',
    MODIFY COLUMN name VARCHAR(128) COMMENT '成员显示名称',
    MODIFY COLUMN role VARCHAR(16) NOT NULL COMMENT '成员在租户内的角色',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '成员账户状态',
    MODIFY COLUMN created_at DATETIME(3) COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(3) COMMENT '最后更新时间',
    MODIFY COLUMN last_login_at DATETIME(3) COMMENT '最近成功登录时间',
    COMMENT = '商户租户成员账户', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE platform_operators
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '运维账户标识',
    MODIFY COLUMN email VARCHAR(255) NOT NULL COMMENT '登录邮箱',
    MODIFY COLUMN password_hash VARCHAR(120) NOT NULL COMMENT '登录密码摘要',
    MODIFY COLUMN name VARCHAR(128) COMMENT '运维人员显示名称',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '运维账户状态',
    MODIFY COLUMN created_at DATETIME(3) COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(3) COMMENT '最后更新时间',
    MODIFY COLUMN last_login_at DATETIME(3) COMMENT '最近成功登录时间',
    COMMENT = '平台运维账户', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE api_keys
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT 'API Key 标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '密钥名称',
    MODIFY COLUMN hash VARCHAR(64) NOT NULL COMMENT '密钥的 SHA-256 摘要',
    MODIFY COLUMN display VARCHAR(32) NOT NULL COMMENT '用于展示的脱敏密钥',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '密钥状态',
    MODIFY COLUMN expires_at DATETIME(3) COMMENT '到期时间',
    MODIFY COLUMN last_used_at DATETIME(3) COMMENT '最近一次记录的密钥使用时间',
    MODIFY COLUMN revoked_at DATETIME(3) COMMENT '密钥吊销时间',
    MODIFY COLUMN created_at DATETIME(3) COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(3) COMMENT '最后更新时间',
    COMMENT = '用于调用平台 API 的租户密钥', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE audit_logs
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '审计记录标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '审计事件所属租户，平台操作使用平台作用域',
    MODIFY COLUMN actor_id VARCHAR(40) NULL COMMENT '操作人标识',
    MODIFY COLUMN action VARCHAR(64) NOT NULL COMMENT '审计动作代码',
    MODIFY COLUMN target VARCHAR(64) COMMENT '操作对象标识',
    MODIFY COLUMN detail VARCHAR(512) COMMENT '审计详情',
    MODIFY COLUMN ip VARCHAR(64) COMMENT '操作来源 IP 地址',
    MODIFY COLUMN created_at DATETIME(3) NOT NULL COMMENT '审计事件发生时间',
    COMMENT = '租户和平台运维的管理操作审计日志', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE key_revocation_outbox
    MODIFY COLUMN id BIGINT NOT NULL COMMENT '单行记录标识，固定为 1',
    MODIFY COLUMN generation BIGINT NOT NULL COMMENT '待同步的最新吊销代次',
    MODIFY COLUMN delivered_generation BIGINT NOT NULL COMMENT '已同步到缓存的吊销代次',
    COMMENT = 'API Key 吊销后的缓存失效通知进度', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE model_access
    MODIFY COLUMN scope VARCHAR(64) NOT NULL COMMENT '规则作用域，平台或具体租户',
    MODIFY COLUMN models_json MEDIUMTEXT NOT NULL COMMENT '允许访问的模型别名列表 JSON',
    MODIFY COLUMN updated_by VARCHAR(40) NOT NULL COMMENT '最后更新人标识',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '平台或租户的模型访问规则', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE node_access
    MODIFY COLUMN node_id VARCHAR(128) NOT NULL COMMENT '节点标识',
    MODIFY COLUMN tenants_json MEDIUMTEXT NOT NULL COMMENT '获准使用节点的租户标识列表 JSON',
    MODIFY COLUMN updated_by VARCHAR(40) NOT NULL COMMENT '最后更新人标识',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN mode VARCHAR(16) NOT NULL DEFAULT 'dedicated' COMMENT '节点访问模式，如专享或共享',
    COMMENT = '平台节点的租户访问与共享规则', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE tenant_nodes
    MODIFY COLUMN node_id VARCHAR(128) NOT NULL COMMENT '节点标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN enroll_by DATETIME(6) NOT NULL COMMENT '节点完成注册的截止时间',
    MODIFY COLUMN revoked_at DATETIME(6) NULL COMMENT '节点吊销时间',
    MODIFY COLUMN revoked_by VARCHAR(40) NOT NULL DEFAULT '' COMMENT '吊销节点的操作人标识',
    COMMENT = '租户专属节点的归属、入驻和吊销信息', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE agent_install_codes
    MODIFY COLUMN code_hash VARCHAR(64) NOT NULL COMMENT '安装码的 SHA-256 摘要',
    MODIFY COLUMN node_id VARCHAR(128) NOT NULL COMMENT '节点标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN registry_addr VARCHAR(255) NOT NULL COMMENT 'Agent 注册时连接的 Registry 地址',
    MODIFY COLUMN gateway_addr VARCHAR(255) NOT NULL COMMENT 'Agent 建立隧道时连接的 Gateway 地址',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN expires_at DATETIME(6) NOT NULL COMMENT '到期时间',
    MODIFY COLUMN redeemed_at DATETIME(6) NULL COMMENT '安装码兑换时间',
    COMMENT = 'Agent 安装与注册使用的一次性安装码', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE node_providers
    MODIFY COLUMN node_id VARCHAR(128) NOT NULL COMMENT '节点标识',
    MODIFY COLUMN runtime_id VARCHAR(64) NOT NULL COMMENT '节点上的运行时标识',
    MODIFY COLUMN scope VARCHAR(32) NOT NULL COMMENT '供应商配置的作用域',
    MODIFY COLUMN kind VARCHAR(32) NOT NULL COMMENT '供应商运行时类型',
    MODIFY COLUMN base_url VARCHAR(2048) NOT NULL COMMENT '供应商接口基础地址',
    MODIFY COLUMN models_json MEDIUMTEXT NOT NULL COMMENT '供应商可用模型列表 JSON',
    MODIFY COLUMN key_ciphertext TEXT NOT NULL COMMENT '加密后的供应商 API Key',
    MODIFY COLUMN key_last4 VARCHAR(8) NOT NULL COMMENT '用于辨识的供应商 API Key 尾四位',
    MODIFY COLUMN updated_by VARCHAR(40) NOT NULL COMMENT '最后更新人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '节点运行时使用的模型供应商连接配置', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE node_provider_versions
    MODIFY COLUMN node_id VARCHAR(128) NOT NULL COMMENT '节点标识',
    MODIFY COLUMN version BIGINT NOT NULL COMMENT '供应商配置版本号',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '节点供应商配置的变更版本', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE node_billing
    MODIFY COLUMN node_id VARCHAR(128) NOT NULL COMMENT '设为免扣点的平台节点标识',
    MODIFY COLUMN updated_by VARCHAR(40) NOT NULL COMMENT '最后更新人标识',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '免扣点的平台节点名单', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_base
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '基础数据迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- base/mysql/0021_tenant_storage.sql
CREATE TABLE IF NOT EXISTS tenant_storage (
 tenant_id VARCHAR(40) NOT NULL COMMENT '租户标识',
 kind VARCHAR(16) NOT NULL COMMENT '存储种类：s3 或 webdav',
 endpoint VARCHAR(2048) NOT NULL COMMENT 'S3 端点（AWS 时为空）或 WebDAV URL',
 region VARCHAR(64) NOT NULL COMMENT 'S3 区域',
 bucket VARCHAR(255) NOT NULL COMMENT 'S3 存储桶',
 prefix VARCHAR(512) NOT NULL COMMENT 'S3 key 前缀或 WebDAV 目录',
 path_style BOOLEAN NOT NULL COMMENT 'S3 是否使用 path-style 寻址',
 username VARCHAR(256) NOT NULL COMMENT 'WebDAV 用户名',
 secret_ciphertext TEXT NOT NULL COMMENT '主密钥加密的凭据 JSON',
 secret_hint VARCHAR(8) NOT NULL COMMENT 'S3 access key id 末四位',
 retention_days BIGINT NOT NULL COMMENT '保留天数，0 表示不删除',
 version BIGINT NOT NULL COMMENT '每次保存加一的版本号',
 updated_by VARCHAR(40) NOT NULL COMMENT '最后保存的用户标识',
 created_at DATETIME(6) NOT NULL COMMENT '创建时间',
 updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
 PRIMARY KEY (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='租户自有对象存储';

-- base/mysql/0022_files.sql
CREATE TABLE IF NOT EXISTS files (
 id VARCHAR(64) NOT NULL COMMENT '文件标识：产物 id 或 Studio 文件 id',
 tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
 storage VARCHAR(16) NOT NULL COMMENT '所在存储：deployment 或 tenant',
 object_key VARCHAR(1024) NOT NULL COMMENT '在所在存储中的键',
 biz_type VARCHAR(32) NOT NULL COMMENT '归属的业务类型：job 或 studio_run',
 biz_id VARCHAR(64) NOT NULL COMMENT '归属的业务对象标识',
 kind VARCHAR(32) NOT NULL COMMENT '业务内的类别',
 filename VARCHAR(512) NOT NULL COMMENT '展示用文件名',
 content_type VARCHAR(255) NOT NULL COMMENT '媒体类型',
 size_bytes BIGINT NOT NULL COMMENT '文件大小，单位为字节',
 sha256 VARCHAR(64) NOT NULL COMMENT '文件的 SHA-256 摘要',
 status VARCHAR(16) NOT NULL COMMENT '状态：pending 或 ready',
 created_at DATETIME(6) NOT NULL COMMENT '登记时间，分区键',
 updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
 PRIMARY KEY (id, created_at),
 INDEX idx_files_storage_biz_kind_created (storage, biz_type, kind, created_at),
 INDEX idx_files_tenant_storage_created (tenant_id, storage, created_at),
 INDEX idx_files_status_created (status, created_at),
 INDEX idx_files_biz (biz_type, biz_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='平台写入对象存储的文件登记';
ALTER TABLE files PARTITION BY RANGE COLUMNS(created_at) (PARTITION p_max VALUES LESS THAN (MAXVALUE));

-- base/mysql/0023_ark_credentials.sql
CREATE TABLE IF NOT EXISTS ark_credentials (
 scope VARCHAR(40) NOT NULL COMMENT '作用域：租户标识，或 platform 表示平台 Key',
 key_ciphertext TEXT NOT NULL COMMENT '主密钥加密的火山方舟 API Key',
 key_last4 VARCHAR(8) NOT NULL COMMENT 'API Key 末四位',
 base_url VARCHAR(2048) NOT NULL COMMENT 'API 根地址，只在平台行设置，空表示北京地址',
 models TEXT NOT NULL COMMENT '模型 id 的 JSON 数组：平台行为允许的模型，租户行为建议',
 version BIGINT NOT NULL COMMENT '每次保存加一的版本号',
 updated_by VARCHAR(40) NOT NULL COMMENT '最后保存的用户或运维标识',
 created_at DATETIME(6) NOT NULL COMMENT '创建时间',
 updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
 PRIMARY KEY (scope)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='火山方舟 API Key（租户自有与平台）';

-- ============================================================
-- routes
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_routes (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- routes/mysql/0001_route_revisions.sql
CREATE TABLE IF NOT EXISTS route_revisions (
 revision BIGINT PRIMARY KEY,
 digest VARCHAR(64) NOT NULL,
 routes_json MEDIUMTEXT NOT NULL,
 actor_id VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 rollback_of BIGINT NOT NULL DEFAULT 0
) ENGINE=InnoDB;

-- routes/mysql/0002_route_actives.sql
CREATE TABLE IF NOT EXISTS route_actives (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 revision BIGINT NOT NULL DEFAULT 0
) ENGINE=InnoDB;

-- routes/mysql/0003_widen_actor_id.sql
ALTER TABLE route_revisions
 MODIFY actor_id VARCHAR(40) NOT NULL;

-- routes/mysql/0004_table_and_column_comments.sql
ALTER TABLE route_revisions
    MODIFY COLUMN revision BIGINT NOT NULL COMMENT '路由发布版本号',
    MODIFY COLUMN digest VARCHAR(64) NOT NULL COMMENT '路由内容摘要',
    MODIFY COLUMN routes_json MEDIUMTEXT NOT NULL COMMENT '完整路由规则快照 JSON',
    MODIFY COLUMN actor_id VARCHAR(40) NOT NULL COMMENT '发布操作人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN rollback_of BIGINT NOT NULL DEFAULT 0 COMMENT '回滚所引用的历史版本号，0 表示普通发布',
    COMMENT = '模型路由规则的发布版本与回滚历史', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE route_actives
    MODIFY COLUMN id INTEGER NOT NULL COMMENT '单行记录标识，固定为 1',
    MODIFY COLUMN revision BIGINT NOT NULL DEFAULT 0 COMMENT '当前生效的路由版本号，0 表示未发布',
    COMMENT = '当前生效的模型路由版本指针', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_routes
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '模型路由迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- ============================================================
-- workflow_templates
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_workflow_templates (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- workflowtemplates/mysql/0001_workflow_template_revisions.sql
CREATE TABLE IF NOT EXISTS workflow_template_revisions (
 template_id VARCHAR(128) NOT NULL,
 revision BIGINT NOT NULL,
 digest VARCHAR(64) NOT NULL,
 description TEXT NOT NULL,
 inputs_json MEDIUMTEXT NOT NULL,
 outputs_json MEDIUMTEXT NOT NULL,
 dependencies_json MEDIUMTEXT NOT NULL,
 visible_tenants_json MEDIUMTEXT NOT NULL,
 graph_json MEDIUMTEXT NOT NULL,
 actor_id VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 rollback_of BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY (template_id, revision)
) ENGINE=InnoDB;

-- workflowtemplates/mysql/0002_workflow_template_actives.sql
CREATE TABLE IF NOT EXISTS workflow_template_actives (
 template_id VARCHAR(128) PRIMARY KEY,
 revision BIGINT NOT NULL DEFAULT 0
) ENGINE=InnoDB;

-- workflowtemplates/mysql/0003_workflow_template_ui_workflow.sql
ALTER TABLE workflow_template_revisions ADD COLUMN workflow_json MEDIUMTEXT NOT NULL DEFAULT ('');

-- workflowtemplates/mysql/0004_widen_actor_id.sql
ALTER TABLE workflow_template_revisions
 MODIFY actor_id VARCHAR(40) NOT NULL;

-- workflowtemplates/mysql/0005_table_and_column_comments.sql
ALTER TABLE workflow_template_revisions
    MODIFY COLUMN template_id VARCHAR(128) NOT NULL COMMENT '工作流模板标识',
    MODIFY COLUMN revision BIGINT NOT NULL COMMENT '模板发布版本号',
    MODIFY COLUMN digest VARCHAR(64) NOT NULL COMMENT '模板内容摘要',
    MODIFY COLUMN description TEXT NOT NULL COMMENT '模板说明',
    MODIFY COLUMN inputs_json MEDIUMTEXT NOT NULL COMMENT '工作流输入参数定义 JSON',
    MODIFY COLUMN outputs_json MEDIUMTEXT NOT NULL COMMENT '工作流输出定义 JSON',
    MODIFY COLUMN dependencies_json MEDIUMTEXT NOT NULL COMMENT '工作流依赖清单 JSON',
    MODIFY COLUMN visible_tenants_json MEDIUMTEXT NOT NULL COMMENT '可见租户标识列表 JSON',
    MODIFY COLUMN graph_json MEDIUMTEXT NOT NULL COMMENT '提交给后端执行的工作流图 JSON',
    MODIFY COLUMN actor_id VARCHAR(40) NOT NULL COMMENT '发布操作人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN rollback_of BIGINT NOT NULL DEFAULT 0 COMMENT '回滚所引用的历史版本号，0 表示普通发布',
    MODIFY COLUMN workflow_json MEDIUMTEXT NOT NULL DEFAULT ('') COMMENT '供工作流编辑器使用的界面工作流 JSON',
    COMMENT = '工作流模板的发布版本与回滚历史', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE workflow_template_actives
    MODIFY COLUMN template_id VARCHAR(128) NOT NULL COMMENT '工作流模板标识',
    MODIFY COLUMN revision BIGINT NOT NULL DEFAULT 0 COMMENT '当前生效的模板版本号，0 表示未发布',
    COMMENT = '每个工作流模板当前生效的版本指针', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_workflow_templates
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '工作流模板迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- ============================================================
-- metrics_history
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_metrics_history (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- metrics_history/mysql/0001_metrics_history_points.sql
CREATE TABLE metrics_history_points (
 id BIGINT AUTO_INCREMENT PRIMARY KEY,
 metric VARCHAR(128) NOT NULL,
 labels VARCHAR(512) NOT NULL,
 bucket_at DATETIME(6) NOT NULL,
 value DOUBLE NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_metrics_history_points_series ON metrics_history_points (metric, labels, bucket_at);
CREATE INDEX idx_metrics_history_points_bucket ON metrics_history_points (bucket_at);

-- metrics_history/mysql/0002_partition_metrics_history_points.sql
ALTER TABLE metrics_history_points
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, bucket_at)
 PARTITION BY RANGE COLUMNS(bucket_at) (PARTITION p_max VALUES LESS THAN (MAXVALUE));

-- metrics_history/mysql/0003_table_and_column_comments.sql
ALTER TABLE metrics_history_points
    MODIFY COLUMN id BIGINT AUTO_INCREMENT NOT NULL COMMENT '指标采样点的自增标识',
    MODIFY COLUMN metric VARCHAR(128) NOT NULL COMMENT '指标名称',
    MODIFY COLUMN labels VARCHAR(512) NOT NULL COMMENT '用于区分指标序列的标签集合',
    MODIFY COLUMN bucket_at DATETIME(6) NOT NULL COMMENT '采样时间桶的起始时间',
    MODIFY COLUMN value DOUBLE NOT NULL COMMENT '指标采样值',
    COMMENT = '按时间桶保存的监控指标历史采样点', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_metrics_history
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '指标历史迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- ============================================================
-- request_logs
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_request_logs (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- request_logs/mysql/0001_request_logs.sql
CREATE TABLE request_logs (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 key_display VARCHAR(64) NOT NULL,
 endpoint VARCHAR(16) NOT NULL,
 status_code SMALLINT NOT NULL,
 outcome VARCHAR(24) NOT NULL,
 duration_ms BIGINT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX idx_request_logs_tenant_created (tenant_id, created_at, id),
 INDEX idx_request_logs_tenant_outcome_created (tenant_id, outcome, created_at, id)
) ENGINE=InnoDB;

-- request_logs/mysql/0002_add_request_logs_created_index.sql
CREATE INDEX idx_request_logs_created ON request_logs (created_at, id);

-- request_logs/mysql/0003_add_request_logs_failure_fields.sql
ALTER TABLE request_logs ADD COLUMN model VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN error_code VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN error_message VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN error_detail VARCHAR(1024) NOT NULL DEFAULT '';

-- request_logs/mysql/0004_partition_request_logs.sql
ALTER TABLE request_logs
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, created_at)
 PARTITION BY RANGE COLUMNS(created_at) (PARTITION p_max VALUES LESS THAN (MAXVALUE));

-- request_logs/mysql/0005_widen_tenant_id.sql
ALTER TABLE request_logs
 MODIFY tenant_id VARCHAR(40) NOT NULL;

-- request_logs/mysql/0006_table_and_column_comments.sql
ALTER TABLE request_logs
    MODIFY COLUMN id VARCHAR(64) NOT NULL COMMENT '请求日志标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN key_display VARCHAR(64) NOT NULL COMMENT '发起请求的 API Key 脱敏显示值',
    MODIFY COLUMN endpoint VARCHAR(16) NOT NULL COMMENT '请求入口类别',
    MODIFY COLUMN status_code SMALLINT NOT NULL COMMENT '返回给调用方的 HTTP 状态码',
    MODIFY COLUMN outcome VARCHAR(24) NOT NULL COMMENT '请求结果分类',
    MODIFY COLUMN duration_ms BIGINT NOT NULL COMMENT '请求耗时，单位毫秒',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '请求日志记录时间',
    MODIFY COLUMN model VARCHAR(128) NOT NULL DEFAULT '' COMMENT '请求使用的模型别名',
    MODIFY COLUMN error_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '请求错误代码',
    MODIFY COLUMN error_message VARCHAR(512) NOT NULL DEFAULT '' COMMENT '返回给调用方的错误消息',
    MODIFY COLUMN error_detail VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '供排查使用的错误详情',
    COMMENT = 'Gateway 上报的租户 API 请求日志与错误信息', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_request_logs
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '请求日志迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- ============================================================
-- alerting
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_alerting (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- alerting/mysql/0001_create_alert_rules.sql
CREATE TABLE alert_rules (
 id VARCHAR(64) PRIMARY KEY,
 name VARCHAR(128) NOT NULL,
 metric VARCHAR(32) NOT NULL,
 operator VARCHAR(8) NOT NULL,
 threshold DOUBLE NOT NULL,
 consecutive_buckets INT NOT NULL,
 webhook_url VARCHAR(512) NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 INDEX idx_alert_rules_enabled (enabled)
) ENGINE=InnoDB;

-- alerting/mysql/0002_create_alert_instances.sql
CREATE TABLE alert_instances (
 id VARCHAR(64) PRIMARY KEY,
 rule_id VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL,
 value_at_fire DOUBLE NOT NULL,
 created_at DATETIME(6) NOT NULL,
 last_evaluated_at DATETIME(6) NOT NULL,
 resolved_at DATETIME(6) NULL,
 acknowledged_by VARCHAR(32) NOT NULL DEFAULT '',
 acknowledged_at DATETIME(6) NULL,
 notify_status VARCHAR(16) NOT NULL,
 notify_attempts INT NOT NULL DEFAULT 0,
 INDEX idx_alert_instances_rule_status (rule_id, status),
 INDEX idx_alert_instances_created (created_at, id)
) ENGINE=InnoDB;

-- alerting/mysql/0003_widen_acknowledged_by.sql
ALTER TABLE alert_instances
 MODIFY acknowledged_by VARCHAR(40) NOT NULL DEFAULT '';

-- alerting/mysql/0004_table_and_column_comments.sql
ALTER TABLE alert_rules
    MODIFY COLUMN id VARCHAR(64) NOT NULL COMMENT '告警规则标识',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '告警规则名称',
    MODIFY COLUMN metric VARCHAR(32) NOT NULL COMMENT '监控指标名称',
    MODIFY COLUMN operator VARCHAR(8) NOT NULL COMMENT '阈值比较运算符',
    MODIFY COLUMN threshold DOUBLE NOT NULL COMMENT '告警触发阈值',
    MODIFY COLUMN consecutive_buckets INT NOT NULL COMMENT '触发告警所需的连续达标时间桶数量',
    MODIFY COLUMN webhook_url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '告警通知的 Webhook 地址',
    MODIFY COLUMN enabled BOOLEAN NOT NULL DEFAULT TRUE COMMENT '是否启用此规则',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '监控告警触发与通知规则', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE alert_instances
    MODIFY COLUMN id VARCHAR(64) NOT NULL COMMENT '告警实例标识',
    MODIFY COLUMN rule_id VARCHAR(64) NOT NULL COMMENT '触发告警的规则标识',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '告警实例状态',
    MODIFY COLUMN value_at_fire DOUBLE NOT NULL COMMENT '触发告警时的指标值',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '告警触发时间',
    MODIFY COLUMN last_evaluated_at DATETIME(6) NOT NULL COMMENT '最近一次评估时间',
    MODIFY COLUMN resolved_at DATETIME(6) NULL COMMENT '告警恢复时间',
    MODIFY COLUMN acknowledged_by VARCHAR(40) NOT NULL DEFAULT '' COMMENT '确认告警的操作人标识',
    MODIFY COLUMN acknowledged_at DATETIME(6) NULL COMMENT '告警确认时间',
    MODIFY COLUMN notify_status VARCHAR(16) NOT NULL COMMENT '告警通知投递状态',
    MODIFY COLUMN notify_attempts INT NOT NULL DEFAULT 0 COMMENT '告警通知投递尝试次数',
    COMMENT = '告警触发、确认、恢复与通知记录', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_alerting
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '告警迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- ============================================================
-- response_turns
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_response_turns (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- response_turns/mysql/0001_create_response_turns.sql
CREATE TABLE response_turns (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 previous_response_id VARCHAR(64) NOT NULL DEFAULT '',
 model VARCHAR(128) NOT NULL DEFAULT '',
 messages TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX idx_response_turns_tenant (tenant_id, created_at, id)
) ENGINE=InnoDB;

-- response_turns/mysql/0002_partition_response_turns.sql
ALTER TABLE response_turns
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, created_at)
 PARTITION BY RANGE COLUMNS(created_at) (PARTITION p_max VALUES LESS THAN (MAXVALUE));

-- response_turns/mysql/0003_widen_tenant_id.sql
ALTER TABLE response_turns
 MODIFY tenant_id VARCHAR(40) NOT NULL;

-- response_turns/mysql/0004_table_and_column_comments.sql
ALTER TABLE response_turns
    MODIFY COLUMN id VARCHAR(64) NOT NULL COMMENT '当前响应的标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN previous_response_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '上一轮的响应标识，首轮为空',
    MODIFY COLUMN model VARCHAR(128) NOT NULL DEFAULT '' COMMENT '当前轮次使用的模型别名',
    MODIFY COLUMN messages TEXT NOT NULL COMMENT '续接对话所需的消息 JSON',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    COMMENT = 'Responses API 保存的对话轮次，用于续接上下文', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_response_turns
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = 'Responses 对话轮次迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- ============================================================
-- usage_records
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_usage_records (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- usage_records/mysql/0001_usage_records.sql
CREATE TABLE usage_records (
 id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 model VARCHAR(128) NOT NULL,
 endpoint VARCHAR(24) NOT NULL,
 prompt_tokens BIGINT NOT NULL,
 completion_tokens BIGINT NOT NULL,
 total_tokens BIGINT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX idx_usage_records_tenant_model_created (tenant_id, model, created_at),
 INDEX idx_usage_records_created (created_at, id)
) ENGINE=InnoDB;

-- usage_records/mysql/0002_partition_usage_records.sql
ALTER TABLE usage_records
 DROP PRIMARY KEY,
 ADD PRIMARY KEY (id, created_at)
 PARTITION BY RANGE COLUMNS(created_at) (PARTITION p_max VALUES LESS THAN (MAXVALUE));

-- usage_records/mysql/0003_add_usage_records_key_user.sql
ALTER TABLE usage_records ADD COLUMN key_id VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE usage_records ADD COLUMN user_id VARCHAR(32) NOT NULL DEFAULT '';

-- usage_records/mysql/0004_widen_id_columns.sql
ALTER TABLE usage_records
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY key_id VARCHAR(40) NOT NULL DEFAULT '',
 MODIFY user_id VARCHAR(40) NOT NULL DEFAULT '';

-- usage_records/mysql/0005_pricing.sql
ALTER TABLE usage_records
 ADD COLUMN cache_read_tokens BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN cache_write_tokens BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN cost_micro BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN base_cost_micro BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN rate_ppm BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN price_source VARCHAR(16) NOT NULL DEFAULT '',
 ADD COLUMN details TEXT NULL,
 ADD COLUMN charged BOOLEAN NULL,
 ALGORITHM=INSTANT;

-- usage_records/mysql/0006_tenant_created_index.sql
ALTER TABLE usage_records ADD INDEX idx_usage_records_tenant_created (tenant_id, created_at, id), ALGORITHM=INPLACE, LOCK=NONE;

-- usage_records/mysql/0007_table_and_column_comments.sql
ALTER TABLE usage_records
    MODIFY COLUMN id VARCHAR(64) NOT NULL COMMENT '用量记录标识，用于重复上报防重',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN model VARCHAR(128) NOT NULL COMMENT '计价使用的模型别名',
    MODIFY COLUMN endpoint VARCHAR(24) NOT NULL COMMENT '请求入口类别',
    MODIFY COLUMN prompt_tokens BIGINT NOT NULL COMMENT '输入 Token 总数，包含缓存部分',
    MODIFY COLUMN completion_tokens BIGINT NOT NULL COMMENT '输出 Token 总数',
    MODIFY COLUMN total_tokens BIGINT NOT NULL COMMENT '输入和输出 Token 总数',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '请求发生时间，用于分区和选择生效价格',
    MODIFY COLUMN key_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '发起请求的 API Key 标识，未归属时为空',
    MODIFY COLUMN user_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '用量归属的用户标识，未归属时为空',
    MODIFY COLUMN cache_read_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '命中缓存读取的输入 Token 数',
    MODIFY COLUMN cache_write_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '写入缓存的输入 Token 数',
    MODIFY COLUMN cost_micro BIGINT NOT NULL DEFAULT 0 COMMENT '本次请求的计费金额，单位为微点',
    MODIFY COLUMN base_cost_micro BIGINT NOT NULL DEFAULT 0 COMMENT '应用价格倍率前的基础费用，单位为微点',
    MODIFY COLUMN rate_ppm BIGINT NOT NULL DEFAULT 0 COMMENT '价格倍率，以百万分之一为单位',
    MODIFY COLUMN price_source VARCHAR(16) NOT NULL DEFAULT '' COMMENT '本次请求采用的价格来源',
    MODIFY COLUMN details TEXT NULL COMMENT '逐计量项的用量、单价和费用明细 JSON',
    MODIFY COLUMN charged BOOLEAN NULL COMMENT '是否实际扣点，未记录扣费结果时为 NULL',
    COMMENT = '请求级模型用量、计价与扣费明细', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_usage_records
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '用量记录迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- ============================================================
-- api_docs
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_api_docs (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- api_docs/mysql/0001_create_api_docs.sql
CREATE TABLE api_docs (
 slug VARCHAR(128) PRIMARY KEY,
 category VARCHAR(64) NOT NULL,
 category_order BIGINT NOT NULL DEFAULT 0,
 sort_order BIGINT NOT NULL DEFAULT 0,
 title VARCHAR(200) NOT NULL,
 method VARCHAR(16) NOT NULL,
 path VARCHAR(255) NOT NULL,
 summary VARCHAR(500) NOT NULL DEFAULT '',
 content_json MEDIUMTEXT NOT NULL,
 status VARCHAR(16) NOT NULL,
 source VARCHAR(16) NOT NULL,
 seed_revision BIGINT NOT NULL DEFAULT 0,
 version BIGINT NOT NULL DEFAULT 1,
 updated_by VARCHAR(64) NOT NULL DEFAULT '',
 updated_at DATETIME(6) NOT NULL,
 INDEX idx_api_docs_status_order (status, category_order, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- api_docs/mysql/0002_table_and_column_comments.sql
ALTER TABLE api_docs
    MODIFY COLUMN slug VARCHAR(128) NOT NULL COMMENT '文档页面的路径标识',
    MODIFY COLUMN category VARCHAR(64) NOT NULL COMMENT '文档分组名称',
    MODIFY COLUMN category_order BIGINT NOT NULL DEFAULT 0 COMMENT '分组展示排序值',
    MODIFY COLUMN sort_order BIGINT NOT NULL DEFAULT 0 COMMENT '分组内的页面排序值',
    MODIFY COLUMN title VARCHAR(200) NOT NULL COMMENT '文档标题',
    MODIFY COLUMN method VARCHAR(16) NOT NULL COMMENT 'HTTP 请求方法',
    MODIFY COLUMN path VARCHAR(255) NOT NULL COMMENT 'API 路径',
    MODIFY COLUMN summary VARCHAR(500) NOT NULL DEFAULT '' COMMENT '文档简介',
    MODIFY COLUMN content_json MEDIUMTEXT NOT NULL COMMENT '结构化文档正文 JSON',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '文档发布状态',
    MODIFY COLUMN source VARCHAR(16) NOT NULL COMMENT '文档来源，如内置或运维编辑',
    MODIFY COLUMN seed_revision BIGINT NOT NULL DEFAULT 0 COMMENT '内置文档的内容修订号',
    MODIFY COLUMN version BIGINT NOT NULL DEFAULT 1 COMMENT '文档修改版本号，用于并发控制',
    MODIFY COLUMN updated_by VARCHAR(64) NOT NULL DEFAULT '' COMMENT '最后更新人标识',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '控制面 API 文档的内容、发布状态与版本', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_api_docs
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = 'API 文档迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- ============================================================
-- billing
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_billing (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- billing/mysql/0001_billing.sql
CREATE TABLE tenant_billing (
 tenant_id VARCHAR(32) PRIMARY KEY,
 mode VARCHAR(16) NOT NULL,
 balance_micro BIGINT NOT NULL,
 dodo_customer_id VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE credit_packages (
 id VARCHAR(32) PRIMARY KEY,
 name VARCHAR(128) NOT NULL,
 kind VARCHAR(16) NOT NULL,
 dodo_product_id VARCHAR(64) NOT NULL,
 credits_micro BIGINT NOT NULL,
 price_minor BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL,
 credits_per_minor_unit_micro BIGINT NOT NULL,
 min_amount_minor BIGINT NOT NULL,
 max_amount_minor BIGINT NOT NULL,
 active BOOLEAN NOT NULL,
 sort_order BIGINT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE model_prices (
 model VARCHAR(128) PRIMARY KEY,
 input_micro_per_m_tok BIGINT NOT NULL,
 output_micro_per_m_tok BIGINT NOT NULL,
 updated_by VARCHAR(32) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE checkout_orders (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 package_id VARCHAR(32) NOT NULL,
 dodo_session_id VARCHAR(128) NOT NULL,
 dodo_payment_id VARCHAR(64),
 amount_minor BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL,
 credits_micro BIGINT NOT NULL,
 refunded_minor BIGINT NOT NULL,
 status VARCHAR(16) NOT NULL,
 created_by VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_checkout_orders_payment ON checkout_orders (dodo_payment_id);
CREATE INDEX idx_checkout_orders_tenant_created ON checkout_orders (tenant_id, created_at, id);
CREATE INDEX idx_checkout_orders_created ON checkout_orders (created_at, id);
CREATE TABLE credit_ledger (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 kind VARCHAR(24) NOT NULL,
 amount_micro BIGINT NOT NULL,
 balance_after_micro BIGINT NOT NULL,
 ref VARCHAR(128) NOT NULL,
 idempotency_key VARCHAR(160),
 actor_id VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_credit_ledger_idempotency ON credit_ledger (idempotency_key);
CREATE INDEX idx_credit_ledger_tenant_created ON credit_ledger (tenant_id, created_at, id);
CREATE INDEX idx_credit_ledger_created ON credit_ledger (created_at, id);
CREATE TABLE usage_charges (
 request_id VARCHAR(64) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 model VARCHAR(128) NOT NULL,
 cost_micro BIGINT NOT NULL,
 charged BOOLEAN NOT NULL,
 created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE INDEX idx_usage_charges_tenant_created ON usage_charges (tenant_id, created_at);
CREATE TABLE billing_webhook_events (
 webhook_id VARCHAR(128) PRIMARY KEY,
 type VARCHAR(64) NOT NULL,
 received_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;

-- billing/mysql/0002_checkout_paid.sql
ALTER TABLE checkout_orders ADD COLUMN paid_amount_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE checkout_orders ADD COLUMN paid_currency VARCHAR(8) NOT NULL DEFAULT '';

-- billing/mysql/0003_refunds.sql
ALTER TABLE checkout_orders ADD COLUMN dodo_product_id VARCHAR(64) NOT NULL DEFAULT '';
CREATE TABLE refund_requests (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 order_id VARCHAR(32) NOT NULL,
 source VARCHAR(16) NOT NULL,
 status VARCHAR(16) NOT NULL,
 amount_minor BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL,
 credits_micro BIGINT NOT NULL,
 reason VARCHAR(1024) NOT NULL,
 requested_by VARCHAR(32) NOT NULL,
 reviewed_by VARCHAR(32) NOT NULL,
 review_note VARCHAR(1024) NOT NULL,
 dodo_refund_id VARCHAR(64),
 failure VARCHAR(256) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_refund_requests_refund ON refund_requests (dodo_refund_id);
CREATE INDEX idx_refund_requests_order ON refund_requests (order_id, status);
CREATE INDEX idx_refund_requests_tenant_created ON refund_requests (tenant_id, created_at, id);
CREATE INDEX idx_refund_requests_created ON refund_requests (created_at, id);

-- billing/mysql/0004_account_sales.sql
CREATE TABLE account_sales_lock (id BIGINT PRIMARY KEY) ENGINE=InnoDB;
INSERT INTO account_sales_lock (id) VALUES (1);
CREATE TABLE account_sales (
 id VARCHAR(32) PRIMARY KEY,
 node_id VARCHAR(128) NOT NULL,
 runtime_id VARCHAR(128) NOT NULL,
 name VARCHAR(128) NOT NULL,
 price_micro BIGINT NOT NULL,
 active BOOLEAN NOT NULL,
 tenant_id VARCHAR(32) NOT NULL,
 expires_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_account_sales_target ON account_sales (node_id, runtime_id);
CREATE TABLE account_purchases (
 id VARCHAR(32) PRIMARY KEY,
 account_id VARCHAR(32) NOT NULL,
 tenant_id VARCHAR(32) NOT NULL,
 price_micro BIGINT NOT NULL,
 expires_at DATETIME(6) NOT NULL,
 created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;

-- billing/mysql/0005_account_fiat.sql
ALTER TABLE account_sales ADD COLUMN price_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE account_sales ADD COLUMN currency VARCHAR(8) NOT NULL DEFAULT '';
ALTER TABLE account_sales ADD COLUMN dodo_product_id VARCHAR(64) NOT NULL DEFAULT '';
UPDATE account_sales SET active = FALSE;
ALTER TABLE account_sales DROP COLUMN price_micro;
ALTER TABLE checkout_orders ADD COLUMN account_id VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE checkout_orders ADD COLUMN account_expires_at DATETIME(6) NULL;
CREATE INDEX idx_checkout_orders_account ON checkout_orders (account_id, status, created_at);

-- billing/mysql/0006_account_presale.sql
CREATE TABLE IF NOT EXISTS account_presales (
 id VARCHAR(32) PRIMARY KEY,
 kind VARCHAR(16) NOT NULL,
 name VARCHAR(128) NOT NULL,
 price_minor BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL,
 dodo_product_id VARCHAR(64) NOT NULL,
 capacity BIGINT NOT NULL,
 active BOOLEAN NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
ALTER TABLE account_sales ADD COLUMN kind VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE checkout_orders ADD COLUMN presale_id VARCHAR(32) NOT NULL DEFAULT '';
CREATE INDEX idx_checkout_orders_presale ON checkout_orders (presale_id, status, created_at);

-- billing/mysql/0007_alipay.sql
CREATE TABLE IF NOT EXISTS alipay_purchases (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 action_id VARCHAR(64) NOT NULL,
 sku_id VARCHAR(128) NOT NULL,
 callback_url VARCHAR(512) NOT NULL,
 status VARCHAR(16) NOT NULL,
 purchase_url VARCHAR(2048) NOT NULL,
 expire_time VARCHAR(64) NOT NULL,
 reason_code VARCHAR(64) NOT NULL,
 created_by VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_alipay_purchases_action ON alipay_purchases (tenant_id, action_id);
CREATE TABLE IF NOT EXISTS alipay_redemptions (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 action_id VARCHAR(64) NOT NULL,
 amount BIGINT NOT NULL,
 quota_unit VARCHAR(8) NOT NULL,
 credits_micro BIGINT NOT NULL,
 reason VARCHAR(256) NOT NULL,
 status VARCHAR(16) NOT NULL,
 reason_code VARCHAR(64) NOT NULL,
 consume_order_id VARCHAR(128) NOT NULL,
 created_by VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_alipay_redemptions_action ON alipay_redemptions (tenant_id, action_id);
CREATE INDEX idx_alipay_redemptions_tenant_created ON alipay_redemptions (tenant_id, created_at, id);
CREATE INDEX idx_alipay_redemptions_status_created ON alipay_redemptions (status, created_at, id);

-- billing/mysql/0008_alipay_presale.sql
ALTER TABLE account_presales ADD COLUMN alipay_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE checkout_orders ADD COLUMN alipay_amount BIGINT NOT NULL DEFAULT 0;
ALTER TABLE alipay_redemptions ADD COLUMN presale_order_id VARCHAR(32) NOT NULL DEFAULT '';

-- billing/mysql/0009_ezfp.sql
ALTER TABLE checkout_orders ADD COLUMN provider VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE checkout_orders ADD COLUMN pay_type VARCHAR(16) NOT NULL DEFAULT '';
CREATE INDEX idx_checkout_orders_provider ON checkout_orders (provider, status, created_at);

-- billing/mysql/0010_order_deletion.sql
ALTER TABLE checkout_orders ADD COLUMN tenant_deleted_at DATETIME(6) NULL;

-- billing/mysql/0011_widen_id_columns.sql
ALTER TABLE tenant_billing
 MODIFY tenant_id VARCHAR(40) NOT NULL;
ALTER TABLE credit_packages
 MODIFY id VARCHAR(40) NOT NULL;
ALTER TABLE model_prices
 MODIFY updated_by VARCHAR(40) NOT NULL;
ALTER TABLE checkout_orders
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY package_id VARCHAR(40) NOT NULL,
 MODIFY created_by VARCHAR(40) NOT NULL,
 MODIFY account_id VARCHAR(40) NOT NULL DEFAULT '',
 MODIFY presale_id VARCHAR(40) NOT NULL DEFAULT '';
ALTER TABLE credit_ledger
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY actor_id VARCHAR(40) NOT NULL;
ALTER TABLE usage_charges
 MODIFY tenant_id VARCHAR(40) NOT NULL;
ALTER TABLE refund_requests
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY order_id VARCHAR(40) NOT NULL,
 MODIFY requested_by VARCHAR(40) NOT NULL,
 MODIFY reviewed_by VARCHAR(40) NOT NULL;
ALTER TABLE account_sales
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL;
ALTER TABLE account_purchases
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY account_id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL;
ALTER TABLE account_presales
 MODIFY id VARCHAR(40) NOT NULL;
ALTER TABLE alipay_purchases
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY created_by VARCHAR(40) NOT NULL;
ALTER TABLE alipay_redemptions
 MODIFY id VARCHAR(40) NOT NULL,
 MODIFY tenant_id VARCHAR(40) NOT NULL,
 MODIFY created_by VARCHAR(40) NOT NULL,
 MODIFY presale_order_id VARCHAR(40) NOT NULL DEFAULT '';

-- billing/mysql/0012_price_catalog.sql
CREATE TABLE IF NOT EXISTS catalog_models (
 model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin PRIMARY KEY,
 provider VARCHAR(64) NOT NULL,
 mode VARCHAR(32) NOT NULL,
 status VARCHAR(24) NOT NULL,
 prices_hash VARCHAR(64) NOT NULL,
 rejected_hash VARCHAR(64) NOT NULL DEFAULT '',
 raw_json MEDIUMTEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS catalog_prices (
 id BIGINT AUTO_INCREMENT PRIMARY KEY,
 model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
 meter VARCHAR(64) NOT NULL,
 variant VARCHAR(32) NOT NULL DEFAULT '',
 above_input_tokens BIGINT NOT NULL DEFAULT 0,
 price_micro_per_m BIGINT NOT NULL,
 derived BOOLEAN NOT NULL DEFAULT FALSE,
 valid_from DATETIME(6) NOT NULL,
 valid_to DATETIME(6) NULL
) ENGINE=InnoDB;
CREATE INDEX idx_catalog_prices_model_from ON catalog_prices (model, valid_from);
CREATE INDEX idx_catalog_prices_valid_to ON catalog_prices (valid_to);
CREATE TABLE IF NOT EXISTS catalog_pending (
 model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin PRIMARY KEY,
 kind VARCHAR(16) NOT NULL,
 proposed_json MEDIUMTEXT NOT NULL,
 prices_hash VARCHAR(64) NOT NULL,
 max_change_bp BIGINT NOT NULL,
 detected_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS catalog_sync_state (
 id BIGINT PRIMARY KEY,
 source VARCHAR(512) NOT NULL DEFAULT '',
 content_hash VARCHAR(64) NOT NULL DEFAULT '',
 embedded_hash VARCHAR(64) NOT NULL DEFAULT '',
 entry_count BIGINT NOT NULL DEFAULT 0,
 version BIGINT NOT NULL DEFAULT 0,
 synced_at DATETIME(6) NULL,
 checked_at DATETIME(6) NULL,
 last_error VARCHAR(512) NOT NULL DEFAULT '',
 last_error_at DATETIME(6) NULL
) ENGINE=InnoDB;
INSERT IGNORE INTO catalog_sync_state (id) VALUES (1);

-- billing/mysql/0013_pricing_config.sql
CREATE TABLE IF NOT EXISTS pricing_revisions (
 id BIGINT AUTO_INCREMENT PRIMARY KEY,
 effective_at DATETIME(6) NOT NULL,
 document MEDIUMTEXT NOT NULL,
 created_by VARCHAR(40) NOT NULL,
 summary VARCHAR(512) NOT NULL,
 created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE INDEX idx_pricing_revisions_effective ON pricing_revisions (effective_at, id);
CREATE TABLE IF NOT EXISTS tenant_price_plans (
 id VARCHAR(40) PRIMARY KEY,
 tenant_id VARCHAR(40) NOT NULL,
 plan_id VARCHAR(40) NOT NULL,
 valid_from DATETIME(6) NOT NULL,
 valid_to DATETIME(6) NULL,
 created_by VARCHAR(40) NOT NULL
) ENGINE=InnoDB;
CREATE INDEX idx_tenant_price_plans_tenant ON tenant_price_plans (tenant_id, valid_from);
CREATE INDEX idx_tenant_price_plans_plan ON tenant_price_plans (plan_id, valid_to);
CREATE TABLE IF NOT EXISTS pricing_config_state (
 id INT PRIMARY KEY,
 version BIGINT NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
INSERT IGNORE INTO pricing_config_state (id, version, updated_at) VALUES (1, 0, UTC_TIMESTAMP(6));

-- billing/mysql/0014_usage_ledger.sql
ALTER TABLE tenant_billing
 ADD COLUMN usage_acc_micro BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN usage_acc_count BIGINT NOT NULL DEFAULT 0,
 ADD COLUMN usage_acc_since DATETIME(6) NULL,
 ADD COLUMN usage_acc_last DATETIME(6) NULL,
 ADD COLUMN usage_acc_until DATETIME(6) NULL,
 ADD COLUMN last_ledger_at DATETIME(6) NULL;
CREATE INDEX idx_tenant_billing_usage_acc_until ON tenant_billing (usage_acc_until);
ALTER TABLE credit_ledger
 ADD COLUMN period_start DATETIME(6) NULL,
 ADD COLUMN period_end DATETIME(6) NULL,
 ADD COLUMN request_count BIGINT NOT NULL DEFAULT 0,
 ALGORITHM=INSTANT;

-- billing/mysql/0015_table_and_column_comments.sql
ALTER TABLE tenant_billing
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN mode VARCHAR(16) NOT NULL COMMENT '租户计费模式',
    MODIFY COLUMN balance_micro BIGINT NOT NULL COMMENT '当前余额，单位为微点',
    MODIFY COLUMN dodo_customer_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'Dodo 支付平台上的客户标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN usage_acc_micro BIGINT NOT NULL DEFAULT 0 COMMENT '待封账的用量扣点合计，单位为微点',
    MODIFY COLUMN usage_acc_count BIGINT NOT NULL DEFAULT 0 COMMENT '待封账的用量请求数',
    MODIFY COLUMN usage_acc_since DATETIME(6) NULL COMMENT '本轮待封账用量的开始时间',
    MODIFY COLUMN usage_acc_last DATETIME(6) NULL COMMENT '本轮待封账用量的最近时间',
    MODIFY COLUMN usage_acc_until DATETIME(6) NULL COMMENT '当前用量累计时段的封账截止时间',
    MODIFY COLUMN last_ledger_at DATETIME(6) NULL COMMENT '最近一笔已落账的点数流水时间',
    COMMENT = '租户计费模式、点数余额与待封账用量', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE credit_packages
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '充值方案标识',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '充值方案名称',
    MODIFY COLUMN kind VARCHAR(16) NOT NULL COMMENT '充值方案类型',
    MODIFY COLUMN dodo_product_id VARCHAR(64) NOT NULL COMMENT 'Dodo 支付平台上的商品标识',
    MODIFY COLUMN credits_micro BIGINT NOT NULL COMMENT '固定充值包发放的点数，单位为微点',
    MODIFY COLUMN price_minor BIGINT NOT NULL COMMENT '固定充值包的售价，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL COMMENT '币种代码',
    MODIFY COLUMN credits_per_minor_unit_micro BIGINT NOT NULL COMMENT '每个货币最小单位兑换的微点数',
    MODIFY COLUMN min_amount_minor BIGINT NOT NULL COMMENT '自定义充值金额下限，单位为货币最小单位',
    MODIFY COLUMN max_amount_minor BIGINT NOT NULL COMMENT '自定义充值金额上限，单位为货币最小单位',
    MODIFY COLUMN active BOOLEAN NOT NULL COMMENT '是否上架',
    MODIFY COLUMN sort_order BIGINT NOT NULL COMMENT '充值方案的展示排序值',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '可购买的固定点数包与自定义充值规则', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE model_prices
    MODIFY COLUMN model VARCHAR(128) NOT NULL COMMENT '模型别名',
    MODIFY COLUMN input_micro_per_m_tok BIGINT NOT NULL COMMENT '每百万输入 Token 的价格，单位为微点',
    MODIFY COLUMN output_micro_per_m_tok BIGINT NOT NULL COMMENT '每百万输出 Token 的价格，单位为微点',
    MODIFY COLUMN updated_by VARCHAR(40) NOT NULL COMMENT '最后更新人标识',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '旧版模型单价配置，用于新计价配置的迁移', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE checkout_orders
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '平台结账订单标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN package_id VARCHAR(40) NOT NULL COMMENT '所购充值方案标识',
    MODIFY COLUMN dodo_session_id VARCHAR(128) NOT NULL COMMENT 'Dodo 结账会话标识',
    MODIFY COLUMN dodo_payment_id VARCHAR(64) COMMENT 'Dodo 支付记录标识',
    MODIFY COLUMN amount_minor BIGINT NOT NULL COMMENT '订单应付金额，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL COMMENT '币种代码',
    MODIFY COLUMN credits_micro BIGINT NOT NULL COMMENT '订单对应的点数，单位为微点',
    MODIFY COLUMN refunded_minor BIGINT NOT NULL COMMENT '已退款金额，单位为货币最小单位',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '订单状态',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN paid_amount_minor BIGINT NOT NULL DEFAULT 0 COMMENT '渠道实际收取的金额，单位为货币最小单位',
    MODIFY COLUMN paid_currency VARCHAR(8) NOT NULL DEFAULT '' COMMENT '渠道实际收款的币种',
    MODIFY COLUMN dodo_product_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '下单时使用的 Dodo 商品标识',
    MODIFY COLUMN account_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '所购账号商品标识',
    MODIFY COLUMN account_expires_at DATETIME(6) NULL COMMENT '账号商品权益到期时间快照',
    MODIFY COLUMN presale_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '所购账号预售商品标识',
    MODIFY COLUMN alipay_amount BIGINT NOT NULL DEFAULT 0 COMMENT '下单时记录的支付宝兑换额度',
    MODIFY COLUMN provider VARCHAR(16) NOT NULL DEFAULT '' COMMENT '订单使用的支付渠道标识',
    MODIFY COLUMN pay_type VARCHAR(16) NOT NULL DEFAULT '' COMMENT '支付渠道内的支付方式',
    MODIFY COLUMN tenant_deleted_at DATETIME(6) NULL COMMENT '租户从订单列表隐藏该订单的时间',
    COMMENT = '租户充值与账号商品的结账订单', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE credit_ledger
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '点数流水标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN kind VARCHAR(24) NOT NULL COMMENT '流水类型，如充值、退款或用量扣费',
    MODIFY COLUMN amount_micro BIGINT NOT NULL COMMENT '本次余额变动额，单位为微点',
    MODIFY COLUMN balance_after_micro BIGINT NOT NULL COMMENT '变动后余额，单位为微点',
    MODIFY COLUMN ref VARCHAR(128) NOT NULL COMMENT '关联的业务单据或事件标识',
    MODIFY COLUMN idempotency_key VARCHAR(160) COMMENT '防止同一事件重复入账的幂等键',
    MODIFY COLUMN actor_id VARCHAR(40) NOT NULL COMMENT '发起余额变动的操作人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '流水入账时间',
    MODIFY COLUMN period_start DATETIME(6) NULL COMMENT '汇总用量账目的起始时间',
    MODIFY COLUMN period_end DATETIME(6) NULL COMMENT '汇总用量账目的截止时间',
    MODIFY COLUMN request_count BIGINT NOT NULL DEFAULT 0 COMMENT '该用量账目汇总的请求数',
    COMMENT = '租户点数余额的资金流水', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE usage_charges
    MODIFY COLUMN request_id VARCHAR(64) NOT NULL COMMENT '已处理的用量请求标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN model VARCHAR(128) NOT NULL COMMENT '计价使用的模型别名',
    MODIFY COLUMN cost_micro BIGINT NOT NULL COMMENT '该请求的费用，单位为微点',
    MODIFY COLUMN charged BOOLEAN NOT NULL COMMENT '是否实际扣点',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '旧版扣费记录的入账时间',
    COMMENT = '旧版请求级扣费防重记录与历史费用', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE billing_webhook_events
    MODIFY COLUMN webhook_id VARCHAR(128) NOT NULL COMMENT '支付渠道的 Webhook 事件标识',
    MODIFY COLUMN type VARCHAR(64) NOT NULL COMMENT 'Webhook 事件类型',
    MODIFY COLUMN received_at DATETIME(6) NOT NULL COMMENT 'Webhook 接收时间',
    COMMENT = '已接收的计费 Webhook 事件，用于防重', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE refund_requests
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '退款申请标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN order_id VARCHAR(40) NOT NULL COMMENT '关联的原结账订单标识',
    MODIFY COLUMN source VARCHAR(16) NOT NULL COMMENT '退款申请来源',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '退款处理状态',
    MODIFY COLUMN amount_minor BIGINT NOT NULL COMMENT '申请退款的金额，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL COMMENT '币种代码',
    MODIFY COLUMN credits_micro BIGINT NOT NULL COMMENT '退款对应的点数，单位为微点',
    MODIFY COLUMN reason VARCHAR(1024) NOT NULL COMMENT '申请退款的原因',
    MODIFY COLUMN requested_by VARCHAR(40) NOT NULL COMMENT '退款申请人标识',
    MODIFY COLUMN reviewed_by VARCHAR(40) NOT NULL COMMENT '退款审核人标识',
    MODIFY COLUMN review_note VARCHAR(1024) NOT NULL COMMENT '退款审核意见',
    MODIFY COLUMN dodo_refund_id VARCHAR(64) COMMENT 'Dodo 退款记录标识',
    MODIFY COLUMN failure VARCHAR(256) NOT NULL COMMENT '退款执行失败的摘要',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '订单退款申请、审核与渠道执行结果', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE account_sales_lock
    MODIFY COLUMN id BIGINT NOT NULL COMMENT '单行锁记录标识，固定为 1',
    COMMENT = '串行化账号商品库存变更的事务锁记录', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE account_sales
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '账号商品标识',
    MODIFY COLUMN node_id VARCHAR(128) NOT NULL COMMENT '节点标识',
    MODIFY COLUMN runtime_id VARCHAR(128) NOT NULL COMMENT '节点上的运行时标识',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '账号商品展示名称',
    MODIFY COLUMN active BOOLEAN NOT NULL COMMENT '是否上架',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '当前获配账号的租户标识，未分配时为空',
    MODIFY COLUMN expires_at DATETIME(6) NOT NULL COMMENT '当前账号权益到期时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN price_minor BIGINT NOT NULL DEFAULT 0 COMMENT '账号商品售价，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL DEFAULT '' COMMENT '币种代码',
    MODIFY COLUMN dodo_product_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'Dodo 支付平台上的商品标识',
    MODIFY COLUMN kind VARCHAR(16) NOT NULL DEFAULT '' COMMENT '账号商品类型',
    COMMENT = '可销售账号的库存、定价与当前分配', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE account_purchases
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '账号购买记录标识',
    MODIFY COLUMN account_id VARCHAR(40) NOT NULL COMMENT '所购账号商品标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN price_micro BIGINT NOT NULL COMMENT '早期点数购买价格，单位为微点',
    MODIFY COLUMN expires_at DATETIME(6) NOT NULL COMMENT '本次购买取得的账号权益到期时间',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    COMMENT = '已完成的账号购买与权益历史', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE account_presales
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '预售商品标识',
    MODIFY COLUMN kind VARCHAR(16) NOT NULL COMMENT '预售账号类型',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '预售商品展示名称',
    MODIFY COLUMN price_minor BIGINT NOT NULL COMMENT '预售商品售价，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL COMMENT '币种代码',
    MODIFY COLUMN dodo_product_id VARCHAR(64) NOT NULL COMMENT 'Dodo 支付平台上的商品标识',
    MODIFY COLUMN capacity BIGINT NOT NULL COMMENT '预售名额上限',
    MODIFY COLUMN active BOOLEAN NOT NULL COMMENT '是否上架',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN alipay_amount BIGINT NOT NULL DEFAULT 0 COMMENT '兑换该预售商品所需的支付宝额度',
    COMMENT = '账号预售商品的定价、库存与上架状态', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE alipay_purchases
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '支付宝购买申请标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN action_id VARCHAR(64) NOT NULL COMMENT '调用方提交的操作标识，用于幂等',
    MODIFY COLUMN sku_id VARCHAR(128) NOT NULL COMMENT '支付宝商品规格标识',
    MODIFY COLUMN callback_url VARCHAR(512) NOT NULL COMMENT '额度购买流程传给支付宝的回调地址',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '购买链接申请状态',
    MODIFY COLUMN purchase_url VARCHAR(2048) NOT NULL COMMENT '支付宝返回的购买链接',
    MODIFY COLUMN expire_time VARCHAR(64) NOT NULL COMMENT '支付宝返回的链接失效时间原值',
    MODIFY COLUMN reason_code VARCHAR(64) NOT NULL COMMENT '支付宝返回的结果原因代码',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '支付宝额度购买链接申请记录', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE alipay_redemptions
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '支付宝额度兑换记录标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN action_id VARCHAR(64) NOT NULL COMMENT '调用方提交的操作标识，用于幂等',
    MODIFY COLUMN amount BIGINT NOT NULL COMMENT '提交兑换的支付宝额度数量',
    MODIFY COLUMN quota_unit VARCHAR(8) NOT NULL COMMENT '支付宝额度的计量单位',
    MODIFY COLUMN credits_micro BIGINT NOT NULL COMMENT '本次兑换对应的点数，单位为微点',
    MODIFY COLUMN reason VARCHAR(256) NOT NULL COMMENT '提交给支付宝的额度消耗原因',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '兑换处理状态',
    MODIFY COLUMN reason_code VARCHAR(64) NOT NULL COMMENT '支付宝返回的结果原因代码',
    MODIFY COLUMN consume_order_id VARCHAR(128) NOT NULL COMMENT '支付宝额度消耗订单标识',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN presale_order_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '关联的账号预售结账订单标识',
    COMMENT = '支付宝额度兑换点数或账号预售商品的处理记录', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE catalog_models
    MODIFY COLUMN model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '上游价目表中的模型标识',
    MODIFY COLUMN provider VARCHAR(64) NOT NULL COMMENT '模型提供方',
    MODIFY COLUMN mode VARCHAR(32) NOT NULL COMMENT '上游模型的能力模式',
    MODIFY COLUMN status VARCHAR(24) NOT NULL COMMENT '模型目录条目的状态',
    MODIFY COLUMN prices_hash VARCHAR(64) NOT NULL COMMENT '当前模型价格内容的摘要',
    MODIFY COLUMN rejected_hash VARCHAR(64) NOT NULL DEFAULT '' COMMENT '最近一次被拒绝的候选价格摘要',
    MODIFY COLUMN raw_json MEDIUMTEXT NOT NULL COMMENT '上游价目表的模型条目原始 JSON',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '同步到平台的上游模型目录与审核状态', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE catalog_prices
    MODIFY COLUMN id BIGINT AUTO_INCREMENT NOT NULL COMMENT '模型价格记录的自增标识',
    MODIFY COLUMN model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '上游价目表中的模型标识',
    MODIFY COLUMN meter VARCHAR(64) NOT NULL COMMENT '输入、输出或缓存等计量项代码',
    MODIFY COLUMN variant VARCHAR(32) NOT NULL DEFAULT '' COMMENT '计量项的价格变体标识',
    MODIFY COLUMN above_input_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '采用此价格所需超过的输入 Token 阈值',
    MODIFY COLUMN price_micro_per_m BIGINT NOT NULL COMMENT '每百万计量单位的价格，单位为微点',
    MODIFY COLUMN derived BOOLEAN NOT NULL DEFAULT FALSE COMMENT '是否由其他价格推导得到',
    MODIFY COLUMN valid_from DATETIME(6) NOT NULL COMMENT '本价格开始生效的时间',
    MODIFY COLUMN valid_to DATETIME(6) NULL COMMENT '本价格停止生效的时间，为空表示尚未结束',
    COMMENT = '上游模型各计量项的单价与生效区间', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE catalog_pending
    MODIFY COLUMN model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '发生变更的上游模型标识',
    MODIFY COLUMN kind VARCHAR(16) NOT NULL COMMENT '待审核的变更类型',
    MODIFY COLUMN proposed_json MEDIUMTEXT NOT NULL COMMENT '候选价格内容 JSON',
    MODIFY COLUMN prices_hash VARCHAR(64) NOT NULL COMMENT '候选价格内容的摘要',
    MODIFY COLUMN max_change_bp BIGINT NOT NULL COMMENT '最大价格变动幅度，单位为基点',
    MODIFY COLUMN detected_at DATETIME(6) NOT NULL COMMENT '首次发现待审核变更的时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '待运维审核的上游价目表变更', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE catalog_sync_state
    MODIFY COLUMN id BIGINT NOT NULL COMMENT '单行记录标识，固定为 1',
    MODIFY COLUMN source VARCHAR(512) NOT NULL DEFAULT '' COMMENT '当前上游价目表来源',
    MODIFY COLUMN content_hash VARCHAR(64) NOT NULL DEFAULT '' COMMENT '上一次成功同步的价目表内容摘要',
    MODIFY COLUMN embedded_hash VARCHAR(64) NOT NULL DEFAULT '' COMMENT '最近一次导入的内置价目表快照摘要',
    MODIFY COLUMN entry_count BIGINT NOT NULL DEFAULT 0 COMMENT '最近一次成功同步的模型条目数',
    MODIFY COLUMN version BIGINT NOT NULL DEFAULT 0 COMMENT '当前价目表版本号',
    MODIFY COLUMN synced_at DATETIME(6) NULL COMMENT '最近一次成功同步时间',
    MODIFY COLUMN checked_at DATETIME(6) NULL COMMENT '最近一次检查上游价目表的时间',
    MODIFY COLUMN last_error VARCHAR(512) NOT NULL DEFAULT '' COMMENT '最近一次同步错误的摘要',
    MODIFY COLUMN last_error_at DATETIME(6) NULL COMMENT '最近一次同步错误的发生时间',
    COMMENT = '上游价目表最近同步、检查与错误状态', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE pricing_revisions
    MODIFY COLUMN id BIGINT AUTO_INCREMENT NOT NULL COMMENT '计价配置版本的自增标识',
    MODIFY COLUMN effective_at DATETIME(6) NOT NULL COMMENT '此版本开始生效的时间',
    MODIFY COLUMN document MEDIUMTEXT NOT NULL COMMENT '完整的计价配置与方案快照 JSON',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN summary VARCHAR(512) NOT NULL COMMENT '本次配置变更摘要',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    COMMENT = '模型定价配置与价格方案的历史版本', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE tenant_price_plans
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '租户方案分配记录标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN plan_id VARCHAR(40) NOT NULL COMMENT '所分配的价格方案标识',
    MODIFY COLUMN valid_from DATETIME(6) NOT NULL COMMENT '本次方案分配开始生效的时间',
    MODIFY COLUMN valid_to DATETIME(6) NULL COMMENT '本次方案分配停止生效的时间，为空表示尚未结束',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    COMMENT = '租户分配的价格方案及生效区间', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE pricing_config_state
    MODIFY COLUMN id INT NOT NULL COMMENT '单行记录标识，固定为 1',
    MODIFY COLUMN version BIGINT NOT NULL COMMENT '当前定价配置状态版本号',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '定价配置变更的全局版本状态', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_billing
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '计费迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- billing/mysql/0016_gift_balance_and_settings.sql
ALTER TABLE tenant_billing
 ADD COLUMN gift_balance_micro BIGINT NOT NULL DEFAULT 0 COMMENT '当前余额中赠送点数的部分，单位为微点',
 ADD COLUMN usage_acc_gift_micro BIGINT NOT NULL DEFAULT 0 COMMENT '待封账用量中从赠送点数扣除的部分，单位为微点',
 ALGORITHM=INSTANT;
ALTER TABLE credit_ledger
 ADD COLUMN gift_amount_micro BIGINT NOT NULL DEFAULT 0 COMMENT '本次变动中赠送点数的部分，单位为微点',
 ADD COLUMN gift_balance_after_micro BIGINT NOT NULL DEFAULT 0 COMMENT '变动后余额中赠送点数的部分，单位为微点',
 ALGORITHM=INSTANT;
CREATE TABLE IF NOT EXISTS billing_settings (
 id INT NOT NULL COMMENT '单行记录标识，固定为 1',
 signup_grant_micro BIGINT NOT NULL COMMENT '自助注册租户获赠的点数，单位为微点',
 updated_by VARCHAR(40) NOT NULL COMMENT '最后修改的运维人员标识',
 updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
 PRIMARY KEY (id)
) ENGINE=InnoDB COMMENT='运维在后台保存的计费设置';

-- ============================================================
-- studio
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_studio (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- studio/mysql/0001_create_studio_conversations.sql
CREATE TABLE studio_conversations (
 id VARCHAR(40) NOT NULL,
 tenant_id VARCHAR(40) NOT NULL,
 user_id VARCHAR(40) NOT NULL,
 title VARCHAR(200) NOT NULL,
 model VARCHAR(128) NOT NULL,
 system_prompt TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY (id),
 INDEX idx_studio_conversations_owner (tenant_id, user_id, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- studio/mysql/0002_create_studio_messages.sql
CREATE TABLE studio_messages (
 id VARCHAR(40) NOT NULL,
 conversation_id VARCHAR(40) NOT NULL,
 tenant_id VARCHAR(40) NOT NULL,
 user_id VARCHAR(40) NOT NULL,
 seq BIGINT NOT NULL,
 role VARCHAR(16) NOT NULL,
 content MEDIUMTEXT NOT NULL,
 reasoning MEDIUMTEXT NOT NULL,
 status VARCHAR(16) NOT NULL,
 model VARCHAR(128) NOT NULL,
 prompt_tokens BIGINT NOT NULL DEFAULT 0,
 completion_tokens BIGINT NOT NULL DEFAULT 0,
 error_code VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY (id),
 UNIQUE KEY uk_studio_messages_seq (conversation_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- studio/mysql/0003_table_and_column_comments.sql
ALTER TABLE studio_conversations
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT 'Studio 对话标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN user_id VARCHAR(40) NOT NULL COMMENT '所属用户标识',
    MODIFY COLUMN title VARCHAR(200) NOT NULL COMMENT '对话标题',
    MODIFY COLUMN model VARCHAR(128) NOT NULL COMMENT '对话选用的模型别名',
    MODIFY COLUMN system_prompt TEXT NOT NULL COMMENT '为对话配置的系统提示词',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = 'Studio 用户创建的对话', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE studio_messages
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT 'Studio 消息标识',
    MODIFY COLUMN conversation_id VARCHAR(40) NOT NULL COMMENT '所属 Studio 对话标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN user_id VARCHAR(40) NOT NULL COMMENT '所属用户标识',
    MODIFY COLUMN seq BIGINT NOT NULL COMMENT '消息在对话中的顺序号',
    MODIFY COLUMN role VARCHAR(16) NOT NULL COMMENT '消息发送方角色',
    MODIFY COLUMN content MEDIUMTEXT NOT NULL COMMENT '消息正文',
    MODIFY COLUMN reasoning MEDIUMTEXT NOT NULL COMMENT '模型返回的思考内容',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '消息生成状态',
    MODIFY COLUMN model VARCHAR(128) NOT NULL COMMENT '生成消息时使用的模型别名',
    MODIFY COLUMN prompt_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '生成本条回复的输入 Token 数',
    MODIFY COLUMN completion_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '生成本条回复的输出 Token 数',
    MODIFY COLUMN error_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '生成失败时的错误代码',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    COMMENT = 'Studio 对话中的消息与生成结果', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_studio
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = 'Studio 对话迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- studio/mysql/0004_create_studio_flows.sql
CREATE TABLE IF NOT EXISTS studio_flows (
 id VARCHAR(40) NOT NULL COMMENT 'Studio 编排标识',
 tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
 user_id VARCHAR(40) NOT NULL COMMENT '所属用户标识',
 name VARCHAR(200) NOT NULL COMMENT '编排名称',
 version BIGINT NOT NULL COMMENT '最新保存的版本号',
 created_at DATETIME(6) NOT NULL COMMENT '创建时间',
 updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
 PRIMARY KEY (id),
 INDEX idx_studio_flows_owner (tenant_id, user_id, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Studio 用户创建的编排';

-- studio/mysql/0005_create_studio_flow_versions.sql
CREATE TABLE IF NOT EXISTS studio_flow_versions (
 flow_id VARCHAR(40) NOT NULL COMMENT '所属 Studio 编排标识',
 version BIGINT NOT NULL COMMENT '版本号，从 1 开始递增',
 definition MEDIUMTEXT NOT NULL COMMENT '通过校验的编排定义 JSON',
 created_at DATETIME(6) NOT NULL COMMENT '保存时间',
 PRIMARY KEY (flow_id, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Studio 编排每次保存的不可变版本';

-- studio/mysql/0006_create_studio_flow_runs.sql
CREATE TABLE IF NOT EXISTS studio_flow_runs (
 id VARCHAR(40) NOT NULL COMMENT 'Studio 编排运行标识',
 flow_id VARCHAR(40) NOT NULL COMMENT '所属 Studio 编排标识',
 tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
 user_id VARCHAR(40) NOT NULL COMMENT '发起运行的用户标识',
 flow_version BIGINT NOT NULL COMMENT '运行所用的编排版本号',
 status VARCHAR(16) NOT NULL COMMENT '运行状态',
 error_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '运行失败时的错误代码',
 failed_node VARCHAR(64) NOT NULL DEFAULT '' COMMENT '导致运行失败的节点标识',
 inputs MEDIUMTEXT NOT NULL COMMENT '提交时的输入值 JSON',
 outputs MEDIUMTEXT NOT NULL COMMENT '运行结果字段 JSON',
 prompt_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '全部模型调用的输入 Token 数',
 completion_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '全部模型调用的输出 Token 数',
 replica_id VARCHAR(128) NOT NULL COMMENT '执行运行的 Gateway 副本标识',
 lease_until DATETIME(6) NOT NULL COMMENT '执行副本持有运行的租约到期时间',
 created_at DATETIME(6) NOT NULL COMMENT '创建时间',
 finished_at DATETIME(6) NULL COMMENT '结束时间，运行中为空',
 PRIMARY KEY (id),
 INDEX idx_studio_flow_runs_flow (flow_id, created_at),
 INDEX idx_studio_flow_runs_active (tenant_id, status, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Studio 编排的运行记录';

-- studio/mysql/0007_create_studio_flow_run_steps.sql
CREATE TABLE IF NOT EXISTS studio_flow_run_steps (
 run_id VARCHAR(40) NOT NULL COMMENT '所属 Studio 编排运行标识',
 node_id VARCHAR(64) NOT NULL COMMENT '编排节点标识',
 tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
 user_id VARCHAR(40) NOT NULL COMMENT '发起运行的用户标识',
 node_type VARCHAR(32) NOT NULL COMMENT '编排节点类型',
 status VARCHAR(16) NOT NULL COMMENT '节点结束状态',
 error_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '节点失败时的错误代码',
 model VARCHAR(128) NOT NULL DEFAULT '' COMMENT '节点调用的模型别名',
 inputs MEDIUMTEXT NOT NULL COMMENT '节点实际求值的输入 JSON，已截断',
 outputs MEDIUMTEXT NOT NULL COMMENT '节点输出 JSON，已截断',
 prompt_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '节点模型调用的输入 Token 数',
 completion_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '节点模型调用的输出 Token 数',
 started_at DATETIME(6) NOT NULL COMMENT '节点开始时间',
 finished_at DATETIME(6) NOT NULL COMMENT '节点结束时间',
 PRIMARY KEY (run_id, node_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Studio 编排运行中每个节点的结果';

-- ============================================================
-- jobs
-- ============================================================

CREATE TABLE IF NOT EXISTS schema_migrations_jobs (id VARCHAR(255) PRIMARY KEY, applied_at DATETIME(6) NOT NULL, checksum VARCHAR(64) NOT NULL DEFAULT '', dirty BOOLEAN NOT NULL DEFAULT FALSE) ENGINE=InnoDB;

-- jobs/0001_create_jobs.sql
CREATE TABLE jobs (
    id               VARCHAR(64)   NOT NULL,
    tenant_id        VARCHAR(32)   NOT NULL,
    workflow_id      VARCHAR(128)  NOT NULL,
    workflow_version VARCHAR(64)   NOT NULL DEFAULT '',
    node_id          VARCHAR(128)  NOT NULL,
    runtime_id       VARCHAR(128)  NOT NULL,
    backend_run_id   VARCHAR(256)  NOT NULL,
    state            VARCHAR(16)   NOT NULL,
    error_summary    VARCHAR(512)  NOT NULL DEFAULT '',
    observed_seq     BIGINT        NOT NULL DEFAULT 0,
    created_at       DATETIME(6)   NOT NULL,
    updated_at       DATETIME(6)   NOT NULL,
    terminal_at      DATETIME(6)   NULL,
    PRIMARY KEY (id),
    KEY idx_jobs_tenant_created (tenant_id, created_at, id),
    KEY idx_jobs_tenant_status (tenant_id, state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- jobs/0002_create_job_artifacts.sql
CREATE TABLE job_artifacts (
    id         VARCHAR(64)  NOT NULL,
    job_id     VARCHAR(64)  NOT NULL,
    tenant_id  VARCHAR(32)  NOT NULL,
    filename   VARCHAR(512) NOT NULL,
    subfolder  VARCHAR(512) NOT NULL DEFAULT '',
    type       VARCHAR(32)  NOT NULL DEFAULT '',
    created_at DATETIME(6)  NOT NULL,
    PRIMARY KEY (id),
    KEY idx_job_artifacts_job (job_id),
    KEY idx_job_artifacts_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- jobs/0003_add_jobs_node_runtime_index.sql
ALTER TABLE jobs ADD INDEX idx_jobs_node_runtime (node_id, runtime_id, state);

-- jobs/0004_add_job_artifacts_storage_fields.sql
ALTER TABLE job_artifacts
    ADD COLUMN sha256       VARCHAR(64)   NOT NULL DEFAULT '',
    ADD COLUMN size_bytes   BIGINT        NOT NULL DEFAULT 0,
    ADD COLUMN content_type VARCHAR(255)  NOT NULL DEFAULT '',
    ADD COLUMN storage_key  VARCHAR(1024) NOT NULL DEFAULT '';

-- jobs/0005_add_job_artifacts_type_created_index.sql
ALTER TABLE job_artifacts ADD INDEX idx_job_artifacts_type_created (type, created_at, id);

-- jobs/0006_widen_tenant_id.sql
ALTER TABLE jobs
 MODIFY tenant_id VARCHAR(40) NOT NULL;
ALTER TABLE job_artifacts
 MODIFY tenant_id VARCHAR(40) NOT NULL;

-- jobs/0007_table_and_column_comments.sql
ALTER TABLE jobs
    MODIFY COLUMN id VARCHAR(64)   NOT NULL COMMENT '异步任务标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN workflow_id VARCHAR(128)  NOT NULL COMMENT '执行的工作流标识',
    MODIFY COLUMN workflow_version VARCHAR(64)   NOT NULL DEFAULT '' COMMENT '执行时选用的工作流版本',
    MODIFY COLUMN node_id VARCHAR(128)  NOT NULL COMMENT '节点标识',
    MODIFY COLUMN runtime_id VARCHAR(128)  NOT NULL COMMENT '节点上的运行时标识',
    MODIFY COLUMN backend_run_id VARCHAR(256)  NOT NULL COMMENT '后端执行实例标识',
    MODIFY COLUMN state VARCHAR(16)   NOT NULL COMMENT '任务执行状态',
    MODIFY COLUMN error_summary VARCHAR(512)  NOT NULL DEFAULT '' COMMENT '任务失败原因摘要',
    MODIFY COLUMN observed_seq BIGINT        NOT NULL DEFAULT 0 COMMENT '最后接收的后端事件顺序号',
    MODIFY COLUMN created_at DATETIME(6)   NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6)   NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN terminal_at DATETIME(6)   NULL COMMENT '任务进入终态的时间',
    COMMENT = '工作流异步任务的执行与状态跟踪记录', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE job_artifacts
    MODIFY COLUMN id VARCHAR(64)  NOT NULL COMMENT '任务产物标识',
    MODIFY COLUMN job_id VARCHAR(64)  NOT NULL COMMENT '生成产物的任务标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN filename VARCHAR(512) NOT NULL COMMENT '产物文件名',
    MODIFY COLUMN subfolder VARCHAR(512) NOT NULL DEFAULT '' COMMENT '产物在后端的子目录',
    MODIFY COLUMN type VARCHAR(32)  NOT NULL DEFAULT '' COMMENT '后端报告的产物类型',
    MODIFY COLUMN created_at DATETIME(6)  NOT NULL COMMENT '创建时间',
    MODIFY COLUMN sha256 VARCHAR(64)   NOT NULL DEFAULT '' COMMENT '产物文件的 SHA-256 摘要',
    MODIFY COLUMN size_bytes BIGINT        NOT NULL DEFAULT 0 COMMENT '产物文件大小，单位为字节',
    MODIFY COLUMN content_type VARCHAR(255)  NOT NULL DEFAULT '' COMMENT '产物的媒体类型',
    MODIFY COLUMN storage_key VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '产物在对象存储中的键',
    COMMENT = '工作流任务生成的产物及其存储元数据', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_jobs
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '异步任务迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

-- jobs/0008_add_job_artifacts_file_id.sql
ALTER TABLE job_artifacts
    ADD COLUMN file_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '所存字节对应的文件标识，只有元数据时为空';

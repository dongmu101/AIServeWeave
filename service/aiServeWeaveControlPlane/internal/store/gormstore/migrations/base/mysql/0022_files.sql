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
ALTER TABLE files PARTITION BY RANGE COLUMNS(created_at) (/*aisw:partitions table=files*/);

CREATE TABLE files (
 id VARCHAR(64) NOT NULL,
 tenant_id VARCHAR(40) NOT NULL,
 storage VARCHAR(16) NOT NULL,
 object_key VARCHAR(1024) NOT NULL,
 biz_type VARCHAR(32) NOT NULL,
 biz_id VARCHAR(64) NOT NULL,
 kind VARCHAR(32) NOT NULL,
 filename VARCHAR(512) NOT NULL,
 content_type VARCHAR(255) NOT NULL,
 size_bytes BIGINT NOT NULL,
 sha256 VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
/*aisw:partitions table=files*/;
CREATE INDEX idx_files_storage_biz_kind_created ON files (storage, biz_type, kind, created_at);
CREATE INDEX idx_files_tenant_storage_created ON files (tenant_id, storage, created_at);
CREATE INDEX idx_files_status_created ON files (status, created_at);
CREATE INDEX idx_files_biz ON files (biz_type, biz_id);
COMMENT ON TABLE files IS '平台写入对象存储的文件登记';
COMMENT ON COLUMN files.id IS '文件标识：产物 id 或 Studio 文件 id';
COMMENT ON COLUMN files.tenant_id IS '所属租户标识';
COMMENT ON COLUMN files.storage IS '所在存储：deployment 或 tenant';
COMMENT ON COLUMN files.object_key IS '在所在存储中的键';
COMMENT ON COLUMN files.biz_type IS '归属的业务类型：job 或 studio_run';
COMMENT ON COLUMN files.biz_id IS '归属的业务对象标识';
COMMENT ON COLUMN files.kind IS '业务内的类别';
COMMENT ON COLUMN files.filename IS '展示用文件名';
COMMENT ON COLUMN files.content_type IS '媒体类型';
COMMENT ON COLUMN files.size_bytes IS '文件大小，单位为字节';
COMMENT ON COLUMN files.sha256 IS '文件的 SHA-256 摘要';
COMMENT ON COLUMN files.status IS '状态：pending 或 ready';
COMMENT ON COLUMN files.created_at IS '登记时间，分区键';
COMMENT ON COLUMN files.updated_at IS '最后更新时间';

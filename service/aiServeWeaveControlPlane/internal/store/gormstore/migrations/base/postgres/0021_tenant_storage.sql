CREATE TABLE IF NOT EXISTS tenant_storage (
 tenant_id VARCHAR(40) NOT NULL PRIMARY KEY,
 kind VARCHAR(16) NOT NULL,
 endpoint VARCHAR(2048) NOT NULL,
 region VARCHAR(64) NOT NULL,
 bucket VARCHAR(255) NOT NULL,
 prefix VARCHAR(512) NOT NULL,
 path_style BOOLEAN NOT NULL,
 username VARCHAR(256) NOT NULL,
 secret_ciphertext TEXT NOT NULL,
 secret_hint VARCHAR(8) NOT NULL,
 retention_days BIGINT NOT NULL,
 version BIGINT NOT NULL,
 updated_by VARCHAR(40) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
COMMENT ON TABLE tenant_storage IS '租户自有对象存储';
COMMENT ON COLUMN tenant_storage.tenant_id IS '租户标识';
COMMENT ON COLUMN tenant_storage.kind IS '存储种类：s3 或 webdav';
COMMENT ON COLUMN tenant_storage.endpoint IS 'S3 端点（AWS 时为空）或 WebDAV URL';
COMMENT ON COLUMN tenant_storage.region IS 'S3 区域';
COMMENT ON COLUMN tenant_storage.bucket IS 'S3 存储桶';
COMMENT ON COLUMN tenant_storage.prefix IS 'S3 key 前缀或 WebDAV 目录';
COMMENT ON COLUMN tenant_storage.path_style IS 'S3 是否使用 path-style 寻址';
COMMENT ON COLUMN tenant_storage.username IS 'WebDAV 用户名';
COMMENT ON COLUMN tenant_storage.secret_ciphertext IS '主密钥加密的凭据 JSON';
COMMENT ON COLUMN tenant_storage.secret_hint IS 'S3 access key id 末四位';
COMMENT ON COLUMN tenant_storage.retention_days IS '保留天数，0 表示不删除';
COMMENT ON COLUMN tenant_storage.version IS '每次保存加一的版本号';
COMMENT ON COLUMN tenant_storage.updated_by IS '最后保存的用户标识';
COMMENT ON COLUMN tenant_storage.created_at IS '创建时间';
COMMENT ON COLUMN tenant_storage.updated_at IS '最后更新时间';

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

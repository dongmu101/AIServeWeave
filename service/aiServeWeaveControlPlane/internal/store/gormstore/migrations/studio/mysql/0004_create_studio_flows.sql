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

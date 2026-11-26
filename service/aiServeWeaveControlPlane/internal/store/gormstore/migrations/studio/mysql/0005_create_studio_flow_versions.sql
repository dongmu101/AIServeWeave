CREATE TABLE IF NOT EXISTS studio_flow_versions (
 flow_id VARCHAR(40) NOT NULL COMMENT '所属 Studio 编排标识',
 version BIGINT NOT NULL COMMENT '版本号，从 1 开始递增',
 definition MEDIUMTEXT NOT NULL COMMENT '通过校验的编排定义 JSON',
 created_at DATETIME(6) NOT NULL COMMENT '保存时间',
 PRIMARY KEY (flow_id, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Studio 编排每次保存的不可变版本';

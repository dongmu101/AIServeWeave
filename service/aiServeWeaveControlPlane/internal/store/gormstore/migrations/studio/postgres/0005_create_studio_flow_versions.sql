CREATE TABLE IF NOT EXISTS studio_flow_versions (
 flow_id VARCHAR(40) NOT NULL,
 version BIGINT NOT NULL,
 definition TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (flow_id, version)
);
COMMENT ON TABLE studio_flow_versions IS 'Studio 编排每次保存的不可变版本';
COMMENT ON COLUMN studio_flow_versions.flow_id IS '所属 Studio 编排标识';
COMMENT ON COLUMN studio_flow_versions.version IS '版本号，从 1 开始递增';
COMMENT ON COLUMN studio_flow_versions.definition IS '通过校验的编排定义 JSON';
COMMENT ON COLUMN studio_flow_versions.created_at IS '保存时间';

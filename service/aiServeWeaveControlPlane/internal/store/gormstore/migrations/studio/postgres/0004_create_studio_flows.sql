CREATE TABLE IF NOT EXISTS studio_flows (
 id VARCHAR(40) NOT NULL PRIMARY KEY,
 tenant_id VARCHAR(40) NOT NULL,
 user_id VARCHAR(40) NOT NULL,
 name VARCHAR(200) NOT NULL,
 version BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_studio_flows_owner ON studio_flows (tenant_id, user_id, updated_at);
COMMENT ON TABLE studio_flows IS 'Studio 用户创建的编排';
COMMENT ON COLUMN studio_flows.id IS 'Studio 编排标识';
COMMENT ON COLUMN studio_flows.tenant_id IS '所属租户标识';
COMMENT ON COLUMN studio_flows.user_id IS '所属用户标识';
COMMENT ON COLUMN studio_flows.name IS '编排名称';
COMMENT ON COLUMN studio_flows.version IS '最新保存的版本号';
COMMENT ON COLUMN studio_flows.created_at IS '创建时间';
COMMENT ON COLUMN studio_flows.updated_at IS '最后更新时间';

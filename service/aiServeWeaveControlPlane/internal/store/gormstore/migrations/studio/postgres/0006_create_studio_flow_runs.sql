CREATE TABLE IF NOT EXISTS studio_flow_runs (
 id VARCHAR(40) NOT NULL PRIMARY KEY,
 flow_id VARCHAR(40) NOT NULL,
 tenant_id VARCHAR(40) NOT NULL,
 user_id VARCHAR(40) NOT NULL,
 flow_version BIGINT NOT NULL,
 status VARCHAR(16) NOT NULL,
 error_code VARCHAR(64) NOT NULL DEFAULT '',
 failed_node VARCHAR(64) NOT NULL DEFAULT '',
 inputs TEXT NOT NULL,
 outputs TEXT NOT NULL,
 prompt_tokens BIGINT NOT NULL DEFAULT 0,
 completion_tokens BIGINT NOT NULL DEFAULT 0,
 replica_id VARCHAR(128) NOT NULL,
 lease_until TIMESTAMPTZ NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 finished_at TIMESTAMPTZ NULL
);
CREATE INDEX IF NOT EXISTS idx_studio_flow_runs_flow ON studio_flow_runs (flow_id, created_at);
CREATE INDEX IF NOT EXISTS idx_studio_flow_runs_active ON studio_flow_runs (tenant_id, status, lease_until);
COMMENT ON TABLE studio_flow_runs IS 'Studio 编排的运行记录';
COMMENT ON COLUMN studio_flow_runs.id IS 'Studio 编排运行标识';
COMMENT ON COLUMN studio_flow_runs.flow_id IS '所属 Studio 编排标识';
COMMENT ON COLUMN studio_flow_runs.tenant_id IS '所属租户标识';
COMMENT ON COLUMN studio_flow_runs.user_id IS '发起运行的用户标识';
COMMENT ON COLUMN studio_flow_runs.flow_version IS '运行所用的编排版本号';
COMMENT ON COLUMN studio_flow_runs.status IS '运行状态';
COMMENT ON COLUMN studio_flow_runs.error_code IS '运行失败时的错误代码';
COMMENT ON COLUMN studio_flow_runs.failed_node IS '导致运行失败的节点标识';
COMMENT ON COLUMN studio_flow_runs.inputs IS '提交时的输入值 JSON';
COMMENT ON COLUMN studio_flow_runs.outputs IS '运行结果字段 JSON';
COMMENT ON COLUMN studio_flow_runs.prompt_tokens IS '全部模型调用的输入 Token 数';
COMMENT ON COLUMN studio_flow_runs.completion_tokens IS '全部模型调用的输出 Token 数';
COMMENT ON COLUMN studio_flow_runs.replica_id IS '执行运行的 Gateway 副本标识';
COMMENT ON COLUMN studio_flow_runs.lease_until IS '执行副本持有运行的租约到期时间';
COMMENT ON COLUMN studio_flow_runs.created_at IS '创建时间';
COMMENT ON COLUMN studio_flow_runs.finished_at IS '结束时间，运行中为空';

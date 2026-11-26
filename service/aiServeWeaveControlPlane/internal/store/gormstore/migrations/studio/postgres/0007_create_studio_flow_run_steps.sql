CREATE TABLE IF NOT EXISTS studio_flow_run_steps (
 run_id VARCHAR(40) NOT NULL,
 node_id VARCHAR(64) NOT NULL,
 tenant_id VARCHAR(40) NOT NULL,
 user_id VARCHAR(40) NOT NULL,
 node_type VARCHAR(32) NOT NULL,
 status VARCHAR(16) NOT NULL,
 error_code VARCHAR(64) NOT NULL DEFAULT '',
 model VARCHAR(128) NOT NULL DEFAULT '',
 inputs TEXT NOT NULL,
 outputs TEXT NOT NULL,
 prompt_tokens BIGINT NOT NULL DEFAULT 0,
 completion_tokens BIGINT NOT NULL DEFAULT 0,
 started_at TIMESTAMPTZ NOT NULL,
 finished_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (run_id, node_id)
);
COMMENT ON TABLE studio_flow_run_steps IS 'Studio 编排运行中每个节点的结果';
COMMENT ON COLUMN studio_flow_run_steps.run_id IS '所属 Studio 编排运行标识';
COMMENT ON COLUMN studio_flow_run_steps.node_id IS '编排节点标识';
COMMENT ON COLUMN studio_flow_run_steps.tenant_id IS '所属租户标识';
COMMENT ON COLUMN studio_flow_run_steps.user_id IS '发起运行的用户标识';
COMMENT ON COLUMN studio_flow_run_steps.node_type IS '编排节点类型';
COMMENT ON COLUMN studio_flow_run_steps.status IS '节点结束状态';
COMMENT ON COLUMN studio_flow_run_steps.error_code IS '节点失败时的错误代码';
COMMENT ON COLUMN studio_flow_run_steps.model IS '节点调用的模型别名';
COMMENT ON COLUMN studio_flow_run_steps.inputs IS '节点实际求值的输入 JSON，已截断';
COMMENT ON COLUMN studio_flow_run_steps.outputs IS '节点输出 JSON，已截断';
COMMENT ON COLUMN studio_flow_run_steps.prompt_tokens IS '节点模型调用的输入 Token 数';
COMMENT ON COLUMN studio_flow_run_steps.completion_tokens IS '节点模型调用的输出 Token 数';
COMMENT ON COLUMN studio_flow_run_steps.started_at IS '节点开始时间';
COMMENT ON COLUMN studio_flow_run_steps.finished_at IS '节点结束时间';

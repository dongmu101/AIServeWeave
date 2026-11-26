CREATE TABLE IF NOT EXISTS studio_flow_run_steps (
 run_id VARCHAR(40) NOT NULL COMMENT '所属 Studio 编排运行标识',
 node_id VARCHAR(64) NOT NULL COMMENT '编排节点标识',
 tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
 user_id VARCHAR(40) NOT NULL COMMENT '发起运行的用户标识',
 node_type VARCHAR(32) NOT NULL COMMENT '编排节点类型',
 status VARCHAR(16) NOT NULL COMMENT '节点结束状态',
 error_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '节点失败时的错误代码',
 model VARCHAR(128) NOT NULL DEFAULT '' COMMENT '节点调用的模型别名',
 inputs MEDIUMTEXT NOT NULL COMMENT '节点实际求值的输入 JSON，已截断',
 outputs MEDIUMTEXT NOT NULL COMMENT '节点输出 JSON，已截断',
 prompt_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '节点模型调用的输入 Token 数',
 completion_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '节点模型调用的输出 Token 数',
 started_at DATETIME(6) NOT NULL COMMENT '节点开始时间',
 finished_at DATETIME(6) NOT NULL COMMENT '节点结束时间',
 PRIMARY KEY (run_id, node_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='Studio 编排运行中每个节点的结果';

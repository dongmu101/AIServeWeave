ALTER TABLE response_turns
    MODIFY COLUMN id VARCHAR(64) NOT NULL COMMENT '当前响应的标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN previous_response_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '上一轮的响应标识，首轮为空',
    MODIFY COLUMN model VARCHAR(128) NOT NULL DEFAULT '' COMMENT '当前轮次使用的模型别名',
    MODIFY COLUMN messages TEXT NOT NULL COMMENT '续接对话所需的消息 JSON',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    COMMENT = 'Responses API 保存的对话轮次，用于续接上下文', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_response_turns
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = 'Responses 对话轮次迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

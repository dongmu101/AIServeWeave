ALTER TABLE usage_records
    MODIFY COLUMN id VARCHAR(64) NOT NULL COMMENT '用量记录标识，用于重复上报防重',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN model VARCHAR(128) NOT NULL COMMENT '计价使用的模型别名',
    MODIFY COLUMN endpoint VARCHAR(24) NOT NULL COMMENT '请求入口类别',
    MODIFY COLUMN prompt_tokens BIGINT NOT NULL COMMENT '输入 Token 总数，包含缓存部分',
    MODIFY COLUMN completion_tokens BIGINT NOT NULL COMMENT '输出 Token 总数',
    MODIFY COLUMN total_tokens BIGINT NOT NULL COMMENT '输入和输出 Token 总数',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '请求发生时间，用于分区和选择生效价格',
    MODIFY COLUMN key_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '发起请求的 API Key 标识，未归属时为空',
    MODIFY COLUMN user_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '用量归属的用户标识，未归属时为空',
    MODIFY COLUMN cache_read_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '命中缓存读取的输入 Token 数',
    MODIFY COLUMN cache_write_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '写入缓存的输入 Token 数',
    MODIFY COLUMN cost_micro BIGINT NOT NULL DEFAULT 0 COMMENT '本次请求的计费金额，单位为微点',
    MODIFY COLUMN base_cost_micro BIGINT NOT NULL DEFAULT 0 COMMENT '应用价格倍率前的基础费用，单位为微点',
    MODIFY COLUMN rate_ppm BIGINT NOT NULL DEFAULT 0 COMMENT '价格倍率，以百万分之一为单位',
    MODIFY COLUMN price_source VARCHAR(16) NOT NULL DEFAULT '' COMMENT '本次请求采用的价格来源',
    MODIFY COLUMN details TEXT NULL COMMENT '逐计量项的用量、单价和费用明细 JSON',
    MODIFY COLUMN charged BOOLEAN NULL COMMENT '是否实际扣点，未记录扣费结果时为 NULL',
    COMMENT = '请求级模型用量、计价与扣费明细', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_usage_records
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '用量记录迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

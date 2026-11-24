COMMENT ON TABLE response_turns IS 'Responses API 保存的对话轮次，用于续接上下文';
COMMENT ON COLUMN response_turns.id IS '当前响应的标识';
COMMENT ON COLUMN response_turns.tenant_id IS '所属租户标识';
COMMENT ON COLUMN response_turns.previous_response_id IS '上一轮的响应标识，首轮为空';
COMMENT ON COLUMN response_turns.model IS '当前轮次使用的模型别名';
COMMENT ON COLUMN response_turns.messages IS '续接对话所需的消息 JSON';
COMMENT ON COLUMN response_turns.created_at IS '创建时间';

COMMENT ON TABLE schema_migrations_response_turns IS 'Responses 对话轮次迁移的版本账本';
COMMENT ON COLUMN schema_migrations_response_turns.id IS '迁移 SQL 文件名';
COMMENT ON COLUMN schema_migrations_response_turns.applied_at IS '迁移完成时间，未完成时记录本次尝试的开始时间';
COMMENT ON COLUMN schema_migrations_response_turns.checksum IS '迁移 SQL 的 SHA-256 校验和';
COMMENT ON COLUMN schema_migrations_response_turns.dirty IS '迁移是否仍未完成';

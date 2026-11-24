COMMENT ON TABLE request_logs IS 'Gateway 上报的租户 API 请求日志与错误信息';
COMMENT ON COLUMN request_logs.id IS '请求日志标识';
COMMENT ON COLUMN request_logs.tenant_id IS '所属租户标识';
COMMENT ON COLUMN request_logs.key_display IS '发起请求的 API Key 脱敏显示值';
COMMENT ON COLUMN request_logs.endpoint IS '请求入口类别';
COMMENT ON COLUMN request_logs.status_code IS '返回给调用方的 HTTP 状态码';
COMMENT ON COLUMN request_logs.outcome IS '请求结果分类';
COMMENT ON COLUMN request_logs.duration_ms IS '请求耗时，单位毫秒';
COMMENT ON COLUMN request_logs.created_at IS '请求日志记录时间';
COMMENT ON COLUMN request_logs.model IS '请求使用的模型别名';
COMMENT ON COLUMN request_logs.error_code IS '请求错误代码';
COMMENT ON COLUMN request_logs.error_message IS '返回给调用方的错误消息';
COMMENT ON COLUMN request_logs.error_detail IS '供排查使用的错误详情';

COMMENT ON TABLE schema_migrations_request_logs IS '请求日志迁移的版本账本';
COMMENT ON COLUMN schema_migrations_request_logs.id IS '迁移 SQL 文件名';
COMMENT ON COLUMN schema_migrations_request_logs.applied_at IS '迁移完成时间，未完成时记录本次尝试的开始时间';
COMMENT ON COLUMN schema_migrations_request_logs.checksum IS '迁移 SQL 的 SHA-256 校验和';
COMMENT ON COLUMN schema_migrations_request_logs.dirty IS '迁移是否仍未完成';

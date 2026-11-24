COMMENT ON TABLE metrics_history_points IS '按时间桶保存的监控指标历史采样点';
COMMENT ON COLUMN metrics_history_points.id IS '指标采样点的自增标识';
COMMENT ON COLUMN metrics_history_points.metric IS '指标名称';
COMMENT ON COLUMN metrics_history_points.labels IS '用于区分指标序列的标签集合';
COMMENT ON COLUMN metrics_history_points.bucket_at IS '采样时间桶的起始时间';
COMMENT ON COLUMN metrics_history_points.value IS '指标采样值';

COMMENT ON TABLE schema_migrations_metrics_history IS '指标历史迁移的版本账本';
COMMENT ON COLUMN schema_migrations_metrics_history.id IS '迁移 SQL 文件名';
COMMENT ON COLUMN schema_migrations_metrics_history.applied_at IS '迁移完成时间，未完成时记录本次尝试的开始时间';
COMMENT ON COLUMN schema_migrations_metrics_history.checksum IS '迁移 SQL 的 SHA-256 校验和';
COMMENT ON COLUMN schema_migrations_metrics_history.dirty IS '迁移是否仍未完成';

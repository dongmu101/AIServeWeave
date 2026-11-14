DROP INDEX IF EXISTS idx_metrics_history_points_series;
DROP INDEX IF EXISTS idx_metrics_history_points_bucket;
ALTER TABLE metrics_history_points DROP CONSTRAINT IF EXISTS metrics_history_points_pkey;
ALTER SEQUENCE metrics_history_points_id_seq OWNED BY NONE;
ALTER TABLE metrics_history_points RENAME TO metrics_history_points_unpartitioned;
CREATE TABLE metrics_history_points (
 id BIGINT NOT NULL DEFAULT nextval('metrics_history_points_id_seq'),
 metric VARCHAR(128) NOT NULL,
 labels VARCHAR(512) NOT NULL,
 bucket_at TIMESTAMPTZ NOT NULL,
 value DOUBLE PRECISION NOT NULL,
 PRIMARY KEY (id, bucket_at)
) PARTITION BY RANGE (bucket_at);
/*aisw:partitions table=metrics_history_points source=metrics_history_points_unpartitioned*/;
INSERT INTO metrics_history_points (id, metric, labels, bucket_at, value)
 SELECT id, metric, labels, bucket_at, value FROM metrics_history_points_unpartitioned;
DROP TABLE metrics_history_points_unpartitioned;
ALTER SEQUENCE metrics_history_points_id_seq OWNED BY metrics_history_points.id;
CREATE UNIQUE INDEX idx_metrics_history_points_series ON metrics_history_points (metric, labels, bucket_at);
CREATE INDEX idx_metrics_history_points_bucket ON metrics_history_points (bucket_at);

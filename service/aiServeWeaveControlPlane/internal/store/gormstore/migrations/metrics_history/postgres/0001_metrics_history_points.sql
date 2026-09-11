CREATE TABLE metrics_history_points (
 id BIGSERIAL PRIMARY KEY,
 metric VARCHAR(128) NOT NULL,
 labels VARCHAR(512) NOT NULL,
 bucket_at TIMESTAMPTZ NOT NULL,
 value DOUBLE PRECISION NOT NULL
);
CREATE UNIQUE INDEX idx_metrics_history_points_series ON metrics_history_points (metric, labels, bucket_at);
CREATE INDEX idx_metrics_history_points_bucket ON metrics_history_points (bucket_at);

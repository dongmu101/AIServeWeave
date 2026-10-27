CREATE TABLE metrics_history_points (
 id BIGINT AUTO_INCREMENT PRIMARY KEY,
 metric VARCHAR(128) NOT NULL,
 labels VARCHAR(512) NOT NULL,
 bucket_at DATETIME(6) NOT NULL,
 value DOUBLE NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_metrics_history_points_series ON metrics_history_points (metric, labels, bucket_at);
CREATE INDEX idx_metrics_history_points_bucket ON metrics_history_points (bucket_at);

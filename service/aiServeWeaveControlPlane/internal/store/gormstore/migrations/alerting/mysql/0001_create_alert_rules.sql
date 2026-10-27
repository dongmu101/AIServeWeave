CREATE TABLE alert_rules (
 id VARCHAR(64) PRIMARY KEY,
 name VARCHAR(128) NOT NULL,
 metric VARCHAR(32) NOT NULL,
 operator VARCHAR(8) NOT NULL,
 threshold DOUBLE NOT NULL,
 consecutive_buckets INT NOT NULL,
 webhook_url VARCHAR(512) NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 INDEX idx_alert_rules_enabled (enabled)
) ENGINE=InnoDB;

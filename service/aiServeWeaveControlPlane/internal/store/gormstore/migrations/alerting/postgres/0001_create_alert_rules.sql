CREATE TABLE alert_rules (
 id VARCHAR(64) PRIMARY KEY,
 name VARCHAR(128) NOT NULL,
 metric VARCHAR(32) NOT NULL,
 operator VARCHAR(8) NOT NULL,
 threshold DOUBLE PRECISION NOT NULL,
 consecutive_buckets INTEGER NOT NULL,
 webhook_url VARCHAR(512) NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_alert_rules_enabled ON alert_rules (enabled);

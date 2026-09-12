CREATE TABLE alert_instances (
 id VARCHAR(64) PRIMARY KEY,
 rule_id VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL,
 value_at_fire DOUBLE PRECISION NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 last_evaluated_at TIMESTAMPTZ NOT NULL,
 resolved_at TIMESTAMPTZ,
 acknowledged_by VARCHAR(32) NOT NULL DEFAULT '',
 acknowledged_at TIMESTAMPTZ,
 notify_status VARCHAR(16) NOT NULL,
 notify_attempts INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_alert_instances_rule_status ON alert_instances (rule_id, status);
CREATE INDEX idx_alert_instances_created ON alert_instances (created_at, id);

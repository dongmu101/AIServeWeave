CREATE TABLE alert_instances (
 id VARCHAR(64) PRIMARY KEY,
 rule_id VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL,
 value_at_fire DOUBLE NOT NULL,
 created_at DATETIME(6) NOT NULL,
 last_evaluated_at DATETIME(6) NOT NULL,
 resolved_at DATETIME(6) NULL,
 acknowledged_by VARCHAR(32) NOT NULL DEFAULT '',
 acknowledged_at DATETIME(6) NULL,
 notify_status VARCHAR(16) NOT NULL,
 notify_attempts INT NOT NULL DEFAULT 0,
 INDEX idx_alert_instances_rule_status (rule_id, status),
 INDEX idx_alert_instances_created (created_at, id)
) ENGINE=InnoDB;

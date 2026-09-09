CREATE TABLE IF NOT EXISTS workflow_template_actives (
 template_id VARCHAR(128) PRIMARY KEY,
 revision BIGINT NOT NULL DEFAULT 0
) ENGINE=InnoDB

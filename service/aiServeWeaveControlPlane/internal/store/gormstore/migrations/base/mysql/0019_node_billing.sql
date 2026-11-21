CREATE TABLE IF NOT EXISTS node_billing (
    node_id VARCHAR(128) PRIMARY KEY,
    updated_by VARCHAR(40) NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;

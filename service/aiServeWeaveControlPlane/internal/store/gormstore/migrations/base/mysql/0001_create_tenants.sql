CREATE TABLE IF NOT EXISTS tenants (
    id VARCHAR(32) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL,
    created_at DATETIME(3),
    updated_at DATETIME(3)
) ENGINE=InnoDB;

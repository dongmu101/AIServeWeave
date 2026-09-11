CREATE TABLE IF NOT EXISTS tenants (
    id VARCHAR(32) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    status VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ
);

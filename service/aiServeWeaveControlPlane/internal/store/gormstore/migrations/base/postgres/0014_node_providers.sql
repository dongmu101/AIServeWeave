CREATE TABLE IF NOT EXISTS node_providers (
    node_id VARCHAR(128) NOT NULL,
    runtime_id VARCHAR(64) NOT NULL,
    scope VARCHAR(32) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    base_url VARCHAR(2048) NOT NULL,
    models_json TEXT NOT NULL,
    key_ciphertext TEXT NOT NULL,
    key_last4 VARCHAR(8) NOT NULL,
    updated_by VARCHAR(32) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (node_id, runtime_id)
);
CREATE TABLE IF NOT EXISTS node_provider_versions (
    node_id VARCHAR(128) PRIMARY KEY,
    version BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

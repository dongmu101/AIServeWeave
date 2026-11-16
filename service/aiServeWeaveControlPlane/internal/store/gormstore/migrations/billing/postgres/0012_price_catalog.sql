CREATE TABLE IF NOT EXISTS catalog_models (
 model VARCHAR(255) PRIMARY KEY,
 provider VARCHAR(64) NOT NULL,
 mode VARCHAR(32) NOT NULL,
 status VARCHAR(24) NOT NULL,
 prices_hash VARCHAR(64) NOT NULL,
 rejected_hash VARCHAR(64) NOT NULL DEFAULT '',
 raw_json TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog_prices (
 id BIGSERIAL PRIMARY KEY,
 model VARCHAR(255) NOT NULL,
 meter VARCHAR(64) NOT NULL,
 variant VARCHAR(32) NOT NULL DEFAULT '',
 above_input_tokens BIGINT NOT NULL DEFAULT 0,
 price_micro_per_m BIGINT NOT NULL,
 derived BOOLEAN NOT NULL DEFAULT FALSE,
 valid_from TIMESTAMPTZ NOT NULL,
 valid_to TIMESTAMPTZ NULL
);
CREATE INDEX idx_catalog_prices_model_from ON catalog_prices (model, valid_from);
CREATE INDEX idx_catalog_prices_valid_to ON catalog_prices (valid_to);
CREATE TABLE IF NOT EXISTS catalog_pending (
 model VARCHAR(255) PRIMARY KEY,
 kind VARCHAR(16) NOT NULL,
 proposed_json TEXT NOT NULL,
 prices_hash VARCHAR(64) NOT NULL,
 max_change_bp BIGINT NOT NULL,
 detected_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS catalog_sync_state (
 id BIGINT PRIMARY KEY,
 source VARCHAR(512) NOT NULL DEFAULT '',
 content_hash VARCHAR(64) NOT NULL DEFAULT '',
 embedded_hash VARCHAR(64) NOT NULL DEFAULT '',
 entry_count BIGINT NOT NULL DEFAULT 0,
 version BIGINT NOT NULL DEFAULT 0,
 synced_at TIMESTAMPTZ NULL,
 checked_at TIMESTAMPTZ NULL,
 last_error VARCHAR(512) NOT NULL DEFAULT '',
 last_error_at TIMESTAMPTZ NULL
);
INSERT INTO catalog_sync_state (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

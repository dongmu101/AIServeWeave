CREATE TABLE IF NOT EXISTS route_revisions (
 revision BIGINT PRIMARY KEY,
 digest VARCHAR(64) NOT NULL,
 routes_json TEXT NOT NULL,
 actor_id VARCHAR(32) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 rollback_of BIGINT NOT NULL DEFAULT 0
)

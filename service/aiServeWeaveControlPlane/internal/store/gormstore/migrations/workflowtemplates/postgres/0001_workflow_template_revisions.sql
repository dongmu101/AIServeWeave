CREATE TABLE IF NOT EXISTS workflow_template_revisions (
 template_id VARCHAR(128) NOT NULL,
 revision BIGINT NOT NULL,
 digest VARCHAR(64) NOT NULL,
 description TEXT NOT NULL,
 inputs_json TEXT NOT NULL,
 outputs_json TEXT NOT NULL,
 dependencies_json TEXT NOT NULL,
 visible_tenants_json TEXT NOT NULL,
 graph_json TEXT NOT NULL,
 actor_id VARCHAR(32) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 rollback_of BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY (template_id, revision)
)

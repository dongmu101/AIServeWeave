CREATE TABLE api_docs (
 slug VARCHAR(128) PRIMARY KEY,
 category VARCHAR(64) NOT NULL,
 category_order BIGINT NOT NULL DEFAULT 0,
 sort_order BIGINT NOT NULL DEFAULT 0,
 title VARCHAR(200) NOT NULL,
 method VARCHAR(16) NOT NULL,
 path VARCHAR(255) NOT NULL,
 summary VARCHAR(500) NOT NULL DEFAULT '',
 content_json MEDIUMTEXT NOT NULL,
 status VARCHAR(16) NOT NULL,
 source VARCHAR(16) NOT NULL,
 seed_revision BIGINT NOT NULL DEFAULT 0,
 version BIGINT NOT NULL DEFAULT 1,
 updated_by VARCHAR(64) NOT NULL DEFAULT '',
 updated_at DATETIME(6) NOT NULL,
 INDEX idx_api_docs_status_order (status, category_order, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

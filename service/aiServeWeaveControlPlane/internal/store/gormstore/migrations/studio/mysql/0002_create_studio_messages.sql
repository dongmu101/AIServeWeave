CREATE TABLE studio_messages (
 id VARCHAR(40) NOT NULL,
 conversation_id VARCHAR(40) NOT NULL,
 tenant_id VARCHAR(40) NOT NULL,
 user_id VARCHAR(40) NOT NULL,
 seq BIGINT NOT NULL,
 role VARCHAR(16) NOT NULL,
 content MEDIUMTEXT NOT NULL,
 reasoning MEDIUMTEXT NOT NULL,
 status VARCHAR(16) NOT NULL,
 model VARCHAR(128) NOT NULL,
 prompt_tokens BIGINT NOT NULL DEFAULT 0,
 completion_tokens BIGINT NOT NULL DEFAULT 0,
 error_code VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY (id),
 UNIQUE KEY uk_studio_messages_seq (conversation_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

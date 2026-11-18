CREATE TABLE studio_conversations (
 id VARCHAR(40) NOT NULL,
 tenant_id VARCHAR(40) NOT NULL,
 user_id VARCHAR(40) NOT NULL,
 title VARCHAR(200) NOT NULL,
 model VARCHAR(128) NOT NULL,
 system_prompt TEXT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY (id),
 INDEX idx_studio_conversations_owner (tenant_id, user_id, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

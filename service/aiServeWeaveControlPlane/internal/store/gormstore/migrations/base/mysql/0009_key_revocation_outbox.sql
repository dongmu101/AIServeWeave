CREATE TABLE IF NOT EXISTS key_revocation_outbox (
    id BIGINT PRIMARY KEY,
    generation BIGINT NOT NULL,
    delivered_generation BIGINT NOT NULL
) ENGINE=InnoDB;
INSERT IGNORE INTO key_revocation_outbox (id, generation, delivered_generation) VALUES (1, 0, 0);

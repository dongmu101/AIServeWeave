ALTER TABLE checkout_orders ADD COLUMN dodo_product_id VARCHAR(64) NOT NULL DEFAULT '';
CREATE TABLE refund_requests (
 id VARCHAR(32) PRIMARY KEY,
 tenant_id VARCHAR(32) NOT NULL,
 order_id VARCHAR(32) NOT NULL,
 source VARCHAR(16) NOT NULL,
 status VARCHAR(16) NOT NULL,
 amount_minor BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL,
 credits_micro BIGINT NOT NULL,
 reason VARCHAR(1024) NOT NULL,
 requested_by VARCHAR(32) NOT NULL,
 reviewed_by VARCHAR(32) NOT NULL,
 review_note VARCHAR(1024) NOT NULL,
 dodo_refund_id VARCHAR(64),
 failure VARCHAR(256) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE UNIQUE INDEX idx_refund_requests_refund ON refund_requests (dodo_refund_id);
CREATE INDEX idx_refund_requests_order ON refund_requests (order_id, status);
CREATE INDEX idx_refund_requests_tenant_created ON refund_requests (tenant_id, created_at, id);
CREATE INDEX idx_refund_requests_created ON refund_requests (created_at, id);

ALTER TABLE tenant_billing
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN mode VARCHAR(16) NOT NULL COMMENT '租户计费模式',
    MODIFY COLUMN balance_micro BIGINT NOT NULL COMMENT '当前余额，单位为微点',
    MODIFY COLUMN dodo_customer_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'Dodo 支付平台上的客户标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN usage_acc_micro BIGINT NOT NULL DEFAULT 0 COMMENT '待封账的用量扣点合计，单位为微点',
    MODIFY COLUMN usage_acc_count BIGINT NOT NULL DEFAULT 0 COMMENT '待封账的用量请求数',
    MODIFY COLUMN usage_acc_since DATETIME(6) NULL COMMENT '本轮待封账用量的开始时间',
    MODIFY COLUMN usage_acc_last DATETIME(6) NULL COMMENT '本轮待封账用量的最近时间',
    MODIFY COLUMN usage_acc_until DATETIME(6) NULL COMMENT '当前用量累计时段的封账截止时间',
    MODIFY COLUMN last_ledger_at DATETIME(6) NULL COMMENT '最近一笔已落账的点数流水时间',
    COMMENT = '租户计费模式、点数余额与待封账用量', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE credit_packages
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '充值方案标识',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '充值方案名称',
    MODIFY COLUMN kind VARCHAR(16) NOT NULL COMMENT '充值方案类型',
    MODIFY COLUMN dodo_product_id VARCHAR(64) NOT NULL COMMENT 'Dodo 支付平台上的商品标识',
    MODIFY COLUMN credits_micro BIGINT NOT NULL COMMENT '固定充值包发放的点数，单位为微点',
    MODIFY COLUMN price_minor BIGINT NOT NULL COMMENT '固定充值包的售价，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL COMMENT '币种代码',
    MODIFY COLUMN credits_per_minor_unit_micro BIGINT NOT NULL COMMENT '每个货币最小单位兑换的微点数',
    MODIFY COLUMN min_amount_minor BIGINT NOT NULL COMMENT '自定义充值金额下限，单位为货币最小单位',
    MODIFY COLUMN max_amount_minor BIGINT NOT NULL COMMENT '自定义充值金额上限，单位为货币最小单位',
    MODIFY COLUMN active BOOLEAN NOT NULL COMMENT '是否上架',
    MODIFY COLUMN sort_order BIGINT NOT NULL COMMENT '充值方案的展示排序值',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '可购买的固定点数包与自定义充值规则', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE model_prices
    MODIFY COLUMN model VARCHAR(128) NOT NULL COMMENT '模型别名',
    MODIFY COLUMN input_micro_per_m_tok BIGINT NOT NULL COMMENT '每百万输入 Token 的价格，单位为微点',
    MODIFY COLUMN output_micro_per_m_tok BIGINT NOT NULL COMMENT '每百万输出 Token 的价格，单位为微点',
    MODIFY COLUMN updated_by VARCHAR(40) NOT NULL COMMENT '最后更新人标识',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '旧版模型单价配置，用于新计价配置的迁移', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE checkout_orders
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '平台结账订单标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN package_id VARCHAR(40) NOT NULL COMMENT '所购充值方案标识',
    MODIFY COLUMN dodo_session_id VARCHAR(128) NOT NULL COMMENT 'Dodo 结账会话标识',
    MODIFY COLUMN dodo_payment_id VARCHAR(64) COMMENT 'Dodo 支付记录标识',
    MODIFY COLUMN amount_minor BIGINT NOT NULL COMMENT '订单应付金额，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL COMMENT '币种代码',
    MODIFY COLUMN credits_micro BIGINT NOT NULL COMMENT '订单对应的点数，单位为微点',
    MODIFY COLUMN refunded_minor BIGINT NOT NULL COMMENT '已退款金额，单位为货币最小单位',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '订单状态',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN paid_amount_minor BIGINT NOT NULL DEFAULT 0 COMMENT '渠道实际收取的金额，单位为货币最小单位',
    MODIFY COLUMN paid_currency VARCHAR(8) NOT NULL DEFAULT '' COMMENT '渠道实际收款的币种',
    MODIFY COLUMN dodo_product_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '下单时使用的 Dodo 商品标识',
    MODIFY COLUMN account_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '所购账号商品标识',
    MODIFY COLUMN account_expires_at DATETIME(6) NULL COMMENT '账号商品权益到期时间快照',
    MODIFY COLUMN presale_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '所购账号预售商品标识',
    MODIFY COLUMN alipay_amount BIGINT NOT NULL DEFAULT 0 COMMENT '下单时记录的支付宝兑换额度',
    MODIFY COLUMN provider VARCHAR(16) NOT NULL DEFAULT '' COMMENT '订单使用的支付渠道标识',
    MODIFY COLUMN pay_type VARCHAR(16) NOT NULL DEFAULT '' COMMENT '支付渠道内的支付方式',
    MODIFY COLUMN tenant_deleted_at DATETIME(6) NULL COMMENT '租户从订单列表隐藏该订单的时间',
    COMMENT = '租户充值与账号商品的结账订单', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE credit_ledger
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '点数流水标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN kind VARCHAR(24) NOT NULL COMMENT '流水类型，如充值、退款或用量扣费',
    MODIFY COLUMN amount_micro BIGINT NOT NULL COMMENT '本次余额变动额，单位为微点',
    MODIFY COLUMN balance_after_micro BIGINT NOT NULL COMMENT '变动后余额，单位为微点',
    MODIFY COLUMN ref VARCHAR(128) NOT NULL COMMENT '关联的业务单据或事件标识',
    MODIFY COLUMN idempotency_key VARCHAR(160) COMMENT '防止同一事件重复入账的幂等键',
    MODIFY COLUMN actor_id VARCHAR(40) NOT NULL COMMENT '发起余额变动的操作人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '流水入账时间',
    MODIFY COLUMN period_start DATETIME(6) NULL COMMENT '汇总用量账目的起始时间',
    MODIFY COLUMN period_end DATETIME(6) NULL COMMENT '汇总用量账目的截止时间',
    MODIFY COLUMN request_count BIGINT NOT NULL DEFAULT 0 COMMENT '该用量账目汇总的请求数',
    COMMENT = '租户点数余额的资金流水', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE usage_charges
    MODIFY COLUMN request_id VARCHAR(64) NOT NULL COMMENT '已处理的用量请求标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN model VARCHAR(128) NOT NULL COMMENT '计价使用的模型别名',
    MODIFY COLUMN cost_micro BIGINT NOT NULL COMMENT '该请求的费用，单位为微点',
    MODIFY COLUMN charged BOOLEAN NOT NULL COMMENT '是否实际扣点',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '旧版扣费记录的入账时间',
    COMMENT = '旧版请求级扣费防重记录与历史费用', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE billing_webhook_events
    MODIFY COLUMN webhook_id VARCHAR(128) NOT NULL COMMENT '支付渠道的 Webhook 事件标识',
    MODIFY COLUMN type VARCHAR(64) NOT NULL COMMENT 'Webhook 事件类型',
    MODIFY COLUMN received_at DATETIME(6) NOT NULL COMMENT 'Webhook 接收时间',
    COMMENT = '已接收的计费 Webhook 事件，用于防重', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE refund_requests
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '退款申请标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN order_id VARCHAR(40) NOT NULL COMMENT '关联的原结账订单标识',
    MODIFY COLUMN source VARCHAR(16) NOT NULL COMMENT '退款申请来源',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '退款处理状态',
    MODIFY COLUMN amount_minor BIGINT NOT NULL COMMENT '申请退款的金额，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL COMMENT '币种代码',
    MODIFY COLUMN credits_micro BIGINT NOT NULL COMMENT '退款对应的点数，单位为微点',
    MODIFY COLUMN reason VARCHAR(1024) NOT NULL COMMENT '申请退款的原因',
    MODIFY COLUMN requested_by VARCHAR(40) NOT NULL COMMENT '退款申请人标识',
    MODIFY COLUMN reviewed_by VARCHAR(40) NOT NULL COMMENT '退款审核人标识',
    MODIFY COLUMN review_note VARCHAR(1024) NOT NULL COMMENT '退款审核意见',
    MODIFY COLUMN dodo_refund_id VARCHAR(64) COMMENT 'Dodo 退款记录标识',
    MODIFY COLUMN failure VARCHAR(256) NOT NULL COMMENT '退款执行失败的摘要',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '订单退款申请、审核与渠道执行结果', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE account_sales_lock
    MODIFY COLUMN id BIGINT NOT NULL COMMENT '单行锁记录标识，固定为 1',
    COMMENT = '串行化账号商品库存变更的事务锁记录', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE account_sales
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '账号商品标识',
    MODIFY COLUMN node_id VARCHAR(128) NOT NULL COMMENT '节点标识',
    MODIFY COLUMN runtime_id VARCHAR(128) NOT NULL COMMENT '节点上的运行时标识',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '账号商品展示名称',
    MODIFY COLUMN active BOOLEAN NOT NULL COMMENT '是否上架',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '当前获配账号的租户标识，未分配时为空',
    MODIFY COLUMN expires_at DATETIME(6) NOT NULL COMMENT '当前账号权益到期时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN price_minor BIGINT NOT NULL DEFAULT 0 COMMENT '账号商品售价，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL DEFAULT '' COMMENT '币种代码',
    MODIFY COLUMN dodo_product_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'Dodo 支付平台上的商品标识',
    MODIFY COLUMN kind VARCHAR(16) NOT NULL DEFAULT '' COMMENT '账号商品类型',
    COMMENT = '可销售账号的库存、定价与当前分配', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE account_purchases
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '账号购买记录标识',
    MODIFY COLUMN account_id VARCHAR(40) NOT NULL COMMENT '所购账号商品标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN price_micro BIGINT NOT NULL COMMENT '早期点数购买价格，单位为微点',
    MODIFY COLUMN expires_at DATETIME(6) NOT NULL COMMENT '本次购买取得的账号权益到期时间',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    COMMENT = '已完成的账号购买与权益历史', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE account_presales
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '预售商品标识',
    MODIFY COLUMN kind VARCHAR(16) NOT NULL COMMENT '预售账号类型',
    MODIFY COLUMN name VARCHAR(128) NOT NULL COMMENT '预售商品展示名称',
    MODIFY COLUMN price_minor BIGINT NOT NULL COMMENT '预售商品售价，单位为货币最小单位',
    MODIFY COLUMN currency VARCHAR(8) NOT NULL COMMENT '币种代码',
    MODIFY COLUMN dodo_product_id VARCHAR(64) NOT NULL COMMENT 'Dodo 支付平台上的商品标识',
    MODIFY COLUMN capacity BIGINT NOT NULL COMMENT '预售名额上限',
    MODIFY COLUMN active BOOLEAN NOT NULL COMMENT '是否上架',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN alipay_amount BIGINT NOT NULL DEFAULT 0 COMMENT '兑换该预售商品所需的支付宝额度',
    COMMENT = '账号预售商品的定价、库存与上架状态', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE alipay_purchases
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '支付宝购买申请标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN action_id VARCHAR(64) NOT NULL COMMENT '调用方提交的操作标识，用于幂等',
    MODIFY COLUMN sku_id VARCHAR(128) NOT NULL COMMENT '支付宝商品规格标识',
    MODIFY COLUMN callback_url VARCHAR(512) NOT NULL COMMENT '额度购买流程传给支付宝的回调地址',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '购买链接申请状态',
    MODIFY COLUMN purchase_url VARCHAR(2048) NOT NULL COMMENT '支付宝返回的购买链接',
    MODIFY COLUMN expire_time VARCHAR(64) NOT NULL COMMENT '支付宝返回的链接失效时间原值',
    MODIFY COLUMN reason_code VARCHAR(64) NOT NULL COMMENT '支付宝返回的结果原因代码',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '支付宝额度购买链接申请记录', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE alipay_redemptions
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '支付宝额度兑换记录标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN action_id VARCHAR(64) NOT NULL COMMENT '调用方提交的操作标识，用于幂等',
    MODIFY COLUMN amount BIGINT NOT NULL COMMENT '提交兑换的支付宝额度数量',
    MODIFY COLUMN quota_unit VARCHAR(8) NOT NULL COMMENT '支付宝额度的计量单位',
    MODIFY COLUMN credits_micro BIGINT NOT NULL COMMENT '本次兑换对应的点数，单位为微点',
    MODIFY COLUMN reason VARCHAR(256) NOT NULL COMMENT '提交给支付宝的额度消耗原因',
    MODIFY COLUMN status VARCHAR(16) NOT NULL COMMENT '兑换处理状态',
    MODIFY COLUMN reason_code VARCHAR(64) NOT NULL COMMENT '支付宝返回的结果原因代码',
    MODIFY COLUMN consume_order_id VARCHAR(128) NOT NULL COMMENT '支付宝额度消耗订单标识',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    MODIFY COLUMN presale_order_id VARCHAR(40) NOT NULL DEFAULT '' COMMENT '关联的账号预售结账订单标识',
    COMMENT = '支付宝额度兑换点数或账号预售商品的处理记录', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE catalog_models
    MODIFY COLUMN model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '上游价目表中的模型标识',
    MODIFY COLUMN provider VARCHAR(64) NOT NULL COMMENT '模型提供方',
    MODIFY COLUMN mode VARCHAR(32) NOT NULL COMMENT '上游模型的能力模式',
    MODIFY COLUMN status VARCHAR(24) NOT NULL COMMENT '模型目录条目的状态',
    MODIFY COLUMN prices_hash VARCHAR(64) NOT NULL COMMENT '当前模型价格内容的摘要',
    MODIFY COLUMN rejected_hash VARCHAR(64) NOT NULL DEFAULT '' COMMENT '最近一次被拒绝的候选价格摘要',
    MODIFY COLUMN raw_json MEDIUMTEXT NOT NULL COMMENT '上游价目表的模型条目原始 JSON',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '同步到平台的上游模型目录与审核状态', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE catalog_prices
    MODIFY COLUMN id BIGINT AUTO_INCREMENT NOT NULL COMMENT '模型价格记录的自增标识',
    MODIFY COLUMN model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '上游价目表中的模型标识',
    MODIFY COLUMN meter VARCHAR(64) NOT NULL COMMENT '输入、输出或缓存等计量项代码',
    MODIFY COLUMN variant VARCHAR(32) NOT NULL DEFAULT '' COMMENT '计量项的价格变体标识',
    MODIFY COLUMN above_input_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '采用此价格所需超过的输入 Token 阈值',
    MODIFY COLUMN price_micro_per_m BIGINT NOT NULL COMMENT '每百万计量单位的价格，单位为微点',
    MODIFY COLUMN derived BOOLEAN NOT NULL DEFAULT FALSE COMMENT '是否由其他价格推导得到',
    MODIFY COLUMN valid_from DATETIME(6) NOT NULL COMMENT '本价格开始生效的时间',
    MODIFY COLUMN valid_to DATETIME(6) NULL COMMENT '本价格停止生效的时间，为空表示尚未结束',
    COMMENT = '上游模型各计量项的单价与生效区间', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE catalog_pending
    MODIFY COLUMN model VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL COMMENT '发生变更的上游模型标识',
    MODIFY COLUMN kind VARCHAR(16) NOT NULL COMMENT '待审核的变更类型',
    MODIFY COLUMN proposed_json MEDIUMTEXT NOT NULL COMMENT '候选价格内容 JSON',
    MODIFY COLUMN prices_hash VARCHAR(64) NOT NULL COMMENT '候选价格内容的摘要',
    MODIFY COLUMN max_change_bp BIGINT NOT NULL COMMENT '最大价格变动幅度，单位为基点',
    MODIFY COLUMN detected_at DATETIME(6) NOT NULL COMMENT '首次发现待审核变更的时间',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '待运维审核的上游价目表变更', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE catalog_sync_state
    MODIFY COLUMN id BIGINT NOT NULL COMMENT '单行记录标识，固定为 1',
    MODIFY COLUMN source VARCHAR(512) NOT NULL DEFAULT '' COMMENT '当前上游价目表来源',
    MODIFY COLUMN content_hash VARCHAR(64) NOT NULL DEFAULT '' COMMENT '上一次成功同步的价目表内容摘要',
    MODIFY COLUMN embedded_hash VARCHAR(64) NOT NULL DEFAULT '' COMMENT '最近一次导入的内置价目表快照摘要',
    MODIFY COLUMN entry_count BIGINT NOT NULL DEFAULT 0 COMMENT '最近一次成功同步的模型条目数',
    MODIFY COLUMN version BIGINT NOT NULL DEFAULT 0 COMMENT '当前价目表版本号',
    MODIFY COLUMN synced_at DATETIME(6) NULL COMMENT '最近一次成功同步时间',
    MODIFY COLUMN checked_at DATETIME(6) NULL COMMENT '最近一次检查上游价目表的时间',
    MODIFY COLUMN last_error VARCHAR(512) NOT NULL DEFAULT '' COMMENT '最近一次同步错误的摘要',
    MODIFY COLUMN last_error_at DATETIME(6) NULL COMMENT '最近一次同步错误的发生时间',
    COMMENT = '上游价目表最近同步、检查与错误状态', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE pricing_revisions
    MODIFY COLUMN id BIGINT AUTO_INCREMENT NOT NULL COMMENT '计价配置版本的自增标识',
    MODIFY COLUMN effective_at DATETIME(6) NOT NULL COMMENT '此版本开始生效的时间',
    MODIFY COLUMN document MEDIUMTEXT NOT NULL COMMENT '完整的计价配置与方案快照 JSON',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    MODIFY COLUMN summary VARCHAR(512) NOT NULL COMMENT '本次配置变更摘要',
    MODIFY COLUMN created_at DATETIME(6) NOT NULL COMMENT '创建时间',
    COMMENT = '模型定价配置与价格方案的历史版本', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE tenant_price_plans
    MODIFY COLUMN id VARCHAR(40) NOT NULL COMMENT '租户方案分配记录标识',
    MODIFY COLUMN tenant_id VARCHAR(40) NOT NULL COMMENT '所属租户标识',
    MODIFY COLUMN plan_id VARCHAR(40) NOT NULL COMMENT '所分配的价格方案标识',
    MODIFY COLUMN valid_from DATETIME(6) NOT NULL COMMENT '本次方案分配开始生效的时间',
    MODIFY COLUMN valid_to DATETIME(6) NULL COMMENT '本次方案分配停止生效的时间，为空表示尚未结束',
    MODIFY COLUMN created_by VARCHAR(40) NOT NULL COMMENT '创建人标识',
    COMMENT = '租户分配的价格方案及生效区间', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE pricing_config_state
    MODIFY COLUMN id INT NOT NULL COMMENT '单行记录标识，固定为 1',
    MODIFY COLUMN version BIGINT NOT NULL COMMENT '当前定价配置状态版本号',
    MODIFY COLUMN updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
    COMMENT = '定价配置变更的全局版本状态', ALGORITHM=INPLACE, LOCK=NONE;

ALTER TABLE schema_migrations_billing
    MODIFY COLUMN id VARCHAR(255) NOT NULL COMMENT '迁移 SQL 文件名',
    MODIFY COLUMN applied_at DATETIME(6) NOT NULL COMMENT '迁移完成时间，未完成时记录本次尝试的开始时间',
    MODIFY COLUMN checksum VARCHAR(64) NOT NULL DEFAULT '' COMMENT '迁移 SQL 的 SHA-256 校验和',
    MODIFY COLUMN dirty BOOLEAN NOT NULL DEFAULT FALSE COMMENT '迁移是否仍未完成',
    COMMENT = '计费迁移的版本账本', ALGORITHM=INPLACE, LOCK=NONE;

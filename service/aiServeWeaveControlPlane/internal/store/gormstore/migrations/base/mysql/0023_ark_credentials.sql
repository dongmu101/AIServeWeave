CREATE TABLE IF NOT EXISTS ark_credentials (
 scope VARCHAR(40) NOT NULL COMMENT '作用域：租户标识，或 platform 表示平台 Key',
 key_ciphertext TEXT NOT NULL COMMENT '主密钥加密的火山方舟 API Key',
 key_last4 VARCHAR(8) NOT NULL COMMENT 'API Key 末四位',
 base_url VARCHAR(2048) NOT NULL COMMENT 'API 根地址，只在平台行设置，空表示北京地址',
 models TEXT NOT NULL COMMENT '模型 id 的 JSON 数组：平台行为允许的模型，租户行为建议',
 version BIGINT NOT NULL COMMENT '每次保存加一的版本号',
 updated_by VARCHAR(40) NOT NULL COMMENT '最后保存的用户或运维标识',
 created_at DATETIME(6) NOT NULL COMMENT '创建时间',
 updated_at DATETIME(6) NOT NULL COMMENT '最后更新时间',
 PRIMARY KEY (scope)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='火山方舟 API Key（租户自有与平台）';

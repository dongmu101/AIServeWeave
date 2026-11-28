CREATE TABLE IF NOT EXISTS ark_credentials (
 scope VARCHAR(40) NOT NULL PRIMARY KEY,
 key_ciphertext TEXT NOT NULL,
 key_last4 VARCHAR(8) NOT NULL,
 base_url VARCHAR(2048) NOT NULL,
 models TEXT NOT NULL,
 version BIGINT NOT NULL,
 updated_by VARCHAR(40) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
COMMENT ON TABLE ark_credentials IS '火山方舟 API Key（租户自有与平台）';
COMMENT ON COLUMN ark_credentials.scope IS '作用域：租户标识，或 platform 表示平台 Key';
COMMENT ON COLUMN ark_credentials.key_ciphertext IS '主密钥加密的火山方舟 API Key';
COMMENT ON COLUMN ark_credentials.key_last4 IS 'API Key 末四位';
COMMENT ON COLUMN ark_credentials.base_url IS 'API 根地址，只在平台行设置，空表示北京地址';
COMMENT ON COLUMN ark_credentials.models IS '模型 id 的 JSON 数组：平台行为允许的模型，租户行为建议';
COMMENT ON COLUMN ark_credentials.version IS '每次保存加一的版本号';
COMMENT ON COLUMN ark_credentials.updated_by IS '最后保存的用户或运维标识';
COMMENT ON COLUMN ark_credentials.created_at IS '创建时间';
COMMENT ON COLUMN ark_credentials.updated_at IS '最后更新时间';

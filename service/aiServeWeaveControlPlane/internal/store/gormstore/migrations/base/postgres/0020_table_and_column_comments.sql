COMMENT ON TABLE tenants IS '商户租户与请求配额';
COMMENT ON COLUMN tenants.id IS '租户标识';
COMMENT ON COLUMN tenants.name IS '租户名称';
COMMENT ON COLUMN tenants.status IS '租户状态';
COMMENT ON COLUMN tenants.created_at IS '创建时间';
COMMENT ON COLUMN tenants.updated_at IS '最后更新时间';
COMMENT ON COLUMN tenants.requests_per_minute IS '每分钟请求数上限，0 表示不限';
COMMENT ON COLUMN tenants.tokens_per_minute IS '每分钟 Token 数上限，0 表示不限';
COMMENT ON COLUMN tenants.max_concurrent IS '并发请求数上限，0 表示不限';

COMMENT ON TABLE users IS '商户租户成员账户';
COMMENT ON COLUMN users.id IS '租户成员标识';
COMMENT ON COLUMN users.tenant_id IS '所属租户标识';
COMMENT ON COLUMN users.email IS '登录邮箱';
COMMENT ON COLUMN users.password_hash IS '登录密码摘要';
COMMENT ON COLUMN users.name IS '成员显示名称';
COMMENT ON COLUMN users.role IS '成员在租户内的角色';
COMMENT ON COLUMN users.status IS '成员账户状态';
COMMENT ON COLUMN users.created_at IS '创建时间';
COMMENT ON COLUMN users.updated_at IS '最后更新时间';
COMMENT ON COLUMN users.last_login_at IS '最近成功登录时间';

COMMENT ON TABLE platform_operators IS '平台运维账户';
COMMENT ON COLUMN platform_operators.id IS '运维账户标识';
COMMENT ON COLUMN platform_operators.email IS '登录邮箱';
COMMENT ON COLUMN platform_operators.password_hash IS '登录密码摘要';
COMMENT ON COLUMN platform_operators.name IS '运维人员显示名称';
COMMENT ON COLUMN platform_operators.status IS '运维账户状态';
COMMENT ON COLUMN platform_operators.created_at IS '创建时间';
COMMENT ON COLUMN platform_operators.updated_at IS '最后更新时间';
COMMENT ON COLUMN platform_operators.last_login_at IS '最近成功登录时间';

COMMENT ON TABLE api_keys IS '用于调用平台 API 的租户密钥';
COMMENT ON COLUMN api_keys.id IS 'API Key 标识';
COMMENT ON COLUMN api_keys.tenant_id IS '所属租户标识';
COMMENT ON COLUMN api_keys.created_by IS '创建人标识';
COMMENT ON COLUMN api_keys.name IS '密钥名称';
COMMENT ON COLUMN api_keys.hash IS '密钥的 SHA-256 摘要';
COMMENT ON COLUMN api_keys.display IS '用于展示的脱敏密钥';
COMMENT ON COLUMN api_keys.status IS '密钥状态';
COMMENT ON COLUMN api_keys.expires_at IS '到期时间';
COMMENT ON COLUMN api_keys.last_used_at IS '最近一次记录的密钥使用时间';
COMMENT ON COLUMN api_keys.revoked_at IS '密钥吊销时间';
COMMENT ON COLUMN api_keys.created_at IS '创建时间';
COMMENT ON COLUMN api_keys.updated_at IS '最后更新时间';

COMMENT ON TABLE audit_logs IS '租户和平台运维的管理操作审计日志';
COMMENT ON COLUMN audit_logs.id IS '审计记录标识';
COMMENT ON COLUMN audit_logs.tenant_id IS '审计事件所属租户，平台操作使用平台作用域';
COMMENT ON COLUMN audit_logs.actor_id IS '操作人标识';
COMMENT ON COLUMN audit_logs.action IS '审计动作代码';
COMMENT ON COLUMN audit_logs.target IS '操作对象标识';
COMMENT ON COLUMN audit_logs.detail IS '审计详情';
COMMENT ON COLUMN audit_logs.ip IS '操作来源 IP 地址';
COMMENT ON COLUMN audit_logs.created_at IS '审计事件发生时间';

COMMENT ON TABLE key_revocation_outbox IS 'API Key 吊销后的缓存失效通知进度';
COMMENT ON COLUMN key_revocation_outbox.id IS '单行记录标识，固定为 1';
COMMENT ON COLUMN key_revocation_outbox.generation IS '待同步的最新吊销代次';
COMMENT ON COLUMN key_revocation_outbox.delivered_generation IS '已同步到缓存的吊销代次';

COMMENT ON TABLE model_access IS '平台或租户的模型访问规则';
COMMENT ON COLUMN model_access.scope IS '规则作用域，平台或具体租户';
COMMENT ON COLUMN model_access.models_json IS '允许访问的模型别名列表 JSON';
COMMENT ON COLUMN model_access.updated_by IS '最后更新人标识';
COMMENT ON COLUMN model_access.updated_at IS '最后更新时间';

COMMENT ON TABLE node_access IS '平台节点的租户访问与共享规则';
COMMENT ON COLUMN node_access.node_id IS '节点标识';
COMMENT ON COLUMN node_access.tenants_json IS '获准使用节点的租户标识列表 JSON';
COMMENT ON COLUMN node_access.updated_by IS '最后更新人标识';
COMMENT ON COLUMN node_access.updated_at IS '最后更新时间';
COMMENT ON COLUMN node_access.mode IS '节点访问模式，如专享或共享';

COMMENT ON TABLE tenant_nodes IS '租户专属节点的归属、入驻和吊销信息';
COMMENT ON COLUMN tenant_nodes.node_id IS '节点标识';
COMMENT ON COLUMN tenant_nodes.tenant_id IS '所属租户标识';
COMMENT ON COLUMN tenant_nodes.created_by IS '创建人标识';
COMMENT ON COLUMN tenant_nodes.created_at IS '创建时间';
COMMENT ON COLUMN tenant_nodes.enroll_by IS '节点完成注册的截止时间';
COMMENT ON COLUMN tenant_nodes.revoked_at IS '节点吊销时间';
COMMENT ON COLUMN tenant_nodes.revoked_by IS '吊销节点的操作人标识';

COMMENT ON TABLE agent_install_codes IS 'Agent 安装与注册使用的一次性安装码';
COMMENT ON COLUMN agent_install_codes.code_hash IS '安装码的 SHA-256 摘要';
COMMENT ON COLUMN agent_install_codes.node_id IS '节点标识';
COMMENT ON COLUMN agent_install_codes.tenant_id IS '所属租户标识';
COMMENT ON COLUMN agent_install_codes.registry_addr IS 'Agent 注册时连接的 Registry 地址';
COMMENT ON COLUMN agent_install_codes.gateway_addr IS 'Agent 建立隧道时连接的 Gateway 地址';
COMMENT ON COLUMN agent_install_codes.created_by IS '创建人标识';
COMMENT ON COLUMN agent_install_codes.created_at IS '创建时间';
COMMENT ON COLUMN agent_install_codes.expires_at IS '到期时间';
COMMENT ON COLUMN agent_install_codes.redeemed_at IS '安装码兑换时间';

COMMENT ON TABLE node_providers IS '节点运行时使用的模型供应商连接配置';
COMMENT ON COLUMN node_providers.node_id IS '节点标识';
COMMENT ON COLUMN node_providers.runtime_id IS '节点上的运行时标识';
COMMENT ON COLUMN node_providers.scope IS '供应商配置的作用域';
COMMENT ON COLUMN node_providers.kind IS '供应商运行时类型';
COMMENT ON COLUMN node_providers.base_url IS '供应商接口基础地址';
COMMENT ON COLUMN node_providers.models_json IS '供应商可用模型列表 JSON';
COMMENT ON COLUMN node_providers.key_ciphertext IS '加密后的供应商 API Key';
COMMENT ON COLUMN node_providers.key_last4 IS '用于辨识的供应商 API Key 尾四位';
COMMENT ON COLUMN node_providers.updated_by IS '最后更新人标识';
COMMENT ON COLUMN node_providers.created_at IS '创建时间';
COMMENT ON COLUMN node_providers.updated_at IS '最后更新时间';

COMMENT ON TABLE node_provider_versions IS '节点供应商配置的变更版本';
COMMENT ON COLUMN node_provider_versions.node_id IS '节点标识';
COMMENT ON COLUMN node_provider_versions.version IS '供应商配置版本号';
COMMENT ON COLUMN node_provider_versions.updated_at IS '最后更新时间';

COMMENT ON TABLE node_billing IS '免扣点的平台节点名单';
COMMENT ON COLUMN node_billing.node_id IS '设为免扣点的平台节点标识';
COMMENT ON COLUMN node_billing.updated_by IS '最后更新人标识';
COMMENT ON COLUMN node_billing.updated_at IS '最后更新时间';

COMMENT ON TABLE schema_migrations_base IS '基础数据迁移的版本账本';
COMMENT ON COLUMN schema_migrations_base.id IS '迁移 SQL 文件名';
COMMENT ON COLUMN schema_migrations_base.applied_at IS '迁移完成时间，未完成时记录本次尝试的开始时间';
COMMENT ON COLUMN schema_migrations_base.checksum IS '迁移 SQL 的 SHA-256 校验和';
COMMENT ON COLUMN schema_migrations_base.dirty IS '迁移是否仍未完成';

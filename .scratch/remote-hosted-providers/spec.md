# Console 远程下发托管 API 接口与 Key

Status: ready-for-human

## 背景

商户和平台运维都要能把托管 API 接口（`kind: openai` / `deepseek` / `openai_compatible` / `anthropic` 等）连同 Key 配到 Agent 上：

- 商户：配到自己的商户自有节点（`tnode.<tenant>.<hex>`），只给自己用。
- 运维：配到平台节点，按共享或专属节点规则给商户用。

已交付（2026-11-02）：

- Agent 本地设置页可以直接填 Key，存为 `keys/<id>.key`（0600），YAML 只写 `api_key_file`。见 [Agent STATUS「托管模型供应商接入」](../../service/aiServeWeaveAgent/STATUS.md#托管模型供应商接入)。
- 商户自有节点上的模型不受模型授权列表限制。见 [Gateway STATUS「模型授权」](../../service/aiServeWeaveGateway/STATUS.md#模型授权)。

本规格补上最后一块：在 Console 里填接口和 Key，远程下发到 Agent，不需要登录节点机器。

## 与现有安全红线的冲突

AGENTS.md 有两条红线：「凭据不进入隧道」，以及「推理后端的 Authorization 由 Agent 本地配置提供」。[托管供应商说明](../../common/runtime/hosted/README.md) 还要求：Gateway 下发的配置不能新建托管 ID，也不能改端点和 Key。

本功能必须放宽这两条。放宽到什么程度：

- **Key 仍然不以明文进入隧道。** 控制面把「端点 + Key + 模型目录」整体用 HPKE 加密到目标节点证书的公钥。Gateway 和隧道只看到密文，拿不到 Key，也改不了端点：密文用 AEAD 绑定，改动即解密失败。
- **不设节点本地开关（2026-11-02 用户决定）。** 商户节点与平台节点的 Agent 都直接接受；能写哪台节点由控制面按 node_id 归属限制。
- **远程运行时与本地声明隔离。** 远程 ID 统一带 `remote-` 前缀，不能覆盖本地声明，也不能修改本地 Key 文件。要停用某个远程运行时，在 Console 删除它，或在本地声明同 ID 覆盖。

实施第一个任务时，同步修改 AGENTS.md 的这两条红线和托管供应商说明，写明上述例外。

## 设计

### 角色与范围

| 操作者 | 可以下发到 | 不可以 |
| --- | --- | --- |
| 商户 owner/admin | 本商户的 `tnode.` 节点 | 其他商户的节点、平台节点 |
| 平台运维 | 非 `tnode.` 的平台节点 | 商户自有节点（与「专属节点不能绑定 `tnode.`」相同） |

归属仍以 node_id 为准（`nodeaccess.OwnerOf`），不依赖任何同步数据。

### 数据流

1. Console 调用控制面接口：
   - 商户：`PUT /admin/v1/nodes/:node_id/providers/:id`
   - 运维：`PUT /operator/v1/nodes/:node_id/providers/:id`
   - 请求体：`{kind, base_url, api_key, models}`。`api_key` 只写不读，省略表示保持原值。
2. 控制面落库，表 `node_providers`：
   - Key 用控制面主密钥做 AES-256-GCM 静态加密。主密钥来自 `RemoteProviders.MasterKeyFile`；未配置 `RemoteProviders` 时功能不挂载，配置了但缺主密钥、token 或 Registry 时拒绝启动。
   - 列表接口只返回 Key 末 4 位和更新时间。
   - 每台节点维护一个单调递增的 `version`。
3. 下发：
   - Gateway 在每条 Control 流建立时从 mTLS 对端证书取得节点公钥（SPKI），经内部端点报告给控制面；控制面只采用指纹与 Registry `ListNodeStates.key_fingerprint` 一致的公钥（ECDSA P-256，经 `.ECDH()`）。
   - 把该节点的全部远程运行时整份打包，用 HPKE 封装，套件为 DHKEM(P-256, HKDF-SHA256) + HKDF-SHA256 + AES-256-GCM。
   - HPKE `info` = `"aisw-remote-providers/v1" || node_id || version`。
   - 经已有的「控制面 → Gateway 内部转发」路径发出。Gateway 侧新增 `GatewayControl.remote_providers`，承载 `{version, sealed}`。
4. Agent：
   - 用节点私钥解封，校验 `info` 里的 node_id 与自己的一致、`version` 大于已应用的版本。
   - 写入本地 `remote-providers.json`（0600），然后走现有的运行时热重载。
   - 经 `AgentControl.remote_providers` 上报 `applied_version`、`attempted_version`、封闭枚举 `outcome` 与逐项状态，不带原始错误文本。
5. 对账：控制面定期比对节点上报的版本，落后就重发。证书续期换了密钥时，旧密文解不开，Agent 上报 `open_failed`，控制面用新公钥重新封装。

### 为什么整份下发

每次都发整份列表，并带版本号：

- 删除和修改天然幂等。
- 重放旧消息会因为版本更低被拒。
- Agent 离线期间漏掉的变更，下次对账一次补齐。

### Agent 侧约束

- 只接受托管种类。`base_url` 必须是 `https`；`*_compatible` 需要明确的主机名，并拒绝回环、链路本地和私网地址（防止借 Agent 访问内网）。
- 每台节点最多 32 个远程运行时；整份明文上限 256 KiB。
- 远程运行时自动受 `allowed_runtimes` 约束：列表为空时放行；非空时只有 `remote-*` 的 ID 被列入才放行，沿用现有的空列表语义。
- 本地设置页只读展示远程运行时（ID、种类、端点、来源，不显示 Key），提供「删除并忽略此 ID」。

### 计费与授权

- 商户自有节点：沿用已有的免扣费和授权豁免。
- 平台节点：沿用模型授权、专属节点与单价表，新模型需要运维配置单价（否则按 `*` 默认价）。

### 审计

动作 `node.provider.update`、`node.provider.delete`。详情只记 `kind` 与 `base_url` 的主机名，不记 Key。

## 任务拆分

1. 修订红线文档；proto 增加 `GatewayControl.remote_providers` 与状态回报字段；`common/` 新增 HPKE 封装与打包契约包。
2. Agent：解封、本地存储、热重载、状态上报、设置页只读展示。
3. Registry：按 node_id 读取当前证书公钥的管理接口（如已有则复用）。
4. 控制面：`node_providers` 表与迁移（PostgreSQL/MySQL）、主密钥、商户与运维接口、下发与对账、审计。
5. Gateway：内部转发端点，把密文写入目标节点的 Control 流。
6. Console：商户「我的节点」和运维「节点管理」里的接口与 Key 编辑面板（Key 只写不读）。
7. 真实环境验收：真实 Key、证书续期后重新封装、离线节点上线补齐。

## 实施记录（2026-11-02）

任务 1–6 的代码已交付，默认测试通过；任务 7 未做。落地时的取舍：

- 待确认项：最初实现了 `remote_providers` 本地开关（商户节点默认开、平台节点默认关），随后按用户要求移除，Agent 一律接受；ID 统一 `remote-` 前缀；主密钥轮换本期不做。
- 任务 3 没有新增 Registry 接口：`ListNodeStates` 增加 `key_fingerprint`，节点公钥由 Gateway 从 mTLS 对端取得后报告，控制面对照指纹核验。
- 任务 2 未完成的部分：设置页只读展示远程运行时、本地「删除并忽略某个 ID」。
- 交付位置见根 [STATUS.md「托管供应商远程下发」](../../STATUS.md#托管供应商远程下发2026-11-02)。

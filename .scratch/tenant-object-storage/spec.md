# 租户自有对象存储（S3 / WebDAV）

状态：ready-for-human（2026-11-27 代码已交付、默认测试通过；已在 Cloudflare R2 上以进程内用例验证存储层、Gateway 与控制面保存；尚未在运行中的容器与 Console 浏览器环境验收）

## 目标

租户的 owner 或 admin 可以在 Console 为本租户配置自己的 S3 或 WebDAV 存储。配置之后，本租户的文件写入这套存储，不再写入部署的 `-artifact-storage`：

- 工作流 job 产物：由 Gateway 的 jobPersister 从节点复制过来的字节。
- Studio 写入的文件：编排运行上传的输入文件，以及推理生图的结果。

没有配置的租户保持现状，使用部署存储；部署也没有存储时，行为与以前相同。

## 已定的决定（2026-11-27 与用户确认）

1. 由租户的 owner 或 admin 在 Console 配置，整个租户一套，不支持按成员分别配置。
2. 覆盖范围：Studio 文件与工作流 job 产物。
3. 地址指向内网（回环、私网、链路本地、CGNAT、保留段，以及云元数据地址 169.254.169.254）时默认拒绝连接。运维在控制面配置中打开 `TenantStorage.AllowPrivateEndpoints` 后放行；同一个开关也允许使用 `http://`。
4. 保留期由租户自己设置，单位为天；为 0 表示 Gateway 从不删除这些文件。

## 设计

### 存放位置

- 租户存储中的 key 与部署存储相同：
  - job 产物为 `<租户>/<job>/<产物 id>`。
  - Studio 文件为 `studio/<租户>/<运行>/<id>`。
  - 两者都位于租户配置的 S3 prefix 或 WebDAV 目录之下。
  - 这两个前缀归平台所有：租户不要在其下放自己的文件，设了保留期时清理会删除它们。前缀之外的文件平台从不读写。
- 不为每个对象记录它存在哪套存储。读取时依次尝试：
  1. 租户当前的存储；
  2. 部署存储，覆盖租户配置自有存储之前写入的文件；
  3. job 产物照旧再从节点实时拉取。

  key 里含租户 id 与随机 id，因此这样依次尝试不会读到别人的对象。
- 写入只写租户当前的存储，从不回退到部署存储：
  - 租户的配置读不到时（控制面不可达），jobPersister 退避后重试，Studio 上传回 503 `storage_unavailable`。
  - 不能因为读不到配置，就把租户数据写进它明确不想用的存储。
- 更换或删除配置不迁移旧文件。旧文件留在原来的存储里，平台不再读取，对应下载回 404，job 产物会尝试从节点实时拉取。Console 在保存与删除前都会提示这一点。

### 生命周期

- **字节**：
  - 租户存储中的文件由 Gateway 的租户存储清理删除，规则是按对象修改时间，删除超过租户 `retention_days` 的文件。清理每小时一轮，只扫描 `<租户>/` 与 `studio/<租户>/` 两个前缀。
  - `retention_days = 0` 时不扫描。
  - 部署存储中的文件仍按 `-studio-artifact-retention` 与 `artifactcleanup` 的规则删除。
- **记录**：
  - job_artifacts 记录照旧按部署的 `artifactcleanup` 保留期删除。删除记录后，平台的下载接口不再提供该产物，但字节仍留在租户自己的存储里。
  - artifactcleanup 删除字节时只对部署存储调用 Delete。字节在租户存储中时，这次调用删除的是不存在的 key，什么也不发生。

### 凭据

- **加密存储**：控制面用 `TenantStorage.MasterKey` 或 `TenantStorage.MasterKeyFile`（32 字节，hex 或 base64）以 AES-256-GCM 加密密钥部分。
  - S3 加密 access key 与 secret；WebDAV 加密 password。
  - 附加数据为 `aisw-tenant-storage/v1\0<租户 id>`。
- **开关**：不配主密钥时功能关闭。Console 显示「部署未开启」，所有写接口回 404。
- **只写不读**：接口与界面从不返回密钥，S3 只显示 access key 的末四位，WebDAV 用户名明文显示。
- **何时必须重填密钥**：
  - S3：kind、endpoint、region、bucket 都不变时，access key 与 secret 可以各自省略，省略的那个保留已保存的值；只改 prefix 或 path_style 不算位置变化。
  - WebDAV：url 与用户名都不变时，可以省略密码以保留已保存的值；换了用户名必须重填密码。
  - 位置变了，或已保存的密钥因主密钥更换而无法解密时，必须重填，否则回 400 `credentials_required`。这条规则与节点供应商相同，保证已保存的密钥不会被送到另一个端点。
- **Gateway 取用**：经内部接口取得明文配置（InternalToken 鉴权，只在内网调用），按租户缓存 30 秒。
  - 控制面暂时不可达时，已读到的配置最多再沿用 10 分钟；超过后写入失败，读取仍会尝试部署存储。
  - 只有配置变化（后端字段或 allow_private_endpoints）时才重建后端；仅 version 或保留期变化时沿用原后端。
  - 已保存的密钥无法解密时，内部接口回 503，而不是「未配置」，Gateway 因此不会把该租户的文件写进部署存储。
- **并发保存**：同一租户的保存在数据库中按行串行，每次保存各得一个 version，最后一次保存生效。

### 保存时的连通性检查

- **检查方式**：控制面在保存前用同一套 `objectstore` 后端与内网限制，在 `<prefix>/.aisw-probe/<随机>` 写入 16 字节，读回比对后删除，整个检查限时 20 秒。检查失败时不保存，已有配置保持不变。任何一步失败都回 400 `storage_unreachable`，`reason` 取值如下（不回显上游原文）：
  - `network`：连不上。
  - `auth`：鉴权失败。
  - `not_found`：bucket 或目录不存在。
  - `permission`：没有读写权限。
  - `endpoint_not_allowed`：地址被内网限制拦下。
  - `other`：其他原因。
- **共用代码**：为此把 Gateway 的 `objectstore` 包移到 `common/objectstore`，控制面与 Gateway 共用。

### 内网限制

- **适用范围**：只用于租户存储。部署自己的 `-artifact-storage` 不受限制，那是运维自己的配置。
- **实现**：`objectstore.GuardedHTTPClient(allowPrivate)` 在拨号时检查解析后的 IP。同时覆盖 DNS 重绑定与跳转，并且不使用环境代理。
- **拦下的地址段**：
  - IPv4：0/8、10/8、100.64/10、127/8、169.254/16、172.16/12、192.0.0/24、192.168/16、198.18/15、224/4、240/4。
  - IPv6：未指定地址、回环、`fc00::/7`、`fe80::/10`、组播。
  - IPv4 映射的 IPv6 地址按 IPv4 判断。
- **保存时的预检**：主机名直接是 IP 字面量且落在上述地址段时，回 400 `endpoint_not_allowed`；不开放时 `http://` 也回这个码。

## 接口

### Console（租户会话，`/admin/v1/storage`）

只有 owner 与 admin 可用，其他角色回 403。响应头为 `Cache-Control: no-store`。

`GET /admin/v1/storage` → 200

```json
{
  "enabled": true,
  "allow_private_endpoints": false,
  "storage": null
}
```

`storage` 非空时：

```json
{
  "kind": "s3",
  "s3": {"endpoint": "https://s3.example.com", "region": "us-east-1", "bucket": "b", "prefix": "aisw", "path_style": true, "access_key_id_last4": "ABCD"},
  "webdav": null,
  "retention_days": 0,
  "updated_by": "usr_…",
  "updated_at": "2026-11-27T00:00:00Z"
}
```

WebDAV 时 `s3` 为 null，`webdav` 为 `{"url": "https://nas.example.com/dav", "dir": "aisw", "username": "u"}`。

`PUT /admin/v1/storage`：

```json
{
  "kind": "s3",
  "s3": {"endpoint": "", "region": "", "bucket": "b", "prefix": "", "path_style": false, "access_key_id": "…", "secret_access_key": "…"},
  "retention_days": 30
}
```

```json
{
  "kind": "webdav",
  "webdav": {"url": "https://nas.example.com/dav", "dir": "", "username": "u", "password": "…"},
  "retention_days": 0
}
```

- 密钥字段省略或为 null 表示保留已保存的值，见上文「何时必须重填密钥」。
- S3 的 `endpoint` 留空表示 AWS。
- 成功时 200，返回与 GET 中 `storage` 相同形状的对象。
- 失败时为 400 `{error, code, reason?}`，`code` 取值：
  - `invalid_storage`：字段不合法，例如缺 bucket、URL 不合法、超长，或 `retention_days` 不在 0–36500。
  - `credentials_required`
  - `endpoint_not_allowed`
  - `storage_unreachable`：附 `reason`。
- 未开启时 404。

`DELETE /admin/v1/storage` → 204；没有配置时 404。

审计动作：`tenant.storage.update`（detail 为 `kind=<s3|webdav> host=<主机名，AWS 时为 aws>`，S3 另加 ` bucket=<桶>`，最后是 ` retention_days=N`；从不含密钥）与 `tenant.storage.delete`。

### Gateway（内部，InternalToken）

`GET /internal/v1/tenants/:tenant_id/storage` → 200：

```json
{"configured": false, "allow_private_endpoints": false, "retention_days": 0}
```

```json
{
  "configured": true,
  "allow_private_endpoints": false,
  "version": 3,
  "kind": "s3",
  "s3": {"endpoint": "", "region": "", "bucket": "b", "prefix": "", "path_style": false, "access_key_id": "…", "secret_access_key": "…"},
  "webdav": null,
  "retention_days": 30
}
```

`GET /internal/v1/tenant-storage/retained?after=<tenant_id>&limit=100` → 200 `{"items": [{"tenant_id": "…", …同上…}], "next": "<tenant_id>|''"}`：只列 `retention_days > 0` 的租户，按 tenant_id 升序分页。

- `limit` 省略时取上限 500，超过上限按上限处理；不是正整数时回 400。
- 返回满一页时 `next` 为本页最后一个租户 id，否则为空。
- 密钥无法解密的租户被跳过并记日志。

未开启主密钥时，两个接口分别回 `{"configured": false, …}` 与空列表。第一个接口在密钥无法解密时回 503。

Gateway 把 404 当作「控制面版本过旧、尚无此功能」：读单个租户视为未配置，列表视为空的末页。Gateway 不认识返回的 kind 时，视为读不到配置。

## 数据

`tenant_storage` 表放在 base 命名空间，迁移为 0021：

| 列 | 类型 | 说明 |
| --- | --- | --- |
| tenant_id | VARCHAR(40) PK | 租户 |
| kind | VARCHAR(16) | `s3` 或 `webdav` |
| endpoint | VARCHAR(2048) | S3 endpoint 或 WebDAV URL |
| region | VARCHAR(64) | S3 region |
| bucket | VARCHAR(255) | S3 bucket |
| prefix | VARCHAR(512) | S3 prefix 或 WebDAV 目录 |
| path_style | BOOLEAN | S3 path-style |
| username | VARCHAR(256) | WebDAV 用户名 |
| secret_ciphertext | TEXT | 加密后的密钥 JSON |
| secret_hint | VARCHAR(8) | S3 access key 末四位 |
| retention_days | BIGINT | 0 表示不删除 |
| version | BIGINT | 每次保存加一 |
| updated_by | VARCHAR(40) | 最后保存的用户 |
| created_at / updated_at | 时间 | |

## 不在本期

- 已有文件在两套存储之间的迁移。
- 按成员分别配置。
- 对话附件：对话附件尚未交付，交付时沿用本设计。
- 在控制面记录 Gateway 侧的写入失败并展示在 Console。目前写入失败只见于 Gateway 日志，以及 Studio 的 503。

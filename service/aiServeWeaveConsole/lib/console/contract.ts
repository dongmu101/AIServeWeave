import { ApiError } from "./errors.ts";

/**
 * This module mirrors the control plane's Admin API wire format
 * (`service/aiServeWeaveControlPlane/internal/types/types.go`) and validates
 * every response against it before the UI sees a value.
 *
 * The validation is not defensive decoration: `expires_at` and `last_used_at`
 * carry `omitempty` on the Go side, list endpoints return bare arrays rather
 * than an envelope, and a revocation answers 204 with no body. Code that
 * assumes otherwise fails as an undefined property deep inside a component,
 * far from the response that caused it.
 *
 * 本模块镜像控制面 Admin API 的线上格式
 * （`service/aiServeWeaveControlPlane/internal/types/types.go`），并在界面看到任何值
 * 之前按它校验每一个响应。
 *
 * 这层校验不是防御性的装饰：Go 那边 `expires_at` 与 `last_used_at` 带 `omitempty`，
 * 列表端点返回裸数组而不是信封结构，吊销以 204 无响应体作答。假设并非如此的代码，
 * 会在离肇事响应很远的某个组件深处以「读取 undefined 的属性」的形式失败。
 */

/**
 * Role is the set of roles the control plane accepts (`model.Role*`).
 *
 * Role 是控制面接受的角色集合（`model.Role*`）。
 */
export type Role = "owner" | "admin" | "member";

/**
 * ROLES lists the roles in descending privilege, for selectors and labels.
 *
 * ROLES 按权限从高到低列出角色，供选择器与文案使用。
 */
export const ROLES: readonly Role[] = ["owner", "admin", "member"];

/**
 * ROLE_LABELS is the Chinese label for each role. Values sent to the API are
 * always the raw role, never the label.
 *
 * ROLE_LABELS 是每个角色的中文标签。发往 API 的一律是原始角色值，绝不是标签。
 */
export const ROLE_LABELS: Record<Role, string> = {
  owner: "所有者",
  admin: "管理员",
  member: "成员",
};

/**
 * Page is one slice of a list, mirroring the envelope the Admin API returns.
 *
 * `nextCursor` is null on the last page. There is no total: the control plane
 * does not send one, and a Console that invented one — by counting what it
 * holds — would be reporting the size of its own buffer as the size of the
 * tenant's data.
 *
 * Page 是一份列表中的一段，镜像 Admin API 返回的信封结构。
 *
 * 最后一页时 `nextCursor` 为 null。这里没有总数：控制面不发送它，而一个自行编出总数的
 * Console——比如统计自己手上有多少条——等于把自己缓冲区的大小当作该租户数据的规模来报告。
 */
export interface Page<T> {
  items: T[];
  nextCursor: string | null;
}

/**
 * TenantProfile mirrors types.TenantProfileResponse: the caller's own tenant
 * and the quota that applies to it.
 *
 * TenantProfile 镜像 types.TenantProfileResponse：调用方自己所属的租户，以及适用于它的
 * 配额。
 */
export interface TenantProfile {
  tenant: Tenant;
  limits: TenantLimits;
}

/**
 * Tenant mirrors types.Tenant.
 *
 * Tenant 镜像 types.Tenant。
 */
export interface Tenant {
  id: string;
  name: string;
  status: string;
  createdAt: string;
}

/**
 * User mirrors types.User. `lastLoginAt` is null when the field was omitted,
 * which means "never signed in" and must not be rendered as a date.
 *
 * User 镜像 types.User。字段被省略时 `lastLoginAt` 为 null，含义是「从未登录」，
 * 不能当作日期渲染。
 */
export interface User {
  id: string;
  tenantId: string;
  email: string;
  name: string;
  role: Role;
  status: string;
  lastLoginAt: string | null;
  createdAt: string;
}

/**
 * ApiKeySummary mirrors types.APIKey: the read form of a credential, carrying
 * the display form and never the plaintext or the hash.
 *
 * ApiKeySummary 镜像 types.APIKey：凭据的读取形式，携带 display 形式，绝不携带明文
 * 或哈希。
 */
export interface ApiKeySummary {
  id: string;
  tenantId: string;
  name: string;
  display: string;
  status: string;
  createdBy: string;
  expiresAt: string | null;
  lastUsedAt: string | null;
  revokedAt: string | null;
  createdAt: string;
}

/**
 * AuditEntry mirrors types.AuditEntry: one administrative action.
 *
 * AuditEntry 镜像 types.AuditEntry：一次管理操作。
 */
export interface AuditEntry {
  id: string;
  actorId: string;
  action: string;
  target: string;
  detail: string;
  ip: string;
  createdAt: string;
}

/**
 * LoginResult mirrors types.LoginResponse. It exists only on the Console
 * server: the token never reaches the browser, so no client module imports it.
 *
 * LoginResult 镜像 types.LoginResponse。它只存在于 Console 服务端：令牌从不到达
 * 浏览器，因此没有任何客户端模块引用它。
 */
export interface LoginResult {
  token: string;
  expiresAt: string;
  user: User;
}

/**
 * TenantLimits mirrors quota.Limits. Every field carries `omitempty`, so an
 * absent field is zero — and zero means unlimited, not unknown.
 *
 * TenantLimits 镜像 quota.Limits。每个字段都带 `omitempty`，因此字段缺席即为零
 * —— 而零表示不限制，不表示未知。
 */
export interface TenantLimits {
  requestsPerMinute: number;
  tokensPerMinute: number;
  maxConcurrent: number;
}

/** fail raises the contract error every parser in this module throws.
 *
 * fail 抛出本模块每个解析器都会抛出的契约错误。 */
function fail(): never {
  throw new ApiError("contract");
}

/** record narrows an unknown value to a plain object.
 *
 * record 把一个 unknown 值收窄为普通对象。 */
function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    fail();
  }
  return value as Record<string, unknown>;
}

/** text reads a required string field.
 *
 * text 读取一个必填的字符串字段。 */
function text(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  if (typeof value !== "string") {
    fail();
  }
  return value;
}

/**
 * timestamp reads a required RFC 3339 field and keeps it as a string. The
 * value stays textual until a component formats it, so a server timestamp is
 * never silently reinterpreted in the viewer's timezone by an intermediate
 * layer.
 *
 * timestamp 读取一个必填的 RFC 3339 字段并保持为字符串。该值在被组件格式化之前一直
 * 是文本，因此服务端时间戳不会被中间层悄悄按浏览者的时区重新解释。
 */
function timestamp(source: Record<string, unknown>, key: string): string {
  const value = text(source, key);
  if (Number.isNaN(Date.parse(value))) {
    fail();
  }
  return value;
}

/**
 * optionalTimestamp reads an `omitempty` time field. Absent, null and empty
 * all become null; a present but unparseable value is a contract violation.
 *
 * optionalTimestamp 读取一个 `omitempty` 的时间字段。缺席、null 与空串都变成 null；
 * 存在但无法解析的值属于契约违例。
 */
function optionalTimestamp(
  source: Record<string, unknown>,
  key: string
): string | null {
  const value = source[key];
  if (value === undefined || value === null || value === "") {
    return null;
  }
  if (typeof value !== "string" || Number.isNaN(Date.parse(value))) {
    fail();
  }
  return value;
}

/**
 * count reads an `omitempty` non-negative integer. Absent is zero, matching
 * Go's encoding, and zero is a configured value rather than a missing one.
 *
 * count 读取一个 `omitempty` 的非负整数。缺席即为零，与 Go 的编码一致，而零是一个
 * 已配置的值，不是缺失的值。
 */
function count(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  if (value === undefined || value === null) {
    return 0;
  }
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0) {
    fail();
  }
  return value;
}

/** role reads a role field, refusing a value outside the known set.
 *
 * role 读取一个角色字段，拒绝已知集合之外的值。 */
function role(source: Record<string, unknown>, key: string): Role {
  const value = text(source, key);
  if (!(ROLES as readonly string[]).includes(value)) {
    fail();
  }
  return value as Role;
}

/**
 * parsePage validates a list envelope.
 *
 * An absent `next_cursor` means this is the last page, and it is absent rather
 * than null because the Go field carries omitempty. Reading a missing field as
 * "there might be more" would leave a Console paging forever; reading a
 * present one as "done" would silently truncate the list. Both are one line
 * apart, which is why this lives in one function.
 *
 * parsePage 校验一个列表信封。
 *
 * `next_cursor` 缺席表示这是最后一页；它是缺席而不是 null，因为 Go 那边的字段带
 * omitempty。把字段缺失读成「可能还有」，会让 Console 永远翻下去；把它存在时读成
 * 「到头了」，则会悄悄截断列表。两者只差一行，这正是它写成一个函数的原因。
 */
function parsePage<T>(value: unknown, parse: (item: unknown) => T): Page<T> {
  const source = record(value);
  const items = source.items;
  if (!Array.isArray(items)) {
    fail();
  }
  const cursor = source.next_cursor;
  if (cursor !== undefined && cursor !== null && typeof cursor !== "string") {
    fail();
  }
  return {
    items: items.map(parse),
    nextCursor: typeof cursor === "string" && cursor !== "" ? cursor : null,
  };
}

/**
 * parseUser validates one types.User.
 *
 * parseUser 校验一个 types.User。
 */
export function parseUser(value: unknown): User {
  const source = record(value);
  return {
    id: text(source, "id"),
    tenantId: text(source, "tenant_id"),
    email: text(source, "email"),
    name: text(source, "name"),
    role: role(source, "role"),
    status: text(source, "status"),
    lastLoginAt: optionalTimestamp(source, "last_login_at"),
    createdAt: timestamp(source, "created_at"),
  };
}

/**
 * parseUsers validates one page of `GET /admin/v1/users`.
 *
 * parseUsers 校验 `GET /admin/v1/users` 的一页。
 */
export function parseUsers(value: unknown): Page<User> {
  return parsePage(value, parseUser);
}

/**
 * parseApiKey validates one types.APIKey.
 *
 * parseApiKey 校验一个 types.APIKey。
 */
export function parseApiKey(value: unknown): ApiKeySummary {
  const source = record(value);
  return {
    id: text(source, "id"),
    tenantId: text(source, "tenant_id"),
    name: text(source, "name"),
    display: text(source, "display"),
    status: text(source, "status"),
    createdBy: text(source, "created_by"),
    expiresAt: optionalTimestamp(source, "expires_at"),
    lastUsedAt: optionalTimestamp(source, "last_used_at"),
    revokedAt: optionalTimestamp(source, "revoked_at"),
    createdAt: timestamp(source, "created_at"),
  };
}

/**
 * parseApiKeys validates one page of `GET /admin/v1/apikeys`.
 *
 * parseApiKeys 校验 `GET /admin/v1/apikeys` 的一页。
 */
export function parseApiKeys(value: unknown): Page<ApiKeySummary> {
  return parsePage(value, parseApiKey);
}

/**
 * parseAuditEntries validates one page of `GET /admin/v1/audit`.
 *
 * parseAuditEntries 校验 `GET /admin/v1/audit` 的一页。
 */
export function parseAuditEntries(value: unknown): Page<AuditEntry> {
  return parsePage(value, (item) => {
    const source = record(item);
    return {
      id: text(source, "id"),
      actorId: text(source, "actor_id"),
      action: text(source, "action"),
      target: text(source, "target"),
      detail: text(source, "detail"),
      ip: text(source, "ip"),
      createdAt: timestamp(source, "created_at"),
    };
  });
}

/**
 * parseTenantLimits validates a quota.Limits document.
 *
 * parseTenantLimits 校验一个 quota.Limits 文档。
 */
export function parseTenantLimits(value: unknown): TenantLimits {
  const source = record(value);
  return {
    requestsPerMinute: count(source, "requests_per_minute"),
    tokensPerMinute: count(source, "tokens_per_minute"),
    maxConcurrent: count(source, "max_concurrent"),
  };
}

/**
 * CreatedApiKey mirrors types.CreateAPIKeyResponse: the one response in this
 * API that carries a plaintext credential.
 *
 * The plaintext is returned once, to the call that created it, and no read
 * path can produce it again. It must be shown to the person and then dropped:
 * it does not belong in a list, a toast, a cache, or any state that outlives
 * the dialog showing it.
 *
 * CreatedApiKey 镜像 types.CreateAPIKeyResponse：本 API 中唯一携带明文凭据的响应。
 *
 * 明文只向创建它的那次调用返回一次，之后没有任何读取路径能再产生它。它必须展示给
 * 当事人然后丢弃：它不属于列表、通知、缓存，或任何比展示它的对话框活得更久的状态。
 */
export interface CreatedApiKey {
  plaintext: string;
  key: ApiKeySummary;
}

/**
 * parseCreatedApiKey validates a key creation response.
 *
 * parseCreatedApiKey 校验一次创建 key 的响应。
 */
export function parseCreatedApiKey(value: unknown): CreatedApiKey {
  const source = record(value);
  const plaintext = text(source, "key");
  if (plaintext === "") {
    fail();
  }
  return { plaintext, key: parseApiKey(source.api_key) };
}

/**
 * parseTenantProfile validates a types.TenantProfileResponse.
 *
 * parseTenantProfile 校验一个 types.TenantProfileResponse。
 */
export function parseTenantProfile(value: unknown): TenantProfile {
  const source = record(value);
  const tenant = record(source.tenant);
  return {
    tenant: {
      id: text(tenant, "id"),
      name: text(tenant, "name"),
      status: text(tenant, "status"),
      createdAt: timestamp(tenant, "created_at"),
    },
    // An absent `limits` is not an unconfigured tenant — it is a response that
    // does not match the contract, and treating it as unlimited would tell
    // somebody their tenant has no quota when the truth is unknown.
    //
    // `limits` 缺席不等于「未配置配额的租户」——那是一个不符契约的响应，而把它当作
    // 不限制，等于在真相未知时告诉别人他们的租户没有配额。
    limits: parseTenantLimits(
      source.limits === undefined ? fail() : source.limits
    ),
  };
}

/**
 * parseLoginResult validates a types.LoginResponse.
 *
 * parseLoginResult 校验一个 types.LoginResponse。
 */
export function parseLoginResult(value: unknown): LoginResult {
  const source = record(value);
  const token = text(source, "token");
  if (token === "") {
    fail();
  }
  return {
    token,
    expiresAt: timestamp(source, "expires_at"),
    user: parseUser(source.user),
  };
}

/**
 * parseNoContent is the parser for endpoints that answer 204, such as key
 * revocation. It exists so every call site goes through the same shape.
 *
 * parseNoContent 是那些以 204 作答的端点（例如吊销 key）所用的解析器。它的存在是为了
 * 让每个调用点都走同一种形状。
 */
export function parseNoContent(): null {
  return null;
}

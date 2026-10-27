import { ApiError } from "./errors.ts";

/**
 * This module is the Console's side of the API reference contract
 * (ControlPlane internal/apidocs and logic/apidocs.go): parsing the tenant and
 * operator responses, grouping pages for navigation, substituting example
 * placeholders, and bounding the operator's write bodies.
 *
 * Page content keeps the wire's snake_case keys rather than being renamed:
 * operators edit it as JSON, and the shape they edit should be the shape the
 * seed files and the API use, byte for byte.
 *
 * 本模块是 Console 一侧的 API 参考文档契约（ControlPlane 的 internal/apidocs 与
 * logic/apidocs.go）：解析租户与运维响应、为导航给页面分组、替换示例占位符，以及
 * 限定运维写入请求体的大小。
 *
 * 页面正文保留线上的 snake_case 键名而不改名：运维以 JSON 形式编辑它，而他们编辑的
 * 形状应当与种子文件和 API 所用的形状逐字节一致。
 */

/** MAX_CONTENT_BYTES mirrors apidocs.MaxContentBytes. / MAX_CONTENT_BYTES 与 apidocs.MaxContentBytes 对齐。 */
export const MAX_CONTENT_BYTES = 256 * 1024;

/** MAX_REQUEST_BYTES mirrors apidocs.MaxRequestBytes. / MAX_REQUEST_BYTES 与 apidocs.MaxRequestBytes 对齐。 */
export const MAX_REQUEST_BYTES = 2 * MAX_CONTENT_BYTES;

const MAX_PARAM_DEPTH = 6;

/** ApiDocParam mirrors apidocs.Param. / ApiDocParam 与 apidocs.Param 对齐。 */
export interface ApiDocParam {
  name: string;
  type: string;
  required: boolean;
  default?: string;
  description: string;
  enum?: string[];
  children?: ApiDocParam[];
}

/** ApiDocCodeExample mirrors apidocs.CodeExample. / ApiDocCodeExample 与 apidocs.CodeExample 对齐。 */
export interface ApiDocCodeExample {
  lang: string;
  label: string;
  code: string;
}

/** ApiDocResponseExample mirrors apidocs.ResponseExample. / ApiDocResponseExample 与 apidocs.ResponseExample 对齐。 */
export interface ApiDocResponseExample {
  status: number;
  label: string;
  content_type?: string;
  body: string;
}

/** ApiDocSection mirrors apidocs.Section. / ApiDocSection 与 apidocs.Section 对齐。 */
export interface ApiDocSection {
  title: string;
  body?: string;
  code?: string;
  code_lang?: string;
}

/** ApiDocContent mirrors apidocs.Content; absent lists parse as empty.
 * ApiDocContent 与 apidocs.Content 对齐；缺失的列表解析为空。 */
export interface ApiDocContent {
  highlights: string[];
  notice?: string;
  auth: "bearer" | "none";
  headers: ApiDocParam[];
  path_params: ApiDocParam[];
  query_params: ApiDocParam[];
  body_type: "json" | "multipart" | "none";
  body_params: ApiDocParam[];
  response_fields: ApiDocParam[];
  request_examples: ApiDocCodeExample[];
  response_examples: ApiDocResponseExample[];
  sections: ApiDocSection[];
}

/** ApiDocSummary mirrors logic.APIDocSummary. / ApiDocSummary 与 logic.APIDocSummary 对齐。 */
export interface ApiDocSummary {
  slug: string;
  category: string;
  categoryOrder: number;
  sortOrder: number;
  title: string;
  method: string;
  path: string;
  summary: string;
  updatedAt: string;
}

/** ApiDocDetail mirrors logic.APIDocDetail. / ApiDocDetail 与 logic.APIDocDetail 对齐。 */
export interface ApiDocDetail extends ApiDocSummary {
  content: ApiDocContent;
}

/** OperatorApiDocSummary mirrors logic.OperatorAPIDocSummary.
 * OperatorApiDocSummary 与 logic.OperatorAPIDocSummary 对齐。 */
export interface OperatorApiDocSummary extends ApiDocSummary {
  status: "published" | "draft";
  source: "seed" | "operator";
  seedRevision: number;
  version: number;
  updatedBy: string;
  seeded: boolean;
}

/** OperatorApiDocDetail mirrors logic.OperatorAPIDocDetail.
 * OperatorApiDocDetail 与 logic.OperatorAPIDocDetail 对齐。 */
export interface OperatorApiDocDetail extends OperatorApiDocSummary {
  content: ApiDocContent;
}

/** ApiDocGroup is one navigation category. / ApiDocGroup 是导航中的一个分类。 */
export interface ApiDocGroup<T extends ApiDocSummary = ApiDocSummary> {
  category: string;
  items: T[];
}

function fail(): never {
  throw new ApiError("invalid");
}

function record(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : fail();
}

function str(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  return typeof value === "string" ? value : fail();
}

function optStr(source: Record<string, unknown>, key: string): string | undefined {
  const value = source[key];
  if (value === undefined || value === null) return undefined;
  return typeof value === "string" ? value : fail();
}

function int(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  return typeof value === "number" && Number.isSafeInteger(value) ? value : fail();
}

function list<T>(value: unknown, parse: (item: unknown) => T): T[] {
  if (value === undefined || value === null) return [];
  return Array.isArray(value) ? value.map(parse) : fail();
}

function oneOf<T extends string>(value: string, allowed: readonly T[]): T {
  return (allowed as readonly string[]).includes(value) ? (value as T) : fail();
}

function parseParam(value: unknown, depth = 1): ApiDocParam {
  if (depth > MAX_PARAM_DEPTH) fail();
  const source = record(value);
  const param: ApiDocParam = {
    name: str(source, "name"),
    type: str(source, "type"),
    required: source.required === undefined ? false : typeof source.required === "boolean" ? source.required : fail(),
    description: optStr(source, "description") ?? "",
  };
  const def = optStr(source, "default");
  if (def !== undefined && def !== "") param.default = def;
  const values = list(source.enum, (item) => (typeof item === "string" ? item : fail()));
  if (values.length > 0) param.enum = values;
  const children = list(source.children, (item) => parseParam(item, depth + 1));
  if (children.length > 0) param.children = children;
  return param;
}

/** parseApiDocContent validates one page body. / parseApiDocContent 校验一个页面正文。 */
export function parseApiDocContent(value: unknown): ApiDocContent {
  const source = record(value);
  const params = (key: string) => list(source[key], (item) => parseParam(item));
  return {
    highlights: list(source.highlights, (item) => (typeof item === "string" ? item : fail())),
    notice: optStr(source, "notice") || undefined,
    auth: oneOf(str(source, "auth"), ["bearer", "none"] as const),
    headers: params("headers"),
    path_params: params("path_params"),
    query_params: params("query_params"),
    body_type: oneOf(str(source, "body_type"), ["json", "multipart", "none"] as const),
    body_params: params("body_params"),
    response_fields: params("response_fields"),
    request_examples: list(source.request_examples, (item) => {
      const ex = record(item);
      return { lang: str(ex, "lang"), label: str(ex, "label"), code: str(ex, "code") };
    }),
    response_examples: list(source.response_examples, (item) => {
      const ex = record(item);
      const status = int(ex, "status");
      if (status < 100 || status > 599) fail();
      return { status, label: optStr(ex, "label") ?? "", content_type: optStr(ex, "content_type") || undefined, body: optStr(ex, "body") ?? "" };
    }),
    sections: list(source.sections, (item) => {
      const sec = record(item);
      return { title: str(sec, "title"), body: optStr(sec, "body") || undefined, code: optStr(sec, "code") || undefined, code_lang: optStr(sec, "code_lang") || undefined };
    }),
  };
}

function parseSummary(value: unknown): ApiDocSummary {
  const source = record(value);
  const updatedAt = str(source, "updated_at");
  if (Number.isNaN(Date.parse(updatedAt))) fail();
  return {
    slug: str(source, "slug"),
    category: str(source, "category"),
    categoryOrder: int(source, "category_order"),
    sortOrder: int(source, "sort_order"),
    title: str(source, "title"),
    method: str(source, "method"),
    path: str(source, "path"),
    summary: optStr(source, "summary") ?? "",
    updatedAt,
  };
}

function parseOperatorSummary(value: unknown): OperatorApiDocSummary {
  const source = record(value);
  return {
    ...parseSummary(source),
    status: oneOf(str(source, "status"), ["published", "draft"] as const),
    source: oneOf(str(source, "source"), ["seed", "operator"] as const),
    seedRevision: int(source, "seed_revision"),
    version: int(source, "version"),
    updatedBy: optStr(source, "updated_by") ?? "",
    seeded: typeof source.seeded === "boolean" ? source.seeded : fail(),
  };
}

/** parseApiDocList validates GET /admin/v1/api-docs. / parseApiDocList 校验 GET /admin/v1/api-docs。 */
export function parseApiDocList(value: unknown): ApiDocSummary[] {
  return list(record(value).items, parseSummary);
}

/** parseApiDocDetail validates GET /admin/v1/api-docs/{slug}. / parseApiDocDetail 校验 GET /admin/v1/api-docs/{slug}。 */
export function parseApiDocDetail(value: unknown): ApiDocDetail {
  const source = record(value);
  return { ...parseSummary(source), content: parseApiDocContent(source.content) };
}

/** parseOperatorApiDocList validates GET /operator/v1/api-docs. / parseOperatorApiDocList 校验 GET /operator/v1/api-docs。 */
export function parseOperatorApiDocList(value: unknown): OperatorApiDocSummary[] {
  return list(record(value).items, parseOperatorSummary);
}

/** parseOperatorApiDocDetail validates one operator page response.
 * parseOperatorApiDocDetail 校验运维侧单个页面的响应。 */
export function parseOperatorApiDocDetail(value: unknown): OperatorApiDocDetail {
  const source = record(value);
  return { ...parseOperatorSummary(source), content: parseApiDocContent(source.content) };
}

/** groupApiDocs groups pages by category, ordering groups by their smallest
 * category order and pages by sort order, then slug, the same order the
 * control plane lists them in.
 *
 * groupApiDocs 按分类给页面分组：分组按其中最小的分类序号排序，组内按排序号、再按
 * slug 排序，与控制面列出它们的顺序相同。 */
export function groupApiDocs<T extends ApiDocSummary>(items: readonly T[]): ApiDocGroup<T>[] {
  const groups = new Map<string, { order: number; items: T[] }>();
  for (const item of items) {
    const group = groups.get(item.category);
    if (group) {
      group.items.push(item);
      group.order = Math.min(group.order, item.categoryOrder);
    } else {
      groups.set(item.category, { order: item.categoryOrder, items: [item] });
    }
  }
  return [...groups.entries()]
    .sort((a, b) => a[1].order - b[1].order || a[0].localeCompare(b[0]))
    .map(([category, group]) => ({
      category,
      items: group.items.sort((a, b) => a.sortOrder - b.sortOrder || a.slug.localeCompare(b.slug)),
    }));
}

/** ExampleValues fills the placeholders seeded examples carry.
 * ExampleValues 用来填充种子示例中携带的占位符。 */
export interface ExampleValues {
  baseUrl: string;
  apiKey: string;
  model: string;
}

/** fillPlaceholders substitutes {{BASE_URL}}, {{API_KEY}} and {{MODEL}}. It
 * never sees a real key: the page passes a fixed stand-in, because a
 * reference page is exactly the kind of screen that ends up in a screenshot.
 *
 * fillPlaceholders 替换 {{BASE_URL}}、{{API_KEY}} 与 {{MODEL}}。它从不接触真实的
 * Key：页面传入的是固定的替身，因为参考文档页面恰恰是最容易被截图外传的那种界面。 */
export function fillPlaceholders(text: string, values: ExampleValues): string {
  return text
    .replaceAll("{{BASE_URL}}", values.baseUrl)
    .replaceAll("{{API_KEY}}", values.apiKey)
    .replaceAll("{{MODEL}}", values.model);
}

/** apiDocBodyLimit returns the larger body bound for the operator's page
 * writes, or undefined for any other path so the default stays in force.
 *
 * apiDocBodyLimit 为运维的页面写入返回更大的请求体上限；其他路径返回 undefined，
 * 使默认上限继续生效。 */
export function apiDocBodyLimit(path: string): number | undefined {
  return /^\/operator\/v1\/api-docs(\/[^/]+)?$/.test(path) ? MAX_REQUEST_BYTES : undefined;
}

/** ApiDocInput mirrors logic.APIDocInput's wire form — what the operator
 * editor holds as JSON. / ApiDocInput 与 logic.APIDocInput 的线上形式对齐——即运维
 * 编辑器里以 JSON 形式持有的内容。 */
export interface ApiDocInput {
  category: string;
  category_order: number;
  sort_order: number;
  title: string;
  method: string;
  path: string;
  summary: string;
  status: "published" | "draft";
  content: ApiDocContent;
}

/** toApiDocInput converts a loaded page into the editor's JSON form.
 * toApiDocInput 把已加载的页面转换为编辑器的 JSON 形式。 */
export function toApiDocInput(doc: OperatorApiDocDetail): ApiDocInput {
  return {
    category: doc.category,
    category_order: doc.categoryOrder,
    sort_order: doc.sortOrder,
    title: doc.title,
    method: doc.method,
    path: doc.path,
    summary: doc.summary,
    status: doc.status,
    content: doc.content,
  };
}

/** blankApiDocInput is the starting point for a new page.
 * blankApiDocInput 是新建页面的起点。 */
export function blankApiDocInput(): ApiDocInput {
  return {
    category: "文本对话",
    category_order: 10,
    sort_order: 100,
    title: "",
    method: "POST",
    path: "/v1/",
    summary: "",
    status: "draft",
    content: {
      highlights: [],
      auth: "bearer",
      headers: [],
      path_params: [],
      query_params: [],
      body_type: "json",
      body_params: [],
      response_fields: [],
      request_examples: [{ lang: "curl", label: "cURL", code: "curl {{BASE_URL}}/v1/... \\\n  -H 'Authorization: Bearer {{API_KEY}}'" }],
      response_examples: [{ status: 200, label: "成功", content_type: "application/json", body: "{}" }],
      sections: [],
    },
  };
}

/** parseApiDocInput reads the editor's JSON text, checks the shape the
 * control plane will accept, and returns it; the control plane still
 * validates every rule. A malformed text throws ApiError("invalid").
 *
 * parseApiDocInput 读取编辑器中的 JSON 文本，检查控制面会接受的形状并返回；控制面
 * 仍会校验每一条规则。格式有误的文本抛出 ApiError("invalid")。 */
export function parseApiDocInput(text: string): ApiDocInput {
  if (new TextEncoder().encode(text).byteLength > MAX_REQUEST_BYTES) fail();
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    fail();
  }
  const source = record(value);
  const allowed = new Set(["category", "category_order", "sort_order", "title", "method", "path", "summary", "status", "content"]);
  for (const key of Object.keys(source)) if (!allowed.has(key)) fail();
  return {
    category: str(source, "category"),
    category_order: int(source, "category_order"),
    sort_order: int(source, "sort_order"),
    title: str(source, "title"),
    method: oneOf(str(source, "method"), ["GET", "POST", "PUT", "PATCH", "DELETE"] as const),
    path: str(source, "path"),
    summary: optStr(source, "summary") ?? "",
    status: oneOf(str(source, "status"), ["published", "draft"] as const),
    content: parseApiDocContent(source.content),
  };
}

/** isValidApiDocSlug mirrors apidocs.ValidSlug. / isValidApiDocSlug 与 apidocs.ValidSlug 对齐。 */
export function isValidApiDocSlug(slug: string): boolean {
  return /^[a-z0-9][a-z0-9-]{0,127}$/.test(slug);
}

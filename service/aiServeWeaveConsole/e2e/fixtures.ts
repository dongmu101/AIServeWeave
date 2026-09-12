import { test as base, expect, type Page } from "@playwright/test";
import { deriveSessionKey, seal, SESSION_COOKIE } from "../lib/console/session-payload.ts";
import { sealOperator, OPERATOR_COOKIE } from "../lib/console/operator-session.ts";

/** LONG_TEXT exercises unbroken identifiers and names within a bounded fixture.
 * LONG_TEXT 用有上限的样本覆盖不含空格的长标识符与名称。 */
export const LONG_TEXT = "P10_" + "LongIdentifier".repeat(12);
const at = "2026-09-12T00:00:00Z";
const user = { id: "usr_p10", tenant_id: "tnt_p10", name: LONG_TEXT, email: "p10@example.invalid", role: "owner", status: "active", created_at: at };
const key = { id: "key_p10", tenant_id: user.tenant_id, name: LONG_TEXT, display: "aisw_test12345", status: "active", created_by: user.id, created_at: at };
const observed = { collected_at: at, replicas: [], partial: false };
const job = { id: "job_p10", workflow_id: LONG_TEXT, state: "running", created_at: at, updated_at: at, artifact_ids: [] };
const history = { job_id: job.id, workflow_id: job.workflow_id, workflow_version: "1", state: "succeeded", created_at: at, updated_at: at, artifacts: [] };
const routeSnapshot = { revision: 1, digest: "d".repeat(64), created_at: at, actor_id: "op_p10", routes: [{ model: LONG_TEXT, targets: [{ runtime_model: LONG_TEXT }] }] };
const template = { template_id: LONG_TEXT, revision: 1, digest: "d".repeat(64), created_at: at, actor_id: "op_p10", description: LONG_TEXT, visible_tenant_ids: ["tnt_p10"], inputs: [{ name: "text", node: "1", field: "text", type: "string", required: true }] };

/** auditPage serves at most 200 rows from a synthetic 10,000-entry data set.
 * auditPage 从一万条合成记录中每次最多提供 200 行。 */
export function auditPage(url: URL) {
  const size = Math.min(200, Math.max(1, Number(url.searchParams.get("limit")) || 100));
  const start = Math.max(0, Number(url.searchParams.get("cursor")) || 0);
  const filtered = !!url.searchParams.get("actor_id");
  const end = Math.min(filtered ? 1 : 10_000, start + size);
  return {
    items: Array.from({ length: Math.max(0, end - start) }, (_, i) => ({
      id: `audit_${start + i}`, actor_id: filtered ? url.searchParams.get("actor_id") : `actor_${start + i}`,
      action: "apikey.create", target: `target_${start + i}`, detail: `${LONG_TEXT}_${start + i}`, ip: "127.0.0.1", created_at: at,
    })),
    next_cursor: end < (filtered ? 1 : 10_000) ? String(end) : "",
  };
}

/** installFixtures seals test-only identities and intercepts every browser API call.
 * installFixtures 密封仅用于测试的身份，并拦截浏览器发出的全部 API 调用。 */
async function installFixtures(page: Page, unexpected: string[]) {
  const sessionKey = deriveSessionKey("p10-browser-acceptance-only-not-a-deployment-secret");
  const expiresAt = new Date(Date.now() + 3_600_000).toISOString();
  const cookies = [
    { name: SESSION_COOKIE, value: seal({ token: "synthetic-tenant-token", expiresAt, user: { id: user.id, tenantId: user.tenant_id, email: user.email, name: user.name, role: user.role } }, sessionKey) },
    { name: OPERATOR_COOKIE, value: sealOperator({ token: "synthetic-operator-token", expiresAt, operator: { id: "op_p10", email: "operator@example.invalid", name: LONG_TEXT } }, sessionKey) },
  ];
  await page.context().addCookies(cookies.map(cookie => ({ ...cookie, url: "http://127.0.0.1:3100", httpOnly: true, sameSite: "Lax" as const })));
  await page.route("**/api/**", async route => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace(/^\/api\/(admin|operator)/, "");
    const method = route.request().method();
    let json: unknown;
    if (method === "GET") {
      if (path.endsWith("/audit")) json = auditPage(url);
      else if (path === "/admin/v1/users") json = { items: [user, { ...user, id: "usr_peer", email: "peer@example.invalid", role: "member" }], next_cursor: "" };
      else if (path === "/admin/v1/apikeys") json = { items: [key], next_cursor: "" };
      else if (path.endsWith("/requests")) {
        const data = auditPage(url);
        json = { ...data, items: data.items.map(row => ({ request_id: row.id.replace("audit_", "req_"), key_display: key.display, endpoint: "chat", outcome: "ok", status_code: 200, duration_ms: 42, created_at: at })) };
      }
      else if (path === "/admin/v1/tenants/current") json = { tenant: { id: user.tenant_id, name: LONG_TEXT, status: "active", created_at: at }, limits: {} };
      else if (path.endsWith("/workflows")) json = { ...observed, templates: [{ id: LONG_TEXT, description: LONG_TEXT, valid: true, inputs: [] }] };
      else if (path === "/admin/v1/jobs") json = { ...observed, truncated: false, jobs: [job] };
      else if (path === "/admin/v1/jobs/history") json = { items: [history], next_cursor: "" };
      else if (path === "/admin/v1/jobs/history/job_p10") json = history;
      else if (path === "/operator/v1/operators") json = { items: ["op_p10", "op_peer"].map(id => ({ id, name: LONG_TEXT, email: `${id}@example.invalid`, status: "active", created_at: at })), next_cursor: "" };
      else if (path === "/operator/v1/nodes/states") json = { items: [{ node_id: LONG_TEXT, pending_approval: true, disabled: false, maintenance: false, first_seen_at: at, last_seen_at: at }] };
      else if (path === "/operator/v1/nodes") json = { ...observed, nodes: [{ node_id: LONG_TEXT, live: true, maintenance: false, observed_at: at, labels: { region: LONG_TEXT }, runtimes: [] }] };
      else if (path === "/operator/v1/models") json = { ...observed, models: [{ id: LONG_TEXT, available_deployments: 0, deployments: [] }] };
      else if (path === "/operator/v1/routes") json = routeSnapshot;
      else if (path === "/operator/v1/routes/history") json = { items: [] };
      else if (path === "/operator/v1/routes/status") json = { desired_revision: 0, desired_digest: "", checked_at: at, complete: false, replicas: [] };
      else if (path === "/operator/v1/workflow-templates") json = { items: [template] };
      else if (path === `/operator/v1/workflow-templates/${LONG_TEXT}`) json = { ...template, graph: { "1": { class_type: "Text", inputs: { text: "synthetic input" } } } };
      else if (path === `/operator/v1/workflow-templates/${LONG_TEXT}/history`) json = { items: [] };
      else if (path === "/operator/v1/workflow-templates/status") json = { desired_template_count: 1, desired_bundle_digest: "d".repeat(64), checked_at: at, complete: false, replicas: [] };
      else if (path === "/operator/v1/metrics/history") json = { since: at, until: "2026-09-13T00:00:00Z", series: [{ metric: "gateway_http_requests_total", labels: { endpoint: "chat", status: "200" }, points: [{ bucket_at: at, value: 12 }, { bucket_at: "2026-09-12T00:05:00Z", value: 24 }] }] };
    }
    if (json === undefined) {
      unexpected.push(`${method} ${url.pathname}`);
      await route.fulfill({ status: 501, json: { error: "unexpected_fixture_request" } });
    } else await route.fulfill({ json });
  });
}

/** test adds isolated API fixtures and fails on unhandled API or browser errors.
 * test 添加隔离的 API 样本，遇到未声明的 API 请求或浏览器异常时判失败。 */
export const test = base.extend({
  page: async ({ page }, runFixture) => {
    const unexpected: string[] = [];
    page.on("pageerror", error => unexpected.push(error.message));
    await installFixtures(page, unexpected);
    await runFixture(page);
    expect(unexpected, "all API calls and browser errors must be accounted for").toEqual([]);
  },
});
export { expect };

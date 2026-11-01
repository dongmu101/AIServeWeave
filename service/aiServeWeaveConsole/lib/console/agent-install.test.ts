import assert from "node:assert/strict";
import test from "node:test";
import { request } from "./api-client.ts";
import {
  buildInstallCommand,
  credentialFor,
  INSTALL_METHODS,
  INSTALL_OSES,
  INSTALL_SOURCES,
  parseOperatorInstall,
  parsePlatformCheck,
  parseTenantInstall,
  type InstallIssue,
} from "./agent-install.ts";
import { resolveOperatorUpstream, resolveUpstream } from "./upstream-routes.ts";

const node = "node_" + "a".repeat(32);
const tenantNode = "tnode.tenant_1." + "b".repeat(32);
const code = "ABCD-EFGH-JKMN-PQRS-TVWX";
const base = "https://open7x.com";
const codeResponse = { credential: "code", code, node_id: node, expires_at: "2026-10-30T01:00:00Z", install_base_url: base };
const tokenResponse = {
  credential: "token", node_id: node, expires_at: "2026-10-30T01:00:00Z", install_base_url: base,
  bootstrap_token: "T".repeat(43), ca_sha256: "a".repeat(64), registry_addr: "registry.open7x.com:9443", gateway_addr: "agent.open7x.com:8443",
};

test("install issue parsing accepts the two credential shapes", () => {
  const issued = parseOperatorInstall(codeResponse);
  assert.equal(issued.code, code);
  assert.equal(issued.token, null);
  assert.equal(parseOperatorInstall(tokenResponse).token?.registryAddr, "registry.open7x.com:9443");
  assert.equal(parseTenantInstall({ ...codeResponse, node_id: tenantNode }).nodeId, tenantNode);
});

test("install issue parsing rejects malformed or mixed responses", () => {
  const bad: Array<[string, unknown]> = [
    ["not an object", "x"],
    ["unknown credential", { ...codeResponse, credential: "password" }],
    ["lowercase code", { ...codeResponse, code: code.toLowerCase() }],
    ["code with ambiguous letters", { ...codeResponse, code: "ABCD-EFGH-JKMN-PQRS-TVWI" }],
    ["unseparated code", { ...codeResponse, code: code.replaceAll("-", "") }],
    ["code credential carrying a token", { ...codeResponse, bootstrap_token: "T".repeat(43) }],
    ["token credential carrying a code", { ...tokenResponse, code }],
    ["token missing its fingerprint", { ...tokenResponse, ca_sha256: undefined }],
    ["short token", { ...tokenResponse, bootstrap_token: "short" }],
    ["token with shell metacharacters", { ...tokenResponse, bootstrap_token: "a;b".padEnd(40, "x") }],
    ["address with shell metacharacters", { ...tokenResponse, gateway_addr: "a.example:1;rm" }],
    ["tenant id on the operator surface", { ...codeResponse, node_id: tenantNode }],
    ["plain http base", { ...codeResponse, install_base_url: "http://open7x.com" }],
    ["base with a path", { ...codeResponse, install_base_url: "https://open7x.com/x" }],
    ["base with a trailing slash", { ...codeResponse, install_base_url: "https://open7x.com/" }],
    ["base with credentials", { ...codeResponse, install_base_url: "https://u:p@open7x.com" }],
    ["bad expiry", { ...codeResponse, expires_at: "soon" }],
  ];
  for (const [name, value] of bad) assert.throws(() => parseOperatorInstall(value), `${name} was accepted`);
  assert.throws(() => parseTenantInstall(codeResponse), "operator id on the tenant surface was accepted");
});

const codeIssue: InstallIssue = parseOperatorInstall(codeResponse);
const tokenIssue: InstallIssue = parseOperatorInstall(tokenResponse);

test("commands cover every system, method and source", () => {
  const expected: Record<string, string | null> = {
    "ubuntu/code/platform": `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | sudo bash -s -- --code '${code}'`,
    "ubuntu/code/github": `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | sudo bash -s -- --code '${code}' --source github`,
    "ubuntu/interactive/platform": `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | sudo bash -s --`,
    "ubuntu/interactive/github": `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | sudo bash -s -- --source github`,
    "macos/code/platform": `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | bash -s -- --code '${code}'`,
    "macos/code/github": `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | bash -s -- --code '${code}' --source github`,
    "macos/interactive/platform": `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | bash -s --`,
    "macos/interactive/github": `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | bash -s -- --source github`,
    "windows/code/platform": `& ([scriptblock]::Create((irm 'https://open7x.com/agent/install.ps1'))) -Code '${code}'`,
    "windows/code/github": `& ([scriptblock]::Create((irm 'https://open7x.com/agent/install.ps1'))) -Code '${code}' -Source github`,
    "windows/interactive/platform": `irm 'https://open7x.com/agent/install.ps1' | iex`,
    "windows/interactive/github": `& ([scriptblock]::Create((irm 'https://open7x.com/agent/install.ps1'))) -Source github`,
  };
  for (const os of INSTALL_OSES) for (const source of INSTALL_SOURCES) for (const method of INSTALL_METHODS) {
    const key = `${os}/${method}/${source}`;
    if (method === "token") continue;
    assert.equal(buildInstallCommand(codeIssue, { os, method, source }), expected[key], key);
  }
  const tokenArgs = (flags: string[]) => flags.join(" ");
  const posixToken = tokenArgs(["--node-id", `'${node}'`, "--token", `'${"T".repeat(43)}'`, "--ca-sha256", `'${"a".repeat(64)}'`, "--registry", "'registry.open7x.com:9443'", "--gateway", "'agent.open7x.com:8443'"]);
  const psToken = tokenArgs(["-NodeId", `'${node}'`, "-Token", `'${"T".repeat(43)}'`, "-CaSha256", `'${"a".repeat(64)}'`, "-Registry", "'registry.open7x.com:9443'", "-Gateway", "'agent.open7x.com:8443'"]);
  assert.equal(buildInstallCommand(tokenIssue, { os: "ubuntu", method: "token", source: "platform" }), `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | sudo bash -s -- ${posixToken}`);
  assert.equal(buildInstallCommand(tokenIssue, { os: "macos", method: "token", source: "github" }), `curl -fsSL --proto '=https' 'https://open7x.com/agent/install.sh' | bash -s -- ${posixToken} --source github`);
  assert.equal(buildInstallCommand(tokenIssue, { os: "windows", method: "token", source: "platform" }), `& ([scriptblock]::Create((irm 'https://open7x.com/agent/install.ps1'))) ${psToken}`);
  assert.equal(buildInstallCommand(tokenIssue, { os: "windows", method: "token", source: "github" }), `& ([scriptblock]::Create((irm 'https://open7x.com/agent/install.ps1'))) ${psToken} -Source github`);
});

test("an issued credential only serves the methods it can", () => {
  for (const os of INSTALL_OSES) {
    assert.equal(buildInstallCommand(codeIssue, { os, method: "token", source: "platform" }), null);
    assert.equal(buildInstallCommand(tokenIssue, { os, method: "code", source: "platform" }), null);
    assert.equal(buildInstallCommand(tokenIssue, { os, method: "interactive", source: "platform" }), null);
  }
  assert.equal(credentialFor("code"), "code");
  assert.equal(credentialFor("interactive"), "code");
  assert.equal(credentialFor("token"), "token");
});

test("interactive commands never contain the code", () => {
  for (const os of INSTALL_OSES) for (const source of INSTALL_SOURCES) {
    assert.ok(!buildInstallCommand(codeIssue, { os, method: "interactive", source })?.includes(code));
  }
});

test("quoting keeps hostile values inert", () => {
  // The parser refuses such values, so build the issue by hand to test the quoting itself.
  // 解析器会拒绝这类值，所以手工构造 issue 来单独测试引用逻辑。
  const hostile: InstallIssue = { ...tokenIssue, token: { bootstrapToken: "it's $(reboot) `x` \"y\"", caSha256: "a".repeat(64), registryAddr: "r:1", gatewayAddr: "g:1" } };
  assert.match(buildInstallCommand(hostile, { os: "ubuntu", method: "token", source: "platform" }) ?? "", /--token 'it'\\''s \$\(reboot\) `x` "y"'/);
  assert.match(buildInstallCommand(hostile, { os: "windows", method: "token", source: "platform" }) ?? "", /-Token 'it''s \$\(reboot\) `x` "y"'/);
});

test("install routes exist only on their own surface and drop query parameters", () => {
  const operator = ["operator", "v1", "agent-install-codes"];
  assert.deepEqual(resolveOperatorUpstream("POST", operator, new URLSearchParams("node_id=x")), { path: "/operator/v1/agent-install-codes", search: "" });
  assert.equal(resolveUpstream("POST", operator, new URLSearchParams()), null);
  assert.equal(resolveOperatorUpstream("GET", operator, new URLSearchParams()), null);
  const tenant = ["admin", "v1", "nodes", "install-codes"];
  assert.deepEqual(resolveUpstream("POST", tenant, new URLSearchParams("tenant_id=other")), { path: "/admin/v1/nodes/install-codes", search: "" });
  assert.equal(resolveOperatorUpstream("POST", tenant, new URLSearchParams()), null);
  assert.equal(resolveUpstream("GET", tenant, new URLSearchParams()), null);
  // The id route must not swallow the new segment. / 按 id 的路由不得吞掉新增的路径段。
  assert.equal(resolveUpstream("DELETE", tenant, new URLSearchParams())?.path, "/admin/v1/nodes/install-codes");
});

test("a lost install code response never automatically issues a second code", async () => {
  for (const [surface, path] of [["operator", "/operator/v1/agent-install-codes"], [undefined, "/admin/v1/nodes/install-codes"]] as const) {
    let calls = 0;
    await assert.rejects(request({ method: "POST", surface, path, body: { ttl_minutes: 60, credential: "code" }, parse: parseTenantInstall }, {
      fetchImpl: async () => { calls++; throw new TypeError("lost response"); },
      sleep: async () => { assert.fail("an issue must never retry"); },
    }));
    assert.equal(calls, 1);
  }
});

test("platform check tells installable, refused and unreachable apart", () => {
  assert.deepEqual(parsePlatformCheck({ version: "v1.2.3", installable: true }), { state: "ok" });
  assert.deepEqual(parsePlatformCheck({ version: "dev", installable: false, reason: "x" }), { state: "refused", version: "dev" });
  assert.deepEqual(parsePlatformCheck({ version: "<script>", installable: false }), { state: "refused", version: "?" });
  for (const bad of [null, "x", [], {}, { version: "v1.2.3" }, { installable: "yes" }]) {
    assert.deepEqual(parsePlatformCheck(bad), { state: "unreachable" }, JSON.stringify(bad));
  }
});

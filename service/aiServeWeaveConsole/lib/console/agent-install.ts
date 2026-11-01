import { ApiError } from "./errors.ts";

/** InstallOS is the operating system the command is written for. / InstallOS 是命令所面向的操作系统。 */
export type InstallOS = "ubuntu" | "macos" | "windows";
/** InstallMethod is how the node owner supplies the credential. / InstallMethod 是节点提供者提供凭据的方式。 */
export type InstallMethod = "code" | "interactive" | "token";
/** InstallSource is where the Agent binary is downloaded from. / InstallSource 是 Agent 二进制的下载来源。 */
export type InstallSource = "platform" | "github";

/** INSTALL_OSES lists the systems the dialog offers. / INSTALL_OSES 列出对话框提供的系统。 */
export const INSTALL_OSES: readonly InstallOS[] = ["ubuntu", "macos", "windows"];
/** INSTALL_METHODS lists the credential methods the dialog offers. / INSTALL_METHODS 列出对话框提供的凭据方式。 */
export const INSTALL_METHODS: readonly InstallMethod[] = ["code", "interactive", "token"];
/** INSTALL_SOURCES lists the artifact sources the dialog offers. / INSTALL_SOURCES 列出对话框提供的制品来源。 */
export const INSTALL_SOURCES: readonly InstallSource[] = ["platform", "github"];

/** InstallToken is the material of a directly issued token credential. / InstallToken 是直接签发的令牌凭据所含的材料。 */
export interface InstallToken { bootstrapToken: string; caSha256: string; registryAddr: string; gatewayAddr: string }

/**
 * InstallIssue holds a one-time install credential only in page memory. Code is
 * set for credential "code"; token for credential "token".
 *
 * InstallIssue 仅在页面内存中保存一次性安装凭据。credential 为 "code" 时设置 code，
 * 为 "token" 时设置 token。
 */
export interface InstallIssue {
  credential: "code" | "token";
  nodeId: string;
  expiresAt: string;
  baseUrl: string;
  code: string | null;
  token: InstallToken | null;
}

// Mirrors installcode's Crockford Base32 display form. / 镜像 installcode 的 Crockford Base32 显示形式。
const CODE = /^[0-9A-HJKMNP-TV-Z]{4}(?:-[0-9A-HJKMNP-TV-Z]{4}){4}$/;
const OPERATOR_NODE_ID = /^node_[a-f0-9]{32}$/;
const TENANT_NODE_ID = /^tnode\.[A-Za-z0-9_-]{1,89}\.[a-f0-9]{32}$/;
const BOOTSTRAP_TOKEN = /^[A-Za-z0-9_-]{32,128}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const ADDRESS = /^[A-Za-z0-9.:-]{1,253}$/;

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

function text(value: unknown, pattern: RegExp): string {
  if (typeof value !== "string" || !pattern.test(value)) throw new ApiError("contract");
  return value;
}

function origin(value: unknown): string {
  if (typeof value !== "string") throw new ApiError("contract");
  let url: URL;
  try { url = new URL(value); } catch { throw new ApiError("contract"); }
  if (url.protocol !== "https:" || url.origin !== value || url.username || url.password) throw new ApiError("contract");
  return value;
}

function parseInstallIssueFor(value: unknown, nodeId: RegExp): InstallIssue {
  const data = record(value);
  const common = {
    nodeId: text(data.node_id, nodeId),
    expiresAt: typeof data.expires_at === "string" && Number.isFinite(Date.parse(data.expires_at)) ? data.expires_at : (() => { throw new ApiError("contract"); })(),
    baseUrl: origin(data.install_base_url),
  };
  if (data.credential === "code") {
    if (data.bootstrap_token !== undefined) throw new ApiError("contract");
    return { ...common, credential: "code", code: text(data.code, CODE), token: null };
  }
  if (data.credential === "token") {
    if (data.code !== undefined) throw new ApiError("contract");
    return {
      ...common,
      credential: "token",
      code: null,
      token: {
        bootstrapToken: text(data.bootstrap_token, BOOTSTRAP_TOKEN),
        caSha256: text(data.ca_sha256, SHA256),
        registryAddr: text(data.registry_addr, ADDRESS),
        gatewayAddr: text(data.gateway_addr, /^[A-Za-z0-9.:,-]{1,512}$/),
      },
    };
  }
  throw new ApiError("contract");
}

/** parseOperatorInstall validates POST /operator/v1/agent-install-codes. / parseOperatorInstall 校验 POST /operator/v1/agent-install-codes。 */
export function parseOperatorInstall(value: unknown): InstallIssue {
  return parseInstallIssueFor(value, OPERATOR_NODE_ID);
}

/** parseTenantInstall validates POST /admin/v1/nodes/install-codes. / parseTenantInstall 校验 POST /admin/v1/nodes/install-codes。 */
export function parseTenantInstall(value: unknown): InstallIssue {
  return parseInstallIssueFor(value, TENANT_NODE_ID);
}

/** credentialFor is the credential kind the server must issue for a method. / credentialFor 是某种方式要求服务端签发的凭据类型。 */
export function credentialFor(method: InstallMethod): "code" | "token" {
  return method === "token" ? "token" : "code";
}

// Single-quote a value for POSIX shells and PowerShell, so no value is ever interpreted.
// 把值放进单引号，供 POSIX shell 与 PowerShell 使用，使任何值都不会被解释执行。
function posixQuote(value: string): string { return `'${value.replace(/'/g, `'\\''`)}'`; }
function powershellQuote(value: string): string { return `'${value.replace(/'/g, "''")}'`; }

/**
 * buildInstallCommand writes the one-line command for issue. It returns null
 * when the issued credential cannot serve the method (a code cannot become a
 * token install); the caller then asks for a new one.
 *
 * buildInstallCommand 写出 issue 对应的一行命令。已签发的凭据无法满足所选方式（兑换码
 * 不能变成令牌安装）时返回 null，由调用方提示重新生成。
 */
export function buildInstallCommand(issue: InstallIssue, choice: { os: InstallOS; method: InstallMethod; source: InstallSource }): string | null {
  if (issue.credential !== credentialFor(choice.method)) return null;
  const github = choice.source === "github";
  if (choice.os === "windows") {
    const url = powershellQuote(`${issue.baseUrl}/agent/install.ps1`);
    const options: string[] = [];
    if (choice.method === "code" && issue.code) options.push("-Code", powershellQuote(issue.code));
    if (choice.method === "token" && issue.token) {
      options.push("-NodeId", powershellQuote(issue.nodeId), "-Token", powershellQuote(issue.token.bootstrapToken),
        "-CaSha256", powershellQuote(issue.token.caSha256), "-Registry", powershellQuote(issue.token.registryAddr), "-Gateway", powershellQuote(issue.token.gatewayAddr));
    }
    if (github) options.push("-Source", "github");
    // Without options, irm | iex asks for the code on the terminal. / 没有选项时，irm | iex 会在终端询问兑换码。
    if (options.length === 0) return `irm ${url} | iex`;
    return `& ([scriptblock]::Create((irm ${url}))) ${options.join(" ")}`;
  }
  const options: string[] = [];
  if (choice.method === "code" && issue.code) options.push("--code", posixQuote(issue.code));
  if (choice.method === "token" && issue.token) {
    options.push("--node-id", posixQuote(issue.nodeId), "--token", posixQuote(issue.token.bootstrapToken),
      "--ca-sha256", posixQuote(issue.token.caSha256), "--registry", posixQuote(issue.token.registryAddr), "--gateway", posixQuote(issue.token.gatewayAddr));
  }
  if (github) options.push("--source", "github");
  const shell = choice.os === "ubuntu" ? "sudo bash" : "bash";
  return `curl -fsSL --proto '=https' ${posixQuote(`${issue.baseUrl}/agent/install.sh`)} | ${shell} -s --${options.length ? ` ${options.join(" ")}` : ""}`;
}

/** PlatformCheck is what the platform's public /agent/v1/manifest says about installing. / PlatformCheck 是平台公开的 /agent/v1/manifest 对安装的说明。 */
export type PlatformCheck = { state: "ok" } | { state: "refused"; version: string } | { state: "unreachable" };

const MANIFEST_VERSION = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

/**
 * parsePlatformCheck reads a manifest response. A manifest that is not valid,
 * or an old Gateway without the installable flag, counts as unreachable rather
 * than ok, so the warning errs on the side of showing.
 *
 * parsePlatformCheck 解析清单响应。格式不对，或没有 installable 字段的旧 Gateway，按
 * 不可达处理而不是 ok，让警告宁可多显示。
 */
export function parsePlatformCheck(value: unknown): PlatformCheck {
  if (!value || typeof value !== "object" || Array.isArray(value)) return { state: "unreachable" };
  const data = value as Record<string, unknown>;
  if (typeof data.installable !== "boolean") return { state: "unreachable" };
  if (data.installable) return { state: "ok" };
  const version = typeof data.version === "string" && MANIFEST_VERSION.test(data.version) ? data.version : "?";
  return { state: "refused", version };
}

import { ApiError } from "./errors.ts";

/** EnrollmentCA mirrors the public certificate download response.
 * EnrollmentCA 镜像公开证书下载响应。 */
export interface EnrollmentCA { pem: string; sha256: string }

/** EnrollmentBundle holds a new node's credential archive only in page memory.
 * EnrollmentBundle 仅在页面内存保留新节点的凭据压缩包。 */
export interface EnrollmentBundle { nodeId: string; expiresAt: string; filename: string; bytes: Uint8Array<ArrayBuffer> }

/** parseEnrollmentCA validates the certificate-only response and file checksum.
 * parseEnrollmentCA 校验证书响应和文件校验和。 */
export function parseEnrollmentCA(value: unknown): EnrollmentCA {
  const data = record(value);
  if (typeof data.ca_pem !== "string" || data.ca_pem.length > 65536 || !/^(?:-----BEGIN CERTIFICATE-----\n[A-Za-z0-9+/=\n]+\n-----END CERTIFICATE-----\n)+$/.test(data.ca_pem) || typeof data.ca_sha256 !== "string" || !/^[a-f0-9]{64}$/.test(data.ca_sha256)) throw new ApiError("contract");
  return { pem: data.ca_pem, sha256: data.ca_sha256 };
}

/** parseEnrollmentBundle bounds binary decoding and enforces a fixed safe filename.
 * parseEnrollmentBundle 限制二进制解码大小，并强制使用固定格式的安全文件名。 */
export function parseEnrollmentBundle(value: unknown): EnrollmentBundle {
  const data = record(value);
  if (typeof data.node_id !== "string" || !/^node_[a-f0-9]{32}$/.test(data.node_id) || data.filename !== `aiserveweave-enrollment-${data.node_id}.zip` || typeof data.expires_at !== "string" || !Number.isFinite(Date.parse(data.expires_at)) || typeof data.archive_base64 !== "string" || data.archive_base64.length > 128 * 1024 || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(data.archive_base64)) throw new ApiError("contract");
  let bytes: Uint8Array<ArrayBuffer>;
  try { bytes = Uint8Array.from(atob(data.archive_base64), char => char.charCodeAt(0)); } catch { throw new ApiError("contract"); }
  if (bytes.length < 4 || bytes[0] !== 0x50 || bytes[1] !== 0x4b || bytes[2] !== 0x03 || bytes[3] !== 0x04) throw new ApiError("contract");
  return { nodeId: data.node_id, expiresAt: data.expires_at, filename: data.filename, bytes };
}

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}

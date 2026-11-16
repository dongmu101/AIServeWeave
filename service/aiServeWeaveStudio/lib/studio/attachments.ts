/**
 * Client-side attachment checks, run before anything is sent, so a file the
 * Gateway would refuse is caught while the user is still composing. The
 * Gateway re-checks every upload (extension plus sniffed bytes); this is only
 * the early, friendlier half.
 *
 * 客户端附件检查，在发送之前运行，让 Gateway 会拒绝的文件在用户还在编辑时就被发现。
 * Gateway 会对每个上传重新检查（扩展名加字节嗅探）；这里只是更早、更友好的那一半。
 */

export const MAX_ATTACHMENTS = 10;
export const MAX_ATTACHMENT_BYTES = 20 * 1024 * 1024;

const ALLOWED: Record<string, "image" | "document"> = {
  png: "image",
  jpg: "image",
  jpeg: "image",
  gif: "image",
  webp: "image",
  pdf: "document",
  txt: "document",
  md: "document",
  csv: "document",
  json: "document",
};

/** ACCEPT is the file input's accept attribute, derived from ALLOWED.
 * ACCEPT 是文件输入框的 accept 属性，由 ALLOWED 派生。 */
export const ACCEPT = Object.keys(ALLOWED).map((ext) => `.${ext}`).join(",");

export interface Candidate {
  name: string;
  size: number;
}

export type Verdict = { ok: true; kind: "image" | "document" } | { ok: false; reason: string };

/** checkAttachment judges one file by its extension and size.
 * checkAttachment 按扩展名与大小判断一个文件。 */
export function checkAttachment(file: Candidate): Verdict {
  const dot = file.name.lastIndexOf(".");
  const ext = dot > 0 ? file.name.slice(dot + 1).toLowerCase() : "";
  const kind = ALLOWED[ext];
  if (!kind) return { ok: false, reason: `不支持「${displayName(file.name)}」的文件类型` };
  if (file.size === 0) return { ok: false, reason: `「${displayName(file.name)}」是空文件` };
  if (file.size > MAX_ATTACHMENT_BYTES) return { ok: false, reason: `「${displayName(file.name)}」超过 20 MB` };
  return { ok: true, kind };
}

/** admit splits incoming files into accepted and rejected, honouring the
 * per-message limit given how many are already attached.
 *
 * admit 把新加入的文件分成接受与拒绝两组，并结合已附加的数量遵守单条消息的上限。 */
export function admit<T extends Candidate>(existing: number, incoming: T[]): { accepted: T[]; rejected: string[] } {
  const accepted: T[] = [];
  const rejected: string[] = [];
  for (const file of incoming) {
    const verdict = checkAttachment(file);
    if (!verdict.ok) {
      rejected.push(verdict.reason);
    } else if (existing + accepted.length >= MAX_ATTACHMENTS) {
      rejected.push(`每条消息最多 ${MAX_ATTACHMENTS} 个附件`);
    } else {
      accepted.push(file);
    }
  }
  return { accepted, rejected: [...new Set(rejected)] };
}

/** displayName shortens a long file name for a message, keeping its extension.
 * displayName 为提示文案缩短过长的文件名，保留扩展名。 */
export function displayName(name: string): string {
  if (name.length <= 40) return name;
  const dot = name.lastIndexOf(".");
  const ext = dot > 0 && name.length - dot <= 8 ? name.slice(dot) : "";
  return `${name.slice(0, 40 - ext.length - 1)}…${ext}`;
}

/** formatSize renders a byte count for an attachment chip.
 * formatSize 为附件标签渲染字节数。 */
export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

import type { FlowDefinition, NodeType } from "./flows.ts";
import { templateRefs } from "./flow-template.ts";

/**
 * Artifacts (images and files) that image_generation and comfyui_template
 * nodes produce. A run records only the public artifact id; the bytes come
 * from the BFF's download route. Whether an output is an artifact is decided
 * from the definition the run ran, never from the shape of the value.
 *
 * image_generation 与 comfyui_template 节点产出的产物（图片与文件）。运行只记录公开的
 * 产物 id；字节来自 BFF 的下载路由。一个输出是不是产物，由运行所用的定义决定，而不是由
 * 值的形状决定。
 */

export type ArtifactKind = "image" | "video" | "file";

export interface ArtifactItem {
  id: string;
  filename: string;
  kind: ArtifactKind;
}

/** ArtifactShape is how an artifact output is shown: "image" is one artifact
 * id, "list" is a JSON array of artifacts.
 * ArtifactShape 是产物输出的展示形式："image" 是一个产物 id，"list" 是产物组成的 JSON 数组。 */
export type ArtifactShape = "image" | "list";

export const RUN_ID_PATTERN = /^frn_[A-Za-z0-9_-]{1,36}$/;
export const ARTIFACT_ID_PATTERN = /^[A-Za-z0-9_-]{1,128}$/;

const KINDS: ArtifactKind[] = ["image", "video", "file"];

/** artifactFieldShape is how field of a node of type is shown when it names
 * an artifact, or null when it is plain text.
 * artifactFieldShape 在 type 类型节点的 field 指向产物时给出其展示形式，纯文本时为 null。 */
export function artifactFieldShape(type: NodeType | string, field: string): ArtifactShape | null {
  if (type === "image_generation") return field === "image" ? "image" : null;
  if (type === "comfyui_template") return field === "image" ? "image" : field === "artifacts" ? "list" : null;
  return null;
}

/** artifactOutputs maps each result field whose ref is the sole reference to
 * an artifact field to how it is shown.
 *
 * artifactOutputs 把「ref 是对某个产物字段的唯一引用」的每个结果字段映射到其展示形式。 */
export function artifactOutputs(def: FlowDefinition): Record<string, ArtifactShape> {
  const out: Record<string, ArtifactShape> = {};
  for (const output of def.outputs) {
    const { refs, sole } = templateRefs(output.ref);
    if (!sole) continue;
    const node = def.nodes.find((n) => n.id === refs[0].scope);
    const shape = node ? artifactFieldShape(node.type, refs[0].field) : null;
    if (shape) out[output.name] = shape;
  }
  return out;
}

/** parseArtifactList reads an `artifacts` value: a JSON array of
 * {"artifact_id","filename","kind"}. It returns null for anything else, so
 * the caller can show the raw text instead. An unknown kind is treated as a
 * plain file.
 *
 * parseArtifactList 读取 `artifacts` 的值：由 {"artifact_id","filename","kind"} 组成的
 * JSON 数组。其他任何内容返回 null，调用方可改为展示原文。未知的 kind 当作普通文件。 */
export function parseArtifactList(value: string): ArtifactItem[] | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch {
    return null;
  }
  if (!Array.isArray(parsed)) return null;
  const out: ArtifactItem[] = [];
  for (const entry of parsed) {
    if (typeof entry !== "object" || entry === null) return null;
    const source = entry as Record<string, unknown>;
    if (typeof source.artifact_id !== "string" || !ARTIFACT_ID_PATTERN.test(source.artifact_id)) return null;
    const filename = typeof source.filename === "string" ? source.filename : "";
    const kind = KINDS.find((k) => k === source.kind) ?? "file";
    out.push({ id: source.artifact_id, filename, kind });
  }
  return out;
}

/** artifactURL is where the browser fetches an artifact of a run from, or ""
 * when either id is not one the BFF would accept.
 *
 * artifactURL 是浏览器获取某次运行的产物的地址；任一 id 不是 BFF 会接受的形式时为空串。 */
export function artifactURL(runId: string, artifactId: string): string {
  if (!RUN_ID_PATTERN.test(runId) || !ARTIFACT_ID_PATTERN.test(artifactId)) return "";
  return `/api/studio/runs/${runId}/artifacts/${artifactId}`;
}

/** downloadName is the file name a download of an artifact is offered under.
 * downloadName 是产物下载时提供的文件名。 */
export function downloadName(item: Pick<ArtifactItem, "id" | "filename">): string {
  return item.filename.trim() || item.id;
}

const PASSED_HEADERS = ["content-type", "content-length", "content-disposition"] as const;

/** artifactResponseHeaders is the header set the BFF answers a download
 * with: the Gateway's type, length and disposition, plus the headers that
 * make the bytes inert in the browser. A length is passed only when the body
 * was not re-encoded on the way, since the two would then disagree.
 *
 * artifactResponseHeaders 是 BFF 应答下载时的头集合：Gateway 给出的类型、长度与
 * disposition，加上让这些字节在浏览器中保持惰性的头。只有响应体在途中没有被重新编码时
 * 才传递长度，否则两者会不一致。 */
export function artifactResponseHeaders(upstream: Headers): Headers {
  const out = new Headers();
  const encoded = (upstream.get("content-encoding") ?? "identity").toLowerCase() !== "identity";
  for (const name of PASSED_HEADERS) {
    if (name === "content-length" && encoded) continue;
    const value = upstream.get(name);
    if (value !== null) out.set(name, value);
  }
  if (!out.has("content-type")) out.set("content-type", "application/octet-stream");
  // Never inline: an <img> still renders an attachment, a navigation downloads it.
  // 绝不内联：<img> 仍能渲染 attachment，直接打开则是下载。
  if (!out.has("content-disposition")) out.set("content-disposition", "attachment");
  out.set("X-Content-Type-Options", "nosniff");
  out.set("Content-Security-Policy", "sandbox");
  out.set("Cache-Control", "private, no-store");
  return out;
}

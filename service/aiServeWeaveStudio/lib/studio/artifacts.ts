import { isFileInput, type FileValue, type FlowDefinition, type NodeType } from "./flows.ts";
import { templateRefs, type TemplateRef } from "./flow-template.ts";

/**
 * Artifacts (images and files) that image_generation and comfyui_template
 * nodes produce. A run records only the public artifact id; the bytes come
 * from the BFF's download route. Whether an output is an artifact is decided
 * from the definition the run ran, never from the shape of the value.
 *
 * image_generation 与 comfyui_template 节点产出的产物（图片与文件）。运行只记录公开的
 * 产物 id；字节来自 BFF 的下载路由。一个输出是不是产物，由运行所用的定义决定，而不是由
 * 值的形状决定。
 *
 * An image or file input of a run is an artifact too: the file the run was
 * started with, stored under the run and downloaded the same way.
 * 运行的 image 或 file 输入同样是产物：运行启动时收到的文件，存放在该运行之下，下载方式
 * 相同。
 */

export type ArtifactKind = "image" | "video" | "file";

export interface ArtifactItem {
  id: string;
  filename: string;
  kind: ArtifactKind;
}

/** ArtifactShape is how an artifact output is shown: "image" is one artifact
 * id of a picture, "file" one artifact id of a file of any kind (its name is
 * looked up in the run's input files), "list" a JSON array of artifacts.
 * ArtifactShape 是产物输出的展示形式："image" 是一张图片的一个产物 id，"file" 是任意文件的
 * 一个产物 id（其文件名到运行的输入文件里查），"list" 是产物组成的 JSON 数组。 */
export type ArtifactShape = "image" | "file" | "list";

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

/** refArtifactShape is how the value of ref is shown when it names an
 * artifact in def — an image or file input, or an artifact field of a node —
 * or null when it is plain text or names nothing.
 *
 * refArtifactShape 在 ref 指向 def 中的产物时给出其值的展示形式——image 或 file 输入，或节点
 * 的产物字段；纯文本或不指向任何东西时为 null。 */
export function refArtifactShape(def: FlowDefinition, ref: TemplateRef): ArtifactShape | null {
  if (ref.scope === "inputs") {
    const input = def.inputs.find((i) => i.name === ref.field);
    if (!input || !isFileInput(input)) return null;
    return input.type === "image" ? "image" : "file";
  }
  const node = def.nodes.find((n) => n.id === ref.scope);
  return node ? artifactFieldShape(node.type, ref.field) : null;
}

/** refFileShape is the same for a reference that names exactly one file: an
 * image or file input, or the image of a node that makes one. Only such a
 * reference may be the whole of a ComfyUI template's file input; a node's
 * `artifacts` list may not.
 *
 * refFileShape 对恰好指向一个文件的引用给出同样的结果：image 或 file 输入，或产出图片的节点的
 * image。只有这样的引用才能作为 ComfyUI 模板文件型输入的全部内容；节点的 artifacts 列表不行。 */
export function refFileShape(def: FlowDefinition, ref: TemplateRef): "image" | "file" | null {
  const shape = refArtifactShape(def, ref);
  return shape === "list" ? null : shape;
}

/** artifactOutputs maps each result field whose ref is the sole reference to
 * an artifact — a node's artifact field or a file input — to how it is shown.
 *
 * artifactOutputs 把「ref 是对某个产物的唯一引用」——节点的产物字段或文件输入——的每个结果
 * 字段映射到其展示形式。 */
export function artifactOutputs(def: FlowDefinition): Record<string, ArtifactShape> {
  const out: Record<string, ArtifactShape> = {};
  for (const output of def.outputs) {
    const { refs, sole } = templateRefs(output.ref);
    if (!sole) continue;
    const shape = refArtifactShape(def, refs[0]);
    if (shape) out[output.name] = shape;
  }
  return out;
}

/** stepFileInputs maps each input of a comfyui_template node that is bound to
 * exactly one file to how that file is shown, so a node's detail can show
 * the file where its input would otherwise be an artifact id.
 *
 * stepFileInputs 把 comfyui_template 节点中绑定到恰好一个文件的每个输入映射到该文件的展示
 * 形式，节点详情因此能在输入原本只是产物 id 的地方展示文件。 */
export function stepFileInputs(def: FlowDefinition, nodeId: string): Record<string, "image" | "file"> {
  const node = def.nodes.find((n) => n.id === nodeId);
  const inputs = node?.type === "comfyui_template" && typeof node.config?.inputs === "object" && node.config.inputs !== null ? node.config.inputs : {};
  const out: Record<string, "image" | "file"> = {};
  for (const [name, value] of Object.entries(inputs)) {
    if (typeof value !== "string") continue;
    const { refs, sole } = templateRefs(value);
    const shape = sole ? refFileShape(def, refs[0]) : null;
    if (shape) out[name] = shape;
  }
  return out;
}

/** parseFileValue reads the FileValue a run records for an image or file
 * input, or null for anything else.
 * parseFileValue 读取运行为 image 或 file 输入记录的 FileValue，其他内容返回 null。 */
export function parseFileValue(value: unknown): FileValue | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const source = value as Record<string, unknown>;
  if (typeof source.artifact_id !== "string" || !ARTIFACT_ID_PATTERN.test(source.artifact_id)) return null;
  return { artifactId: source.artifact_id, filename: typeof source.filename === "string" ? source.filename : "" };
}

export interface RunInputFile {
  name: string;
  label: string;
  type: "image" | "file";
  file: FileValue;
}

/** runInputFiles lists the files a run was started with, in the order the
 * definition declares its inputs.
 * runInputFiles 按定义声明输入的顺序，列出运行启动时收到的文件。 */
export function runInputFiles(def: FlowDefinition, inputs: Record<string, unknown>): RunInputFile[] {
  const out: RunInputFile[] = [];
  for (const input of def.inputs) {
    if (input.type !== "image" && input.type !== "file") continue;
    const file = parseFileValue(inputs[input.name]);
    if (file) out.push({ name: input.name, label: input.label || input.name, type: input.type, file });
  }
  return out;
}

const IMAGE_EXTENSIONS = ["png", "jpg", "jpeg", "webp", "gif", "bmp"];
const VIDEO_EXTENSIONS = ["mp4", "webm", "mov"];

/** kindOfFilename guesses how a file is shown from its extension.
 * kindOfFilename 按扩展名推断文件的展示方式。 */
export function kindOfFilename(filename: string): ArtifactKind {
  const dot = filename.lastIndexOf(".");
  const ext = dot < 0 ? "" : filename.slice(dot + 1).toLowerCase();
  if (IMAGE_EXTENSIONS.includes(ext)) return "image";
  if (VIDEO_EXTENSIONS.includes(ext)) return "video";
  return "file";
}

/** downloadFailureText is what a failed artifact download says. A 404 is the
 * retention sweep: stored objects are kept for a limited time.
 * downloadFailureText 是产物下载失败时的提示。404 是保留期清理：存放的对象只保留一段时间。 */
export function downloadFailureText(status: number): string {
  if (status === 404) return "这个文件已不存在，可能超过保留期被清理了。";
  if (status === 401) return "登录已过期，请重新登录。";
  return "下载失败，请稍后重试。";
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

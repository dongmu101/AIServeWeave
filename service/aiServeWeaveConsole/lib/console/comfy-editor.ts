import { ApiError } from "./errors.ts";
import { MAX_GRAPH_BYTES, MAX_WORKFLOW_BYTES } from "./workflow-templates.ts";

/**
 * This module holds the pure rules of the embedded ComfyUI workflow editor:
 * where it lives, which upstream calls the Console forwards for it, which
 * requests the sub-application may make, and how its export is checked
 * before it touches a draft. The React host is components/console/comfy-editor.tsx
 * and the forwarding route is app/api/operator/comfy-editor/[...path]/route.ts.
 *
 * 本模块承载嵌入式 ComfyUI 工作流编辑器的纯规则：它挂在哪里、Console 为它转发哪些
 * 上游调用、子应用可以发出哪些请求，以及它的导出在进入草稿之前如何校验。React 宿主在
 * components/console/comfy-editor.tsx，转发路由在
 * app/api/operator/comfy-editor/[...path]/route.ts。
 */

/** COMFY_EDITOR_BASE is the same-origin path the editor is served under. / COMFY_EDITOR_BASE 是编辑器所在的同源路径。 */
export const COMFY_EDITOR_BASE = "/api/operator/comfy-editor/";
/**
 * COMFY_EDITOR_ENTRY is the URL wujie loads. It names index.html explicitly:
 * the frontend derives its API base by dropping the last path segment, and a
 * bare trailing slash would be stripped by Next.js before it got there.
 *
 * COMFY_EDITOR_ENTRY 是 wujie 加载的 URL。它显式写出 index.html：前端去掉路径最后一段
 * 来推导 API 基址，而单纯的结尾斜杠在到达那里之前就会被 Next.js 去掉。
 */
export const COMFY_EDITOR_ENTRY = `${COMFY_EDITOR_BASE}index.html`;
/** COMFY_EDITOR_UPSTREAM is the control-plane prefix, mirroring comfyeditor.Prefix. / COMFY_EDITOR_UPSTREAM 是控制面前缀，与 comfyeditor.Prefix 对齐。 */
export const COMFY_EDITOR_UPSTREAM = "/operator/v1/comfy-editor/";
/** MAX_EDITOR_WRITE_BYTES mirrors comfyeditor.MaxWriteBytes. / MAX_EDITOR_WRITE_BYTES 与 comfyeditor.MaxWriteBytes 对齐。 */
export const MAX_EDITOR_WRITE_BYTES = 1 << 20;

const EDITOR_METHODS = new Set(["GET", "HEAD", "POST", "DELETE"]);
const MAX_SEGMENTS = 32;
const MAX_SEGMENT_CHARS = 256;
const MAX_SEARCH_CHARS = 2048;

/** EditorUpstream is one resolved editor call. / EditorUpstream 是一次已解析的编辑器调用。 */
export interface EditorUpstream { path: string; search: string }

/**
 * resolveEditorUpstream maps a Console editor request onto the control plane,
 * or returns null. Segments are re-encoded one by one, so nothing in the
 * request can leave the editor prefix; what the stub answers is decided
 * there, not here.
 *
 * resolveEditorUpstream 把一次 Console 编辑器请求映射到控制面，否则返回 null。各段逐个
 * 重新编码，请求里的任何内容都无法离开编辑器前缀；桩后端回答什么由那边决定，不在这里。
 */
export function resolveEditorUpstream(method: string, segments: string[], search: string): EditorUpstream | null {
  if (!EDITOR_METHODS.has(method) || segments.length === 0 || segments.length > MAX_SEGMENTS || search.length > MAX_SEARCH_CHARS) return null;
  for (const segment of segments) {
    // A segment may carry a decoded "/" (userdata names are %2F-joined), so
    // dot parts are checked inside it too.
    // 一段里可能带着解码后的 "/"（userdata 名称以 %2F 连接），因此段内的点号部分也要检查。
    if (!segment || segment.length > MAX_SEGMENT_CHARS || /[\u0000-\u001f\u007f\\]/.test(segment) || segment.split("/").some((part) => part === "." || part === "..")) return null;
  }
  return { path: COMFY_EDITOR_UPSTREAM + segments.map(encodeURIComponent).join("/"), search };
}

/**
 * isEditorRequestAllowed is the sub-application's network allow list: only
 * same-origin requests under COMFY_EDITOR_BASE, plus data: and blob: URLs. It is defence in depth — the
 * editor runs same-origin, so it is not a boundary — but it stops the
 * frontend's own calls to registries, telemetry or CDNs from leaving.
 *
 * isEditorRequestAllowed 是子应用的网络允许列表：只放行 COMFY_EDITOR_BASE 之下的同源
 * 请求，以及 data: 与 blob: URL。它是纵深防御——编辑器同源运行，因此它不是边界——但能拦住前端自己对注册表、
 * 遥测或 CDN 的调用。
 */
export function isEditorRequestAllowed(url: string, origin: string): boolean {
  let parsed: URL;
  try { parsed = new URL(url, origin + COMFY_EDITOR_BASE); } catch { return false; }
  // data: and blob: never leave the browser (the frontend loads WASM this way).
  // data: 与 blob: 从不离开浏览器（前端用这种方式加载 WASM）。
  if (parsed.protocol === "data:" || parsed.protocol === "blob:") return true;
  return parsed.origin === origin && parsed.pathname.startsWith(COMFY_EDITOR_BASE) && !parsed.pathname.split("/").includes("..");
}

/** EDITOR_FORWARDED_REQUEST_HEADERS are the only request headers sent upstream. / EDITOR_FORWARDED_REQUEST_HEADERS 是仅有的向上游转发的请求头。 */
export const EDITOR_FORWARDED_REQUEST_HEADERS = ["if-modified-since", "range"] as const;
/** EDITOR_FORWARDED_RESPONSE_HEADERS are the only response headers passed back. / EDITOR_FORWARDED_RESPONSE_HEADERS 是仅有的回传响应头。 */
export const EDITOR_FORWARDED_RESPONSE_HEADERS = ["content-type", "content-length", "cache-control", "last-modified", "content-range", "accept-ranges"] as const;

/**
 * EditorExport is what the bridge's exportGraph produced, checked. missing
 * names node types outside the editor's catalogue: their inputs come out
 * unnamed, so an export with any is never applied to a draft.
 *
 * EditorExport 是桥接 exportGraph 的产出，已校验。missing 列出编辑器目录之外的节点
 * 类型：它们的输入导出时没有名字，因此含有任何这类节点的导出绝不会应用到草稿。
 */
export interface EditorExport { workflow: Record<string, unknown>; graph: Record<string, unknown>; missing: string[] }

function byteLength(text: string): number { return new TextEncoder().encode(text).length; }

/**
 * parseEditorExport checks the bridge's export before it touches a draft: a
 * UI workflow with a nodes array and a non-empty API graph, each within the
 * same bounds the control plane enforces. Node contents are not re-validated
 * here; /validate on the control plane does that once.
 *
 * parseEditorExport 在桥接的导出进入草稿之前校验它：一份带 nodes 数组的 UI 工作流与一张
 * 非空的 API 图，各自在控制面执行的同一套上限之内。节点内容这里不重复校验；控制面的
 * /validate 只做一次。
 */
export function parseEditorExport(text: unknown): EditorExport {
  if (typeof text !== "string") throw new ApiError("invalid");
  let value: unknown;
  try { value = JSON.parse(text); } catch { throw new ApiError("invalid"); }
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("invalid");
  const { workflow, output, missing } = value as { workflow?: unknown; output?: unknown; missing?: unknown };
  if (!Array.isArray(missing) || missing.length > 1000 || !missing.every((t) => typeof t === "string")) throw new ApiError("invalid");
  if (!workflow || typeof workflow !== "object" || Array.isArray(workflow) || !Array.isArray((workflow as { nodes?: unknown }).nodes)) throw new ApiError("invalid");
  if (!output || typeof output !== "object" || Array.isArray(output) || Object.keys(output).length === 0) throw new ApiError("invalid");
  const saved = withoutViewport(workflow as Record<string, unknown>);
  if (byteLength(JSON.stringify(saved)) > MAX_WORKFLOW_BYTES || byteLength(JSON.stringify(output)) > MAX_GRAPH_BYTES) throw new ApiError("invalid");
  return { workflow: saved, graph: output as Record<string, unknown>, missing: missing as string[] };
}

/**
 * withoutViewport drops extra.ds, the canvas pan and zoom at export time. It
 * is where the operator happened to be looking, not part of the layout, and
 * keeping it would turn every export after a scroll into a new digest and a
 * new revision. Loading a workflow without it makes the editor fit the view.
 *
 * withoutViewport 去掉 extra.ds，即导出时画布的平移与缩放。它只是运维当时看向哪里，
 * 不属于布局；保留它会让每次滚动之后的导出都变成新摘要、新版本。载入不带它的工作流时，
 * 编辑器会自动适应视图。
 */
function withoutViewport(workflow: Record<string, unknown>): Record<string, unknown> {
  const extra = workflow.extra;
  if (!extra || typeof extra !== "object" || Array.isArray(extra) || !("ds" in extra)) return workflow;
  const rest = { ...(extra as Record<string, unknown>) };
  delete rest.ds;
  return { ...workflow, extra: rest };
}

/**
 * FAKE_WEBSOCKET_SCRIPT runs in the sub-application before any of its own
 * scripts and replaces WebSocket with a local stand-in that reports an idle
 * queue. The editor never executes anything, so there is no progress stream
 * to carry, and no socket ever leaves the browser.
 *
 * FAKE_WEBSOCKET_SCRIPT 在子应用自己的任何脚本之前运行，用一个汇报「队列空闲」的本地
 * 替身取代 WebSocket。编辑器从不执行任何东西，因此没有进度流需要承载，也不会有任何
 * socket 离开浏览器。
 */
export const FAKE_WEBSOCKET_SCRIPT = `(function(){
  function FakeWS(url){ var self=this; this.url=String(url); this.readyState=0; this.binaryType='blob'; this.protocol=''; this.extensions=''; this.bufferedAmount=0; this._l={};
    setTimeout(function(){ if(self.readyState!==0) return; self.readyState=1; self._emit('open',{});
      self._emit('message',{data:JSON.stringify({type:'status',data:{status:{exec_info:{queue_remaining:0}},sid:'aiserveweave-editor'}})}); },0); }
  FakeWS.CONNECTING=0; FakeWS.OPEN=1; FakeWS.CLOSING=2; FakeWS.CLOSED=3;
  FakeWS.prototype.addEventListener=function(t,f){(this._l[t]=this._l[t]||[]).push(f)};
  FakeWS.prototype.removeEventListener=function(t,f){this._l[t]=(this._l[t]||[]).filter(function(x){return x!==f})};
  FakeWS.prototype._emit=function(t,e){ e.target=this; var h=this['on'+t]; if(h)h.call(this,e); (this._l[t]||[]).slice().forEach(function(f){f.call(this,e)},this); };
  FakeWS.prototype.send=function(d){ var self=this; try{ var m=JSON.parse(d); if(m&&m.type==='feature_flags'){ setTimeout(function(){ self._emit('message',{data:JSON.stringify({type:'feature_flags',data:{}})}) },0) } }catch(_){} };
  FakeWS.prototype.close=function(){ if(this.readyState===3) return; this.readyState=3; this._emit('close',{code:1000,reason:'',wasClean:true}); };
  window.WebSocket = FakeWS;
})();`;

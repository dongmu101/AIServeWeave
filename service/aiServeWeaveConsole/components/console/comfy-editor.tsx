"use client";

import * as React from "react";
import {
  COMFY_EDITOR_ENTRY,
  FAKE_WEBSOCKET_SCRIPT,
  isEditorRequestAllowed,
  parseEditorExport,
  type EditorExport,
} from "@/lib/console/comfy-editor";

/** EditorSource is what to open: a saved UI workflow, or an API-format graph laid out by the editor itself.
 * EditorSource 是要打开的内容：已保存的 UI 工作流，或由编辑器自己排布的 API Format 图。 */
export type EditorSource = { kind: "workflow"; text: string } | { kind: "api"; text: string };

/** ComfyEditorHandle is what the page drives the editor with. / ComfyEditorHandle 是页面驱动编辑器的接口。 */
export interface ComfyEditorHandle {
  load(source: EditorSource): Promise<void>;
  exportGraph(): Promise<EditorExport>;
  toggleAppMode(): Promise<void>;
}

/** Bridge mirrors the string-only API of the control plane's bridge.js. / Bridge 与控制面 bridge.js 只收发字符串的 API 对应。 */
interface Bridge {
  loadWorkflow(text: string): Promise<void>;
  loadApi(text: string): Promise<void>;
  exportGraph(): Promise<string>;
  toggleAppMode(): Promise<void>;
}

const APP_NAME = "aiserveweave-comfy-editor";
// Loading a graph fires the editor's own change events; for this long after a
// programmatic load they are not reported as operator edits.
// 加载一张图会触发编辑器自己的变更事件；程序化加载之后的这段时间内，它们不被当作运维的编辑上报。
const LOAD_QUIET_MS = 1000;

/**
 * ComfyEditor mounts the official ComfyUI_frontend with wujie, same-origin,
 * against the control plane's editor-only stub. The sub-application gets a
 * fake WebSocket and a fetch restricted to the editor prefix; everything it
 * hands back crosses as strings (see bridge.js for why) and is checked by
 * parseEditorExport before the page sees it. Same-origin is an accepted
 * risk, recorded in the Console AGENTS file: the frontend is pinned and
 * served without any third-party extension.
 *
 * ComfyEditor 用 wujie 以同源方式挂载官方 ComfyUI_frontend，后端是控制面的「只编辑」
 * 桩。子应用拿到一个假的 WebSocket 和一个只允许访问编辑器前缀的 fetch；它交回的一切都以
 * 字符串跨越边界（原因见 bridge.js），并在页面看到之前经 parseEditorExport 校验。同源是
 * 一项已接受的风险，记录在 Console 的 AGENTS 文件里：前端版本固定，且不提供任何第三方
 * 扩展。
 */
export function ComfyEditor({ ref, onReady, onEdit, height = 640 }: {
  ref?: React.Ref<ComfyEditorHandle>;
  onReady?: () => void;
  onEdit?: () => void;
  height?: number;
}) {
  const host = React.useRef<HTMLDivElement>(null);
  const bridge = React.useRef<Bridge | null>(null);
  const quietUntil = React.useRef(0);
  const callbacks = React.useRef({ onReady, onEdit });
  const [state, setState] = React.useState<"loading" | "ready" | "error">("loading");

  React.useEffect(() => { callbacks.current = { onReady, onEdit }; });

  React.useEffect(() => {
    let cancelled = false;
    const el = host.current;
    if (!el) return;
    void import("wujie").then(({ startApp }) => {
      if (cancelled) return;
      const origin = window.location.origin;
      return startApp({
        name: APP_NAME,
        url: origin + COMFY_EDITOR_ENTRY,
        el,
        alive: false,
        sync: false,
        props: {
          onReady(value: Bridge) {
            if (cancelled) return;
            bridge.current = value;
            setState("ready");
            callbacks.current.onReady?.();
          },
          onChange() {
            if (performance.now() >= quietUntil.current) callbacks.current.onEdit?.();
          },
        },
        plugins: [{ jsBeforeLoaders: [{ content: FAKE_WEBSOCKET_SCRIPT }] }],
        fetch(input: RequestInfo, init?: RequestInit) {
          // Objects from the sub-application's realm fail instanceof here, so the URL is read structurally.
          // 子应用 realm 的对象在这里过不了 instanceof，因此按结构读取 URL。
          const url = typeof input === "string" ? input : String((input as { url?: unknown }).url ?? input);
          if (!isEditorRequestAllowed(url, origin)) return Promise.reject(new TypeError("blocked by the editor allow list"));
          return window.fetch(input, init);
        },
        loadError() { if (!cancelled) setState("error"); },
      });
    }).catch(() => { if (!cancelled) setState("error"); });
    return () => {
      cancelled = true;
      bridge.current = null;
      void import("wujie").then(({ destroyApp }) => destroyApp(APP_NAME)).catch(() => {});
    };
  }, []);

  React.useImperativeHandle(ref, () => ({
    async load(source) {
      const b = bridge.current;
      if (!b) throw new Error("editor not ready");
      quietUntil.current = performance.now() + LOAD_QUIET_MS;
      if (source.kind === "workflow") await b.loadWorkflow(source.text); else await b.loadApi(source.text);
      quietUntil.current = performance.now() + LOAD_QUIET_MS;
    },
    async exportGraph() {
      const b = bridge.current;
      if (!b) throw new Error("editor not ready");
      return parseEditorExport(await b.exportGraph());
    },
    async toggleAppMode() {
      await bridge.current?.toggleAppMode();
    },
  }), []);

  return <div className="relative overflow-hidden rounded-lg border" style={{ height }}>
    <div ref={host} className="h-full w-full" />
    {state !== "ready" && <div className="absolute inset-0 grid place-items-center bg-background/80 text-sm text-muted-foreground">
      {state === "loading" ? "正在加载 ComfyUI 编辑器…" : "编辑器加载失败：控制面可能未配置 ComfyEditor.DistDir，或会话已过期。"}
    </div>}
  </div>;
}

// SPDX-License-Identifier: Apache-2.0
//
// AIServeWeave bridge extension for the embedded ComfyUI_frontend editor.
//
// It hands the Console host a narrow, string-only API through wujie props.
// Strings, not objects, cross the boundary on purpose: host objects belong to
// another JavaScript realm, and ComfyUI decides "is this input a link?" with
// `instanceof Array`, which is false for a host array — a graph passed by
// reference silently loses every link. Parsing inside this realm avoids that.
//
// 嵌入式 ComfyUI_frontend 编辑器的 AIServeWeave 桥接扩展。
//
// 它通过 wujie props 交给 Console 宿主一个窄小、只收发字符串的 API。跨边界只传
// 字符串而不传对象是刻意的：宿主对象属于另一个 JavaScript realm，而 ComfyUI 用
// `instanceof Array` 判断「这个输入是不是连线」，宿主的数组在这里会被判为否——按引用
// 传进来的图会悄无声息地丢掉全部连线。在本 realm 内解析就避开了这个问题。
import { app } from "../../scripts/app.js";
import { api } from "../../scripts/api.js";

const MAX_DOCUMENT_CHARS = 8 * 1024 * 1024;

function parse(text) {
  if (typeof text !== "string" || text.length > MAX_DOCUMENT_CHARS) throw new Error("document must be a bounded JSON string");
  return JSON.parse(text);
}

app.registerExtension({
  name: "aiserveweave.bridge",
  async setup() {
    const props = (window.$wujie && window.$wujie.props) || {};
    const bridge = Object.freeze({
      /** Load a UI-format workflow JSON string. / 加载 UI 格式工作流 JSON 字符串。 */
      async loadWorkflow(text) {
        await app.loadGraphData(parse(text), true, true, null);
      },
      /** Load an API-format graph JSON string (templates published before the editor existed). / 加载 API Format 图 JSON 字符串。 */
      async loadApi(text) {
        await app.loadApiJson(parse(text), "template.json");
      },
      /**
       * Export {workflow, output, missing} as one JSON string. missing lists
       * node types outside the catalogue: the editor cannot name their
       * inputs, so their values in output are not trustworthy.
       *
       * 以一个 JSON 字符串导出 {workflow, output, missing}。missing 列出目录之外的
       * 节点类型：编辑器叫不出它们的输入名，因此 output 里它们的取值不可信。
       */
      async exportGraph() {
        const p = await app.graphToPrompt();
        const defs = await api.getNodeDefs();
        // A node outside the catalogue is exported without even a class_type,
        // so it is named from the workflow node with the same id.
        // 目录之外的节点导出时连 class_type 都没有，因此按相同 id 的工作流节点取名。
        const types = new Map((p.workflow.nodes || []).map((n) => [String(n.id), n.type]));
        const missing = [...new Set(Object.entries(p.output)
          .filter(([, n]) => typeof n.class_type !== "string" || !Object.hasOwn(defs, n.class_type))
          .map(([id, n]) => String(n.class_type || types.get(id) || (n._meta && n._meta.title) || id)))].sort();
        return JSON.stringify({ workflow: p.workflow, output: p.output, missing });
      },
      setReadOnly(value) {
        app.canvas.read_only = value === true;
        app.canvas.setDirty(true, true);
      },
      async toggleAppMode() {
        await app.extensionManager.command.execute("Comfy.ToggleLinear");
      },
    });
    let pending = 0;
    api.addEventListener("graphChanged", () => {
      if (typeof props.onChange !== "function") return;
      clearTimeout(pending);
      pending = setTimeout(() => props.onChange(), 250);
    });
    if (typeof props.onReady === "function") props.onReady(bridge);
  },
});

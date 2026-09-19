import { ModelPullView } from "./model-pull-view";

/** ModelPullPage lets a platform operator look up one node's model pull
 * status (STATUS.md's P2 model distribution, subtask five) and trigger a
 * pull by name. There is no node listing here: the underlying status
 * endpoint is per-node, and an operator who wants to browse connected nodes
 * first uses the existing fleet page.
 *
 * ModelPullPage 让平台运维查询单个节点的模型拉取状态（STATUS.md 的 P2 模型
 * 分发子任务五）并按名字触发一次拉取。这里没有节点列表：底层状态端点本就
 * 是按节点查询的，想先浏览已连接节点的运维请使用既有的机群页面。 */
export default function ModelPullPage() {
  return <ModelPullView />;
}

import { ComfyUIManagedView } from "./comfyui-managed-view";

/** ComfyUIManagedPage lets a platform operator look up one node's Managed
 * ComfyUI instance (STATUS.md's P2 ComfyUI Managed Docker deployment,
 * subtask five), trigger its start/stop/restart lifecycle, and install a
 * custom node from that node's own local allowlist by name. There is no
 * node listing here: the underlying status endpoint is per-node, and an
 * operator who wants to browse connected nodes first uses the existing
 * fleet page.
 *
 * ComfyUIManagedPage 让平台运维查询单个节点的 Managed ComfyUI 实例状态
 * (STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务五)、触发其
 * start/stop/restart 生命周期，并按名字安装该节点本地允许列表里的一个自定义
 * 节点。这里没有节点列表：底层状态端点本就是按节点查询的，想先浏览已连接节点
 * 的运维请使用既有的机群页面。 */
export default function ComfyUIManagedPage() {
  return <ComfyUIManagedView />;
}

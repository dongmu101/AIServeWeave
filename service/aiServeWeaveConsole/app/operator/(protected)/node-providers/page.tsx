import { NodeProvidersView } from "./node-providers-view";

/** NodeProvidersPage lets a platform operator provision hosted API providers
 * and their keys onto one platform node. Tenant-owned nodes (tnode.) are
 * managed by their tenant at /console/nodes, never here.
 *
 * NodeProvidersPage 让平台运维为一台平台节点下发托管 API 接口与 Key。商户自有节点
 * （tnode.）由商户在 /console/nodes 管理，从不在这里。 */
export default function NodeProvidersPage() {
  return <NodeProvidersView />;
}

import { Suspense } from "react";

import { NodeProvidersView } from "./node-providers-view";

/** NodeProvidersPage lets a platform operator provision hosted API providers
 * and their keys onto one platform node. Tenant-owned nodes (tnode.) are
 * managed by their tenant at /console/nodes, never here. The view reads the
 * ?node= preselection, so it sits behind a Suspense boundary.
 *
 * NodeProvidersPage 让平台运维为一台平台节点下发托管 API 接口与 Key。商户自有节点
 * （tnode.）由商户在 /console/nodes 管理，从不在这里。视图读取 ?node= 预选参数，
 * 因此放在 Suspense 边界内。 */
export default function NodeProvidersPage() {
  return <Suspense fallback={null}><NodeProvidersView /></Suspense>;
}

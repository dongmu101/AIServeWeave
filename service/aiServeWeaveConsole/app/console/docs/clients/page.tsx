import { ClientsGuide } from "@/app/console/docs/clients/clients-guide";

/** ClientsPage explains how common clients and SDKs connect to the Gateway.
 * A static segment, so it takes precedence over the [slug] endpoint pages.
 *
 * ClientsPage 说明常见客户端与 SDK 如何接入网关。它是静态路由段，优先于 [slug] 端点页面。 */
export default function ClientsPage() {
  return <ClientsGuide />;
}

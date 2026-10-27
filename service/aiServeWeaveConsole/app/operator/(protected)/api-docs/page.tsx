import { ApiDocsAdminView } from "./api-docs-view";
import { publicGatewayUrl } from "@/lib/server/config";

/** ApiDocsAdminPage manages the tenant-facing API reference. / ApiDocsAdminPage 管理面向租户的 API 参考文档。 */
export default function ApiDocsAdminPage() {
  return <ApiDocsAdminView baseUrl={publicGatewayUrl()} />;
}

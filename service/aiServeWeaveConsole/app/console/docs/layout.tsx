import type * as React from "react";

import { DocsShell } from "@/app/console/docs/docs-shell";
import { publicGatewayUrl } from "@/lib/server/config";

/**
 * The API reference frame. The navigation lives in this layout so moving
 * between pages keeps the list instead of re-reading it, and the base URL is
 * resolved here on the server — the browser never learns any Console
 * configuration beyond the one public address the examples print.
 *
 * API 参考文档的框架。导航放在本布局里，使页面之间跳转时保留列表而不是重新读取；
 * 基地址在服务端于此处解析——浏览器除了示例中打印的那一个公开地址外，得不到任何
 * Console 配置。
 */
export default function DocsLayout({ children }: { children: React.ReactNode }) {
  return <DocsShell baseUrl={publicGatewayUrl()}>{children}</DocsShell>;
}

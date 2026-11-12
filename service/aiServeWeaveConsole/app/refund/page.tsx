import { LegalPage, legalMetadata } from "@/components/site/legal-page";

/** The public refund page; it needs no session. / 公开的 refund 页面，不需要会话。 */
export function generateMetadata() {
  return legalMetadata("refund");
}

export default function RefundPage() {
  return <LegalPage kind="refund" />;
}

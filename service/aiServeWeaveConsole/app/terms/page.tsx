import { LegalPage, legalMetadata } from "@/components/site/legal-page";

/** The public terms page; it needs no session. / 公开的 terms 页面，不需要会话。 */
export function generateMetadata() {
  return legalMetadata("terms");
}

export default function TermsPage() {
  return <LegalPage kind="terms" />;
}

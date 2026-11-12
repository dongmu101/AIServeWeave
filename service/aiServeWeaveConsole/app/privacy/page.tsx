import { LegalPage, legalMetadata } from "@/components/site/legal-page";

/** The public privacy page; it needs no session. / 公开的 privacy 页面，不需要会话。 */
export function generateMetadata() {
  return legalMetadata("privacy");
}

export default function PrivacyPage() {
  return <LegalPage kind="privacy" />;
}

"use client";

import * as React from "react";
import Link from "next/link";
import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import { ErrorMessage } from "@/components/i18n/error-message";
import { useResource } from "@/components/console/use-resource";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { ErrorState, FormError, LoadingState } from "@/components/console/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { parseEnrollmentCA, parseEnrollmentBundle, type EnrollmentBundle } from "@/lib/console/agent-enrollment";
import { formatDateTime } from "@/lib/console/format";

/** AgentEnrollmentView keeps one-time credentials out of URLs and persistent storage.
 * AgentEnrollmentView 防止一次性凭据进入 URL 和持久化存储。 */
export function AgentEnrollmentView() {
  const { t, locale } = useI18n();
  const ca = useResource({ method: "GET", surface: "operator", path: "/operator/v1/agent-enrollment", parse: parseEnrollmentCA });
  const run = useConsoleRequest();
  const [registry, setRegistry] = React.useState("");
  const [gateway, setGateway] = React.useState("");
  const [ttl, setTTL] = React.useState("60");
  const [pending, setPending] = React.useState(false);
  const inFlight = React.useRef(false);
  const [error, setError] = React.useState<unknown>(null);
  const [bundle, setBundle] = React.useState<EnrollmentBundle | null>(null);

  async function generate(event: React.FormEvent) {
    event.preventDefault();
    if (inFlight.current) return;
    inFlight.current = true;
    setPending(true); setError(null); setBundle(null);
    try {
      setBundle(await run({ method: "POST", surface: "operator", path: "/operator/v1/agent-enrollment", body: { registry_addr: registry.trim(), gateway_addr: gateway.trim(), ttl_minutes: Number(ttl) }, parse: parseEnrollmentBundle }));
    } catch (failure) { setError(failure); }
    finally { inFlight.current = false; setPending(false); }
  }

  return <div className="grid max-w-3xl gap-6">
    <div><h1 className="font-heading text-lg font-semibold"><Message id="operator.agentEnrollment" /></h1><p className="mt-2 text-sm text-muted-foreground"><Message id="operator.enrollmentIntro" /></p></div>
    <section className="grid gap-3 rounded-xl border p-4" aria-labelledby="ca-title">
      <h2 id="ca-title" className="font-semibold"><Message id="operator.enrollmentCA" /></h2>
      {ca.error ? <ErrorState message={ca.error} onRetry={ca.reload} /> : ca.loading ? <LoadingState /> : <>
        <p className="text-sm text-muted-foreground"><Message id="operator.enrollmentCANote" /></p>
        <code className="break-all text-xs">SHA256: {ca.data?.sha256}</code>
        <Button variant="outline" className="w-fit" disabled={!ca.data} onClick={() => { if (ca.data) downloadFile(new Blob([ca.data.pem], { type: "application/x-pem-file" }), "ca.pem"); }}><Message id="operator.enrollmentDownloadCA" /></Button>
      </>}
    </section>
    <form onSubmit={generate} className="grid gap-4 rounded-xl border p-4" aria-labelledby="bundle-title">
      <h2 id="bundle-title" className="font-semibold"><Message id="operator.enrollmentBundle" /></h2>
      <p className="text-sm text-muted-foreground"><Message id="operator.enrollmentAddressesNote" /></p>
      <label className="grid gap-2 text-sm" htmlFor="enrollment-registry"><Message id="operator.enrollmentRegistry" /><Input id="enrollment-registry" required maxLength={253} value={registry} onChange={e => setRegistry(e.target.value)} placeholder="registry.open7x.com:9443" disabled={pending} /></label>
      <label className="grid gap-2 text-sm" htmlFor="enrollment-gateway"><Message id="operator.enrollmentGateway" /><Input id="enrollment-gateway" required maxLength={253} value={gateway} onChange={e => setGateway(e.target.value)} placeholder="agent.open7x.com:8443" disabled={pending} /></label>
      <label className="grid gap-2 text-sm" htmlFor="enrollment-ttl"><Message id="operator.enrollmentTTL" /><select id="enrollment-ttl" className="h-9 rounded-md border bg-background px-3" value={ttl} onChange={e => setTTL(e.target.value)} disabled={pending}><option value="15">{t("operator.enrollment15Minutes")}</option><option value="60">{t("operator.enrollment1Hour")}</option><option value="1440">{t("operator.enrollment24Hours")}</option></select></label>
      <p className="text-sm text-muted-foreground"><Message id="operator.enrollmentContents" /></p>
      <Button type="submit" disabled={pending || !ca.data} aria-busy={pending} className="w-fit"><Message id={pending ? "operator.enrollmentGenerating" : "operator.enrollmentGenerate"} /></Button>
      {error ? <><FormError message={<ErrorMessage error={error} />} /><p className="text-sm text-muted-foreground"><Message id="operator.enrollmentUncertain" /></p></> : null}
    </form>
    {bundle ? <section className="grid gap-3 rounded-xl border p-4" aria-live="polite">
      <p className="font-semibold"><Message id="operator.enrollmentReady" /></p>
      <code className="break-all text-sm">{bundle.nodeId}</code>
      <p className="text-sm"><Message id="operator.enrollmentExpires" values={{ value: formatDateTime(bundle.expiresAt, undefined, locale) }} /></p>
      <Button className="w-fit" onClick={() => downloadFile(new Blob([bundle.bytes], { type: "application/zip" }), bundle.filename)}><Message id="operator.enrollmentDownloadBundle" /></Button>
      <p className="text-sm text-muted-foreground"><Message id="operator.enrollmentPrivate" /></p>
    </section> : null}
    <Link href="/operator/fleet" className="text-sm underline"><Message id="operator.enrollmentFleet" /></Link>
  </div>;
}

function downloadFile(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url; anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click(); anchor.remove();
  // Release after the browser has begun consuming the download. / 浏览器开始读取下载后释放。
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

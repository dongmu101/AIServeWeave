"use client";

import * as React from "react";

import { ErrorMessage } from "@/components/i18n/error-message";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { FormError } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { Button } from "@/components/ui/button";
import {
  buildInstallCommand,
  credentialFor,
  INSTALL_METHODS,
  INSTALL_OSES,
  INSTALL_SOURCES,
  type InstallIssue,
  type InstallMethod,
  type InstallOS,
  type InstallSource,
} from "@/lib/console/agent-install";
import { ApiError } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";
import type { MessageKey } from "@/lib/i18n/messages";

const OS_LABEL: Record<InstallOS, MessageKey> = { ubuntu: "install.osUbuntu", macos: "install.osMacos", windows: "install.osWindows" };
const OS_HINT: Record<InstallOS, MessageKey> = { ubuntu: "install.hintUbuntu", macos: "install.hintMacos", windows: "install.hintWindows" };
const METHOD_LABEL: Record<InstallMethod, MessageKey> = { code: "install.methodCode", interactive: "install.methodInteractive", token: "install.methodToken" };
const SOURCE_LABEL: Record<InstallSource, MessageKey> = { platform: "install.sourcePlatform", github: "install.sourceGithub" };

/**
 * AgentInstallPanel issues a one-time install credential and shows the one-line
 * command for it. The credential lives only in this component's memory: it is
 * requested once, never retried (a second request would issue a second
 * credential), and gone when the page is left.
 *
 * AgentInstallPanel 签发一次性安装凭据并显示对应的一行命令。凭据只保存在本组件的内存中：
 * 只请求一次、从不重试（第二次请求会签发第二份凭据），离开页面即丢失。
 */
export function AgentInstallPanel({ surface, path, parse, extraBody, onIssued, children }: {
  surface?: "operator";
  path: string;
  parse: (value: unknown) => InstallIssue;
  /** Extra request fields; null keeps the form from being submitted. / 额外的请求字段；为 null 时不允许提交。 */
  extraBody: Record<string, string> | null;
  /** Called after a credential was issued, e.g. to refresh a node list. / 凭据签发后调用，例如刷新节点列表。 */
  onIssued?: () => void;
  children?: React.ReactNode;
}) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const [os, setOS] = React.useState<InstallOS>("ubuntu");
  const [method, setMethod] = React.useState<InstallMethod>("code");
  const [source, setSource] = React.useState<InstallSource>("platform");
  const [ttl, setTTL] = React.useState("60");
  const [pending, setPending] = React.useState(false);
  const inFlight = React.useRef(false);
  const [error, setError] = React.useState<unknown>(null);
  const [issue, setIssue] = React.useState<InstallIssue | null>(null);
  const [copied, setCopied] = React.useState(false);

  async function generate(event: React.FormEvent) {
    event.preventDefault();
    if (inFlight.current || extraBody === null) return;
    inFlight.current = true;
    setPending(true); setError(null); setIssue(null); setCopied(false);
    try {
      setIssue(await run({ method: "POST", surface, path, body: { ...extraBody, ttl_minutes: Number(ttl), credential: credentialFor(method) }, parse }));
      onIssued?.();
    } catch (failure) { setError(failure); }
    finally { inFlight.current = false; setPending(false); }
  }

  const command = issue ? buildInstallCommand(issue, { os, method, source }) : null;

  async function copy() {
    if (!command) return;
    try { await navigator.clipboard.writeText(command); setCopied(true); } catch { setCopied(false); }
  }

  const select = (id: string, value: string, onChange: (value: string) => void, options: ReadonlyArray<readonly [string, MessageKey]>) =>
    <select id={id} className="h-9 w-fit rounded-md border bg-background px-3" value={value} onChange={(e) => { onChange(e.target.value); setCopied(false); }} disabled={pending}>
      {options.map(([optionValue, label]) => <option key={optionValue} value={optionValue}>{t(label)}</option>)}
    </select>;

  const disabled = error instanceof ApiError && error.kind === "not_found";
  return <section className="grid gap-4 rounded-xl border p-4" aria-labelledby="install-title">
    <h2 id="install-title" className="font-semibold"><Message id="install.title" /></h2>
    <p className="text-sm text-muted-foreground"><Message id="install.intro" /></p>
    {children}
    <form onSubmit={generate} className="grid gap-4">
      <div className="flex flex-wrap gap-4">
        <label className="grid gap-2 text-sm" htmlFor="install-os"><Message id="install.os" />{select("install-os", os, (v) => setOS(v as InstallOS), INSTALL_OSES.map((v) => [v, OS_LABEL[v]] as const))}</label>
        <label className="grid gap-2 text-sm" htmlFor="install-method"><Message id="install.method" />{select("install-method", method, (v) => setMethod(v as InstallMethod), INSTALL_METHODS.map((v) => [v, METHOD_LABEL[v]] as const))}</label>
        <label className="grid gap-2 text-sm" htmlFor="install-source"><Message id="install.source" />{select("install-source", source, (v) => setSource(v as InstallSource), INSTALL_SOURCES.map((v) => [v, SOURCE_LABEL[v]] as const))}</label>
        <label className="grid gap-2 text-sm" htmlFor="install-ttl"><Message id="operator.enrollmentTTL" />{select("install-ttl", ttl, setTTL, [["15", "operator.enrollment15Minutes"], ["60", "operator.enrollment1Hour"], ["1440", "operator.enrollment24Hours"]])}</label>
      </div>
      <p className="text-sm text-muted-foreground"><Message id={OS_HINT[os]} /></p>
      {source === "github" ? <p className="text-sm text-muted-foreground"><Message id="install.githubNote" /></p> : null}
      <Button type="submit" disabled={pending || extraBody === null} aria-busy={pending} className="w-fit"><Message id={pending ? "operator.enrollmentGenerating" : "install.generate"} /></Button>
      {disabled ? <p className="text-sm text-muted-foreground"><Message id="install.notEnabled" /></p>
        : error ? <><FormError message={<ErrorMessage error={error} />} /><p className="text-sm text-muted-foreground"><Message id="operator.enrollmentUncertain" /></p></> : null}
    </form>
    {issue ? <div className="grid gap-3" aria-live="polite">
      <p className="font-semibold"><Message id="install.ready" /></p>
      <code className="break-all text-sm">{issue.nodeId}</code>
      <p className="text-sm"><Message id="install.expires" values={{ value: formatDateTime(issue.expiresAt, undefined, locale) }} /></p>
      {command ? <>
        <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-md border bg-muted p-3 text-xs" tabIndex={0}>{command}</pre>
        <Button variant="outline" className="w-fit" onClick={copy}><Message id={copied ? "install.copied" : "install.copy"} /></Button>
        {method === "interactive" && issue.code ? <div className="grid gap-1 text-sm">
          <p><Message id="install.interactiveCode" /></p>
          <code className="w-fit select-all rounded-md border bg-muted px-3 py-2 text-base tracking-wider">{issue.code}</code>
        </div> : null}
        <p className="text-sm text-muted-foreground"><Message id={method === "token" ? "install.tokenSecret" : "install.codeSecret"} /></p>
      </> : <p className="text-sm text-muted-foreground"><Message id="install.regenerate" /></p>}
    </div> : null}
  </section>;
}

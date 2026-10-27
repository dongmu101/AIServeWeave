"use client";
import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import { toast } from "sonner";

import { Pager } from "@/components/console/pager";
import { ErrorState, FormError, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { parseTenants, type Tenant } from "@/lib/console/contract";
import { formatDateTime } from "@/lib/console/format";
import { parseModelAccess, parseModelLines, PLATFORM_SCOPE, type ModelAccess } from "@/lib/console/model-access";
import { parseRouteSnapshot } from "@/lib/console/model-routes";

const PAGE_SIZE = 25;

/** ModelAccessView edits the platform list and one tenant's list at a time. / ModelAccessView 编辑平台列表，并逐个编辑商户列表。 */
export function ModelAccessView() {
  const { t } = useI18n();
  const [query, setQuery] = React.useState("");
  const [selected, setSelected] = React.useState<Tenant | null>(null);
  const routes = useResource({ method: "GET", surface: "operator", path: "/operator/v1/routes", parse: parseRouteSnapshot });
  const tenants = usePagedResource<Tenant>({ path: "/operator/v1/tenants", surface: "operator", filters: { q: query }, pageSize: PAGE_SIZE, parse: parseTenants });
  const aliases = React.useMemo(() => (routes.data?.routes ?? []).map((route) => route.model), [routes.data]);

  return <div className="grid gap-4">
    <div><h1 className="font-heading text-lg font-semibold"><Message id="operator.modelAccess" /></h1><p className="text-sm text-muted-foreground"><Message id="operator.modelAccessDescription" /></p></div>
    <AccessEditor key={PLATFORM_SCOPE} scope={PLATFORM_SCOPE} title={t("operator.platformModelAccess")} aliases={aliases} />
    <Card><CardHeader><CardTitle><Message id="operator.tenantModelAccess" /></CardTitle></CardHeader><CardContent className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
      <div className="grid content-start gap-3">
        <Input aria-label={t("operator.searchTenants")} placeholder={t("operator.searchTenants")} value={query} onChange={(e) => setQuery(e.target.value)} />
        {tenants.error ? <ErrorState message={tenants.error} onRetry={tenants.reload} /> : tenants.loading ? <LoadingState label={t("operator.loadingTenants")} rows={3} /> : <>
          {(tenants.items ?? []).length === 0 ? <p className="text-sm text-muted-foreground"><Message id="operator.noTenants" /></p> : <ul className="grid gap-2">{(tenants.items ?? []).map((tenant) => <li key={tenant.id}>
            <Button variant={selected?.id === tenant.id ? "secondary" : "ghost"} className="h-auto w-full justify-start whitespace-normal text-left" aria-current={selected?.id === tenant.id ? "true" : undefined} onClick={() => setSelected(tenant)}>
              <span className="grid min-w-0 gap-0.5"><span className="break-all">{tenant.name || tenant.id}</span><code className="break-all text-xs text-muted-foreground">{tenant.id}</code></span>
              {tenant.status !== "active" ? <Badge variant="destructive" className="ml-auto">{tenant.status}</Badge> : null}
            </Button>
          </li>)}</ul>}
          <Pager resource={tenants} loadedCount={tenants.items?.length ?? 0} />
        </>}
      </div>
      {selected ? <AccessEditor key={selected.id} scope={selected.id} title={t("operator.selectedTenant", { name: selected.name || selected.id, id: selected.id })} aliases={aliases} /> : <p className="text-sm text-muted-foreground"><Message id="operator.selectATenantFirst" /></p>}
    </CardContent></Card>
  </div>;
}

/** AccessEditor loads, edits and replaces one scope's list. / AccessEditor 加载、编辑并整体替换一个 scope 的列表。 */
function AccessEditor({ scope, title, aliases }: { scope: string; title: string; aliases: string[] }) {
  const { t, locale } = useI18n();
  const run = useConsoleRequest();
  const path = `/operator/v1/model-access/${encodeURIComponent(scope)}`;
  const current = useResource({ method: "GET", surface: "operator", path, parse: parseModelAccess });
  const [saved, setSaved] = React.useState<ModelAccess | null>(null);
  const [draft, setDraft] = React.useState<string | null>(null);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const shown = saved ?? current.data;
  const text = draft ?? shown?.models.join("\n") ?? "";
  const lines = new Set(text.split(/\r?\n/).map((line) => line.trim()).filter(Boolean));

  async function save() {
    setError(null);
    let models: string[];
    try { models = parseModelLines(text); } catch (failure) { setError(<ErrorMessage error={failure} />); return; }
    if (models.length === 0 && (shown?.models.length ?? 0) > 0 && !window.confirm(t("operator.clearModelAccessConfirm"))) return;
    setPending(true);
    try {
      const result = await run({ method: "PUT", surface: "operator", path, body: { models }, parse: parseModelAccess });
      setSaved(result); setDraft(null); toast.success(<Message id="operator.modelAccessSaved" />);
    } catch (failure) { setError(<ErrorMessage error={failure} />); }
    finally { setPending(false); }
  }

  return <Card><CardHeader><CardTitle className="flex flex-wrap items-center gap-2 text-base"><span className="break-all">{title}</span>{shown ? <Badge variant="outline">{shown.models.length === 0 ? t("operator.unrestricted") : t("operator.restrictedCount", { count: shown.models.length })}</Badge> : null}</CardTitle></CardHeader><CardContent className="grid gap-3">
    {current.error && !saved ? <ErrorState message={current.error} onRetry={current.reload} /> : !shown ? <LoadingState label={t("operator.loading")} rows={2} /> : <>
      {shown.updatedAt ? <p className="text-xs text-muted-foreground"><Message id="operator.lastUpdated" values={{ actor: shown.updatedBy ?? "—", time: formatDateTime(shown.updatedAt, undefined, locale) }} /></p> : null}
      <label className="grid gap-1 text-sm"><span><Message id="operator.modelListHint" /></span>
        <textarea className="min-h-40 w-full rounded-md border bg-transparent p-2 font-mono text-sm" spellCheck={false} value={text} disabled={pending} onChange={(e) => setDraft(e.target.value)} />
      </label>
      <div className="grid gap-1 text-sm"><span className="text-muted-foreground">{aliases.length > 0 ? t("operator.knownAliases") : t("operator.noPublishedAliases")}</span>
        <div className="flex flex-wrap gap-1">{aliases.map((alias) => <Button key={alias} size="sm" variant={lines.has(alias) ? "secondary" : "outline"} disabled={pending || lines.has(alias)} onClick={() => setDraft(text.trim() === "" ? alias : `${text.replace(/\s+$/, "")}\n${alias}`)}>{alias}</Button>)}</div>
      </div>
      <div className="flex flex-wrap gap-2"><Button disabled={pending || draft === null} onClick={save}><Message id="operator.saveModelAccess" /></Button><Button variant="outline" disabled={pending || draft === null} onClick={() => { setDraft(null); setError(null); }}><Message id="operator.discardModelAccessChanges" /></Button></div>
      <FormError message={error} />
    </>}
  </CardContent></Card>;
}

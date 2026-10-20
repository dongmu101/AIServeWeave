"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import { localizeApiDocSummary } from "@/lib/i18n/docs/localize";

import * as React from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "cn";

import { MethodBadge } from "@/components/console/api-doc-article";
import { ErrorState, LoadingState } from "@/components/console/states";
import { useResource, type Resource } from "@/components/console/use-resource";
import { Input } from "@/components/ui/input";
import { groupApiDocs, parseApiDocList, type ApiDocSummary, type ExampleValues } from "@/lib/console/api-docs";
import { matchesQuery } from "@/lib/console/format";

/** EXAMPLE_KEY stands in for a key in every example. The page never holds a
 * real one. / EXAMPLE_KEY 是所有示例中 Key 的替身，页面从不持有真实的 Key。 */
const EXAMPLE_KEY = "YOUR_API_KEY";
const DEFAULT_MODEL = "your-model";

interface DocsContextValue {
  list: Resource<ApiDocSummary[]>;
  values: ExampleValues;
  model: string;
  setModel: (model: string) => void;
}

const DocsContext = React.createContext<DocsContextValue | null>(null);

/** useDocs reads the reference's shared state. / useDocs 读取参考文档的共享状态。 */
export function useDocs(): DocsContextValue {
  const value = React.useContext(DocsContext);
  if (!value) throw new Error("useDocs must be used inside DocsShell");
  return value;
}

/**
 * DocsShell holds the navigation list and the example values every page
 * substitutes, and renders the left-hand navigation.
 *
 * DocsShell 持有导航列表与每个页面替换示例所用的值，并渲染左侧导航。
 */
export function DocsShell({ baseUrl, children }: { baseUrl: string; children: React.ReactNode }) {
  const { locale, messages } = useI18n();
  const rawList = useResource<ApiDocSummary[]>({ method: "GET", path: "/admin/v1/api-docs", parse: parseApiDocList });
  const list = React.useMemo(() => ({ ...rawList, data: rawList.data?.map(doc => localizeApiDocSummary(doc, locale, messages)) ?? null }), [rawList, locale, messages]);
  const [model, setModel] = React.useState(DEFAULT_MODEL);
  const values = React.useMemo<ExampleValues>(
    () => ({ baseUrl, apiKey: EXAMPLE_KEY, model: model.trim() || DEFAULT_MODEL }),
    [baseUrl, model]
  );
  const context = React.useMemo(() => ({ list, values, model, setModel }), [list, values, model, setModel]);

  return (
    <DocsContext.Provider value={context}>
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[14rem_minmax(0,1fr)] lg:gap-8">
        <DocsNav list={list} />
        <div className="min-w-0">{children}</div>
      </div>
    </DocsContext.Provider>
  );
}

function DocsNav({ list }: { list: Resource<ApiDocSummary[]> }) {
  const {t } = useI18n();
  const pathname = usePathname();
  const [query, setQuery] = React.useState("");
  const groups = React.useMemo(
    () => groupApiDocs((list.data ?? []).filter((doc) => matchesQuery([doc.title, doc.path, doc.category, doc.slug], query))),
    [list.data, query]
  );

  const body = list.error ? (
    <ErrorState message={list.error} onRetry={list.reload} />
  ) : list.loading && !list.data ? (
    <LoadingState label={t("tenant.loadingDocumentationIndex")} rows={6} />
  ) : (
    <nav aria-label={t("tenant.apiDocumentationIndex")} className="flex flex-col gap-4">
      <Link
        href="/console/docs"
        aria-current={pathname === "/console/docs" ? "page" : undefined}
        className={cn(
          "rounded-md px-2 py-1.5 text-sm",
          pathname === "/console/docs" ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground"
        )}
      >
        <Message id="tenant.quickStart" /></Link>
      {groups.map((group) => (
        <div key={group.category} className="flex flex-col gap-0.5">
          <p className="px-2 pb-1 text-xs font-semibold text-foreground">{group.category}</p>
          {group.items.map((doc) => {
            const href = `/console/docs/${doc.slug}`;
            const active = pathname === href;
            return (
              <Link
                key={doc.slug}
                href={href}
                aria-current={active ? "page" : undefined}
                className={cn(
                  "flex items-center gap-2 rounded-md px-2 py-1.5 text-sm",
                  active ? "bg-primary/10 font-medium text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground"
                )}
              >
                <MethodBadge method={doc.method} className="w-12 justify-center" />
                <span className="min-w-0 truncate">{doc.title}</span>
              </Link>
            );
          })}
        </div>
      ))}
      {groups.length === 0 ? <p className="px-2 text-sm text-muted-foreground"><Message id="tenant.noMatchingDocumentation" /></p> : null}
    </nav>
  );

  return (
    <aside className="min-w-0 lg:sticky lg:top-4 lg:max-h-[calc(100vh-2rem)] lg:self-start lg:overflow-y-auto">
      <details className="group rounded-xl border p-3 lg:border-0 lg:p-0" open>
        <summary className="cursor-pointer text-sm font-medium select-none lg:hidden"><Message id="tenant.documentationIndex" /></summary>
        <div className="mt-3 flex flex-col gap-3 lg:mt-0">
          <Input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("tenant.searchApis")}
            aria-label={t("tenant.searchApis")}
          />
          {body}
        </div>
      </details>
    </aside>
  );
}

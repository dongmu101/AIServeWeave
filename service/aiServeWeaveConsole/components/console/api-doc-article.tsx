"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import Link from "next/link";
import { CheckIcon, CopyIcon, InfoIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import {
  fillPlaceholders,
  type ApiDocCodeExample,
  type ApiDocContent,
  type ApiDocParam,
  type ApiDocResponseExample,
  type ExampleValues,
} from "@/lib/console/api-docs";

/**
 * The API reference's rendering pieces, shared by the tenant pages and the
 * operator editor's preview so an operator sees exactly what tenants will.
 * Everything here renders text as text: page content is stored data, and no
 * part of it is ever interpreted as HTML.
 *
 * API 参考文档的渲染部件，由租户页面与运维编辑器的预览共用，使运维看到的正是租户将
 * 看到的。这里的一切都把文本当文本渲染：页面正文是存储的数据，任何部分都不会被当作
 * HTML 解释。
 */

const METHOD_STYLES: Record<string, string> = {
  GET: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  POST: "bg-blue-500/10 text-blue-700 dark:text-blue-400",
  PUT: "bg-amber-500/10 text-amber-700 dark:text-amber-400",
  PATCH: "bg-violet-500/10 text-violet-700 dark:text-violet-400",
  DELETE: "bg-red-500/10 text-red-700 dark:text-red-400",
};

/** MethodBadge shows an HTTP method in its conventional colour.
 * MethodBadge 以惯用配色展示 HTTP 方法。 */
export function MethodBadge({ method, className }: { method: string; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded px-1.5 py-0.5 font-mono text-[11px] font-semibold leading-none",
        METHOD_STYLES[method] ?? "bg-muted text-muted-foreground",
        className
      )}
    >
      {method}
    </span>
  );
}

/** CopyButton copies text to the clipboard and confirms briefly.
 * CopyButton 把文本复制到剪贴板并短暂确认。 */
export function CopyButton({ text, label: inputLabel }: { text: string; label?: string }) {
  const {t } = useI18n();
  const label = inputLabel ?? t("common.copy");
  const [copied, setCopied] = React.useState(false);
  React.useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
        } catch {
          toast.error(<Message id={"common.copyFailedSelectTheTextManually"} />);
        }
      }}
    >
      {copied ? <CheckIcon className="size-4" /> : <CopyIcon className="size-4" />}
    </button>
  );
}

/** EndpointBar shows the method and the full URL of an endpoint.
 * EndpointBar 展示端点的方法与完整 URL。 */
export function EndpointBar({ method, path, baseUrl }: { method: string; path: string; baseUrl: string }) {
  const {t } = useI18n();
  const url = `${baseUrl}${path}`;
  return (
    <div className="flex min-w-0 items-center gap-2 rounded-xl border bg-muted/30 px-3 py-2">
      <MethodBadge method={method} className="text-xs" />
      <code className="min-w-0 flex-1 truncate font-mono text-sm" title={url}>
        <span className="text-muted-foreground">{baseUrl}</span>
        {path}
      </code>
      <CopyButton text={url} label={t("common.copyAddress")} />
    </div>
  );
}

function ParamItem({ param }: { param: ApiDocParam }) {
  return (
    <div className="py-3">
      <div className="flex flex-wrap items-center gap-2">
        <code className="font-mono text-sm font-semibold text-primary">{param.name}</code>
        <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">{param.type}</span>
        {param.required ? (
          <span className="rounded bg-destructive/10 px-1.5 py-0.5 text-xs text-destructive"><Message id="tenant.required" /></span>
        ) : null}
        {param.default ? (
          <span className="text-xs text-muted-foreground"><Message id="common.default" values={{ code: <code className="font-mono">{param.default}</code> }} /></span>
        ) : null}
      </div>
      {param.description ? (
        <p className="mt-1 whitespace-pre-line text-sm text-muted-foreground">{param.description}</p>
      ) : null}
      {param.enum ? (
        <div className="mt-1.5 flex flex-wrap items-center gap-1 text-xs text-muted-foreground"><Message id="common.allowedValues_06b8d9" values={{ value: param.enum.map((value) => (
            <code key={value} className="rounded border px-1.5 py-0.5 font-mono text-foreground">
              {value}
            </code>
          )) }} /></div>
      ) : null}
      {param.children ? (
        <details className="mt-2 rounded-lg border px-3">
          <summary className="cursor-pointer py-2 text-xs text-muted-foreground select-none"><Message id="common.childFields_832fa2" values={{ count: param.children.length }} /></summary>
          <ParamList params={param.children} />
        </details>
      ) : null}
    </div>
  );
}

/** ParamList renders a parameter table, nesting children in disclosures.
 * ParamList 渲染参数表，子字段放在可展开区域里。 */
export function ParamList({ params }: { params: readonly ApiDocParam[] }) {
  return (
    <div className="divide-y">
      {params.map((param, index) => (
        <ParamItem key={`${param.name}-${index}`} param={param} />
      ))}
    </div>
  );
}

function DocSection({ title, aside, children }: { title: string; aside?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center gap-2 border-b pb-2">
        <h2 className="font-heading text-lg font-semibold">{title}</h2>
        {aside}
      </div>
      {children}
    </section>
  );
}

function CodeBlock({ code, lang }: { code: string; lang?: string }) {
  return (
    <div className="relative min-w-0 rounded-lg border bg-muted/40">
      <div className="absolute top-1.5 right-1.5">
        <CopyButton text={code} />
      </div>
      <pre className="overflow-x-auto p-3 pr-10 font-mono text-xs leading-relaxed" data-lang={lang}>
        <code>{code}</code>
      </pre>
    </div>
  );
}

/** ExamplePanel is a tabbed code panel: request samples by language, or
 * response samples by status.
 *
 * ExamplePanel 是带标签页的代码面板：请求示例按语言分页，响应示例按状态码分页。 */
export function ExamplePanel({
  title,
  tabs,
}: {
  title: string;
  tabs: readonly { key: string; label: string; code: string; tone?: "ok" | "error" }[];
}) {
  const [active, setActive] = React.useState(0);
  const id = React.useId();
  if (tabs.length === 0) return null;
  const current = tabs[Math.min(active, tabs.length - 1)];
  return (
    <div className="min-w-0 overflow-hidden rounded-xl border bg-card">
      <div className="flex items-center gap-1 border-b px-2">
        <span className="sr-only">{title}</span>
        <div role="tablist" aria-label={title} className="flex min-w-0 flex-1 gap-1 overflow-x-auto">
          {tabs.map((tab, index) => {
            const selected = index === active;
            return (
              <button
                key={tab.key}
                type="button"
                role="tab"
                id={`${id}-tab-${index}`}
                aria-selected={selected}
                aria-controls={`${id}-panel`}
                onClick={() => setActive(index)}
                className={cn(
                  "shrink-0 border-b-2 px-2 py-2 font-mono text-xs",
                  selected ? "border-primary text-primary" : "border-transparent text-muted-foreground hover:text-foreground",
                  tab.tone === "error" && !selected && "text-destructive/80"
                )}
              >
                {tab.label}
              </button>
            );
          })}
        </div>
        <CopyButton text={current.code} />
      </div>
      <pre
        id={`${id}-panel`}
        role="tabpanel"
        aria-labelledby={`${id}-tab-${active}`}
        className="overflow-x-auto p-3 font-mono text-xs leading-relaxed"
      >
        <code>{current.code}</code>
      </pre>
    </div>
  );
}

function requestTabs(examples: readonly ApiDocCodeExample[], values: ExampleValues) {
  return examples.map((ex, index) => ({ key: `${ex.lang}-${index}`, label: ex.label, code: fillPlaceholders(ex.code, values) }));
}

function responseTabs(examples: readonly ApiDocResponseExample[], values: ExampleValues) {
  // A status shown more than once (a streamed and a plain 200, say) carries
  // its label so the tabs stay distinguishable.
  // 出现多次的状态码（例如流式与普通两种 200）附带说明文字，使标签页可以区分。
  const repeated = (status: number) => examples.filter((ex) => ex.status === status).length > 1;
  return examples.map((ex, index) => ({
    key: `${ex.status}-${index}`,
    label: repeated(ex.status) && ex.label ? `${ex.status} ${ex.label}` : String(ex.status),
    code: fillPlaceholders(ex.body, values),
    tone: ex.status >= 400 ? ("error" as const) : ("ok" as const),
  }));
}

const BODY_TYPE_LABEL: Record<ApiDocContent["body_type"], string> = {
  json: "application/json",
  multipart: "multipart/form-data",
  none: "",
};

/** ApiDocPage is the shape ApiDocArticle renders. / ApiDocPage 是 ApiDocArticle 渲染的形状。 */
export interface ApiDocPage {
  category: string;
  title: string;
  method: string;
  path: string;
  summary: string;
  content: ApiDocContent;
}

/**
 * ApiDocArticle renders one endpoint's page: the reference column, and the
 * examples column beside it on wide screens or below it on narrow ones.
 *
 * ApiDocArticle 渲染一个端点的页面：参考说明一栏，以及宽屏时位于其旁、窄屏时位于其下
 * 的示例一栏。
 */
export function ApiDocArticle({ doc, values }: { doc: ApiDocPage; values: ExampleValues }) {
  const {t } = useI18n();
  const c = doc.content;
  const responseLabels = c.response_examples
    .filter((ex) => ex.label)
    .map((ex) => `${ex.status} ${ex.label}`);
  return (
    <div className="grid grid-cols-1 gap-8 xl:grid-cols-[minmax(0,1fr)_minmax(0,26rem)] 2xl:grid-cols-[minmax(0,1fr)_minmax(0,32rem)]">
      <article className="flex min-w-0 flex-col gap-6">
        <header className="flex flex-col gap-3">
          <p className="text-sm font-medium text-primary">{doc.category}</p>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">{doc.title}</h1>
          {doc.summary ? <p className="text-muted-foreground">{doc.summary}</p> : null}
          {c.highlights.length > 0 ? (
            <ul className="ml-5 list-disc space-y-1 text-sm">
              {c.highlights.map((item, index) => (
                <li key={index}>{item}</li>
              ))}
            </ul>
          ) : null}
        </header>

        <EndpointBar method={doc.method} path={doc.path} baseUrl={values.baseUrl} />

        {c.notice ? (
          <div className="flex gap-3 rounded-xl border bg-muted/30 p-4 text-sm">
            <InfoIcon className="mt-0.5 size-4 shrink-0 text-primary" />
            <p className="whitespace-pre-line">{c.notice}</p>
          </div>
        ) : null}

        <DocSection title={t("tenant.authentication")}>
          {c.auth === "bearer" ? (
            <ParamList
              params={[
                {
                  name: "Authorization",
                  type: "string",
                  required: true,
                  description: t("common.allApisUseBearerTokenAuthenticationWithThe"),
                },
              ]}
            />
          ) : (
            <p className="text-sm text-muted-foreground"><Message id="common.thisEndpointDoesNotRequireAuthentication" /></p>
          )}
          {c.auth === "bearer" ? (
            <p className="text-sm text-muted-foreground"><Message id="common.createAKeyOnTheCompleteKeyIs" values={{ link: <Link href="/console/keys" className="font-medium text-primary underline underline-offset-4">
                <Message id="tenant.apiKeyManagementPage" /></Link> }} /></p>
          ) : null}
        </DocSection>

        {c.headers.length > 0 ? (
          <DocSection title={t("common.headers")}>
            <ParamList params={c.headers} />
          </DocSection>
        ) : null}
        {c.path_params.length > 0 ? (
          <DocSection title={t("common.pathParameters")}>
            <ParamList params={c.path_params} />
          </DocSection>
        ) : null}
        {c.query_params.length > 0 ? (
          <DocSection title={t("common.queryParameters")}>
            <ParamList params={c.query_params} />
          </DocSection>
        ) : null}
        {c.body_params.length > 0 ? (
          <DocSection
            title={t("common.requestBody")}
            aside={
              BODY_TYPE_LABEL[c.body_type] ? (
                <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
                  {BODY_TYPE_LABEL[c.body_type]}
                </code>
              ) : null
            }
          >
            <ParamList params={c.body_params} />
          </DocSection>
        ) : null}
        {c.response_fields.length > 0 ? (
          <DocSection title={t("common.response")}>
            <ParamList params={c.response_fields} />
          </DocSection>
        ) : null}
        {responseLabels.length > 0 ? (
          <DocSection title={t("tenant.statusCode")}>
            <ul className="grid gap-1 text-sm sm:grid-cols-2">
              {c.response_examples.map((ex, index) => (
                <li key={index} className="flex gap-2">
                  <code className={cn("font-mono", ex.status >= 400 ? "text-destructive" : "text-emerald-700 dark:text-emerald-400")}>
                    {ex.status}
                  </code>
                  <span className="text-muted-foreground">{ex.label}</span>
                </li>
              ))}
            </ul>
          </DocSection>
        ) : null}

        {c.sections.map((section, index) => (
          <DocSection key={index} title={section.title}>
            {section.body
              ? section.body.split(/\n{2,}/).map((paragraph, i) => (
                  <p key={i} className="whitespace-pre-line text-sm leading-relaxed">
                    {fillPlaceholders(paragraph, values)}
                  </p>
                ))
              : null}
            {section.code ? <CodeBlock code={fillPlaceholders(section.code, values)} lang={section.code_lang} /> : null}
          </DocSection>
        ))}
      </article>

      <aside className="flex min-w-0 flex-col gap-4">
        <ExamplePanel title={t("common.exampleRequest")} tabs={requestTabs(c.request_examples, values)} />
        <ExamplePanel title={t("common.exampleResponse")} tabs={responseTabs(c.response_examples, values)} />
      </aside>
    </div>
  );
}

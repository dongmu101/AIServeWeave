"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import Link from "next/link";
import { ArrowRightIcon, PlugIcon } from "lucide-react";

import { useDocs } from "@/app/console/docs/docs-shell";
import { ExampleModelField } from "@/app/console/docs/example-model-field";
import { CopyButton, ExamplePanel, MethodBadge } from "@/components/console/api-doc-article";
import { EmptyState } from "@/components/console/states";
import { fillPlaceholders, groupApiDocs } from "@/lib/console/api-docs";

const QUICK_START = [
  {
    key: "curl",
    label: "cURL",
    code: `curl {{BASE_URL}}/v1/chat/completions \\
  -H "Authorization: Bearer {{API_KEY}}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "{{MODEL}}",
    "messages": [{"role": "user", "content": "你好"}]
  }'`,
  },
  {
    key: "python",
    label: "Python",
    code: `from openai import OpenAI

client = OpenAI(base_url="{{BASE_URL}}/v1", api_key="{{API_KEY}}")
resp = client.chat.completions.create(
    model="{{MODEL}}",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)`,
  },
  {
    key: "javascript",
    label: "JavaScript",
    code: `const resp = await fetch("{{BASE_URL}}/v1/chat/completions", {
  method: "POST",
  headers: {
    "Authorization": "Bearer {{API_KEY}}",
    "Content-Type": "application/json",
  },
  body: JSON.stringify({
    model: "{{MODEL}}",
    messages: [{ role: "user", content: "你好" }],
  }),
});
console.log((await resp.json()).choices[0].message.content);`,
  },
];

const ERROR_EXAMPLES = [
  {
    key: "429",
    label: "429",
    tone: "error" as const,
    code: `{
  "error": {
    "message": "this tenant is over its requests_per_minute limit; retry after 12s",
    "type": "rate_limit_error",
    "code": "requests_per_minute"
  }
}`,
  },
  {
    key: "402",
    label: "402",
    tone: "error" as const,
    code: `{
  "error": {
    "message": "the account balance is exhausted; top up credits to continue",
    "type": "insufficient_quota",
    "code": "insufficient_balance"
  }
}`,
  },
  {
    key: "401",
    label: "401",
    tone: "error" as const,
    code: `{
  "error": {
    "message": "missing or invalid API key",
    "type": "invalid_request_error",
    "code": "invalid_api_key"
  }
}`,
  },
];

/** ERROR_ROWS lists every status the Gateway answers with, the codes a
 * caller can branch on, and what to do. Codes are wire values and are never
 * translated. / ERROR_ROWS 列出 Gateway 会返回的每个状态码、调用方可据以分支的
 * code 及处理方式。code 是协议值，不翻译。 */
const ERROR_ROWS = [
  { status: 400, codes: ["invalid_request_error", "capability_unsupported"], retry: false, note: "tenant.docsError400" },
  { status: 401, codes: ["invalid_api_key"], retry: false, note: "tenant.docsError401" },
  { status: 402, codes: ["insufficient_balance", "account_subscription_expired"], retry: false, note: "tenant.docsError402" },
  { status: 404, codes: ["model_not_found"], retry: false, note: "tenant.docsError404" },
  { status: 409, codes: ["continuation_unavailable", "job_already_finished"], retry: false, note: "tenant.docsError409" },
  { status: 413, codes: ["request_too_large", "response_too_large"], retry: false, note: "tenant.docsError413" },
  {
    status: 429,
    codes: ["requests_per_minute", "tokens_per_minute", "max_concurrent", "rate_limited", "backpressure", "queue_full"],
    retry: true,
    note: "tenant.docsError429",
  },
  { status: 500, codes: ["internal_error"], retry: true, note: "tenant.docsError500" },
  { status: 501, codes: ["cancel_unsupported"], retry: false, note: "tenant.docsError501" },
  { status: 502, codes: ["upstream_error"], retry: true, note: "tenant.docsError502" },
  { status: 503, codes: ["verification_unavailable", "model_unavailable", "connection_failed", "queue_unavailable"], retry: true, note: "tenant.docsError503" },
  { status: 504, codes: ["timeout"], retry: true, note: "tenant.docsError504" },
] as const;

/** CONVENTION_CODES are the literal values each convention names; they are
 * protocol text and stay untranslated. / CONVENTION_CODES 是每条约定提到的字面值，
 * 属于协议文本，不翻译。 */
const CONVENTION_CODES = {
  "tenant.docsConventionModel": ["GET /v1/models", "id"],
  "tenant.docsConventionStream": ['"stream": true', "text/event-stream"],
  "tenant.docsConventionRetry": ["Retry-After", "429"],
} as const;

const CONVENTIONS = [
  "tenant.docsConventionModel",
  "tenant.docsConventionStream",
  "tenant.docsConventionRetry",
] as const;

/**
 * DocsOverview is the reference's landing page. It states what holds for
 * every endpoint once, so each endpoint page can stay about that endpoint.
 *
 * DocsOverview 是参考文档的首页。它把对所有端点都成立的事一次讲清，使每个端点页面
 * 只需讲那个端点本身。
 */
export function DocsOverview() {
  const {t } = useI18n();
  const { list, values } = useDocs();
  const groups = React.useMemo(() => groupApiDocs(list.data ?? []), [list.data]);
  const tabs = QUICK_START.map((tab) => ({ ...tab, code: fillPlaceholders(tab.code, values) }));

  return (
    <div className="flex max-w-5xl flex-col gap-8">
      <header className="flex flex-col gap-2">
        <h1 className="font-heading text-2xl font-semibold tracking-tight"><Message id="tenant.apiDocumentation" /></h1>
        <p className="text-muted-foreground">
          <Message id="tenant.theGatewaySupportsOpenaiAnthropicAndOllamaApi" /></p>
      </header>

      <section className="grid gap-4 md:grid-cols-2">
        <div className="flex flex-col gap-2 rounded-xl border bg-card p-4">
          <h2 className="font-heading font-semibold"><Message id="tenant.baseUrl" /></h2>
          <div className="flex items-center gap-2 rounded-lg bg-muted/40 px-3 py-2">
            <code className="min-w-0 flex-1 truncate font-mono text-sm">{values.baseUrl}</code>
            <CopyButton text={values.baseUrl} label={t("tenant.copyBaseUrl")} />
          </div>
          <p className="text-sm text-muted-foreground"><Message id="tenant.setTheOpenaiSdkSToOllamaClients" values={{ code: <code className="font-mono">base_url</code>, code2: <code className="font-mono">{values.baseUrl}/v1</code> }} /></p>
        </div>
        <div className="flex flex-col gap-2 rounded-xl border bg-card p-4">
          <h2 className="font-heading font-semibold"><Message id="tenant.authentication" /></h2>
          <code className="rounded-lg bg-muted/40 px-3 py-2 font-mono text-sm">Authorization: Bearer YOUR_API_KEY</code>
          <p className="text-sm text-muted-foreground"><Message id="tenant.createAKeyOnAndAlsoAcceptThe" values={{ link: <Link href="/console/keys" className="font-medium text-accent-foreground underline underline-offset-4">
              <Message id="tenant.apiKeyManagementPage" /></Link>, code: <code className="font-mono">/v1/messages</code>, code2: <code className="font-mono">/v1/models</code>, code3: <code className="font-mono">x-api-key</code> }} /></p>
        </div>
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="font-heading text-lg font-semibold"><Message id="tenant.quickStart" /></h2>
        <ExampleModelField />
        <ExamplePanel title={t("tenant.quickStartExample")} tabs={tabs} />
      </section>

      <Link
        href="/console/docs/clients"
        className="group flex items-center gap-4 rounded-xl border bg-card p-4 hover:bg-muted/40"
      >
        <PlugIcon className="size-5 shrink-0 text-accent-foreground" />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="font-heading font-semibold"><Message id="tenant.docsClientsTitle" /></span>
          <span className="text-sm text-muted-foreground"><Message id="tenant.docsClientsCardSummary" /></span>
        </span>
        <ArrowRightIcon className="size-4 shrink-0 text-muted-foreground group-hover:text-foreground" />
      </Link>

      <section id="errors" className="flex scroll-mt-4 flex-col gap-3">
        <h2 className="font-heading text-lg font-semibold"><Message id="tenant.docsErrorHandling" /></h2>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.docsErrorFormatIntro" values={{ code: <code className="font-mono">/v1/messages</code>, code2: <code className="font-mono">/api/*</code> }} />
        </p>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.docsErrorRequestId" values={{ code: <code className="font-mono">X-Request-Id</code>, code2: <code className="font-mono">Retry-After</code> }} />
        </p>
        <div className="flex flex-col gap-4">
          <ul className="flex flex-col divide-y rounded-xl border md:hidden">
            {ERROR_ROWS.map((row) => (
              <li key={row.status} className="flex flex-col gap-1.5 p-3 text-sm">
                <div className="flex items-center gap-2">
                  <span className="font-mono font-semibold text-destructive">{row.status}</span>
                  <span className="ml-auto rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
                    <Message id="tenant.docsErrorRetryable" />: <Message id={row.retry ? "tenant.docsErrorRetryYes" : "tenant.docsErrorRetryNo"} />
                  </span>
                </div>
                <div className="flex flex-wrap gap-1">
                  {row.codes.map((code) => (
                    <code key={code} className="rounded border px-1.5 py-0.5 font-mono text-xs break-all">{code}</code>
                  ))}
                </div>
                <p className="text-muted-foreground"><Message id={row.note} /></p>
              </li>
            ))}
          </ul>
          <div className="hidden min-w-0 overflow-x-auto rounded-xl border md:block">
            <table className="w-full text-left text-sm">
              <thead className="bg-muted/40 text-xs text-muted-foreground">
                <tr>
                  <th scope="col" className="px-3 py-2 font-medium whitespace-nowrap"><Message id="tenant.statusCode" /></th>
                  <th scope="col" className="px-3 py-2 font-medium">code</th>
                  <th scope="col" className="px-3 py-2 font-medium"><Message id="tenant.docsErrorMeaning" /></th>
                  <th scope="col" className="px-3 py-2 font-medium whitespace-nowrap"><Message id="tenant.docsErrorRetryable" /></th>
                </tr>
              </thead>
              <tbody className="divide-y">
                {ERROR_ROWS.map((row) => (
                  <tr key={row.status} className="align-top">
                    <td className="px-3 py-2 font-mono text-destructive">{row.status}</td>
                    <td className="px-3 py-2">
                      <div className="flex flex-col gap-0.5">
                        {row.codes.map((code) => (
                          <code key={code} className="font-mono text-xs whitespace-nowrap">{code}</code>
                        ))}
                      </div>
                    </td>
                    <td className="min-w-64 px-3 py-2 text-muted-foreground"><Message id={row.note} /></td>
                    <td className="px-3 py-2 text-muted-foreground whitespace-nowrap">
                      <Message id={row.retry ? "tenant.docsErrorRetryYes" : "tenant.docsErrorRetryNo"} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <ExamplePanel title={t("tenant.exampleErrorResponse")} tabs={ERROR_EXAMPLES} />
        </div>
      </section>

      <section id="conventions" className="flex scroll-mt-4 flex-col gap-3">
        <h2 className="font-heading text-lg font-semibold"><Message id="tenant.docsConventions" /></h2>
        <ul className="ml-5 list-disc space-y-1.5 text-sm text-muted-foreground">
          {CONVENTIONS.map((id) => (
            <li key={id}>
              <Message
                id={id}
                values={{
                  code: <code className="font-mono">{CONVENTION_CODES[id][0]}</code>,
                  code2: <code className="font-mono">{CONVENTION_CODES[id][1]}</code>,
                }}
              />
            </li>
          ))}
        </ul>
      </section>

      <section className="flex flex-col gap-4">
        <h2 className="font-heading text-lg font-semibold"><Message id="tenant.allApis" /></h2>
        {list.data && groups.length === 0 ? <EmptyState title={t("tenant.noDocumentationHasBeenPublishedYet")} /> : null}
        <div className="grid gap-4 md:grid-cols-2">
          {groups.map((group) => (
            <div key={group.category} className="flex flex-col gap-1 rounded-xl border bg-card p-4">
              <h3 className="pb-1 text-sm font-semibold">{group.category}</h3>
              {group.items.map((doc) => (
                <Link
                  key={doc.slug}
                  href={`/console/docs/${doc.slug}`}
                  className="flex min-w-0 items-center gap-2 rounded-md px-1 py-1 text-sm hover:bg-muted"
                >
                  <MethodBadge method={doc.method} className="w-12 justify-center" />
                  <span className="shrink-0">{doc.title}</span>
                  <code className="min-w-0 truncate font-mono text-xs text-muted-foreground">{doc.path}</code>
                </Link>
              ))}
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

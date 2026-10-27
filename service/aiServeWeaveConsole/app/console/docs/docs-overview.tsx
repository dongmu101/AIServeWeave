"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import Link from "next/link";

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

const ERROR_EXAMPLE = `{
  "error": {
    "message": "this tenant is over its requests_per_minute limit; retry after 12s",
    "type": "rate_limit_error",
    "code": "requests_per_minute"
  }
}`;

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
        <div className="flex flex-col gap-2 rounded-xl border p-4">
          <h2 className="font-heading font-semibold"><Message id="tenant.baseUrl" /></h2>
          <div className="flex items-center gap-2 rounded-lg bg-muted/40 px-3 py-2">
            <code className="min-w-0 flex-1 truncate font-mono text-sm">{values.baseUrl}</code>
            <CopyButton text={values.baseUrl} label={t("tenant.copyBaseUrl")} />
          </div>
          <p className="text-sm text-muted-foreground"><Message id="tenant.setTheOpenaiSdkSToOllamaClients" values={{ code: <code className="font-mono">base_url</code>, code2: <code className="font-mono">{values.baseUrl}/v1</code> }} /></p>
        </div>
        <div className="flex flex-col gap-2 rounded-xl border p-4">
          <h2 className="font-heading font-semibold"><Message id="tenant.authentication" /></h2>
          <code className="rounded-lg bg-muted/40 px-3 py-2 font-mono text-sm">Authorization: Bearer YOUR_API_KEY</code>
          <p className="text-sm text-muted-foreground"><Message id="tenant.createAKeyOnAndAlsoAcceptThe" values={{ link: <Link href="/console/keys" className="font-medium text-primary underline underline-offset-4">
              <Message id="tenant.apiKeyManagementPage" /></Link>, code: <code className="font-mono">/v1/messages</code>, code2: <code className="font-mono">/v1/models</code>, code3: <code className="font-mono">x-api-key</code> }} /></p>
        </div>
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="font-heading text-lg font-semibold"><Message id="tenant.quickStart" /></h2>
        <ExampleModelField />
        <ExamplePanel title={t("tenant.quickStartExample")} tabs={tabs} />
      </section>

      <section className="grid gap-4 md:grid-cols-2">
        <div className="flex flex-col gap-2">
          <h2 className="font-heading text-lg font-semibold"><Message id="tenant.errorFormat" /></h2>
          <p className="text-sm text-muted-foreground"><Message id="tenant.errorResponsesUseTheOpenaiFormatExceptFor_819019" values={{ code: <code className="font-mono">X-Request-Id</code> }} /></p>
          <ul className="ml-5 list-disc space-y-1 text-sm text-muted-foreground">
            <li><Message id="tenant.apiKeyIsMissingOrInvalid_37b2d2" values={{ code: <code className="font-mono">401</code> }} /></li>
            <li><Message id="tenant.tenantQuotaExceededWaitAsSpecifiedByThe_d633ee" values={{ code: <code className="font-mono">429</code>, code2: <code className="font-mono">Retry-After</code> }} /></li>
            <li><Message id="tenant.keyValidationIsTemporarilyUnavailableOrTheBackend_404b62" values={{ code: <code className="font-mono">503</code> }} /></li>
          </ul>
        </div>
        <ExamplePanel title={t("tenant.exampleErrorResponse")} tabs={[{ key: "429", label: "429", code: ERROR_EXAMPLE, tone: "error" }]} />
      </section>

      <section className="flex flex-col gap-4">
        <h2 className="font-heading text-lg font-semibold"><Message id="tenant.allApis" /></h2>
        {list.data && groups.length === 0 ? <EmptyState title={t("tenant.noDocumentationHasBeenPublishedYet")} /> : null}
        <div className="grid gap-4 md:grid-cols-2">
          {groups.map((group) => (
            <div key={group.category} className="flex flex-col gap-1 rounded-xl border p-4">
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

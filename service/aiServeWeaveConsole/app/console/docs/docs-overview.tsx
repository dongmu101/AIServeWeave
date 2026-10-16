"use client";

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
  const { list, values } = useDocs();
  const groups = React.useMemo(() => groupApiDocs(list.data ?? []), [list.data]);
  const tabs = QUICK_START.map((tab) => ({ ...tab, code: fillPlaceholders(tab.code, values) }));

  return (
    <div className="flex max-w-5xl flex-col gap-8">
      <header className="flex flex-col gap-2">
        <h1 className="font-heading text-2xl font-semibold tracking-tight">API 文档</h1>
        <p className="text-muted-foreground">
          网关兼容 OpenAI、Anthropic 与 Ollama 的接口格式，现有 SDK 只需替换基地址与 API Key 即可接入。
          下列文档覆盖当前已接入的全部能力，新增模型或能力时会同步更新。
        </p>
      </header>

      <section className="grid gap-4 md:grid-cols-2">
        <div className="flex flex-col gap-2 rounded-xl border p-4">
          <h2 className="font-heading font-semibold">基地址</h2>
          <div className="flex items-center gap-2 rounded-lg bg-muted/40 px-3 py-2">
            <code className="min-w-0 flex-1 truncate font-mono text-sm">{values.baseUrl}</code>
            <CopyButton text={values.baseUrl} label="复制基地址" />
          </div>
          <p className="text-sm text-muted-foreground">
            OpenAI SDK 的 <code className="font-mono">base_url</code> 设为{" "}
            <code className="font-mono">{values.baseUrl}/v1</code>；Ollama 客户端直接使用基地址。
          </p>
        </div>
        <div className="flex flex-col gap-2 rounded-xl border p-4">
          <h2 className="font-heading font-semibold">认证</h2>
          <code className="rounded-lg bg-muted/40 px-3 py-2 font-mono text-sm">Authorization: Bearer YOUR_API_KEY</code>
          <p className="text-sm text-muted-foreground">
            在{" "}
            <Link href="/console/keys" className="font-medium text-primary underline underline-offset-4">
              API Key 管理页面
            </Link>{" "}
            创建密钥。<code className="font-mono">/v1/messages</code> 与 <code className="font-mono">/v1/models</code>{" "}
            另外接受 <code className="font-mono">x-api-key</code> 请求头。
          </p>
        </div>
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="font-heading text-lg font-semibold">快速开始</h2>
        <ExampleModelField />
        <ExamplePanel title="快速开始示例" tabs={tabs} />
      </section>

      <section className="grid gap-4 md:grid-cols-2">
        <div className="flex flex-col gap-2">
          <h2 className="font-heading text-lg font-semibold">错误格式</h2>
          <p className="text-sm text-muted-foreground">
            除 Anthropic 与 Ollama 兼容端点使用各自协议的错误格式外，错误响应统一为 OpenAI 格式。
            每个响应都带 <code className="font-mono">X-Request-Id</code> 头，排查问题时请一并提供。
          </p>
          <ul className="ml-5 list-disc space-y-1 text-sm text-muted-foreground">
            <li>
              <code className="font-mono">401</code> API Key 缺失或无效
            </li>
            <li>
              <code className="font-mono">429</code> 超出租户配额，按 <code className="font-mono">Retry-After</code> 头等待后重试
            </li>
            <li>
              <code className="font-mono">503</code> 暂时无法校验 Key 或后端不可用，可稍后重试
            </li>
          </ul>
        </div>
        <ExamplePanel title="错误响应示例" tabs={[{ key: "429", label: "429", code: ERROR_EXAMPLE, tone: "error" }]} />
      </section>

      <section className="flex flex-col gap-4">
        <h2 className="font-heading text-lg font-semibold">全部接口</h2>
        {list.data && groups.length === 0 ? <EmptyState title="暂无已发布的文档" /> : null}
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

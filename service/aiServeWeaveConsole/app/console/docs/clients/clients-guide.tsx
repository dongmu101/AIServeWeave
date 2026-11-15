"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { MessageKey } from "@/lib/i18n/messages";

import * as React from "react";
import Link from "next/link";
import { ArrowUpRightIcon, DownloadIcon } from "lucide-react";
import { cn } from "cn";

import { useDocs } from "@/app/console/docs/docs-shell";
import { ExampleModelField } from "@/app/console/docs/example-model-field";
import { CopyButton, ExamplePanel } from "@/components/console/api-doc-article";
import { buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { fillPlaceholders, type ExampleValues } from "@/lib/console/api-docs";
import { ccSwitchImportLink, type CcSwitchApp } from "@/lib/console/cc-switch";

/**
 * A client's configuration: prose that may name up to two literal values,
 * and code tabs. Code is protocol text and stays untranslated; it may carry
 * the {{BASE_URL}}, {{API_KEY}} and {{MODEL}} placeholders.
 *
 * 一个客户端的配置：最多提及两个字面值的说明文字，以及代码标签页。代码属于协议文本，
 * 不翻译；其中可以带 {{BASE_URL}}、{{API_KEY}} 与 {{MODEL}} 占位符。
 */
interface ClientGuide {
  id: string;
  title: MessageKey | string;
  /** protocol is the wire protocol the client speaks, shown as a tag.
   * protocol 是该客户端使用的协议，以标签展示。 */
  protocol: string;
  body: MessageKey;
  codes: readonly [string, string];
  tabs: readonly { key: string; label: string; code: string }[];
}

const CLIENTS: readonly ClientGuide[] = [
  {
    id: "claude-code",
    protocol: "Anthropic Messages",
    title: "Claude Code",
    body: "tenant.docsClientClaudeCodeBody",
    codes: ["/v1/messages", "~/.claude/settings.json"],
    tabs: [
      {
        key: "shell",
        label: "Shell",
        code: `export ANTHROPIC_BASE_URL={{BASE_URL}}
export ANTHROPIC_API_KEY={{API_KEY}}
export ANTHROPIC_MODEL={{MODEL}}
claude`,
      },
      {
        key: "settings",
        label: "settings.json",
        code: `{
  "env": {
    "ANTHROPIC_BASE_URL": "{{BASE_URL}}",
    "ANTHROPIC_API_KEY": "{{API_KEY}}",
    "ANTHROPIC_MODEL": "{{MODEL}}"
  }
}`,
      },
    ],
  },
  {
    id: "codex",
    protocol: "OpenAI Responses",
    title: "Codex CLI",
    body: "tenant.docsClientCodexBody",
    codes: ["/v1/responses", "~/.codex/config.toml"],
    tabs: [
      {
        key: "toml",
        label: "config.toml",
        code: `model = "{{MODEL}}"
model_provider = "gateway"

[model_providers.gateway]
name = "Gateway"
base_url = "{{BASE_URL}}/v1"
env_key = "GATEWAY_API_KEY"
wire_api = "responses"`,
      },
      {
        key: "shell",
        label: "Shell",
        code: `export GATEWAY_API_KEY={{API_KEY}}
codex`,
      },
    ],
  },
  {
    id: "cc-switch",
    protocol: "Deep link",
    title: "CC Switch",
    body: "tenant.docsClientCcSwitchBody",
    codes: ["{{BASE_URL}}", "{{BASE_URL}}/v1"],
    tabs: [],
  },
  {
    id: "openai-sdk",
    protocol: "OpenAI",
    title: "OpenAI SDK",
    body: "tenant.docsClientOpenaiSdkBody",
    codes: ["{{BASE_URL}}/v1", "OPENAI_BASE_URL"],
    tabs: [
      {
        key: "python",
        label: "Python",
        code: `from openai import OpenAI

client = OpenAI(base_url="{{BASE_URL}}/v1", api_key="{{API_KEY}}")
resp = client.chat.completions.create(
    model="{{MODEL}}",
    messages=[{"role": "user", "content": "Hello"}],
)
print(resp.choices[0].message.content)`,
      },
      {
        key: "node",
        label: "Node.js",
        code: `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "{{BASE_URL}}/v1", apiKey: "{{API_KEY}}" });
const resp = await client.chat.completions.create({
  model: "{{MODEL}}",
  messages: [{ role: "user", content: "Hello" }],
});
console.log(resp.choices[0].message.content);`,
      },
      {
        key: "env",
        label: "Shell",
        code: `export OPENAI_BASE_URL={{BASE_URL}}/v1
export OPENAI_API_KEY={{API_KEY}}`,
      },
    ],
  },
  {
    id: "anthropic-sdk",
    protocol: "Anthropic",
    title: "Anthropic SDK",
    body: "tenant.docsClientAnthropicSdkBody",
    codes: ["/v1", "/v1/messages"],
    tabs: [
      {
        key: "python",
        label: "Python",
        code: `from anthropic import Anthropic

client = Anthropic(base_url="{{BASE_URL}}", api_key="{{API_KEY}}")
msg = client.messages.create(
    model="{{MODEL}}",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello"}],
)
print(msg.content[0].text)`,
      },
      {
        key: "node",
        label: "Node.js",
        code: `import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({ baseURL: "{{BASE_URL}}", apiKey: "{{API_KEY}}" });
const msg = await client.messages.create({
  model: "{{MODEL}}",
  max_tokens: 1024,
  messages: [{ role: "user", content: "Hello" }],
});
console.log(msg.content[0].text);`,
      },
    ],
  },
  {
    id: "ollama",
    protocol: "Ollama",
    title: "tenant.docsClientOllamaTitle",
    body: "tenant.docsClientOllamaBody",
    codes: ["/api/*", "/api/tags"],
    tabs: [
      {
        key: "python",
        label: "Python",
        code: `from ollama import Client

client = Client(host="{{BASE_URL}}", headers={"Authorization": "Bearer {{API_KEY}}"})
resp = client.chat(model="{{MODEL}}", messages=[{"role": "user", "content": "Hello"}])
print(resp["message"]["content"])`,
      },
      {
        key: "javascript",
        label: "JavaScript",
        code: `import { Ollama } from "ollama";

const client = new Ollama({ host: "{{BASE_URL}}", headers: { Authorization: "Bearer {{API_KEY}}" } });
const resp = await client.chat({ model: "{{MODEL}}", messages: [{ role: "user", content: "Hello" }] });
console.log(resp.message.content);`,
      },
    ],
  },
  {
    id: "chat-apps",
    protocol: "OpenAI-compatible",
    title: "tenant.docsClientChatAppsTitle",
    body: "tenant.docsClientChatAppsBody",
    codes: ["{{BASE_URL}}/v1", "{{BASE_URL}}"],
    tabs: [],
  },
];

function isMessageKey(value: string): value is MessageKey {
  return value.startsWith("tenant.");
}

/**
 * ClientsGuide shows how common tools and SDKs point at this Gateway. Every
 * example uses the same base URL and example model as the endpoint pages.
 *
 * ClientsGuide 说明常见工具与 SDK 如何指向本网关。所有示例使用与端点页面相同的基地址
 * 和示例模型。
 */
export function ClientsGuide() {
  const { t } = useI18n();
  const { values } = useDocs();
  const title = (value: string) => (isMessageKey(value) ? t(value) : value);

  return (
    <div className="flex max-w-5xl flex-col gap-8">
      <header className="flex flex-col gap-2">
        <h1 className="font-heading text-2xl font-semibold tracking-tight"><Message id="tenant.docsClientsTitle" /></h1>
        <p className="max-w-3xl leading-relaxed text-muted-foreground">
          <Message
            id="tenant.docsClientsIntro"
            values={{
              code: <code className="font-mono">YOUR_API_KEY</code>,
              link: <KeysLink />,
            }}
          />
        </p>
      </header>

      <ConnectionCard values={values} />

      <nav aria-label={t("tenant.docsOnThisPage")} className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-4">
        {CLIENTS.map((client) => (
          <a
            key={client.id}
            href={`#${client.id}`}
            className="group flex min-w-0 flex-col gap-1 rounded-lg border bg-card px-3 py-2.5 transition-colors hover:border-brand/60 hover:bg-muted/50"
          >
            <span className="flex items-center justify-between gap-2 text-sm font-medium">
              <span className="truncate">{title(client.title)}</span>
              <ArrowUpRightIcon aria-hidden className="size-3.5 shrink-0 text-muted-foreground group-hover:text-foreground" />
            </span>
            <span className="truncate font-mono text-[11px] text-muted-foreground">{client.protocol}</span>
          </a>
        ))}
      </nav>

      <div className="flex flex-col gap-6">
        {CLIENTS.map((client) => (
          <section key={client.id} id={client.id} className="flex scroll-mt-4 flex-col gap-4 rounded-xl border bg-card p-4 sm:p-5">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="font-heading text-lg font-semibold">{title(client.title)}</h2>
              <span className="rounded-full border bg-muted/50 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">{client.protocol}</span>
            </div>
            <p className="text-sm leading-relaxed text-muted-foreground">
              <Message
                id={client.body}
                values={{
                  code: <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em] break-all text-foreground">{fillPlaceholders(client.codes[0], values)}</code>,
                  code2: <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em] break-all text-foreground">{fillPlaceholders(client.codes[1], values)}</code>,
                }}
              />
            </p>
            {client.id === "cc-switch" ? <CcSwitchImport values={values} /> : null}
            {client.tabs.length > 0 ? (
              <ExamplePanel
                title={title(client.title)}
                tabs={client.tabs.map((tab) => ({ ...tab, code: fillPlaceholders(tab.code, values) }))}
              />
            ) : null}
          </section>
        ))}
      </div>
    </div>
  );
}

function KeysLink() {
  return (
    <Link href="/console/keys" className="font-medium text-accent-foreground underline underline-offset-4">
      <Message id="tenant.apiKeyManagementPage" />
    </Link>
  );
}

/** ConnectionCard shows the three values every client needs. / ConnectionCard 展示每个客户端都需要的三个值。 */
function ConnectionCard({ values }: { values: ExampleValues }) {
  return (
    <section className="rounded-xl border bg-muted/30 p-4 sm:p-5">
      <h2 className="mb-3 text-sm font-semibold"><Message id="tenant.docsClientsConnection" /></h2>
      <dl className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-1">
          <dt className="text-xs text-muted-foreground"><Message id="tenant.baseUrl" /></dt>
          <dd className="flex min-w-0 items-center gap-1">
            <code className="min-w-0 truncate rounded-md border bg-background px-2 py-1 font-mono text-xs">{values.baseUrl}</code>
            <CopyButton text={values.baseUrl} />
          </dd>
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <dt className="text-xs text-muted-foreground">API Key</dt>
          <dd className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <code className="rounded-md border bg-background px-2 py-1 font-mono text-foreground">YOUR_API_KEY</code>
            <span><Message id="tenant.docsClientsKeyHint" values={{ link: <KeysLink /> }} /></span>
          </dd>
        </div>
      </dl>
      <div className="mt-4 border-t pt-4">
        <ExampleModelField />
      </div>
    </section>
  );
}

const CC_SWITCH_APPS: readonly { app: CcSwitchApp; label: MessageKey }[] = [
  { app: "claude", label: "tenant.docsClientCcSwitchImportClaude" },
  { app: "codex", label: "tenant.docsClientCcSwitchImportCodex" },
];

/**
 * CcSwitchImport offers one-click import links. The key typed here stays in
 * this component: it only goes into the link, which opens the local app.
 *
 * CcSwitchImport 提供一键导入链接。这里输入的 Key 只留在本组件中：它只写进链接，
 * 由链接打开本地应用。
 */
function CcSwitchImport({ values }: { values: ExampleValues }) {
  const { t } = useI18n();
  const id = React.useId();
  const [apiKey, setApiKey] = React.useState("");
  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-muted/30 p-3">
      <div className="flex flex-col gap-1">
        <label htmlFor={id} className="text-xs font-medium"><Message id="tenant.docsClientCcSwitchKeyLabel" /></label>
        <Input
          id={id}
          type="password"
          autoComplete="off"
          spellCheck={false}
          value={apiKey}
          maxLength={256}
          onChange={(event) => setApiKey(event.target.value)}
          placeholder="YOUR_API_KEY"
          className="h-8 max-w-sm font-mono text-xs"
        />
        <p className="text-xs text-muted-foreground"><Message id="tenant.docsClientCcSwitchKeyHint" /></p>
      </div>
      <div className="flex flex-wrap gap-2">
        {CC_SWITCH_APPS.map(({ app, label }) => (
          <a
            key={app}
            href={ccSwitchImportLink({
              app,
              name: hostName(values.baseUrl),
              baseUrl: values.baseUrl,
              apiKey: apiKey.trim() || values.apiKey,
              model: values.model,
            })}
            className={cn(buttonVariants({ size: "sm" }))}
          >
            <DownloadIcon aria-hidden />
            {t(label)}
          </a>
        ))}
      </div>
    </div>
  );
}

function hostName(baseUrl: string): string {
  try {
    return new URL(baseUrl).host || "Gateway";
  } catch {
    return "Gateway";
  }
}

"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { MessageKey } from "@/lib/i18n/messages";

import * as React from "react";
import Link from "next/link";

import { useDocs } from "@/app/console/docs/docs-shell";
import { ExampleModelField } from "@/app/console/docs/example-model-field";
import { DocSection, ExamplePanel, PageToc } from "@/components/console/api-doc-article";
import { fillPlaceholders } from "@/lib/console/api-docs";

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
  body: MessageKey;
  codes: readonly [string, string];
  tabs: readonly { key: string; label: string; code: string }[];
}

const CLIENTS: readonly ClientGuide[] = [
  {
    id: "claude-code",
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
    id: "openai-sdk",
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
  const toc = CLIENTS.map((client) => ({ id: client.id, title: title(client.title) }));

  return (
    <div className="flex max-w-5xl flex-col gap-6">
      <header className="flex flex-col gap-2">
        <h1 className="font-heading text-2xl font-semibold tracking-tight"><Message id="tenant.docsClientsTitle" /></h1>
        <p className="text-muted-foreground">
          <Message
            id="tenant.docsClientsIntro"
            values={{
              code: <code className="font-mono">YOUR_API_KEY</code>,
              link: (
                <Link href="/console/keys" className="font-medium text-accent-foreground underline underline-offset-4">
                  <Message id="tenant.apiKeyManagementPage" />
                </Link>
              ),
            }}
          />
        </p>
      </header>

      <div className="flex flex-col gap-3 border-b pb-3">
        <ExampleModelField />
        <PageToc items={toc} />
      </div>

      {CLIENTS.map((client) => (
        <DocSection key={client.id} id={client.id} title={title(client.title)}>
          <p className="text-sm leading-relaxed text-muted-foreground">
            <Message
              id={client.body}
              values={{
                code: <code className="font-mono break-all">{fillPlaceholders(client.codes[0], values)}</code>,
                code2: <code className="font-mono break-all">{fillPlaceholders(client.codes[1], values)}</code>,
              }}
            />
          </p>
          {client.tabs.length > 0 ? (
            <ExamplePanel
              title={title(client.title)}
              tabs={client.tabs.map((tab) => ({ ...tab, code: fillPlaceholders(tab.code, values) }))}
            />
          ) : null}
        </DocSection>
      ))}
    </div>
  );
}

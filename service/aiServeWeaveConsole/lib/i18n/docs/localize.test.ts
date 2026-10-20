import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";
import { parseApiDocDetail } from "../../console/api-docs.ts";
import { LOCALES } from "../locale.ts";
import { loadMessages } from "../messages.ts";
import { localizeApiDoc, localizeApiDocSummary } from "./localize.ts";

const directory = new URL("../../../../aiServeWeaveControlPlane/internal/apidocs/seed/", import.meta.url);
function seeds() {
  return fs.readdirSync(directory).filter(name => name.endsWith(".json")).sort().map(name => {
    const seed = JSON.parse(fs.readFileSync(new URL(name, directory), "utf8"));
    return parseApiDocDetail({ ...seed, updated_at: "2026-01-01T00:00:00Z" });
  });
}

test("known seed metadata and full content get a display-only translation", async () => {
  const source = seeds().find(doc => doc.slug === "artifact-download")!;
  const original = structuredClone(source);
  const en = await loadMessages("en-US");
  assert.equal(localizeApiDocSummary(source, "en-US", en).title, "Download artifact");
  assert.equal(localizeApiDoc(source, "en-US", en).title, "Download artifact");
  assert.deepEqual(source, original);
});

test("unknown slugs and changed author content are returned intact", async () => {
  const en = await loadMessages("en-US");
  const source = seeds()[0];
  for (const doc of [{ ...source, slug: "custom-doc" }, { ...source, summary: "authored content" }, { ...source, content: { ...source.content, notice: "自定义通知" } }]) {
    assert.strictEqual(localizeApiDoc(doc, "en-US", en), doc);
  }
  const changed = { ...source, title: "作者标题" };
  assert.strictEqual(localizeApiDocSummary(changed, "en-US", en), changed);
});

test("canonical matching ignores object field order and update timestamps", async () => {
  const source = seeds().find(doc => doc.slug === "artifact-download")!;
  // Reverse recursively without a JSON replacer that would drop nested keys.
  // 递归反转字段，避免 JSON replacer 丢弃嵌套字段。
  function reverse(value: unknown): unknown {
    if (Array.isArray(value)) return value.map(reverse);
    if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).reverse().map(([key, item]) => [key, reverse(item)]));
    return value;
  }
  const copy = reverse({ ...source, updatedAt: "2026-10-20T00:00:00Z" }) as typeof source;
  assert.equal(localizeApiDoc(copy, "en-US", await loadMessages("en-US")).title, "Download artifact");
});

test("all 19 seeds retain examples, placeholders, protocol values and nested parameter shapes in eight locales", async () => {
  const documents = seeds();
  assert.equal(documents.length, 19);
  for (const locale of LOCALES) {
    const messages = await loadMessages(locale);
    for (const original of documents) {
      const projected = localizeApiDoc(original, locale, messages);
      assert.equal(projected.slug, original.slug);
      assert.equal(projected.method, original.method);
      assert.equal(projected.path, original.path);
      const expected = structuredClone(original.content);
      const actual = structuredClone(projected.content);
      function machine(value: unknown): unknown {
        if (Array.isArray(value)) return value.map(machine);
        if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).filter(([key]) => !["description", "label", "title", "notice", "highlights", "body"].includes(key)).map(([key, item]) => [key, machine(item)]));
        return value;
      }
      assert.deepEqual(machine(actual), machine(expected), `${locale} ${original.slug}`);
      assert.deepEqual(actual.response_examples.map(ex => ex.body), expected.response_examples.map(ex => ex.body));
      assert.deepEqual(actual.sections.map(section => section.code), expected.sections.map(section => section.code));
      if (locale !== "zh-CN") assert.notEqual(projected.summary, original.summary, `${locale} ${original.slug}`);
    }
  }
});

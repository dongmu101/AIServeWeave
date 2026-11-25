import { test } from "node:test";
import assert from "node:assert/strict";

import { groupByFamily, modelFamily, OTHER_FAMILY } from "./model-family.ts";

test("modelFamily", () => {
  const cases: [string, string | null][] = [
    ["claude-sonnet-4-6", "claude"],
    ["Claude-Opus-5-5", "claude"],
    ["openrouter/claude-x", "claude"],
    ["anthropic/custom-alias", "claude"],
    ["gpt-5.6-luna", "openai"],
    ["o3", "openai"],
    ["o4-mini", "openai"],
    ["text-embedding-3-large", "openai"],
    ["gemini-2.5-pro", "gemini"],
    ["gemma-3", "gemini"],
    ["deepseek-chat", "deepseek"],
    ["qwen-coder", "qwen"],
    ["qwq-32b", "qwen"],
    ["glm-4.6", "glm"],
    ["kimi-k2", "kimi"],
    ["doubao-pro", "doubao"],
    ["grok-4", "grok"],
    ["minimax-m2", "minimax"],
    ["llama3", "llama"],
    ["mixtral-8x7b", "mistral"],
    ["hunyuan-turbo", "hunyuan"],
    ["ernie-4.5", "ernie"],
    ["omega-1", null],
    ["o", null],
    ["*", null],
    ["", null],
    ["a/b/c", null],
    ["my-claude", null],
  ];
  for (const [model, want] of cases) assert.equal(modelFamily(model)?.id ?? null, want, model);
});

test("groupByFamily", () => {
  const groups = groupByFamily(["zeta", "gpt-10", "claude-b", "gpt-5", "claude-a", "*"], (m) => m);
  assert.deepEqual(groups.map((g) => [g.id, g.items]), [
    ["claude", ["claude-a", "claude-b"]],
    ["openai", ["gpt-5", "gpt-10"]],
    [OTHER_FAMILY, ["*", "zeta"]],
  ]);
  assert.equal(groups[2].family, null);
  assert.deepEqual(groupByFamily([], (m: string) => m), []);
  const objects = [{ model: "deepseek-r1" }, { model: "deepseek-chat" }];
  assert.deepEqual(groupByFamily(objects, (o) => o.model)[0].items.map((o) => o.model), ["deepseek-chat", "deepseek-r1"]);
});

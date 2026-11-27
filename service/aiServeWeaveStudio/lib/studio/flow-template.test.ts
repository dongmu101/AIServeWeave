import assert from "node:assert/strict";
import { test } from "node:test";

import { templateRefs } from "./flow-template.ts";

test("references are found in order, with their scope and field", () => {
  assert.deepEqual(templateRefs("a {{inputs.topic}} b {{ draft.text }}").refs, [
    { scope: "inputs", field: "topic" },
    { scope: "draft", field: "text" },
  ]);
  assert.deepEqual(templateRefs("").refs, []);
});

test("a sole reference has nothing but white space around it", () => {
  assert.equal(templateRefs("{{img.image}}").sole, true);
  assert.equal(templateRefs("  \n{{ img.image }}\t ").sole, true);
  assert.equal(templateRefs("see {{img.image}}").sole, false);
  assert.equal(templateRefs("{{img.image}}.").sole, false);
  assert.equal(templateRefs("{{a.b}}{{a.c}}").sole, false);
  assert.equal(templateRefs("{{a.b}} {{a.c}}").sole, false);
  assert.equal(templateRefs("").sole, false);
  assert.equal(templateRefs("   ").sole, false);
});

test("text that only looks like a reference stays literal, as on the server", () => {
  for (const text of ["{{name}}", "{{ a | b }}", '{"k": {"j": 1}}', "{{unclosed.ref", "{{1a.b}}", "{{a-b.c}}"]) {
    const parsed = templateRefs(text);
    assert.deepEqual(parsed.refs, [], text);
    assert.equal(parsed.sole, false, text);
  }
});

test("a reference inside extra braces is still found, but the braces make it not sole", () => {
  const parsed = templateRefs("{{{inputs.x}}}");
  assert.deepEqual(parsed.refs, [{ scope: "inputs", field: "x" }]);
  assert.equal(parsed.sole, false);
});

test("a reference with more than two parts is unsupported and yields none", () => {
  assert.deepEqual(templateRefs("{{a.b.c}}"), { refs: [], sole: false });
});

test("identifiers are bounded at 64 characters like flowdef's", () => {
  assert.equal(templateRefs(`{{${"a".repeat(64)}.f}}`).refs.length, 1);
  assert.equal(templateRefs(`{{${"a".repeat(65)}.f}}`).refs.length, 0);
});

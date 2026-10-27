import assert from "node:assert/strict";
import test from "node:test";
import {LOCALES} from "./locale.ts";
import {editorLocale} from "./editor-locale.ts";

test("every display locale maps to an exact editor bundle", () => {
  assert.deepEqual(LOCALES.map(editorLocale), ["zh", "zh-TW", "en", "ja", "ko", "de", "fr", "es"]);
});

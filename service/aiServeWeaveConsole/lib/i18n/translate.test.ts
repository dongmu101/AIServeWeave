import assert from "node:assert/strict";
import test from "node:test";
import { loadMessages, type MessageKey } from "./messages.ts";
import { createTranslator } from "./translate.ts";

test("catalogue chooses German text and interpolates whole phrases", async () => {
 const t = createTranslator("de-DE", await loadMessages("de-DE"));
 assert.equal(t("common.save"), "Speichern");
});
test("plural rules follow the selected language, including French zero", async () => {
 const en = createTranslator("en-US", await loadMessages("en-US"));
 const fr = createTranslator("fr-FR", await loadMessages("fr-FR"));
 assert.equal(en("common.items", {count: 1}), "1 item");
 assert.equal(en("common.items", {count: 2}), "2 items");
 assert.equal(fr("common.items", {count: 0}), "0 élément");
 assert.equal(fr("common.items", {count: 1}), "1 élément");
 assert.equal(fr("common.items", {count: 2}), "2 éléments");
});
test("strict translation rejects missing values; production never displays internal keys", async () => {
 const messages = await loadMessages("en-US");
 const t = createTranslator("en-US", messages);
 assert.throws(() => t("common.items"), /count/);
 assert.throws(() => t("bad.private.key" as MessageKey), /unknown/i);
 assert.equal(createTranslator("en-US", messages, {strict: false})("bad.private.key" as MessageKey), "Unavailable");
});

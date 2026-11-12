import assert from "node:assert/strict";
import test from "node:test";

import { LOCALES } from "../i18n/locale.ts";
import { CONTENT_LOCALES, legalDocument } from "./documents.ts";
import { DEFAULT_REFUND_WINDOW_DAYS, LEGAL_ENV, PLACEHOLDERS, parseLegalEntity } from "./entity.ts";
import { LEGAL_KINDS, type LegalDocument } from "./types.ts";

const FULL = {
  [LEGAL_ENV.serviceName]: "Weave AI",
  [LEGAL_ENV.company]: "Example Ltd.",
  [LEGAL_ENV.address]: "1 Example Road, Singapore",
  [LEGAL_ENV.email]: "support@example.com",
  [LEGAL_ENV.jurisdiction]: "Singapore",
  [LEGAL_ENV.refundWindowDays]: "14",
};

function text(doc: LegalDocument): string {
  return [doc.title, ...doc.intro.flat(), ...doc.sections.flatMap(s => [s.heading, ...s.blocks.flat()])].join("\n");
}

test("parseLegalEntity uses configured values and reports nothing missing", () => {
  const entity = parseLegalEntity(FULL);
  assert.equal(entity.serviceName, "Weave AI");
  assert.equal(entity.company, "Example Ltd.");
  assert.equal(entity.email, "support@example.com");
  assert.equal(entity.emailText, "support@example.com");
  assert.equal(entity.refundWindowDays, 14);
  assert.deepEqual(entity.missing, []);
});

test("parseLegalEntity falls back to visible placeholders and lists what is missing", () => {
  const entity = parseLegalEntity({});
  assert.equal(entity.serviceName, "AIServeWeave");
  assert.equal(entity.company, PLACEHOLDERS.company);
  assert.equal(entity.email, null);
  assert.equal(entity.emailText, PLACEHOLDERS.email);
  assert.equal(entity.refundWindowDays, DEFAULT_REFUND_WINDOW_DAYS);
  assert.deepEqual(entity.missing.sort(), [LEGAL_ENV.address, LEGAL_ENV.company, LEGAL_ENV.email, LEGAL_ENV.jurisdiction].sort());
});

test("parseLegalEntity rejects a malformed email and refund window", () => {
  for (const bad of ["not-an-email", "a b@example.com", "<x@example.com>"]) {
    const entity = parseLegalEntity({ ...FULL, [LEGAL_ENV.email]: bad });
    assert.equal(entity.email, null, bad);
    assert.ok(entity.missing.includes(LEGAL_ENV.email), bad);
  }
  for (const bad of ["-1", "1.5", "abc", "3651"]) {
    const entity = parseLegalEntity({ ...FULL, [LEGAL_ENV.refundWindowDays]: bad });
    assert.equal(entity.refundWindowDays, DEFAULT_REFUND_WINDOW_DAYS, bad);
    assert.ok(entity.missing.includes(LEGAL_ENV.refundWindowDays), bad);
  }
  assert.equal(parseLegalEntity({ ...FULL, [LEGAL_ENV.refundWindowDays]: "0" }).refundWindowDays, 0);
});

test("parseLegalEntity collapses whitespace and bounds length", () => {
  const entity = parseLegalEntity({ ...FULL, [LEGAL_ENV.company]: `  Example\n  Ltd. ${"x".repeat(400)}` });
  assert.ok(entity.company.startsWith("Example Ltd. "));
  assert.equal(entity.company.length, 300);
});

test("every content locale has every document with the same structure as English", () => {
  const entity = parseLegalEntity(FULL);
  for (const kind of LEGAL_KINDS) {
    const english = legalDocument(kind, "en-US", entity).document;
    for (const locale of CONTENT_LOCALES) {
      const doc = legalDocument(kind, locale, entity).document;
      assert.equal(doc.sections.length, english.sections.length, `${locale} ${kind}`);
      doc.sections.forEach((section, i) => {
        assert.deepEqual(
          section.blocks.map(b => (typeof b === "string" ? "p" : `ul${b.length}`)),
          english.sections[i].blocks.map(b => (typeof b === "string" ? "p" : `ul${b.length}`)),
          `${locale} ${kind} §${i + 1}`,
        );
      });
    }
  }
});

test("documents state the configured facts and leave no unresolved values", () => {
  const entity = parseLegalEntity(FULL);
  for (const locale of CONTENT_LOCALES) {
    for (const kind of LEGAL_KINDS) {
      const body = text(legalDocument(kind, locale, entity).document);
      assert.ok(body.includes("Example Ltd."), `${locale} ${kind}`);
      assert.ok(body.includes("support@example.com"), `${locale} ${kind}`);
      assert.doesNotMatch(body, /undefined|null|\[object|\$\{/, `${locale} ${kind}`);
    }
    assert.ok(text(legalDocument("refund", locale, entity).document).includes("14"), locale);
    assert.ok(text(legalDocument("terms", locale, entity).document).includes("Singapore"), locale);
  }
});

test("a zero refund window switches credit packages to non-refundable", () => {
  const open = parseLegalEntity(FULL);
  const closed = parseLegalEntity({ ...FULL, [LEGAL_ENV.refundWindowDays]: "0" });
  for (const locale of CONTENT_LOCALES) {
    const a = legalDocument("refund", locale, open).document.sections[0];
    const b = legalDocument("refund", locale, closed).document.sections[0];
    assert.notDeepEqual(a.blocks, b.blocks, locale);
    assert.ok(!b.blocks.flat().join("").includes(" 0 "), locale);
  }
});

test("languages without their own text fall back to English and say so", () => {
  const entity = parseLegalEntity(FULL);
  for (const locale of LOCALES) {
    const resolved = legalDocument("terms", locale, entity);
    const own = CONTENT_LOCALES.includes(locale);
    assert.equal(resolved.fallback, !own, locale);
    assert.equal(resolved.contentLocale, own ? locale : "en-US", locale);
  }
});

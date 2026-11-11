import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { currencyOptions, isSupportedCurrency, SUPPORTED_CURRENCIES } from "./currencies.ts";

test("the currency list is the control plane's list", () => {
  const source = readFileSync(new URL("../../../aiServeWeaveControlPlane/internal/model/currency.go", import.meta.url), "utf8");
  const block = /var SupportedCurrencies = \[\]string\{([^}]*)\}/.exec(source);
  assert.ok(block, "model.SupportedCurrencies not found");
  assert.deepEqual([...SUPPORTED_CURRENCIES], [...block[1].matchAll(/"([A-Z]{3})"/g)].map(match => match[1]));
});

test("isSupportedCurrency accepts provider codes only", () => {
  for (const code of ["USD", "CNY", "EUR", "JPY", "TWD"]) assert.equal(isSupportedCurrency(code), true, code);
  for (const code of ["", "ISO", "CN", "cny", "USDT"]) assert.equal(isSupportedCurrency(code), false, code);
});

test("currencyOptions keeps an unsupported stored code visible", () => {
  assert.equal(currencyOptions("CNY"), SUPPORTED_CURRENCIES);
  assert.equal(currencyOptions(""), SUPPORTED_CURRENCIES);
  assert.deepEqual(currencyOptions("ISO").slice(0, 2), ["ISO", "AED"]);
});

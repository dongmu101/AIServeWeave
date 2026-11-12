import test from "node:test";
import assert from "node:assert/strict";

import { payChoiceKey, payChoices, withPayChoice, type PayChoice } from "./payment-choice.ts";

const both = { dodo: true, ezfp: true, ezfpTypes: ["alipay", "wxpay"] as ("alipay" | "wxpay")[] };

test("choices are Dodo first, then each enabled EZFP type in the control plane's order", () => {
  assert.deepEqual(payChoices(both, { dodoAvailable: true, ezfpAvailable: true }), [
    { provider: "dodo" }, { provider: "ezfp", payType: "alipay" }, { provider: "ezfp", payType: "wxpay" },
  ]);
  assert.deepEqual(payChoices({ ...both, ezfpTypes: ["wxpay"] }, { dodoAvailable: true, ezfpAvailable: true }), [{ provider: "dodo" }, { provider: "ezfp", payType: "wxpay" }]);
  assert.deepEqual(payChoices({ ...both, ezfpTypes: ["wxpay", "alipay"] }, { dodoAvailable: false, ezfpAvailable: true }).map(payChoiceKey), ["ezfp:wxpay", "ezfp:alipay"]);
});

test("a provider counts only when the deployment enables it and the item is payable through it", () => {
  assert.deepEqual(payChoices(both, { dodoAvailable: true, ezfpAvailable: false }), [{ provider: "dodo" }], "a non-CNY item is Dodo only");
  assert.deepEqual(payChoices(both, { dodoAvailable: false, ezfpAvailable: true }).map(payChoiceKey), ["ezfp:alipay", "ezfp:wxpay"], "a CNY item without a Dodo product is EZFP only");
  assert.deepEqual(payChoices({ dodo: false, ezfp: true, ezfpTypes: ["alipay"] }, { dodoAvailable: true, ezfpAvailable: true }).map(payChoiceKey), ["ezfp:alipay"], "Dodo disabled by the operator");
  assert.deepEqual(payChoices({ dodo: true, ezfp: false, ezfpTypes: ["alipay"] }, { dodoAvailable: true, ezfpAvailable: true }), [{ provider: "dodo" }], "EZFP disabled hides its types");
  assert.deepEqual(payChoices({ dodo: true, ezfp: true, ezfpTypes: [] }, { dodoAvailable: false, ezfpAvailable: true }), [], "EZFP on without a type offers nothing");
  assert.deepEqual(payChoices(both, { dodoAvailable: false, ezfpAvailable: false }), []);
});

test("withPayChoice adds provider and pay_type only for EZFP", () => {
  const body = { package_id: "pkg_1" };
  const dodo: PayChoice = { provider: "dodo" };
  assert.deepEqual(withPayChoice(body, dodo), { package_id: "pkg_1" });
  assert.equal("provider" in withPayChoice(body, dodo), false);
  assert.equal("pay_type" in withPayChoice(body, dodo), false);
  assert.deepEqual(withPayChoice(body, undefined), { package_id: "pkg_1" });
  assert.deepEqual(withPayChoice(body, { provider: "ezfp", payType: "wxpay" }), { package_id: "pkg_1", provider: "ezfp", pay_type: "wxpay" });
  assert.deepEqual(body, { package_id: "pkg_1" }, "the input is not mutated");
});

test("choice keys are distinct", () => {
  assert.equal(new Set([{ provider: "dodo" }, { provider: "ezfp", payType: "alipay" }, { provider: "ezfp", payType: "wxpay" }].map(payChoiceKey as (c: unknown) => string)).size, 3);
});

import assert from "node:assert/strict";
import { test } from "node:test";

import { ContractError, errorMessage, greetingName, parseLogin, parseModels, studioErrorCode } from "./contract.ts";

test("parseLogin reads the control plane's login answer", () => {
  const login = parseLogin({
    token: "t",
    expires_at: "2026-11-17T00:00:00Z",
    user: { id: "u", tenant_id: "tn", email: "a@b.c", name: "", role: "member", status: "active" },
  });
  assert.equal(login.token, "t");
  assert.deepEqual(login.user, { id: "u", tenantId: "tn", email: "a@b.c", name: "", role: "member" });
});

test("parseLogin refuses a missing token or an unparseable expiry", () => {
  const user = { id: "u", tenant_id: "tn", email: "", name: "", role: "owner" };
  assert.throws(() => parseLogin({ token: "", expires_at: "2026-11-17T00:00:00Z", user }), ContractError);
  assert.throws(() => parseLogin({ token: "t", expires_at: "soon", user }), ContractError);
  assert.throws(() => parseLogin({ token: "t", expires_at: "2026-11-17T00:00:00Z" }), ContractError);
});

test("parseModels keeps order, drops duplicates and falls back to the id", () => {
  const models = parseModels({ data: [{ id: "a", name: "A" }, { id: "b", name: "" }, { id: "a", name: "again" }] });
  assert.deepEqual(models, [{ id: "a", name: "A" }, { id: "b", name: "b" }]);
  assert.deepEqual(parseModels({ data: null }), []);
  assert.throws(() => parseModels({ data: [{ name: "no id" }] }), ContractError);
  assert.throws(() => parseModels([]), ContractError);
});

test("studioErrorCode reads the code and tolerates any body", () => {
  assert.equal(studioErrorCode('{"error":{"code":"insufficient_balance","message":"x"}}'), "insufficient_balance");
  assert.equal(studioErrorCode('{"error":"upstream"}'), "");
  assert.equal(studioErrorCode("<html>"), "");
});

test("errorMessage never echoes the code it was given", () => {
  assert.equal(errorMessage("insufficient_balance"), "余额不足，请联系管理员充值。");
  assert.equal(errorMessage("<script>"), "出了点问题，请稍后重试。");
});

test("greetingName prefers the name, then the email's local part", () => {
  assert.equal(greetingName({ name: " 小王 ", email: "w@x.y" }), "小王");
  assert.equal(greetingName({ name: "", email: "dong@x.y" }), "dong");
  assert.equal(greetingName({ name: "", email: "" }), "");
});

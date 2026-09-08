import test from "node:test";
import assert from "node:assert/strict";
import { operatorSignIn, operatorSignOut } from "./operator-client.ts";

test("operator login/logout use their own endpoint, preserve passwords and never retry writes", async () => {
  const calls: string[] = [];
  const fake = (async (url, init) => {
    calls.push(`${init?.method} ${url}`);
    if (init?.method === "POST") {
      assert.deepEqual(JSON.parse(String(init.body)), {email: "ops@test", password: "  "});
      return Response.json({operator: {id: "pop_1", email: "ops@test", name: "Ops"}});
    }
    return new Response(null, {status: 204});
  }) as typeof fetch;
  await operatorSignIn("ops@test", "  ", fake);
  await operatorSignOut(fake);
  assert.deepEqual(calls, ["POST /api/operator-session", "DELETE /api/operator-session"]);
  let failures = 0;
  await assert.rejects(operatorSignIn("ops@test", "", (async () => { failures++; return new Response(null, {status: 503}); }) as typeof fetch));
  assert.equal(failures, 1);
});

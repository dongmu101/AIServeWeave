import assert from "node:assert/strict";
import { test } from "node:test";

import { handoffOriginAllowed } from "./handoff-origin.ts";

test("configured origins are the only ones accepted", () => {
  const consoleOrigins = ["https://open7x.com", "https://www.open7x.com"];
  assert.equal(handoffOriginAllowed({ origin: "https://www.open7x.com", secFetchSite: "same-site", consoleOrigins }), true);
  assert.equal(handoffOriginAllowed({ origin: "https://evil.example", secFetchSite: "cross-site", consoleOrigins }), false);
  assert.equal(handoffOriginAllowed({ origin: null, secFetchSite: "same-site", consoleOrigins }), false);
});

test("without configured origins only a same-site post is accepted", () => {
  const consoleOrigins: string[] = [];
  assert.equal(handoffOriginAllowed({ origin: "https://open7x.com", secFetchSite: "same-site", consoleOrigins }), true);
  assert.equal(handoffOriginAllowed({ origin: "https://evil.example", secFetchSite: "cross-site", consoleOrigins }), false);
  assert.equal(handoffOriginAllowed({ origin: null, secFetchSite: null, consoleOrigins }), false);
});

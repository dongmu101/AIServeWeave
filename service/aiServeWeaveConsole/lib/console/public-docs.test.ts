import assert from "node:assert/strict";
import test from "node:test";

import { isPublicDocsPage, isPublicDocsRead } from "./public-docs.ts";

test("isPublicDocsPage covers only the reference pages", () => {
  for (const path of ["/console/docs", "/console/docs/clients", "/console/docs/chat-completions"]) {
    assert.equal(isPublicDocsPage(path), true, path);
  }
  for (const path of ["/console", "/console/docsx", "/console/keys", "/operator/api-docs", "/docs"]) {
    assert.equal(isPublicDocsPage(path), false, path);
  }
});

test("isPublicDocsRead covers only the two reference reads", () => {
  assert.equal(isPublicDocsRead("GET", "/admin/v1/api-docs"), true);
  assert.equal(isPublicDocsRead("GET", "/admin/v1/api-docs/chat-completions"), true);
  assert.equal(isPublicDocsRead("POST", "/admin/v1/api-docs"), false);
  assert.equal(isPublicDocsRead("GET", "/admin/v1/api-docs/a/b"), false);
  assert.equal(isPublicDocsRead("GET", "/admin/v1/apikeys"), false);
  assert.equal(isPublicDocsRead("GET", "/operator/v1/api-docs"), false);
});

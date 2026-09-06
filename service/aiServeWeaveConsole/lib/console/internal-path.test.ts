import assert from "node:assert/strict";
import test from "node:test";

import { DEFAULT_LANDING, internalPath, loginPath } from "./internal-path.ts";

test("internalPath keeps in-site paths and rejects everything else", () => {
  const cases: { name: string; input: string | null | undefined; want: string }[] = [
    { name: "console path", input: "/console/keys", want: "/console/keys" },
    { name: "path with query", input: "/console?tab=all", want: "/console?tab=all" },
    { name: "missing", input: undefined, want: DEFAULT_LANDING },
    { name: "empty", input: "", want: DEFAULT_LANDING },
    { name: "absolute url", input: "https://evil.example/x", want: DEFAULT_LANDING },
    { name: "protocol relative", input: "//evil.example/x", want: DEFAULT_LANDING },
    { name: "backslash form", input: "/\\evil.example", want: DEFAULT_LANDING },
    { name: "relative path", input: "console", want: DEFAULT_LANDING },
    { name: "login itself", input: "/login", want: DEFAULT_LANDING },
    { name: "login with query", input: "/login?next=/console", want: DEFAULT_LANDING },
  ];

  for (const item of cases) {
    assert.equal(
      internalPath(item.input),
      item.want,
      `${item.name}: internalPath(${JSON.stringify(item.input)})`
    );
  }
});

test("loginPath carries only a sanitized return address", () => {
  const cases: { name: string; input: string | null; want: string }[] = [
    { name: "no return address", input: null, want: "/login" },
    { name: "landing needs no parameter", input: "/console", want: "/login" },
    {
      name: "deeper page is preserved",
      input: "/console/keys",
      want: "/login?next=%2Fconsole%2Fkeys",
    },
    { name: "external address is dropped", input: "https://evil.example", want: "/login" },
  ];

  for (const item of cases) {
    assert.equal(
      loginPath(item.input),
      item.want,
      `${item.name}: loginPath(${JSON.stringify(item.input)})`
    );
  }
});

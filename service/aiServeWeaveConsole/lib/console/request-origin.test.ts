import assert from "node:assert/strict";
import test from "node:test";

import { isSafeMethod, isTrustedWrite } from "./request-origin.ts";

test("reads are exempt from the origin check", () => {
  const cases: { name: string; method: string; want: boolean }[] = [
    { name: "GET", method: "GET", want: true },
    { name: "HEAD", method: "HEAD", want: true },
    { name: "OPTIONS", method: "OPTIONS", want: true },
    { name: "lowercase get", method: "get", want: true },
    { name: "POST", method: "POST", want: false },
    { name: "DELETE", method: "DELETE", want: false },
  ];

  for (const item of cases) {
    assert.equal(isSafeMethod(item.method), item.want, item.name);
  }
});

test("a write is trusted only when it came from this origin", () => {
  const cases: {
    name: string;
    method: string;
    origin: string | null;
    host: string | null;
    secFetchSite: string | null;
    want: boolean;
  }[] = [
    {
      name: "same origin over http",
      method: "POST",
      origin: "http://console.example",
      host: "console.example",
      secFetchSite: "same-origin",
      want: true,
    },
    {
      name: "same origin with a port",
      method: "DELETE",
      origin: "http://localhost:3000",
      host: "localhost:3000",
      secFetchSite: null,
      want: true,
    },
    {
      name: "a read passes with no headers at all",
      method: "GET",
      origin: null,
      host: null,
      secFetchSite: null,
      want: true,
    },
    {
      name: "cross-site origin",
      method: "POST",
      origin: "https://evil.example",
      host: "console.example",
      secFetchSite: "cross-site",
      want: false,
    },
    {
      name: "origin matches but the browser says cross-site",
      method: "POST",
      origin: "http://console.example",
      host: "console.example",
      secFetchSite: "cross-site",
      want: false,
    },
    {
      name: "same-site subdomain is still another origin",
      method: "POST",
      origin: "https://other.example.com",
      host: "console.example.com",
      secFetchSite: "same-site",
      want: false,
    },
    {
      name: "no origin header at all",
      method: "POST",
      origin: null,
      host: "console.example",
      secFetchSite: null,
      want: false,
    },
    {
      name: "origin present but host missing",
      method: "POST",
      origin: "http://console.example",
      host: null,
      secFetchSite: null,
      want: false,
    },
    {
      name: "opaque origin",
      method: "POST",
      origin: "null",
      host: "console.example",
      secFetchSite: null,
      want: false,
    },
    {
      name: "unparseable origin",
      method: "POST",
      origin: "not a url",
      host: "console.example",
      secFetchSite: null,
      want: false,
    },
  ];

  for (const item of cases) {
    assert.equal(
      isTrustedWrite({
        method: item.method,
        origin: item.origin,
        host: item.host,
        secFetchSite: item.secFetchSite,
      }),
      item.want,
      `${item.name}: ${item.method} origin=${item.origin} host=${item.host}`
    );
  }
});

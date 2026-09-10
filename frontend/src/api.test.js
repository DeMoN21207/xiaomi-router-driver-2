import test from "node:test";
import assert from "node:assert/strict";

import { applyRules } from "./api.js";

test("applyRules waits for the background operation", async () => {
  const requests = [];
  const responses = [
    { operation: { id: "apply_1", status: "queued" } },
    { operation: { id: "apply_1", status: "running" } },
    { operation: { id: "apply_1", status: "succeeded", result: { status: "applied", rulesApplied: 2 } } },
  ];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url, options = {}) => {
    requests.push([url, options.method || "GET"]);
    return { ok: true, json: async () => responses.shift() };
  };
  try {
    const result = await applyRules({ pollIntervalMs: 0 });
    assert.equal(result.status, "applied");
    assert.deepEqual(requests, [
      ["/api/rules/apply", "POST"],
      ["/api/rules/apply/apply_1", "GET"],
      ["/api/rules/apply/apply_1", "GET"],
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("applyRules reports a failed background operation", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) => ({
    ok: true,
    json: async () => url === "/api/rules/apply"
      ? { operation: { id: "apply_2", status: "queued" } }
      : { operation: { id: "apply_2", status: "failed", error: "routing failed" } },
  });
  try {
    await assert.rejects(() => applyRules({ pollIntervalMs: 0 }), /routing failed/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

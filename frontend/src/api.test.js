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

test("fetchJSON aborts a request after its deadline", async () => {
	const { fetchJSON } = await import("./api.js");
	const originalFetch = globalThis.fetch;
	globalThis.fetch = async (_url, options) => new Promise((_resolve, reject) => {
		options.signal.addEventListener("abort", () => reject(options.signal.reason), { once: true });
	});
	try {
		await assert.rejects(() => fetchJSON("/api/stuck", { timeoutMs: 10 }), /время ожидания/i);
	} finally {
		globalThis.fetch = originalFetch;
	}
});

test("refreshSubscription allows the full subscription refresh window", async () => {
	const api = await import("./api.js");
	assert.equal(typeof api.refreshSubscription, "function", "refreshSubscription must be exported");

	const originalFetch = globalThis.fetch;
	const originalSetTimeout = globalThis.setTimeout;
	const observedTimeouts = [];
	let observedRequest;
	globalThis.setTimeout = (callback, delay, ...args) => {
		observedTimeouts.push(delay);
		return originalSetTimeout(callback, 60_000, ...args);
	};
	globalThis.fetch = async (url, options = {}) => {
		observedRequest = { url, method: options.method || "GET" };
		return { ok: true, json: async () => ({ status: "refreshed", entries: 81 }) };
	};

	try {
		const result = await api.refreshSubscription("provider/sub");
		assert.equal(result.status, "refreshed");
		assert.deepEqual(observedRequest, {
			url: "/api/providers/provider%2Fsub/refresh",
			method: "POST",
		});
		assert.ok(observedTimeouts.includes(5 * 60 * 1000), `timeouts = ${observedTimeouts.join(", ")}`);
	} finally {
		globalThis.fetch = originalFetch;
		globalThis.setTimeout = originalSetTimeout;
	}
});

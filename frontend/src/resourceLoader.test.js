import test from "node:test";
import assert from "node:assert/strict";
import { createResourceLoader, DEFAULT_RESOURCE_TIMEOUT_MS } from "./resourceLoader.js";

test("resource loaders allow slow USB history reads", () => {
  assert.equal(DEFAULT_RESOURCE_TIMEOUT_MS, 30_000);
});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function record() {
  const data = [], errors = [];
  let settled = 0;
  return { data, errors, get settled() { return settled; }, callbacks: {
    onData: (value) => data.push(value), onError: (error) => errors.push(error), onSettled: () => settled++,
  } };
}

test("slow history neither delays status/events nor prevents the next status refresh", async () => {
  const historyResponse = deferred();
  const status = record(), events = record(), history = record();
  let poll = 0;
  const statusLoader = createResourceLoader("/status", status.callbacks, { fetcher: async () => ++poll });
  const eventsLoader = createResourceLoader("/events", events.callbacks, { fetcher: async () => ["event"] });
  const historyLoader = createResourceLoader("/history", history.callbacks, { fetcher: () => historyResponse.promise });
  const pendingHistory = historyLoader.refresh();
  await Promise.all([statusLoader.refresh(), eventsLoader.refresh()]);
  assert.deepEqual(status.data, [1]);
  assert.deepEqual(events.data, [["event"]]);
  assert.equal(history.settled, 0);
  await statusLoader.refresh();
  assert.deepEqual(status.data, [1, 2]);
  historyResponse.reject(new Error("history unavailable"));
  await pendingHistory;
  assert.equal(history.errors.length, 1);
  assert.equal(status.errors.length, 0);
  assert.deepEqual(status.data, [1, 2]);
});

test("overlapping refreshes share the request for that resource", async () => {
  const response = deferred(), output = record();
  let calls = 0;
  const loader = createResourceLoader("/status", output.callbacks, { fetcher: () => { calls++; return response.promise; } });
  const first = loader.refresh(), second = loader.refresh();
  assert.equal(first, second);
  assert.equal(calls, 1);
  response.resolve({ up: true });
  await first;
  assert.equal(output.data.length, 1);
});

test("a timed out request is aborted and a later refresh can recover", async () => {
  const output = record();
  let signal;
  let calls = 0;
  const loader = createResourceLoader("/status", output.callbacks, {
    timeoutMs: 15,
    fetcher: async (_url, options) => {
      if (++calls > 1) return "recovered";
      signal = options.signal;
      return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true }));
    },
  });
  await loader.refresh();
  assert.equal(signal.aborted, true);
  assert.equal(output.errors.length, 1);
  assert.equal(output.settled, 1);
  await loader.refresh();
  assert.deepEqual(output.data, ["recovered"]);
  assert.equal(output.settled, 2);
});

test("changing period/unmount aborts requests and ignores late results", async () => {
  const response = deferred(), output = record();
  let signal;
  const loader = createResourceLoader("/history?range=7d", output.callbacks, { fetcher: (_url, options) => {
    signal = options.signal;
    return response.promise; // Simulate completion racing with cancellation.
  } });
  const first = loader.refresh();
  loader.dispose();
  assert.equal(signal.aborted, true);
  response.resolve("old period");
  await first;
  await loader.refresh();
  assert.deepEqual(output.data, []);
  assert.deepEqual(output.errors, []);
  assert.equal(output.settled, 0);
});

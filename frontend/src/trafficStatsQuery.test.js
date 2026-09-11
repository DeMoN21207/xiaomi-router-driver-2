import test from "node:test";
import assert from "node:assert/strict";

import {
  appendTrafficRangeParams,
  buildDeviceHistoryURL,
  buildDevicesURL,
  buildSelectedDeviceURL,
  buildSitesURL,
  isAbortError,
} from "./trafficStatsQuery.js";

test("sites URL stays on live endpoint for every filter combination", () => {
  const cases = [
    {
      name: "defaults",
      input: {},
      want: "/api/traffic/sites?sort=bytes&page=1&pageSize=20",
    },
    {
      name: "scope tunneled",
      input: { scope: "tunneled" },
      want: "/api/traffic/sites?sort=bytes&scope=tunneled&page=1&pageSize=20",
    },
    {
      name: "scope direct",
      input: { scope: "direct" },
      want: "/api/traffic/sites?sort=bytes&scope=direct&page=1&pageSize=20",
    },
    {
      name: "search",
      input: { search: "YouTube" },
      want: "/api/traffic/sites?sort=bytes&page=1&pageSize=20&query=YouTube",
    },
    {
      name: "device filter does not switch to history",
      input: { sourceIp: "192.168.31.10", scope: "tunneled", search: "alpha", page: 2, sortBy: "domain", sortDir: "asc" },
      want: "/api/traffic/sites?sort=domain&order=asc&scope=tunneled&page=2&pageSize=20&query=alpha&sourceIp=192.168.31.10",
    },
    {
      name: "updated desc omits order param",
      input: { sortBy: "updated", sortDir: "desc", page: 3 },
      want: "/api/traffic/sites?sort=updated&page=3&pageSize=20",
    },
    {
      name: "packets asc",
      input: { sortBy: "packets", sortDir: "asc" },
      want: "/api/traffic/sites?sort=packets&order=asc&page=1&pageSize=20",
    },
  ];

  for (const item of cases) {
    assert.equal(buildSitesURL(item.input), item.want, item.name);
  }
});

test("device option URLs ignore site scope and range", () => {
  assert.equal(buildDevicesURL(), "/api/traffic/devices?page=1&pageSize=1&siteLimit=0");
  assert.equal(buildSelectedDeviceURL(""), "");
  assert.equal(
    buildSelectedDeviceURL("192.168.31.20"),
    "/api/traffic/devices?sourceIp=192.168.31.20&page=1&pageSize=1&siteLimit=5",
  );
});

test("device history range params cover presets and incomplete custom range", () => {
  assert.equal(
    buildDeviceHistoryURL("192.168.31.10", "7d"),
    "/api/traffic/devices/history?sourceIp=192.168.31.10&range=7d",
  );
  assert.equal(
    buildDeviceHistoryURL("192.168.31.10", "custom", "", ""),
    "/api/traffic/devices/history?sourceIp=192.168.31.10&range=1h",
  );
  const query = appendTrafficRangeParams(new URLSearchParams(), "custom", "2026-03-26T12:00", "2026-03-26T13:00");
  assert.equal(query.get("from"), new Date("2026-03-26T12:00").toISOString());
  assert.equal(query.get("to"), new Date("2026-03-26T13:00").toISOString());
  assert.equal(query.get("range"), null);
});

test("abort errors are ignored by traffic refresh", () => {
  assert.equal(isAbortError({ name: "AbortError" }), true);
  assert.equal(isAbortError(new Error("The operation was aborted")), true);
  assert.equal(isAbortError(new Error("routing failed")), false);
});

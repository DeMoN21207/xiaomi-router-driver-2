export function buildSitesURL({
  sortBy = "bytes",
  sortDir = "desc",
  scope = "",
  page = 1,
  pageSize = 20,
  search = "",
  sourceIp = "",
} = {}) {
  const query = new URLSearchParams();
  query.set("sort", sortBy || "bytes");
  if (sortDir && sortDir !== "desc") {
    query.set("order", sortDir);
  }
  if (scope) {
    query.set("scope", scope);
  }
  query.set("page", String(page > 0 ? page : 1));
  query.set("pageSize", String(pageSize > 0 ? pageSize : 20));
  if (search) {
    query.set("query", search);
  }
  if (sourceIp) {
    query.set("sourceIp", sourceIp);
  }
  return `/api/traffic/sites?${query.toString()}`;
}

export function buildDevicesURL() {
  const query = new URLSearchParams();
  query.set("page", "1");
  query.set("pageSize", "1");
  query.set("siteLimit", "0");
  return `/api/traffic/devices?${query.toString()}`;
}

export function buildSelectedDeviceURL(sourceIp, siteLimit = 5) {
  if (!sourceIp) {
    return "";
  }
  const query = new URLSearchParams();
  query.set("sourceIp", sourceIp);
  query.set("page", "1");
  query.set("pageSize", "1");
  query.set("siteLimit", String(siteLimit));
  return `/api/traffic/devices?${query.toString()}`;
}

export function buildDeviceHistoryURL(sourceIp, range = "1h", from = "", to = "") {
  if (!sourceIp) {
    return "";
  }
  const query = new URLSearchParams();
  query.set("sourceIp", sourceIp);
  appendTrafficRangeParams(query, range, from, to);
  return `/api/traffic/devices/history?${query.toString()}`;
}

export function appendTrafficRangeParams(query, range, from, to) {
  if (range === "custom") {
    if (from && to) {
      const toISO = (value) => (String(value).includes("T") ? new Date(value).toISOString() : value);
      query.set("from", toISO(from));
      query.set("to", toISO(to));
      return query;
    }
    query.set("range", "1h");
    return query;
  }
  query.set("range", range || "1h");
  return query;
}

export function isAbortError(error) {
  return error?.name === "AbortError" || /aborted|abort/i.test(String(error?.message || ""));
}

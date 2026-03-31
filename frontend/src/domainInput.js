const DOMAIN_PATTERN = /^[a-z0-9.-]+$/;
const IPV4_OR_CIDR_PATTERN = /^(?<ip>(?:\d{1,3}\.){3}\d{1,3})(?:\/(?<prefix>\d{1,2}))?$/;

function normalizeIPv4Candidate(value) {
  const match = String(value || "").match(IPV4_OR_CIDR_PATTERN);
  if (!match?.groups) {
    return "";
  }

  const octets = match.groups.ip.split(".").map((item) => Number(item));
  if (octets.length !== 4 || octets.some((item) => Number.isNaN(item) || item < 0 || item > 255)) {
    return "";
  }

  const normalizedIP = octets.join(".");
  if (!match.groups.prefix) {
    return normalizedIP;
  }

  const prefix = Number(match.groups.prefix);
  if (!Number.isInteger(prefix) || prefix < 0 || prefix > 32) {
    return "";
  }

  return `${normalizedIP}/${prefix}`;
}

export function sanitizeDomainCandidate(value) {
  let normalized = String(value || "").toLowerCase().trim();
  if (!normalized) {
    return "";
  }

  normalized = normalized.replace(/^https?:\/\//, "");

  const commentIndex = normalized.indexOf("#");
  if (commentIndex >= 0) {
    normalized = normalized.slice(0, commentIndex);
  }

  const authIndex = normalized.lastIndexOf("@");
  if (authIndex >= 0) {
    normalized = normalized.slice(authIndex + 1);
  }

  const portMatch = normalized.match(/^([^/]+):([0-9]+)$/);
  if (portMatch) {
    normalized = portMatch[1];
  }

  const ipEntry = normalizeIPv4Candidate(normalized);
  if (ipEntry) {
    return ipEntry;
  }

  const slashIndex = normalized.indexOf("/");
  if (slashIndex >= 0) {
    normalized = normalized.slice(0, slashIndex);
  }

  const labels = normalized
    .split(".")
    .map((label) => label.trim().replace(/^\.+|\.+$/g, ""))
    .filter(Boolean)
    .filter((label) => !label.includes("*"));

  normalized = labels.join(".");
  if (!normalized || normalized.includes("..") || !DOMAIN_PATTERN.test(normalized)) {
    return "";
  }

  return normalized;
}

export function parseDomainInput(raw) {
  const unique = new Set();

  for (const line of String(raw || "").split(/\r?\n/)) {
    const withoutComment = line.replace(/#.*$/, "");
    for (const token of withoutComment.split(/[\s,;]+/)) {
      const normalized = sanitizeDomainCandidate(token);
      if (normalized) {
        unique.add(normalized);
      }
    }
  }

  return Array.from(unique);
}

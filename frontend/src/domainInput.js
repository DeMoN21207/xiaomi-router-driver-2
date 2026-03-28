const DOMAIN_PATTERN = /^[a-z0-9.-]+$/;

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

  const slashIndex = normalized.indexOf("/");
  if (slashIndex >= 0) {
    normalized = normalized.slice(0, slashIndex);
  }

  if (/:[0-9]+$/.test(normalized)) {
    normalized = normalized.replace(/:[0-9]+$/, "");
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

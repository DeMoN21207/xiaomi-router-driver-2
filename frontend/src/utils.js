export function formatDate(value) {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return new Intl.DateTimeFormat("ru-RU", {
    day: "2-digit",
    month: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(d);
}

export function formatDateFull(value) {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return new Intl.DateTimeFormat("ru-RU", {
    day: "2-digit",
    month: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(d);
}

export function formatLatencyMs(value, t) {
  return value > 0 ? `${value} ${t("common.ms")}` : t("common.noData");
}

export function formatBytes(value) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";

  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let index = 0;

  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }

  const digits = size >= 100 || index === 0 ? 0 : size >= 10 ? 1 : 2;
  return `${size.toFixed(digits)} ${units[index]}`;
}

export function formatBytesPerSecond(value) {
  return `${formatBytes(value)}/s`;
}

export function levelBadge(level) {
  switch (level) {
    case "info":
      return "bg-secondary/10 text-secondary";
    case "warn":
      return "bg-tertiary/10 text-tertiary";
    case "error":
      return "bg-error/10 text-error";
    default:
      return "bg-outline-variant/20 text-outline";
  }
}

export function levelIcon(level) {
  switch (level) {
    case "info":
      return "info";
    case "warn":
      return "warning";
    case "error":
      return "error";
    default:
      return "pending";
  }
}

export function levelIconColor(level) {
  switch (level) {
    case "info":
      return "text-secondary";
    case "warn":
      return "text-tertiary";
    case "error":
      return "text-error";
    default:
      return "text-outline";
  }
}

export function accentTextClass(accent) {
  switch (accent) {
    case "primary":
      return "text-primary";
    case "secondary":
      return "text-secondary";
    case "tertiary":
      return "text-tertiary";
    case "error":
      return "text-error";
    default:
      return "text-outline";
  }
}

export function statusToneClasses(tone) {
  switch (tone) {
    case "secondary":
      return {
        badge: "bg-secondary/10 text-secondary border-secondary/20",
        dot: "bg-secondary",
      };
    case "tertiary":
      return {
        badge: "bg-tertiary/10 text-tertiary border-tertiary/20",
        dot: "bg-tertiary",
      };
    case "error":
      return {
        badge: "bg-error/10 text-error border-error/20",
        dot: "bg-error",
      };
    default:
      return {
        badge: "bg-outline-variant/20 text-outline border-outline-variant/20",
        dot: "bg-outline",
      };
  }
}

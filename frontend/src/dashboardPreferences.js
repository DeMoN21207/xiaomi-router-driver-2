const DASHBOARD_REFRESH_KEY = "routevpn_dashboard_refresh_ms";
const DEFAULT_DASHBOARD_REFRESH_MS = 10_000;

export const DASHBOARD_REFRESH_OPTIONS = [0, 500, 1_000, 5_000, 10_000, 30_000, 60_000];

export function readDashboardRefreshInterval() {
  try {
    const raw = Number.parseInt(localStorage.getItem(DASHBOARD_REFRESH_KEY) || "", 10);
    if (DASHBOARD_REFRESH_OPTIONS.includes(raw)) {
      return raw;
    }
  } catch {
    // Ignore localStorage access issues and fall back to default.
  }

  return DEFAULT_DASHBOARD_REFRESH_MS;
}

export function writeDashboardRefreshInterval(value) {
  const nextValue = DASHBOARD_REFRESH_OPTIONS.includes(value) ? value : DEFAULT_DASHBOARD_REFRESH_MS;
  try {
    localStorage.setItem(DASHBOARD_REFRESH_KEY, String(nextValue));
  } catch {
    // Ignore localStorage access issues for non-persistent environments.
  }
  return nextValue;
}


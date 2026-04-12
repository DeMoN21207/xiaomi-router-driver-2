let inFlight = 0;
const listeners = new Set();

function emit() {
  for (const fn of listeners) {
    try { fn(inFlight); } catch { /* ignore */ }
  }
}

export function subscribeLoading(fn) {
  listeners.add(fn);
  fn(inFlight);
  return () => { listeners.delete(fn); };
}

export async function fetchJSON(url, options) {
  // Only mutating requests (saves / heavy actions) toggle the global loader.
  // Passive GETs used for initial page rendering should not flash an indicator.
  const method = (options?.method || "GET").toUpperCase();
  const track = method !== "GET" && method !== "HEAD";
  if (track) {
    inFlight += 1;
    emit();
  }
  try {
    const response = await fetch(url, options);
    if (response.ok) return response.json();

    let message = "Запрос завершился ошибкой";
    try {
      const payload = await response.json();
      message = payload.error || message;
    } catch {
      // ignore malformed response body
    }

    throw new Error(message);
  } finally {
    if (track) {
      inFlight = Math.max(0, inFlight - 1);
      emit();
    }
  }
}

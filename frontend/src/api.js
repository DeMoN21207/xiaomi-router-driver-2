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
  const { trackLoading, ...fetchOptions } = options || {};
  // Only mutating requests (saves / heavy actions) toggle the global loader.
  // Passive GETs used for initial page rendering should not flash an indicator.
  const method = (fetchOptions?.method || "GET").toUpperCase();
  const track = trackLoading === undefined ? method !== "GET" && method !== "HEAD" : Boolean(trackLoading);
  if (track) {
    inFlight += 1;
    emit();
  }
	try {
		const authorizedOptions = withAPIToken(fetchOptions);
		let response = await fetch(url, authorizedOptions);
		if (response.status === 401 && typeof window !== "undefined" && typeof window.prompt === "function") {
			const token = window.prompt("Введите API-токен роутера");
			if (token?.trim()) {
				storeAPIToken(token.trim());
				response = await fetch(url, withAPIToken(fetchOptions));
			}
		}
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

function readAPIToken() {
	try {
		return globalThis.sessionStorage?.getItem("vpn-manager-api-token") || "";
	} catch {
		return "";
	}
}

function storeAPIToken(token) {
	try {
		globalThis.sessionStorage?.setItem("vpn-manager-api-token", token);
	} catch {
		// Session storage may be unavailable in restricted browser contexts.
	}
}

function withAPIToken(options = {}) {
	const token = readAPIToken();
	if (!token) return options;
	const headers = new Headers(options.headers || {});
	headers.set("Authorization", `Bearer ${token}`);
	return { ...options, headers };
}

export async function applyRules({ pollIntervalMs = 500, timeoutMs = 5 * 60 * 1000 } = {}) {
	const started = await fetchJSON("/api/rules/apply", { method: "POST" });
	const operationId = started?.operation?.id;
	if (!operationId) return started;

	const deadline = Date.now() + timeoutMs;
	while (Date.now() <= deadline) {
		if (pollIntervalMs > 0) {
			await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
		}
		const payload = await fetchJSON(`/api/rules/apply/${encodeURIComponent(operationId)}`, {
			trackLoading: false,
		});
		const operation = payload?.operation;
		if (operation?.status === "succeeded") return operation.result || { status: "applied" };
		if (operation?.status === "failed") throw new Error(operation.error || "Не удалось применить правила");
	}
	throw new Error("Применение правил не завершилось за отведённое время");
}

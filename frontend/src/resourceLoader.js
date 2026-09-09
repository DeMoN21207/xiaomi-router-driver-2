import { fetchJSON } from "./api.js";

// Each resource has its own request, deadline and refresh lock. A slow history
// request must not hold up status, events or the next status poll.
export function createResourceLoader(url, { onData, onError, onSettled }, { fetcher = fetchJSON, timeoutMs = 8000 } = {}) {
  let disposed = false;
  let pending = null;

  function refresh() {
    if (disposed) return Promise.resolve();
    if (pending) return pending.promise;
    const controller = new AbortController();
    const request = { controller };
    pending = request;
    const timer = setTimeout(() => controller.abort(new Error("Превышено время ожидания данных. Повторите обновление.")), timeoutMs);
    request.promise = (async () => {
      try {
        const data = await fetcher(url, { signal: controller.signal });
        if (!disposed && !controller.signal.aborted) onData(data);
      } catch (error) {
        if (!disposed) onError(error);
      } finally {
        clearTimeout(timer);
        pending = null;
        if (!disposed) onSettled();
      }
    })();
    return request.promise;
  }

  function dispose() {
    disposed = true;
    pending?.controller.abort();
  }

  return { refresh, dispose };
}

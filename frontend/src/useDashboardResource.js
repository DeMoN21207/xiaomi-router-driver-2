import { useCallback, useEffect, useRef, useState } from "react";
import { createResourceLoader } from "./resourceLoader.js";

export function useDashboardResource(url, intervalMs) {
  const [state, setState] = useState({ url, data: null, loading: true, error: "" });
  const loaderRef = useRef(null);

  useEffect(() => {
    setState({ url, data: null, loading: true, error: "" });
    const loader = createResourceLoader(url, {
      onData: (data) => setState((prev) => ({ ...prev, data, error: "" })),
      onError: (error) => setState((prev) => ({ ...prev, error: error.message })),
      onSettled: () => setState((prev) => ({ ...prev, loading: false })),
    });
    loaderRef.current = loader;
    void loader.refresh();
    return () => {
      loader.dispose();
      loaderRef.current = null;
    };
  }, [url]);

  useEffect(() => {
    if (intervalMs <= 0) return undefined;
    const timer = window.setInterval(() => {
      if (document.visibilityState !== "hidden") void loaderRef.current?.refresh();
    }, intervalMs);
    return () => window.clearInterval(timer);
  }, [intervalMs]);

  const refresh = useCallback(() => loaderRef.current?.refresh() ?? Promise.resolve(), []);
  // A changed period must never display values from the previous period.
  const current = state.url === url ? state : { data: null, loading: true, error: "" };
  return { ...current, refresh };
}

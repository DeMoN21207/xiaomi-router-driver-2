import { useEffect, useRef, useState } from "react";
import { subscribeLoading } from "../api.js";

export default function GlobalProgress() {
  const [active, setActive] = useState(false);
  const activeRef = useRef(false);

  useEffect(() => {
    // Delay showing the indicator so truly fast operations (< 250ms) stay silent.
    // Only longer saves / heavy actions will actually surface the spinner.
    let showTimer = null;
    const setVisible = (value) => {
      activeRef.current = value;
      setActive(value);
    };
    const unsub = subscribeLoading((count) => {
      if (count > 0) {
        if (!showTimer && !activeRef.current) {
          showTimer = setTimeout(() => {
            showTimer = null;
            setVisible(true);
          }, 250);
        }
      } else {
        if (showTimer) { clearTimeout(showTimer); showTimer = null; }
        setVisible(false);
      }
    });
    return () => {
      unsub();
      if (showTimer) clearTimeout(showTimer);
    };
  }, []);

  return (
    <>
      {/* Top progress bar */}
      <div
        className={`pointer-events-none fixed top-0 left-0 right-0 z-[100] h-0.5 overflow-hidden transition-opacity duration-200 ${
          active ? "opacity-100" : "opacity-0"
        }`}
      >
        <div className="progress-stripe h-full w-full bg-gradient-to-r from-primary/20 via-primary to-primary/20" />
      </div>

      {/* Floating spinner pill */}
      {active && (
        <div className="pointer-events-none fixed bottom-6 left-1/2 z-[100] flex -translate-x-1/2 items-center gap-2 rounded-full border border-primary/30 bg-surface-container-high/95 px-4 py-2 text-xs font-medium text-on-surface shadow-2xl backdrop-blur">
          <svg className="h-4 w-4 animate-spin text-primary" viewBox="0 0 24 24" fill="none">
            <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="3" strokeOpacity="0.25" />
            <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />
          </svg>
          <span className="text-on-surface-variant">Обработка запроса…</span>
        </div>
      )}
    </>
  );
}

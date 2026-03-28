import { useEffect, useState, useMemo, useCallback } from "react";
import { fetchJSON } from "../api.js";
import { useI18n } from "../i18n.jsx";
import Icon from "../components/Icon.jsx";
import InlineNotice from "../components/InlineNotice.jsx";
import { formatDateFull, levelBadge, levelIcon, levelIconColor } from "../utils.js";

const PAGE_SIZE = 25;

export default function EventsPage() {
  const { t } = useI18n();
  const [events, setEvents] = useState([]);
  const [total, setTotal] = useState(0);
  const [filter, setFilter] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [clearing, setClearing] = useState(false);
  const [error, setError] = useState("");

  const levelFilters = useMemo(
    () => [
      { value: "", label: t("events.all"), icon: "filter_list", color: "primary" },
      { value: "info", label: t("events.info"), icon: "info", color: "secondary" },
      { value: "warn", label: t("events.warnings"), icon: "warning", color: "warning" },
      { value: "error", label: t("events.errors"), icon: "error", color: "error" },
    ],
    [t],
  );

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const data = await fetchJSON(`/api/events?limit=${PAGE_SIZE}&offset=0`);
      setEvents(data.events || []);
      setTotal(data.total || 0);
      setError("");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function loadMore() {
    setLoadingMore(true);
    try {
      const data = await fetchJSON(`/api/events?limit=${PAGE_SIZE}&offset=${events.length}`);
      const newEvents = data.events || [];
      setEvents((prev) => [...prev, ...newEvents]);
      setTotal(data.total || 0);
      setError("");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoadingMore(false);
    }
  }

  async function clearAll() {
    setClearing(true);
    try {
      await fetchJSON("/api/events", { method: "DELETE" });
      setEvents([]);
      setTotal(0);
      setError("");
    } catch (err) {
      setError(err.message);
    } finally {
      setClearing(false);
    }
  }

  const filtered = filter ? events.filter((event) => event.level === filter) : events;
  const hasMore = events.length < total;

  return (
    <div className="space-y-8">
      <div className="flex flex-col justify-between gap-6 md:flex-row md:items-end">
        <div>
          <h1 className="mb-2 font-headline text-4xl font-black tracking-tight">{t("events.title")}</h1>
          <div className="flex items-center gap-2">
            <span className="relative flex h-2 w-2">
              <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-secondary opacity-75" />
              <span className="status-glow-success relative inline-flex h-2 w-2 rounded-full bg-secondary" />
            </span>
            <p className="font-mono text-xs uppercase tracking-widest text-secondary">
              {loading ? t("events.loading") : `${filtered.length} / ${total} ${t("events.entries")}`}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          {events.length > 0 && (
            <button
              type="button"
              onClick={clearAll}
              disabled={clearing}
              className="flex items-center gap-2 rounded-lg border border-error/20 px-4 py-2 text-xs font-bold uppercase tracking-wider text-error transition-colors duration-100 hover:bg-error/10 active:scale-95 disabled:opacity-50"
            >
              <Icon name="delete_sweep" className="h-4 w-4" />
              {clearing ? t("events.clearing") : t("events.clear")}
            </button>
          )}
          <button
            type="button"
            onClick={refresh}
            className="flex items-center gap-2 rounded-lg border border-primary/20 px-4 py-2 text-xs font-bold uppercase tracking-wider text-primary transition-colors duration-100 hover:bg-primary/5 hover:text-on-surface active:scale-95"
          >
            <Icon name="refresh" className="h-4 w-4" />
            {t("events.refresh")}
          </button>
        </div>
      </div>

      {error ? <InlineNotice tone="error" title={t("error.events")} message={error} /> : null}

      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        {levelFilters.map((item) => {
          const isActive = filter === item.value;
          const colorMap = {
            primary: { active: "border-primary/30 bg-primary/10", icon: "text-primary", text: "text-primary" },
            secondary: { active: "border-secondary/30 bg-secondary/10", icon: "text-secondary", text: "text-secondary" },
            warning: { active: "border-tertiary/30 bg-tertiary/10", icon: "text-tertiary", text: "text-tertiary" },
            error: { active: "border-error/30 bg-error/10", icon: "text-error", text: "text-error" },
          };
          const c = colorMap[item.color];
          return (
            <button
              key={item.value}
              type="button"
              onClick={() => setFilter(item.value)}
              className={`rounded-xl p-4 text-left transition-colors ${
                isActive ? `border ${c.active}` : "border border-transparent bg-surface-container-low hover:bg-surface-container"
              }`}
            >
              <div className="mb-2 flex items-center gap-2">
                <Icon name={item.icon} className={`h-5 w-5 ${isActive ? c.icon : "text-outline"}`} />
                <span className="font-headline text-[10px] uppercase tracking-widest text-outline">{t("events.filter")}</span>
              </div>
              <span className={`font-headline font-bold ${isActive ? c.text : "text-on-surface"}`}>{item.label}</span>
            </button>
          );
        })}
      </div>

      <div className="space-y-3">
        {filtered.length === 0 ? (
          <div className="rounded-xl bg-surface-container-low p-12 text-center">
            <Icon name="history" className="mx-auto mb-3 h-12 w-12 text-outline-variant" />
            <p className="text-on-surface-variant">{filter ? t("events.emptyFiltered") : t("events.empty")}</p>
          </div>
        ) : (
          filtered.map((event) => (
            <div key={event.id} className="rounded-xl border border-outline-variant/10 bg-surface-container-low p-5 transition-colors hover:bg-surface-container">
              <div className="mb-2 flex flex-col justify-between gap-3 sm:flex-row sm:items-center">
                <div className="flex items-center gap-3">
                  <Icon name={levelIcon(event.level)} className={`h-5 w-5 ${levelIconColor(event.level)}`} />
                  <span className={`rounded-full px-2.5 py-1 text-[10px] font-bold uppercase tracking-widest ${levelBadge(event.level)}`}>
                    {levelLabel(event.level, t)}
                  </span>
                  <span className="font-headline font-bold text-on-surface">{t(`kind.${event.kind}`) || event.kind}</span>
                </div>
                <span className="font-mono text-xs text-outline">{formatDateFull(event.occurredAt)}</span>
              </div>
              <p className="ml-9 text-sm text-on-surface-variant">{event.message}</p>
            </div>
          ))
        )}

        {hasMore && !filter && (
          <button
            type="button"
            onClick={loadMore}
            disabled={loadingMore}
            className="flex w-full items-center justify-center gap-2 rounded-xl border border-outline-variant/20 bg-surface-container-low py-4 text-sm font-medium text-primary transition-colors hover:bg-surface-container disabled:opacity-50"
          >
            {loadingMore ? (
              <>
                <Icon name="hourglass_empty" className="h-4 w-4 animate-spin" />
                {t("events.loadingMore")}
              </>
            ) : (
              <>
                <Icon name="expand_more" className="h-5 w-5" />
                {t("events.loadMore", { count: String(Math.min(PAGE_SIZE, total - events.length)) })}
              </>
            )}
          </button>
        )}
      </div>
    </div>
  );
}

function levelLabel(level, t) {
  switch (level) {
    case "info":
      return t("events.levelInfo");
    case "warn":
      return t("events.levelWarn");
    case "error":
      return t("events.levelError");
    default:
      return t("events.levelDefault");
  }
}

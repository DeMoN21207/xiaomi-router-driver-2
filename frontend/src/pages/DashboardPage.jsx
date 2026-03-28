import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { fetchJSON } from "../api.js";
import { readDashboardRefreshInterval } from "../dashboardPreferences.js";
import { useI18n } from "../i18n.jsx";
import Icon from "../components/Icon.jsx";
import InlineNotice from "../components/InlineNotice.jsx";
import { accentTextClass, formatBytes, formatDate, formatLatencyMs, levelBadge, statusToneClasses } from "../utils.js";

const TRAFFIC_HISTORY_RANGES = ["1h", "3h", "1d", "3d", "7d", "30d"];

export default function DashboardPage() {
  const { t } = useI18n();
  const [status, setStatus] = useState(null);
  const [config, setConfig] = useState(null);
  const [events, setEvents] = useState([]);
  const [trafficHistory, setTrafficHistory] = useState(null);
  const [historyRange, setHistoryRange] = useState("7d");
  const [customFrom, setCustomFrom] = useState("");
  const [customTo, setCustomTo] = useState("");
  const [autoRefreshMs, setAutoRefreshMs] = useState(() => readDashboardRefreshInterval());
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refreshInFlightRef = useRef(false);

  const buildTrafficHistoryUrl = useCallback((rangeKey, from, to) => {
    if (rangeKey === "custom" && from && to) {
      const toISO = (v) => v.includes("T") ? new Date(v).toISOString() : v;
      return `/api/traffic/history?from=${encodeURIComponent(toISO(from))}&to=${encodeURIComponent(toISO(to))}`;
    }
    return `/api/traffic/history?range=${encodeURIComponent(rangeKey)}`;
  }, []);

  const refresh = useCallback(async (initial = false, rangeKey = historyRange, from = customFrom, to = customTo, showBusy = false) => {
    if (!initial && refreshInFlightRef.current) {
      return;
    }

    refreshInFlightRef.current = true;
    if (initial) setLoading(true);
    else if (showBusy) setBusy(true);
    try {
      const [nextStatus, nextConfig, nextEvents, nextTrafficHistory] = await Promise.all([
        fetchJSON("/api/status"),
        fetchJSON("/api/config"),
        fetchJSON("/api/events?limit=6"),
        fetchJSON(buildTrafficHistoryUrl(rangeKey, from, to)),
      ]);
      setStatus(nextStatus);
      setConfig(nextConfig);
      setEvents(nextEvents.events || []);
      setTrafficHistory(nextTrafficHistory);
      setError("");
    } catch (err) {
      setError(err.message);
    } finally {
      refreshInFlightRef.current = false;
      if (initial) setLoading(false);
      else if (showBusy) setBusy(false);
    }
  }, [historyRange, customFrom, customTo, buildTrafficHistoryUrl]);

  useEffect(() => {
    void refresh(true, historyRange);
  }, [historyRange, refresh]);

  useEffect(() => {
    const syncPreference = () => setAutoRefreshMs(readDashboardRefreshInterval());
    window.addEventListener("focus", syncPreference);
    window.addEventListener("storage", syncPreference);
    return () => {
      window.removeEventListener("focus", syncPreference);
      window.removeEventListener("storage", syncPreference);
    };
  }, []);

  useEffect(() => {
    if (autoRefreshMs <= 0) {
      return undefined;
    }

    const timerId = window.setInterval(() => {
      if (document.visibilityState === "hidden") {
        return;
      }
      void refresh(false, historyRange);
    }, autoRefreshMs);

    return () => window.clearInterval(timerId);
  }, [autoRefreshMs, historyRange, refresh]);

  const providers = config?.providers ?? [];
  const rules = config?.rules ?? [];
  const trafficRoutes = status?.trafficRoutes ?? [];
  const wanState = status?.wan?.state || "unknown";
  const activeVpns = providers.filter((provider) => provider.enabled).length;
  const enabledRules = rules.filter((rule) => rule.enabled).length;
  const totalTrafficBytes = trafficRoutes.reduce((sum, route) => sum + (route.totalBytes || 0), 0);

  return (
    <div className="space-y-8">
      <div className="flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <h1 className="font-headline text-3xl font-bold tracking-tight text-primary md:text-4xl">{t("dashboard.title")}</h1>
          <p className="mt-1 text-on-surface-variant">{t("dashboard.subtitle")}</p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <span className="rounded-full border border-outline-variant/20 bg-surface-container px-3 py-1.5 text-[11px] font-bold uppercase tracking-widest text-outline">
            {autoRefreshMs > 0
              ? t("dashboard.autoRefresh", { interval: formatAutoRefreshInterval(autoRefreshMs, t) })
              : t("dashboard.autoRefreshDisabled")}
          </span>
          <button
            type="button"
            onClick={() => refresh(false, historyRange, customFrom, customTo, true)}
            disabled={busy}
            className="flex items-center gap-2 rounded-xl border border-outline-variant/30 bg-surface-container-high px-5 py-2.5 font-headline text-sm font-medium text-on-surface transition-colors hover:bg-surface-variant disabled:opacity-50"
          >
            <Icon name="refresh" className={`h-4 w-4 text-primary shrink-0${busy ? " animate-spin" : ""}`} />
            <span className="min-w-[5.5em] text-center">{t("dashboard.refresh")}</span>
          </button>
        </div>
      </div>

      {error ? <InlineNotice tone="error" title={t("error.dashboard")} message={error} /> : null}

      <div className="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-4">
        <KpiCard label={t("dashboard.wan")} value={t(`common.wanShort.${wanState}`)} icon="wifi" accent={wanState === "up" ? "secondary" : "error"} loading={loading} />
        <KpiCard label={t("dashboard.activeVpns")} value={loading ? "..." : `${activeVpns} / ${providers.length}`} icon="vpn_lock" accent="primary" loading={loading} />
        <KpiCard label={t("dashboard.routingRules")} value={loading ? "..." : `${enabledRules} ${t("dashboard.active")}`} icon="alt_route" accent="tertiary" loading={loading} />
        <KpiCard
          label={t("dashboard.lastApply")}
          value={formatDate(status?.lastAppliedAt || config?.lastAppliedAt) || t("dashboard.notYet")}
          icon="schedule"
          accent="outline"
          loading={loading}
        />
      </div>

      <SystemResourcesCard t={t} autoRefreshMs={autoRefreshMs} />

      <TrafficHistoryCard
        history={trafficHistory}
        range={historyRange}
        onRangeChange={(r) => { setHistoryRange(r); }}
        customFrom={customFrom}
        customTo={customTo}
        onApplyCustomRange={(from, to) => { setCustomFrom(from); setCustomTo(to); setHistoryRange("custom"); refresh(true, "custom", from, to); }}
        loading={loading}
        t={t}
      />
      <TrafficAnalyticsCard routes={trafficRoutes} totalTrafficBytes={totalTrafficBytes} history={trafficHistory} range={historyRange} loading={loading} t={t} />

      <div className="grid grid-cols-1 gap-8 lg:grid-cols-12">
        <div className="space-y-4 lg:col-span-7">
          <div className="flex items-end justify-between">
            <h2 className="font-headline text-xl font-bold tracking-tight text-primary">{t("dashboard.providers")}</h2>
            <span className="font-mono text-xs text-outline-variant">
              {providers.length} {t("dashboard.configured")}
            </span>
          </div>
          {providers.length === 0 ? (
            <div className="rounded-xl bg-surface-container-low p-8 text-center text-on-surface-variant">{t("dashboard.noProviders")}</div>
          ) : (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              {providers.map((provider) => (
                <ProviderCard key={provider.id} provider={provider} runtime={status?.providers?.find((item) => item.id === provider.id)} t={t} />
              ))}
            </div>
          )}
        </div>

        <div className="space-y-4 lg:col-span-5">
          <div className="flex items-end justify-between">
            <h2 className="font-headline text-xl font-bold tracking-tight text-primary">{t("dashboard.recentEvents")}</h2>
            <span className="font-mono text-xs text-outline-variant">
              {events.length} {t("dashboard.entries")}
            </span>
          </div>
          <div className="max-h-[500px] space-y-3 overflow-y-auto rounded-xl bg-surface-container-low p-4">
            {events.length === 0 ? (
              <p className="py-6 text-center text-sm text-on-surface-variant">{t("dashboard.noEvents")}</p>
            ) : (
              events.map((event) => (
                <div key={event.id} className="rounded-lg border border-outline-variant/10 bg-surface-container-high p-4">
                  <div className="mb-2 flex items-center justify-between">
                    <span className={`rounded px-2 py-0.5 text-[10px] font-bold uppercase tracking-widest ${levelBadge(event.level)}`}>{levelLabel(event.level, t)}</span>
                    <span className="font-mono text-[10px] text-outline">{formatDate(event.occurredAt)}</span>
                  </div>
                  <p className="text-sm font-medium text-on-surface">{t(`kind.${event.kind}`) || event.kind}</p>
                  <p className="mt-1 text-xs text-on-surface-variant">{event.message}</p>
                </div>
              ))
            )}
          </div>
        </div>
      </div>

      {status ? (
        <div className="rounded-xl border border-outline-variant/10 bg-surface-container-low p-6">
          <h2 className="mb-4 font-headline text-lg font-bold text-on-surface">{t("dashboard.systemHealth")}</h2>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <HealthItem label={t("settings.wan")} value={t(`common.wan.${wanState}`)} ok={wanState === "up"} />
            <HealthItem label={t("settings.wanPing")} value={formatLatencyMs(status.wan?.latencyMs, t)} />
            <HealthItem label="OpenVPN" value={status.binaries?.openvpn ? t("common.found") : t("common.notFound")} ok={status.binaries?.openvpn} />
            <HealthItem label="sing-box" value={status.binaries?.singbox ? t("common.found") : t("common.notFound")} ok={status.binaries?.singbox} />
            <HealthItem label="update_routes.sh" value={status.files?.updateRoutes ? t("common.inPlace") : t("common.notFound")} ok={status.files?.updateRoutes} />
            <HealthItem label={t("settings.rootDir")} value={status.projectDirectory || "-"} />
          </div>
        </div>
      ) : null}
    </div>
  );
}

function KpiCard({ label, value, icon, accent, loading }) {
  const border = {
    primary: "border-primary",
    secondary: "border-secondary",
    tertiary: "border-tertiary",
    error: "border-error",
    outline: "border-outline",
  }[accent] || "border-outline-variant";

  return (
    <div className={`rounded-xl border-l-4 bg-surface-container-low p-5 ${border}`}>
      <div className="mb-3 flex items-start justify-between">
        <span className="font-headline text-[10px] font-semibold uppercase tracking-widest text-on-surface-variant">{label}</span>
        <Icon name={icon} className={`h-5 w-5 ${accentTextClass(accent)}`} />
      </div>
      <div className="font-headline text-2xl font-bold text-on-surface min-h-[2rem]">{loading ? <span className="animate-pulse">...</span> : value}</div>
    </div>
  );
}

function TrafficHistoryCard({ history, range, onRangeChange, customFrom, customTo, onApplyCustomRange, loading, t }) {
  const [showCustom, setShowCustom] = useState(range === "custom");
  const [localFrom, setLocalFrom] = useState(customFrom || "");
  const [localTo, setLocalTo] = useState(customTo || "");
  const points = history?.points ?? [];
  const breakdown = history?.breakdown ?? [];
  const hasData = (history?.totalBytes || 0) > 0;
  const chart = buildTrafficHistoryChart(points);
  const peakPoint = points.reduce((current, point) => ((point.totalBytes || 0) > (current.totalBytes || 0) ? point : current), { totalBytes: 0 });
  const providerTotals = [...breakdown.reduce((acc, item) => {
    const key = item.providerId || item.providerName || item.key;
    const current = acc.get(key) || { key, providerName: item.providerName, providerType: item.providerType, totalBytes: 0 };
    current.totalBytes += item.totalBytes || 0;
    acc.set(key, current);
    return acc;
  }, new Map()).values()]
    .sort((left, right) => right.totalBytes - left.totalBytes)
    .slice(0, 4);
  const topRegions = breakdown.slice(0, 6);
  const axisLabels = chart?.coords?.length > 0 ? uniqueAxisLabels([
    chart.coords[0],
    chart.coords[Math.floor(chart.coords.length / 2)],
    chart.coords[chart.coords.length - 1],
  ]) : [];

  return (
    <section className="overflow-hidden rounded-2xl border border-outline-variant/10 bg-surface-container-low shadow-xl">
      <div className="border-b border-outline-variant/10 bg-surface-container px-6 py-5">
        <div className="flex flex-col gap-4 xl:flex-row xl:items-end xl:justify-between">
          <div>
            <h2 className="font-headline text-xl font-bold tracking-tight text-primary">{t("dashboard.trafficHistory")}</h2>
            <p className="mt-1 max-w-3xl text-sm text-on-surface-variant">
              {t("dashboard.trafficHistorySubtitle", { interval: formatTrafficSampleInterval(history?.sampleIntervalSeconds) })}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {TRAFFIC_HISTORY_RANGES.map((option) => {
              const active = option === range && !showCustom;
              return (
                <button
                  key={option}
                  type="button"
                  onClick={() => { if (!loading && (option !== range || showCustom)) { setShowCustom(false); onRangeChange(option); } }}
                  disabled={loading}
                  className={`rounded-full border px-3 py-1.5 text-xs font-bold uppercase tracking-wider transition-colors disabled:opacity-40 ${
                    active
                      ? "border-primary/30 bg-primary/10 text-primary"
                      : "border-outline-variant/20 bg-surface-container-high text-on-surface-variant hover:border-primary/30 hover:text-on-surface"
                  }`}
                >
                  {t(`dashboard.range.${option}`)}
                </button>
              );
            })}
            <button
              type="button"
              onClick={() => setShowCustom((v) => !v)}
              disabled={loading}
              className={`rounded-full border px-3 py-1.5 text-xs font-bold uppercase tracking-wider transition-colors disabled:opacity-40 ${
                showCustom
                  ? "border-primary/30 bg-primary/10 text-primary"
                  : "border-outline-variant/20 bg-surface-container-high text-on-surface-variant hover:border-primary/30 hover:text-on-surface"
              }`}
            >
              <Icon name="date_range" className="-ml-0.5 mr-1 inline-block h-3.5 w-3.5 align-[-3px]" />
              {t("dashboard.range.custom")}
            </button>
          </div>
        </div>

        {showCustom && (
          <div className="mt-3 flex flex-wrap items-end gap-3">
            <div>
              <label className="mb-1 block text-[10px] font-bold uppercase tracking-widest text-outline">{t("dashboard.dateFrom")}</label>
              <input
                type="datetime-local"
                value={localFrom}
                onChange={(e) => setLocalFrom(e.target.value)}
                className="rounded-lg border border-outline-variant/20 bg-surface-container-lowest/50 px-3 py-1.5 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
              />
            </div>
            <div>
              <label className="mb-1 block text-[10px] font-bold uppercase tracking-widest text-outline">{t("dashboard.dateTo")}</label>
              <input
                type="datetime-local"
                value={localTo}
                onChange={(e) => setLocalTo(e.target.value)}
                className="rounded-lg border border-outline-variant/20 bg-surface-container-lowest/50 px-3 py-1.5 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
              />
            </div>
            <button
              type="button"
              disabled={loading || !localFrom || !localTo}
              onClick={() => { onApplyCustomRange(localFrom, localTo); }}
              className="rounded-lg bg-primary/10 px-4 py-1.5 text-sm font-medium text-primary transition-colors hover:bg-primary/20 disabled:opacity-40"
            >
              {t("dashboard.applyRange")}
            </button>
          </div>
        )}

        <div className="mt-4 flex flex-wrap gap-3">
          <MetricPill label={t("dashboard.historyRangeTotal")} value={loading ? "..." : formatBytes(history?.totalBytes || 0)} />
          <MetricPill label={t("dashboard.historyPeak")} value={loading ? "..." : formatBytes(peakPoint.totalBytes || 0)} />
          <MetricPill label={t("dashboard.latestSample")} value={loading ? "..." : (formatDate(history?.latestSampleAt) || t("common.notYet"))} />
        </div>
      </div>

      <div className="grid grid-cols-1 gap-6 px-6 py-6 xl:grid-cols-12">
        <div className="xl:col-span-8">
          {loading ? (
            <div className="rounded-2xl bg-surface-container p-6 text-sm text-on-surface-variant">...</div>
          ) : !hasData || !chart ? (
            <div className="rounded-2xl border border-dashed border-outline-variant/20 bg-surface-container p-6 text-sm text-on-surface-variant">
              <p className="font-medium text-on-surface">{t("dashboard.historyEmpty")}</p>
              <p className="mt-2">{t("dashboard.historyCollecting")}</p>
            </div>
          ) : (
            <MainHistoryChart chart={chart} history={history} range={range} axisLabels={axisLabels} t={t} />
          )}
        </div>

        <div className="space-y-4 xl:col-span-4">
          <TrafficBreakdownPanel
            title={t("dashboard.topProviders")}
            items={providerTotals.map((item) => ({ key: item.key, title: item.providerName, subtitle: t(`common.${item.providerType}`), totalBytes: item.totalBytes }))}
            totalBytes={history?.totalBytes || 0}
            emptyLabel={t("dashboard.historyEmpty")}
          />
          <TrafficBreakdownPanel
            title={t("dashboard.topRegions")}
            items={topRegions.map((item) => ({ key: item.key, title: item.location || item.providerName, subtitle: item.location ? item.providerName : item.providerType, totalBytes: item.totalBytes }))}
            totalBytes={history?.totalBytes || 0}
            emptyLabel={t("dashboard.historyEmpty")}
          />
        </div>
      </div>
    </section>
  );
}

function TrafficAnalyticsCard({ routes, totalTrafficBytes, history, range, loading, t }) {
  const [expandedRoutes, setExpandedRoutes] = useState(() => new Set());
  const routeSeriesByKey = useMemo(() => {
    const entries = (history?.routeSeries ?? []).map((series) => [routeTrafficKey(series), series]);
    return new Map(entries);
  }, [history]);
  const routeKeys = useMemo(() => routes.map((route) => routeTrafficKey(route)), [routes]);

  useEffect(() => {
    setExpandedRoutes((prev) => new Set([...prev].filter((key) => routeKeys.includes(key))));
  }, [routeKeys]);

  const toggleRoute = useCallback((routeKey) => {
    setExpandedRoutes((prev) => {
      const next = new Set(prev);
      if (next.has(routeKey)) {
        next.delete(routeKey);
      } else {
        next.add(routeKey);
      }
      return next;
    });
  }, []);

  const providers = useMemo(() => {
    const summary = new Map();
    const useHistory = (history?.totalBytes || 0) > 0;
    const sourceItems = useHistory ? (history?.breakdown ?? []) : routes;

    for (const item of sourceItems) {
      const key = item.providerId || `${item.providerName}-${item.providerType}`;
      const current = summary.get(key) || {
        key,
        providerName: item.providerName,
        providerType: item.providerType,
        totalBytes: 0,
        routeCount: 0,
      };
      current.totalBytes += item.totalBytes || 0;
      current.routeCount += 1;
      summary.set(key, current);
    }

    return [...summary.values()].sort((left, right) => right.totalBytes - left.totalBytes);
  }, [history, routes]);

  return (
    <section className="overflow-hidden rounded-2xl border border-outline-variant/10 bg-surface-container-low shadow-xl">
      <div className="border-b border-outline-variant/10 bg-surface-container px-6 py-5">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
          <div>
            <h2 className="font-headline text-xl font-bold tracking-tight text-primary">{t("dashboard.trafficAnalytics")}</h2>
            <p className="mt-1 max-w-2xl text-sm text-on-surface-variant">{t("dashboard.trafficSubtitle")}</p>
          </div>
          <div className="flex flex-wrap gap-3">
            <MetricPill label={t("dashboard.trafficTotal")} value={loading ? "..." : formatBytes(totalTrafficBytes)} />
            <MetricPill label={t("dashboard.activeVpns")} value={loading ? "..." : routes.length} />
            <MetricPill label={t("dashboard.historyRangeTotal")} value={loading ? "..." : formatBytes(history?.totalBytes || 0)} />
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-6 px-6 py-6 lg:grid-cols-12">
        <div className="space-y-4 lg:col-span-8">
          {loading ? (
            <div className="rounded-xl bg-surface-container p-6 text-sm text-on-surface-variant">...</div>
          ) : routes.length === 0 ? (
            <div className="rounded-xl bg-surface-container p-6 text-sm text-on-surface-variant">{t("dashboard.noTrafficData")}</div>
          ) : (
            routes.map((route) => {
              const routeKey = routeTrafficKey(route);
              const series = routeSeriesByKey.get(routeKey);
              const chart = buildRouteTrafficChart(series?.points ?? []);
              const isRunning = route.status === "running";
              const rangeTotal = series?.totalBytes || 0;
              const peakBucket = series?.peakBytes || 0;
              const latestBucket = latestRouteBucket(series?.points ?? []);
              const historyShare = (history?.totalBytes || 0) > 0
                ? Math.round((rangeTotal / history.totalBytes) * 100)
                : 0;
              const isExpanded = expandedRoutes.has(routeKey);

              return (
                <div key={routeKey} className="overflow-hidden rounded-xl border border-outline-variant/10 bg-surface-container">
                  <button
                    type="button"
                    onClick={() => toggleRoute(routeKey)}
                    className="flex w-full items-start justify-between gap-4 px-4 py-4 text-left transition-colors hover:bg-surface-container-high/40"
                    aria-expanded={isExpanded}
                  >
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <p className="font-headline text-base font-bold text-on-surface">{route.location || route.providerName}</p>
                        <span className="rounded-full border border-outline-variant/20 bg-surface-container-high px-2.5 py-1 font-mono text-[10px] font-bold uppercase tracking-widest text-outline">
                          {route.interfaceName}
                        </span>
                      </div>
                      <p className="mt-1 text-xs text-on-surface-variant">
                        {route.providerName} · {t(`common.${route.providerType}`)} · {route.domainCount} {t("dashboard.domainsRouted")}
                      </p>
                    </div>
                    <div className="flex shrink-0 items-center gap-3">
                      <div className={`flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[10px] font-bold ${isRunning ? "border-secondary/30 text-secondary" : "border-error/30 text-error"}`}>
                        <div className={`h-1.5 w-1.5 rounded-full ${isRunning ? "bg-secondary" : "bg-error"}`} />
                        {isRunning ? t("settings.runtimeRunning") : t("settings.runtimeStopped")}
                      </div>
                      <Icon name={isExpanded ? "expand_less" : "expand_more"} className="mt-0.5 h-5 w-5 text-outline-variant" />
                    </div>
                  </button>

                  <div className="grid grid-cols-2 gap-3 px-4 pb-4 text-xs text-on-surface-variant xl:grid-cols-5">
                    <TrafficMeta label={t("dashboard.domainsRouted")} value={route.domainCount} />
                    <TrafficMeta label={t("dashboard.liveTotal")} value={formatBytes(route.totalBytes || 0)} />
                    <TrafficMeta label={t("dashboard.historyRangeTotal")} value={formatBytes(rangeTotal)} />
                    <TrafficMeta label={t("dashboard.latestBucket")} value={formatBytes(latestBucket.totalBytes || 0)} />
                    <TrafficMeta label={t("dashboard.historyPeak")} value={formatBytes(peakBucket)} />
                  </div>

                  {isExpanded && (
                    <div className="border-t border-outline-variant/10 px-4 pb-4">
                      <RouteHistoryChart chart={chart} route={route} range={range} historyShare={historyShare} t={t} />
                    </div>
                  )}
                </div>
              );
            })
          )}
        </div>

        <div className="space-y-3 lg:col-span-4">
          <h3 className="font-headline text-sm font-bold uppercase tracking-widest text-outline">{t("dashboard.providers")}</h3>
          {loading ? (
            <div className="rounded-xl bg-surface-container p-4 text-sm text-on-surface-variant">...</div>
          ) : providers.length === 0 ? (
            <div className="rounded-xl bg-surface-container p-4 text-sm text-on-surface-variant">{t("dashboard.noTrafficData")}</div>
          ) : (
            providers.map((provider) => (
              <div key={provider.key} className="rounded-xl bg-surface-container p-4">
                <div className="mb-1 flex items-center justify-between gap-3">
                  <p className="font-headline text-sm font-bold text-on-surface">{provider.providerName}</p>
                  <p className="font-mono text-xs text-secondary">{formatBytes(provider.totalBytes)}</p>
                </div>
                <p className="text-xs text-on-surface-variant">
                  {provider.routeCount} {t("dashboard.regionsActive")} · {t(`common.${provider.providerType}`)}
                </p>
              </div>
            ))
          )}
        </div>
      </div>
    </section>
  );
}

function SystemResourcesCard({ t, autoRefreshMs }) {
  const [res, setRes] = useState(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const data = await fetchJSON("/api/system/resources");
        if (!cancelled) { setRes(data); setLoading(false); }
      } catch { if (!cancelled) setLoading(false); }
    };
    void load();

    const interval = autoRefreshMs > 0 ? autoRefreshMs : 10_000;
    const id = setInterval(() => {
      if (document.visibilityState !== "hidden") void load();
    }, interval);
    return () => { cancelled = true; clearInterval(id); };
  }, [autoRefreshMs]);

  const cpuColor = (res?.cpuUsagePercent ?? 0) > 80 ? "text-error" : (res?.cpuUsagePercent ?? 0) > 50 ? "text-tertiary" : "text-secondary";
  const memPercent = res?.memTotalMB ? ((res.memUsedMB / res.memTotalMB) * 100) : 0;
  const memColor = memPercent > 85 ? "text-error" : memPercent > 60 ? "text-tertiary" : "text-secondary";
  const diskPercent = res?.diskTotalMB ? ((res.diskUsedMB / res.diskTotalMB) * 100) : 0;
  const diskColor = diskPercent > 90 ? "text-error" : diskPercent > 70 ? "text-tertiary" : "text-secondary";
  const dataDiskPercent = res?.dataDiskTotalMB ? ((res.dataDiskUsedMB / res.dataDiskTotalMB) * 100) : 0;
  const dataDiskColor = dataDiskPercent > 90 ? "text-error" : dataDiskPercent > 70 ? "text-tertiary" : "text-secondary";
  const hasDataDisk = (res?.dataDiskTotalMB ?? 0) > 0;

  return (
    <div className="overflow-hidden rounded-2xl border border-outline-variant/10 bg-surface-container-low shadow-xl">
      <div className="border-b border-outline-variant/10 bg-surface-container px-6 py-5">
        <div className="flex items-end justify-between">
          <div>
            <h2 className="font-headline text-xl font-bold tracking-tight text-primary">
              <Icon name="memory" className="-mt-0.5 mr-2 inline-block h-5 w-5 align-middle" />
              {t("dashboard.sysResources")}
            </h2>
            <p className="mt-1 text-sm text-on-surface-variant">{t("dashboard.sysResourcesSubtitle")}</p>
          </div>
          {res?.uptimeFormatted && (
            <div className="flex items-center gap-2 rounded-full border border-outline-variant/20 bg-surface-container-high px-3 py-1.5">
              <Icon name="schedule" className="h-3.5 w-3.5 text-outline" />
              <span className="text-[11px] font-bold uppercase tracking-widest text-outline">
                Uptime: {res.uptimeFormatted}
              </span>
            </div>
          )}
        </div>
      </div>

      <div className={`grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 ${hasDataDisk ? "lg:grid-cols-5" : "lg:grid-cols-4"}`}>
        {/* CPU */}
        <ResourceGauge
          icon="speed"
          label={t("dashboard.resCpu")}
          value={loading ? null : res?.cpuUsagePercent?.toFixed(1)}
          unit="%"
          percent={res?.cpuUsagePercent ?? 0}
          colorClass={cpuColor}
          sub={res ? `Load: ${res.loadAvg1?.toFixed(2)} / ${res.loadAvg5?.toFixed(2)} / ${res.loadAvg15?.toFixed(2)}` : null}
        />

        {/* Memory */}
        <ResourceGauge
          icon="memory"
          label={t("dashboard.resMemory")}
          value={loading ? null : res?.memUsedMB}
          unit={` / ${res?.memTotalMB ?? 0} MB`}
          percent={memPercent}
          colorClass={memColor}
          sub={res?.swapTotalMB > 0 ? `Swap: ${res.swapUsedMB} / ${res.swapTotalMB} MB` : null}
        />

        {/* Root flash */}
        <ResourceGauge
          icon="save"
          label={`${t("dashboard.resDisk")} (${res?.diskPath || "/"})`}
          value={loading ? null : res?.diskUsedMB}
          unit={` / ${res?.diskTotalMB ?? 0} MB`}
          percent={diskPercent}
          colorClass={diskColor}
          sub={res ? `${t("dashboard.resFree")}: ${(res.diskFreePercent ?? 0).toFixed(1)}%` : null}
        />

        {/* USB / Data disk */}
        {hasDataDisk && (
          <ResourceGauge
            icon="usb"
            label={`${t("dashboard.resDisk")} (USB)`}
            value={loading ? null : res?.dataDiskUsedMB}
            unit={` / ${res?.dataDiskTotalMB ?? 0} MB`}
            percent={dataDiskPercent}
            colorClass={dataDiskColor}
            sub={res ? `${t("dashboard.resFree")}: ${(res.dataDiskFreePercent ?? 0).toFixed(1)}%` : null}
          />
        )}

        {/* Processes / Load */}
        <div className="rounded-xl border border-outline-variant/10 bg-surface-container p-4">
          <div className="mb-3 flex items-center gap-2">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10">
              <Icon name="apps" className="h-4.5 w-4.5 text-primary" />
            </div>
            <span className="text-[10px] font-bold uppercase tracking-widest text-outline">{t("dashboard.resProcesses")}</span>
          </div>
          <div className="font-headline text-2xl font-bold text-on-surface">
            {loading ? <span className="animate-pulse">...</span> : (res?.processCount ?? "—")}
          </div>
          <div className="mt-2 space-y-1.5">
            <div className="flex items-center justify-between text-xs text-on-surface-variant">
              <span>Load 1m</span>
              <span className="font-mono font-medium text-on-surface">{loading ? "..." : res?.loadAvg1?.toFixed(2)}</span>
            </div>
            <div className="flex items-center justify-between text-xs text-on-surface-variant">
              <span>Load 5m</span>
              <span className="font-mono font-medium text-on-surface">{loading ? "..." : res?.loadAvg5?.toFixed(2)}</span>
            </div>
            <div className="flex items-center justify-between text-xs text-on-surface-variant">
              <span>Load 15m</span>
              <span className="font-mono font-medium text-on-surface">{loading ? "..." : res?.loadAvg15?.toFixed(2)}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function ResourceGauge({ icon, label, value, unit, percent, colorClass, sub }) {
  const clampedPercent = Math.min(100, Math.max(0, percent));
  return (
    <div className="rounded-xl border border-outline-variant/10 bg-surface-container p-4">
      <div className="mb-3 flex items-center gap-2">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10">
          <Icon name={icon} className="h-4.5 w-4.5 text-primary" />
        </div>
        <span className="text-[10px] font-bold uppercase tracking-widest text-outline">{label}</span>
      </div>
      <div className={`font-headline text-2xl font-bold ${colorClass}`}>
        {value === null ? <span className="animate-pulse">...</span> : <>{value}<span className="text-sm font-normal text-on-surface-variant">{unit}</span></>}
      </div>
      <div className="mt-3 h-2 overflow-hidden rounded-full bg-surface-variant/30">
        <div
          className={`h-full rounded-full transition-all duration-500 ${
            clampedPercent > 80 ? "bg-error" : clampedPercent > 50 ? "bg-tertiary" : "bg-secondary"
          }`}
          style={{ width: `${clampedPercent}%` }}
        />
      </div>
      {sub && <p className="mt-2 text-[11px] text-on-surface-variant">{sub}</p>}
    </div>
  );
}

function MetricPill({ label, value }) {
  return (
    <div className="rounded-xl border border-outline-variant/10 bg-surface-container-low px-4 py-3">
      <p className="mb-1 text-[10px] font-bold uppercase tracking-widest text-outline">{label}</p>
      <p className="font-headline text-lg font-bold text-on-surface">{value}</p>
    </div>
  );
}

function ProviderCard({ provider, runtime, t }) {
  const navigate = useNavigate();
  const tone = !provider.enabled ? "outline" : runtime?.health === "ready" ? "secondary" : runtime?.health === "warning" ? "tertiary" : "error";
  const toneClasses = statusToneClasses(tone);
  const isOnline = provider.enabled && runtime?.health === "ready";
  const hasError = provider.enabled && !isOnline;
  const errorMessage = runtime?.lastError || runtime?.message || runtime?.healthDetails || "";

  return (
    <div
      onClick={() => navigate("/connections")}
      className="cursor-pointer rounded-xl border border-outline-variant/10 bg-surface-container-high p-5 transition-all hover:-translate-y-[2px] hover:border-primary/30"
    >
      <div className="mb-4 flex justify-between gap-3">
        <div>
          <h3 className="font-headline text-lg font-bold">{provider.name}</h3>
          <p className="font-mono text-xs text-outline">{t(`common.${provider.type}`)}</p>
        </div>
        <div className={`flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[10px] font-bold ${toneClasses.badge}`}>
          <div className={`h-1.5 w-1.5 rounded-full ${toneClasses.dot} ${isOnline ? "status-glow-success" : ""}`} />
          {!provider.enabled ? t("connections.off") : isOnline ? t("connections.online") : t("connections.problem")}
        </div>
      </div>

      {hasError && (
        <div className="mb-3 flex items-start gap-2 rounded-lg bg-error-container/15 px-3 py-2">
          <Icon name="error_outline" className="mt-0.5 h-4 w-4 shrink-0 text-error" />
          <p className="text-xs text-error/90">{errorMessage || t("connections.problem")}</p>
        </div>
      )}

      <div className="grid grid-cols-2 gap-3 text-sm">
        <div>
          <p className="mb-1 text-[10px] uppercase tracking-tighter text-outline-variant">{t("dashboard.location")}</p>
          <p className="font-medium">{provider.selectedLocation || "-"}</p>
        </div>
        <div>
          <p className="mb-1 text-[10px] uppercase tracking-tighter text-outline-variant">{t("dashboard.source")}</p>
          <p className="truncate font-mono text-xs text-secondary">{provider.source}</p>
        </div>
      </div>
    </div>
  );
}

function TrafficBreakdownPanel({ title, items, totalBytes, emptyLabel }) {
  return (
    <div className="rounded-2xl border border-outline-variant/10 bg-surface-container p-4">
      <h3 className="mb-3 font-headline text-sm font-bold uppercase tracking-widest text-outline">{title}</h3>
      {items.length === 0 ? (
        <p className="text-sm text-on-surface-variant">{emptyLabel}</p>
      ) : (
        <div className="space-y-3">
          {items.map((item) => {
            const share = totalBytes > 0 ? Math.max(6, Math.round((item.totalBytes / totalBytes) * 100)) : 0;
            return (
              <div key={item.key} className="rounded-xl bg-surface-container-high p-3">
                <div className="mb-2 flex items-start justify-between gap-3">
                  <div>
                    <p className="font-headline text-sm font-bold text-on-surface">{item.title}</p>
                    <p className="text-xs text-on-surface-variant">{item.subtitle}</p>
                  </div>
                  <p className="font-mono text-xs text-secondary">{formatBytes(item.totalBytes || 0)}</p>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-surface-container-lowest/70">
                  <div className="h-full rounded-full bg-gradient-to-r from-primary via-secondary to-tertiary" style={{ width: `${share}%` }} />
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function TrafficMeta({ label, value }) {
  return (
    <div>
      <p className="mb-1 text-[10px] font-bold uppercase tracking-widest text-outline">{label}</p>
      <p className="font-mono text-sm text-secondary">{value}</p>
    </div>
  );
}

function HealthItem({ label, value, ok }) {
  return (
    <div className="flex items-center justify-between rounded-lg bg-surface-container p-3">
      <span className="text-sm text-on-surface-variant">{label}</span>
      <span className={`text-sm font-medium ${ok === true ? "text-secondary" : ok === false ? "text-error" : "text-on-surface"}`}>{value}</span>
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

function buildTrafficHistoryChart(points) {
  if (points.length === 0) {
    return null;
  }

  const width = 720;
  const height = 220;
  const padding = 18;
  const maxValue = points.reduce((max, point) => Math.max(max, point.totalBytes || 0), 0);
  if (maxValue <= 0) {
    return null;
  }

  const innerWidth = width - padding * 2;
  const innerHeight = height - padding * 2;
  const coords = points.map((point, index) => {
    const x = padding + (points.length === 1 ? innerWidth / 2 : (index * innerWidth) / (points.length - 1));
    const y = padding + innerHeight - ((point.totalBytes || 0) / maxValue) * innerHeight;
    return {
      at: point.at,
      totalBytes: point.totalBytes || 0,
      x,
      y,
    };
  });

  const linePath = coords.map((point, index) => `${index === 0 ? "M" : "L"} ${point.x.toFixed(1)} ${point.y.toFixed(1)}`).join(" ");
  const baseline = padding + innerHeight;
  const lastPoint = coords[coords.length - 1];
  const areaPath = `${linePath} L ${lastPoint.x.toFixed(1)} ${baseline.toFixed(1)} L ${coords[0].x.toFixed(1)} ${baseline.toFixed(1)} Z`;

  return {
    padding,
    maxValue,
    coords,
    linePath,
    areaPath,
    guides: [0, 0.33, 0.66, 1].map((ratio) => ({
      id: ratio,
      y: padding + innerHeight - innerHeight * ratio,
    })),
  };
}

function buildRouteTrafficChart(points) {
  if (!points || points.length === 0) {
    return null;
  }

  const maxValue = points.reduce((max, point) => Math.max(max, (point.rxBytes || 0), (point.txBytes || 0), (point.totalBytes || 0)), 0);
  if (maxValue <= 0) {
    return null;
  }

  const width = 520;
  const height = 140;
  const padding = 14;
  const innerWidth = width - padding * 2;
  const innerHeight = height - padding * 2;

  const toCoord = (value, index) => {
    const x = padding + (points.length === 1 ? innerWidth / 2 : (index * innerWidth) / (points.length - 1));
    const y = padding + innerHeight - ((value || 0) / maxValue) * innerHeight;
    return { x, y };
  };

  const rxCoords = points.map((p, i) => ({ ...toCoord(p.rxBytes, i), at: p.at, bytes: p.rxBytes || 0 }));
  const txCoords = points.map((p, i) => ({ ...toCoord(p.txBytes, i), at: p.at, bytes: p.txBytes || 0 }));
  const baseline = padding + innerHeight;

  const buildPath = (coords) => coords.map((c, i) => `${i === 0 ? "M" : "L"} ${c.x.toFixed(1)} ${c.y.toFixed(1)}`).join(" ");
  const buildArea = (coords) => {
    const line = buildPath(coords);
    const last = coords[coords.length - 1];
    const first = coords[0];
    return `${line} L ${last.x.toFixed(1)} ${baseline.toFixed(1)} L ${first.x.toFixed(1)} ${baseline.toFixed(1)} Z`;
  };

  return {
    width,
    height,
    padding,
    rx: { linePath: buildPath(rxCoords), areaPath: buildArea(rxCoords), coords: rxCoords },
    tx: { linePath: buildPath(txCoords), areaPath: buildArea(txCoords), coords: txCoords },
    labels: uniqueAxisLabels([points[0], points[Math.floor(points.length / 2)], points[points.length - 1]]),
    guides: [0, 0.33, 0.66, 1].map((ratio) => ({
      id: ratio,
      y: padding + innerHeight - innerHeight * ratio,
    })),
  };
}

function latestRouteBucket(points) {
  if (!points || points.length === 0) {
    return { totalBytes: 0 };
  }

  for (let index = points.length - 1; index >= 0; index -= 1) {
    if ((points[index].totalBytes || 0) > 0) {
      return points[index];
    }
  }

  return points[points.length - 1];
}

function routeTrafficKey(route) {
  return [route.providerId || route.providerName || "", route.location || route.interfaceName || "", route.interfaceName || ""].join("|");
}

function uniqueAxisLabels(labels) {
  const seen = new Set();
  return labels.filter((label) => {
    if (!label?.at || seen.has(label.at)) {
      return false;
    }
    seen.add(label.at);
    return true;
  });
}

function formatTrafficPointLabel(value, range) {
  if (!value) return "";

  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }

  const options = range === "30d"
    ? { day: "2-digit", month: "short" }
    : (range === "1h" || range === "3h")
      ? { hour: "2-digit", minute: "2-digit" }
      : { day: "2-digit", month: "short", hour: "2-digit" };

  return new Intl.DateTimeFormat(undefined, options).format(date);
}

function formatTrafficSampleInterval(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return "-";

  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m`;
  }

  const hours = Math.round(minutes / 60);
  return `${hours}h`;
}

function RouteHistoryChart({ chart, route, range, historyShare, t }) {
  const svgRef = useRef(null);
  const routeKey = routeTrafficKey(route);

  if (!chart) {
    return (
      <div className="mt-4 rounded-2xl border border-outline-variant/10 bg-surface-container-high px-4 py-4">
        <div className="rounded-xl border border-dashed border-outline-variant/20 bg-surface-container px-4 py-5 text-sm text-on-surface-variant">
          <p className="font-medium text-on-surface">{t("dashboard.routeHistoryEmpty")}</p>
          <p className="mt-1">{t("dashboard.routeHistoryHint")}</p>
        </div>
      </div>
    );
  }

  // merge rx+tx coords by index for tooltip lookup and x-snap hover
  const mergedPoints = chart.rx.coords.map((rx, i) => ({
    ...rx,
    rxBytes: rx.bytes,
    txBytes: chart.tx.coords[i]?.bytes || 0,
    totalBytes: (rx.bytes || 0) + (chart.tx.coords[i]?.bytes || 0),
  }));
  const tooltipPoints = mergedPoints.map((point, index) => ({
    at: point.at,
    payload: point,
    hits: [
      {
        x: chart.rx.coords[index]?.x || 0,
        y: chart.rx.coords[index]?.y ?? chart.height,
      },
      {
        x: chart.tx.coords[index]?.x || 0,
        y: chart.tx.coords[index]?.y ?? chart.height,
      },
    ],
  }));

  return (
    <div className="mt-4 rounded-2xl border border-outline-variant/10 bg-surface-container-high px-4 py-4">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3 text-[10px] font-bold uppercase tracking-widest text-outline">
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: "rgba(0, 180, 130, 0.9)" }} />
            {t("dashboard.rx")}
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: "rgba(130, 90, 255, 0.9)" }} />
            {t("dashboard.tx")}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-3 text-[10px] font-bold uppercase tracking-widest text-outline">
          <span>{t("dashboard.trafficShare")} {historyShare}%</span>
          <span>{t(`dashboard.range.${range}`)}</span>
        </div>
      </div>
      <div className="relative">
        <svg ref={svgRef} viewBox={`0 0 ${chart.width} ${chart.height}`} className="h-36 w-full overflow-visible" style={{ touchAction: "none" }}>
          <defs>
            <linearGradient id={`rxArea-${routeKey}`} x1="0%" y1="0%" x2="0%" y2="100%">
              <stop offset="0%" stopColor="rgba(0, 180, 130, 0.4)" />
              <stop offset="100%" stopColor="rgba(0, 180, 130, 0.02)" />
            </linearGradient>
            <linearGradient id={`txArea-${routeKey}`} x1="0%" y1="0%" x2="0%" y2="100%">
              <stop offset="0%" stopColor="rgba(130, 90, 255, 0.35)" />
              <stop offset="100%" stopColor="rgba(130, 90, 255, 0.02)" />
            </linearGradient>
          </defs>
          {chart.guides.map((guide) => (
            <line key={guide.id} x1={chart.padding} y1={guide.y} x2={chart.width - chart.padding} y2={guide.y} stroke="rgba(116, 119, 138, 0.15)" strokeDasharray="4 6" />
          ))}
          <path d={chart.rx.areaPath} fill={`url(#rxArea-${routeKey})`} />
          <path d={chart.rx.linePath} fill="none" stroke="rgba(0, 180, 130, 0.9)" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
          <path d={chart.tx.areaPath} fill={`url(#txArea-${routeKey})`} />
          <path d={chart.tx.linePath} fill="none" stroke="rgba(130, 90, 255, 0.9)" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
          {chart.rx.coords.map((c) => (
            <circle key={`rx-${c.at}`} cx={c.x} cy={c.y} r="3" fill="rgba(0, 180, 130, 0.95)" />
          ))}
          {chart.tx.coords.map((c) => (
            <circle key={`tx-${c.at}`} cx={c.x} cy={c.y} r="3" fill="rgba(130, 90, 255, 0.95)" />
          ))}
        </svg>
        <ChartTooltip
          svgRef={svgRef}
          points={tooltipPoints}
          range={range}
          mode="bucket"
          renderContent={(point) => (
            <div className="space-y-1">
              <div className="flex items-center justify-between gap-3">
                <span className="inline-flex items-center gap-1.5 text-[10px] text-outline">
                  <span className="h-1.5 w-1.5 rounded-full" style={{ background: "rgba(0, 180, 130, 0.9)" }} />
                  {t("dashboard.rx")}
                </span>
                <span className="font-mono text-xs font-bold text-on-surface">{formatBytes(point.rxBytes || 0)}</span>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span className="inline-flex items-center gap-1.5 text-[10px] text-outline">
                  <span className="h-1.5 w-1.5 rounded-full" style={{ background: "rgba(130, 90, 255, 0.9)" }} />
                  {t("dashboard.tx")}
                </span>
                <span className="font-mono text-xs font-bold text-on-surface">{formatBytes(point.txBytes || 0)}</span>
              </div>
            </div>
          )}
        />
      </div>
      <div className="mt-2 flex items-center justify-between gap-3 text-[10px] font-bold uppercase tracking-widest text-outline">
        {chart.labels.map((label) => (
          <span key={label.at}>{formatTrafficPointLabel(label.at, range)}</span>
        ))}
      </div>
    </div>
  );
}

function MainHistoryChart({ chart, history, range, axisLabels, t }) {
  const svgRef = useRef(null);
  return (
    <div className="rounded-2xl border border-outline-variant/10 bg-surface-container p-4">
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <p className="font-headline text-sm font-bold uppercase tracking-widest text-outline">{t("dashboard.trafficHistory")}</p>
          <p className="mt-1 text-xs text-on-surface-variant">{t("dashboard.historyRangeTotal")} {formatBytes(history?.totalBytes || 0)}</p>
        </div>
        <p className="font-mono text-xs text-outline-variant">{formatBytes(chart.maxValue)}</p>
      </div>
      <div className="relative">
        <svg ref={svgRef} viewBox="0 0 720 220" className="h-56 w-full overflow-visible" style={{ touchAction: "none" }}>
          <defs>
            <linearGradient id="trafficHistoryArea" x1="0%" y1="0%" x2="0%" y2="100%">
              <stop offset="0%" stopColor="rgba(0, 82, 204, 0.55)" />
              <stop offset="100%" stopColor="rgba(0, 135, 90, 0.06)" />
            </linearGradient>
          </defs>
          {chart.guides.map((guide) => (
            <line key={guide.id} x1={chart.padding} y1={guide.y} x2={720 - chart.padding} y2={guide.y} stroke="rgba(116, 119, 138, 0.18)" strokeDasharray="6 8" />
          ))}
          <path d={chart.areaPath} fill="url(#trafficHistoryArea)" />
          <path d={chart.linePath} fill="none" stroke="rgba(0, 82, 204, 0.95)" strokeWidth="4" strokeLinecap="round" strokeLinejoin="round" />
          {chart.coords.map((point) => (
            <g key={point.at}>
              <circle cx={point.x} cy={point.y} r="4" fill="rgba(0, 135, 90, 0.95)" />
              <circle cx={point.x} cy={point.y} r="9" fill="rgba(0, 135, 90, 0.12)" />
            </g>
          ))}
        </svg>
        <ChartTooltip
          svgRef={svgRef}
          points={chart.coords}
          range={range}
          threshold={36}
          renderContent={(point) => (
            <p className="font-mono text-sm font-bold text-on-surface">{formatBytes(point.totalBytes || 0)}</p>
          )}
        />
      </div>
      <div className="mt-3 flex items-center justify-between gap-3 text-[10px] font-bold uppercase tracking-widest text-outline">
        {axisLabels.map((label) => (
          <span key={label.at}>{formatTrafficPointLabel(label.at, range)}</span>
        ))}
      </div>
    </div>
  );
}

function ChartTooltip({ svgRef, points, range, renderContent, mode = "proximity", threshold = 24 }) {
  const [active, setActive] = useState(null);
  const [pos, setPos] = useState({ x: 0, y: 0 });

  const handleMove = useCallback((e) => {
    const svg = svgRef.current;
    if (!svg || !points || points.length === 0) return;

    const rect = svg.getBoundingClientRect();
    const mouseX = e.clientX - rect.left;
    const mouseY = e.clientY - rect.top;
    const viewBoxWidth = svg.viewBox.baseVal.width || rect.width || 1;
    const viewBoxHeight = svg.viewBox.baseVal.height || rect.height || 1;

    let closest = null;
    let closestDist = Infinity;
    for (const point of points) {
      const hits = point.hits?.length ? point.hits : [point];
      for (const hit of hits) {
        const pointX = (hit.x / viewBoxWidth) * rect.width;
        const pointY = (hit.y / viewBoxHeight) * rect.height;
        const dist = mode === "x"
          ? Math.abs(pointX - mouseX)
          : Math.hypot(pointX - mouseX, pointY - mouseY);
        if (dist < closestDist) {
          closestDist = dist;
          closest = { point, x: pointX, y: pointY };
        }
      }
    }

    if (closest && (mode === "x" || mode === "bucket" || closestDist < threshold)) {
      setActive(closest.point.payload ?? closest.point);
      setPos({ x: closest.x, y: closest.y });
    } else {
      setActive(null);
    }
  }, [svgRef, points, mode, threshold]);

  const handleLeave = useCallback(() => setActive(null), []);

  useEffect(() => {
    const svg = svgRef.current;
    if (!svg) return;
    svg.addEventListener("pointermove", handleMove);
    svg.addEventListener("pointerleave", handleLeave);
    return () => {
      svg.removeEventListener("pointermove", handleMove);
      svg.removeEventListener("pointerleave", handleLeave);
    };
  }, [svgRef, handleMove, handleLeave]);

  if (!active) return null;

  const tooltipWidth = 170;
  const parentWidth = svgRef.current?.parentElement?.offsetWidth || 400;
  const isRight = pos.x > parentWidth / 2;

  return (
    <div
      className="pointer-events-none absolute z-10 rounded-lg border border-outline-variant/20 bg-surface-container-highest/95 px-3 py-2 shadow-lg backdrop-blur-sm"
      style={{
        left: isRight ? pos.x - tooltipWidth - 12 : pos.x + 12,
        top: Math.max(4, pos.y - 30),
        width: tooltipWidth,
      }}
    >
      <p className="mb-1 text-[10px] font-bold uppercase tracking-widest text-outline">
        {formatTrafficPointLabel(active.at, range)}
      </p>
      {renderContent(active)}
    </div>
  );
}

function formatAutoRefreshInterval(value, t) {
  if (!Number.isFinite(value) || value <= 0) {
    return t("common.notYet");
  }

  const seconds = value / 1000;
  if (seconds < 1) {
    return `${value} ${t("common.ms")}`;
  }
  return `${Math.round(seconds)} ${t("common.sec")}`;
}

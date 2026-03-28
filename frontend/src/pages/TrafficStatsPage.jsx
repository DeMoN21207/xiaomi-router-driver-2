import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { fetchJSON } from "../api.js";
import { readDashboardRefreshInterval } from "../dashboardPreferences.js";
import { useI18n } from "../i18n.jsx";
import Icon from "../components/Icon.jsx";
import InlineNotice from "../components/InlineNotice.jsx";
import { formatBytes, formatBytesPerSecond, formatDate, formatDateFull } from "../utils.js";

const scopeOptions = ["all", "tunneled", "direct"];
const siteSortOptions = ["bytes", "packets", "domain"];
const deviceHistoryRangeOptions = ["1h", "3h", "1d", "3d", "7d", "30d"];
const deviceSiteLimit = 1;
const devicePageSize = 1;
const sitePageSize = 20;

export default function TrafficStatsPage() {
  const { t } = useI18n();
  const initialLoadRef = useRef(true);

  const [siteData, setSiteData] = useState(null);
  const [deviceData, setDeviceData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [siteSortBy, setSiteSortBy] = useState("bytes");
  const [scope, setScope] = useState("all");
  const [siteSearch, setSiteSearch] = useState("");
  const [selectedDeviceIp, setSelectedDeviceIp] = useState("");
  const [sitePage, setSitePage] = useState(1);
  const [deviceHistoryData, setDeviceHistoryData] = useState(null);
  const [deviceHistoryRange, setDeviceHistoryRange] = useState("1h");
  const [deviceHistoryFrom, setDeviceHistoryFrom] = useState("");
  const [deviceHistoryTo, setDeviceHistoryTo] = useState("");
  const [resetting, setResetting] = useState(false);
  const [collecting, setCollecting] = useState(false);
  const [autoRefreshMs, setAutoRefreshMs] = useState(() => readDashboardRefreshInterval());

  const buildSitesURL = useCallback(() => {
    const query = new URLSearchParams();
    query.set("sort", siteSortBy);
    query.set("page", String(sitePage));
    query.set("pageSize", String(sitePageSize));
    if (scope !== "all") {
      query.set("scope", scope);
    }
    if (siteSearch.trim()) {
      query.set("query", siteSearch.trim());
    }
    if (selectedDeviceIp) {
      query.set("sourceIp", selectedDeviceIp);
      if (deviceHistoryRange === "custom" && deviceHistoryFrom && deviceHistoryTo) {
        const toISO = (value) => (value.includes("T") ? new Date(value).toISOString() : value);
        query.set("from", toISO(deviceHistoryFrom));
        query.set("to", toISO(deviceHistoryTo));
      } else {
        query.set("range", deviceHistoryRange);
      }
      return `/api/traffic/sites/history?${query.toString()}`;
    }
    return `/api/traffic/sites?${query.toString()}`;
  }, [deviceHistoryFrom, deviceHistoryRange, deviceHistoryTo, scope, selectedDeviceIp, sitePage, siteSearch, siteSortBy]);

  const buildDevicesURL = useCallback(() => {
    const query = new URLSearchParams();
    query.set("sort", "name");
    query.set("page", "1");
    query.set("pageSize", String(devicePageSize));
    query.set("siteLimit", String(deviceSiteLimit));
    if (selectedDeviceIp) {
      query.set("sourceIp", selectedDeviceIp);
    }
    return `/api/traffic/devices?${query.toString()}`;
  }, [selectedDeviceIp]);

  const buildDeviceHistoryURL = useCallback(() => {
    if (!selectedDeviceIp) {
      return "";
    }

    const query = new URLSearchParams();
    query.set("sourceIp", selectedDeviceIp);
    if (deviceHistoryRange === "custom" && deviceHistoryFrom && deviceHistoryTo) {
      const toISO = (value) => (value.includes("T") ? new Date(value).toISOString() : value);
      query.set("from", toISO(deviceHistoryFrom));
      query.set("to", toISO(deviceHistoryTo));
    } else {
      query.set("range", deviceHistoryRange);
    }
    return `/api/traffic/devices/history?${query.toString()}`;
  }, [deviceHistoryFrom, deviceHistoryRange, deviceHistoryTo, selectedDeviceIp]);

  const refresh = useCallback(async (initial = false) => {
    if (initial) {
      setLoading(true);
    }

    try {
      const [sitesResult, devicesResult, deviceHistoryResult] = await Promise.all([
        fetchJSON(buildSitesURL()),
        fetchJSON(buildDevicesURL()),
        selectedDeviceIp ? fetchJSON(buildDeviceHistoryURL()) : Promise.resolve(null),
      ]);
      setSiteData(sitesResult);
      setDeviceData(devicesResult);
      setDeviceHistoryData(deviceHistoryResult);
      setError("");
    } catch (err) {
      setError(err.message);
    } finally {
      if (initial) {
        setLoading(false);
      }
    }
  }, [buildDeviceHistoryURL, buildDevicesURL, buildSitesURL, selectedDeviceIp]);

  useEffect(() => {
    const initial = initialLoadRef.current;
    initialLoadRef.current = false;
    void refresh(initial);
  }, [refresh]);

  useEffect(() => {
    setSitePage(1);
  }, [scope, siteSortBy, siteSearch, selectedDeviceIp]);

  useEffect(() => {
    if (siteData && siteData.totalPages > 0 && sitePage > siteData.totalPages) {
      setSitePage(siteData.totalPages);
    }
  }, [siteData, sitePage]);

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
      void refresh(false);
    }, autoRefreshMs);

    return () => window.clearInterval(timerId);
  }, [autoRefreshMs, refresh]);

  const deviceOptions = deviceData?.options ?? [];

  useEffect(() => {
    if (selectedDeviceIp && !deviceOptions.some((item) => item.sourceIp === selectedDeviceIp)) {
      setSelectedDeviceIp("");
    }
  }, [deviceOptions, selectedDeviceIp]);

  useEffect(() => {
    if (!selectedDeviceIp) {
      setDeviceHistoryData(null);
    }
  }, [selectedDeviceIp]);

  async function handleCollectNow() {
    setCollecting(true);
    try {
      const [sitesResult, devicesResult, deviceHistoryResult] = await Promise.all([
        fetchJSON(buildSitesURL(), { method: "POST" }),
        fetchJSON(buildDevicesURL()),
        selectedDeviceIp ? fetchJSON(buildDeviceHistoryURL()) : Promise.resolve(null),
      ]);
      setSiteData(sitesResult);
      setDeviceData(devicesResult);
      setDeviceHistoryData(deviceHistoryResult);
      setError("");
    } catch (err) {
      setError(err.message);
    } finally {
      setCollecting(false);
    }
  }

  async function handleReset() {
    if (!window.confirm(t("trafficStats.confirmReset"))) {
      return;
    }

    setResetting(true);
    try {
      await fetchJSON("/api/traffic/sites", { method: "DELETE" });
      setSitePage(1);
      await refresh(true);
    } catch (err) {
      setError(err.message);
    } finally {
      setResetting(false);
    }
  }

  const sites = siteData?.sites ?? [];
  const totalBytes = siteData?.totalBytes ?? 0;
  const siteTotal = siteData?.total ?? 0;
  const deviceTotal = deviceData?.total ?? 0;
  const maxSiteBytes = useMemo(
    () => sites.reduce((maxValue, item) => Math.max(maxValue, item.bytes), 0),
    [sites],
  );
  const selectedDevice = selectedDeviceIp
    ? deviceOptions.find((item) => item.sourceIp === selectedDeviceIp) ?? null
    : null;
  const selectedDeviceStats = selectedDeviceIp
    ? deviceData?.devices?.find((item) => item.sourceIp === selectedDeviceIp) ?? null
    : null;
  const selectedDeviceHistory = selectedDeviceIp ? deviceHistoryData : null;

  const summaryCards = selectedDeviceIp
    ? [
        {
          label: t("trafficStats.traffic"),
          value: loading || !selectedDeviceStats ? "..." : formatBytes(selectedDeviceStats.bytes),
        },
        {
          label: t("trafficStats.packets"),
          value: loading || !selectedDeviceStats ? "..." : selectedDeviceStats.packets.toLocaleString(),
        },
        {
          label: t("trafficStats.tunneled"),
          value: loading || !selectedDeviceStats ? "..." : formatBytes(selectedDeviceStats.tunneledBytes),
        },
        {
          label: t("trafficStats.direct"),
          value: loading || !selectedDeviceStats ? "..." : formatBytes(selectedDeviceStats.directBytes),
        },
      ]
    : [
        {
          label: t("trafficStats.totalTraffic"),
          value: loading ? "..." : formatBytes(totalBytes),
        },
        {
          label: t("trafficStats.sitesTracked"),
          value: loading ? "..." : siteTotal.toLocaleString(),
        },
        {
          label: t("trafficStats.devicesTracked"),
          value: loading ? "..." : deviceTotal.toLocaleString(),
        },
        {
          label: t("trafficStats.lastUpdate"),
          value: loading ? "..." : (formatDate(siteData?.updatedAt || deviceData?.updatedAt) || t("common.notYet")),
        },
      ];

  return (
    <div className="space-y-8">
      <div className="flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <h1 className="font-headline text-3xl font-bold tracking-tight text-primary md:text-4xl">
            {t("trafficStats.title")}
          </h1>
          <p className="mt-1 text-on-surface-variant">{t("trafficStats.subtitle")}</p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <span className="rounded-full border border-outline-variant/20 bg-surface-container px-3 py-1.5 text-[11px] font-bold uppercase tracking-widest text-outline">
            {autoRefreshMs > 0
              ? t("dashboard.autoRefresh", { interval: formatAutoRefreshInterval(autoRefreshMs, t) })
              : t("dashboard.autoRefreshDisabled")}
          </span>
          <button
            type="button"
            onClick={() => refresh(false)}
            disabled={loading || collecting}
            className="flex items-center gap-2 rounded-xl border border-outline-variant/30 bg-surface-container-high px-5 py-2.5 font-headline text-sm font-medium text-on-surface transition-colors hover:bg-surface-variant disabled:opacity-50"
          >
            <Icon name="refresh" className="h-4 w-4 text-primary" />
            {t("dashboard.refresh")}
          </button>
          <button
            type="button"
            onClick={handleCollectNow}
            disabled={collecting || loading}
            className="flex items-center gap-2 rounded-xl border border-primary/20 bg-primary/10 px-5 py-2.5 font-headline text-sm font-medium text-primary transition-colors hover:bg-primary/15 disabled:opacity-50"
          >
            <Icon name="bar_chart" className="h-4 w-4" />
            {collecting ? t("trafficStats.collectingNow") : t("trafficStats.collectNow")}
          </button>
          <button
            type="button"
            onClick={handleReset}
            disabled={resetting || loading || collecting}
            className="flex items-center gap-2 rounded-xl border border-error/20 bg-error-container/10 px-5 py-2.5 font-headline text-sm font-medium text-error transition-colors hover:bg-error-container/20 disabled:opacity-50"
          >
            <Icon name="delete_sweep" className="h-4 w-4" />
            {t("trafficStats.reset")}
          </button>
        </div>
      </div>

      {error ? <InlineNotice tone="error" title={t("error.dashboard")} message={error} /> : null}

      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        {summaryCards.map((card) => (
          <SummaryCard key={card.label} label={card.label} value={card.value} />
        ))}
      </div>

      {selectedDevice ? (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-primary/20 bg-primary/10 px-5 py-4 shadow-lg">
          <div className="min-w-0">
            <div className="text-[11px] font-bold uppercase tracking-widest text-primary">
              {t("trafficStats.filteredByDevice")}
            </div>
            <div className="mt-1 truncate font-headline text-lg font-bold text-on-surface">
              {deviceLabel(selectedDevice)}
            </div>
            <div className="mt-1 truncate font-mono text-xs text-on-surface-variant">
              {selectedDevice.sourceIp}
              {selectedDevice.deviceMac ? ` / ${selectedDevice.deviceMac}` : ""}
            </div>
          </div>
          <button
            type="button"
            onClick={() => setSelectedDeviceIp("")}
            className="rounded-xl border border-primary/20 bg-surface-container px-4 py-2 text-sm font-medium text-on-surface transition-colors hover:border-primary/40 hover:text-primary"
          >
            {t("trafficStats.deviceFilterAll")}
          </button>
        </div>
      ) : null}

      {selectedDevice ? (
        <DeviceActivityHistorySection
          device={selectedDevice}
          history={selectedDeviceHistory}
          range={deviceHistoryRange}
          customFrom={deviceHistoryFrom}
          customTo={deviceHistoryTo}
          onRangeChange={setDeviceHistoryRange}
          onApplyCustomRange={(from, to) => {
            setDeviceHistoryFrom(from);
            setDeviceHistoryTo(to);
            setDeviceHistoryRange("custom");
          }}
          loading={loading}
          t={t}
        />
      ) : null}

      <section id="traffic-sites" className="space-y-4">
        <div className="flex flex-wrap items-center gap-4">
          <div className="relative min-w-[260px] flex-1">
            <Icon name="search" className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-outline" />
            <input
              type="text"
              value={siteSearch}
              onChange={(event) => setSiteSearch(event.target.value)}
              placeholder={t("trafficStats.searchPlaceholder")}
              className="w-full rounded-xl border border-outline-variant/20 bg-surface-container-lowest/50 py-2.5 pl-10 pr-4 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
            />
          </div>

          <select
            value={selectedDeviceIp}
            onChange={(event) => setSelectedDeviceIp(event.target.value)}
            className="min-w-[260px] rounded-xl border border-outline-variant/20 bg-surface-container-lowest/50 px-4 py-2.5 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
          >
            <option value="">{t("trafficStats.deviceFilterAll")}</option>
            {deviceOptions.map((item) => (
              <option key={item.sourceIp} value={item.sourceIp}>
                {deviceOptionLabel(item)}
              </option>
            ))}
          </select>

          <div className="flex flex-wrap gap-2">
            {scopeOptions.map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => setScope(option)}
                className={`rounded-full border px-3 py-1.5 text-xs font-bold uppercase tracking-wider transition-colors ${
                  scope === option
                    ? "border-secondary/30 bg-secondary/10 text-secondary"
                    : "border-outline-variant/20 bg-surface-container-high text-on-surface-variant hover:border-secondary/30 hover:text-on-surface"
                }`}
              >
                {t(`trafficStats.scope.${option}`)}
              </button>
            ))}
          </div>

          <div className="flex flex-wrap gap-2">
            {siteSortOptions.map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => setSiteSortBy(option)}
                className={`rounded-full border px-3 py-1.5 text-xs font-bold uppercase tracking-wider transition-colors ${
                  siteSortBy === option
                    ? "border-primary/30 bg-primary/10 text-primary"
                    : "border-outline-variant/20 bg-surface-container-high text-on-surface-variant hover:border-primary/30 hover:text-on-surface"
                }`}
              >
                {t(`trafficStats.sort.${option}`)}
              </button>
            ))}
          </div>
        </div>

        <div className="overflow-hidden rounded-2xl border border-outline-variant/10 bg-surface-container-low shadow-xl">
          {loading && !siteData ? (
            <div className="p-8 text-center text-sm text-on-surface-variant">...</div>
          ) : sites.length === 0 ? (
            <div className="p-8 text-center text-sm text-on-surface-variant">
              {siteSearch || selectedDevice ? t("trafficStats.noResults") : t("trafficStats.empty")}
            </div>
          ) : (
            <>
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b border-outline-variant/10 bg-surface-container text-left">
                      <th className="px-5 py-3 text-[10px] font-bold uppercase tracking-widest text-outline">#</th>
                      <th className="px-5 py-3 text-[10px] font-bold uppercase tracking-widest text-outline">{t("trafficStats.domain")}</th>
                      <th className="px-5 py-3 text-[10px] font-bold uppercase tracking-widest text-outline">{t("trafficStats.route")}</th>
                      <th className="px-5 py-3 text-right text-[10px] font-bold uppercase tracking-widest text-outline">{t("trafficStats.traffic")}</th>
                      <th className="hidden px-5 py-3 text-right text-[10px] font-bold uppercase tracking-widest text-outline lg:table-cell">{t("trafficStats.packets")}</th>
                      <th className="hidden px-5 py-3 text-right text-[10px] font-bold uppercase tracking-widest text-outline xl:table-cell">{t("trafficStats.share")}</th>
                      <th className="px-5 py-3 text-[10px] font-bold uppercase tracking-widest text-outline" style={{ minWidth: 140 }}></th>
                    </tr>
                  </thead>
                  <tbody>
                    {sites.map((item, index) => {
                      const share = totalBytes > 0 ? (item.bytes / totalBytes) * 100 : 0;
                      const barWidth = maxSiteBytes > 0 ? Math.max(2, (item.bytes / maxSiteBytes) * 100) : 0;
                      return (
                        <tr key={`${item.domain}-${item.lastIp}`} className="border-b border-outline-variant/5 transition-colors hover:bg-surface-container/50">
                          <td className="px-5 py-3 font-mono text-xs text-outline">
                            {(siteData.page - 1) * siteData.pageSize + index + 1}
                          </td>
                          <td className="px-5 py-3">
                            <div className="flex flex-col gap-1">
                              <span className="font-medium text-on-surface">{item.domain}</span>
                              <span className="font-mono text-[11px] text-outline-variant">{item.lastIp || "-"}</span>
                            </div>
                          </td>
                          <td className="px-5 py-3">
                            <RouteBadge item={item} t={t} />
                          </td>
                          <td className="px-5 py-3 text-right font-mono text-secondary">{formatBytes(item.bytes)}</td>
                          <td className="hidden px-5 py-3 text-right font-mono text-outline-variant lg:table-cell">
                            {item.packets.toLocaleString()}
                          </td>
                          <td className="hidden px-5 py-3 text-right font-mono text-outline-variant xl:table-cell">
                            {share.toFixed(1)}%
                          </td>
                          <td className="px-5 py-3">
                            <div className="h-2 overflow-hidden rounded-full bg-surface-container-highest/50">
                              <div
                                className={`h-full rounded-full transition-[width] duration-500 ${
                                  item.viaTunnel
                                    ? "bg-gradient-to-r from-primary via-secondary to-tertiary"
                                    : "bg-gradient-to-r from-outline-variant via-outline to-outline"
                                }`}
                                style={{ width: `${barWidth}%` }}
                              />
                            </div>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
              <PaginationControls
                page={siteData.page}
                pageSize={siteData.pageSize}
                total={siteData.total}
                totalPages={siteData.totalPages}
                onPageChange={setSitePage}
                t={t}
              />
            </>
          )}
        </div>
      </section>
    </div>
  );
}

function DeviceActivityHistorySection({ device, history, range, customFrom, customTo, onRangeChange, onApplyCustomRange, loading, t }) {
  const [showCustom, setShowCustom] = useState(range === "custom");
  const [localFrom, setLocalFrom] = useState(customFrom || "");
  const [localTo, setLocalTo] = useState(customTo || "");
  const points = history?.points ?? [];
  const chart = buildDeviceHistoryChart(points);
  const latestPoint = latestActiveDevicePoint(points);
  const liveRate = history?.bucketSeconds > 0 ? (latestPoint?.bytes || 0) / history.bucketSeconds : 0;
  const hasData = (history?.totalBytes || 0) > 0;

  useEffect(() => {
    setShowCustom(range === "custom");
  }, [range]);

  useEffect(() => {
    setLocalFrom(customFrom || "");
  }, [customFrom]);

  useEffect(() => {
    setLocalTo(customTo || "");
  }, [customTo]);

  return (
    <section className="overflow-hidden rounded-2xl border border-outline-variant/10 bg-surface-container-low shadow-xl">
      <div className="border-b border-outline-variant/10 bg-surface-container px-6 py-5">
        <div className="flex flex-col gap-4 xl:flex-row xl:items-end xl:justify-between">
          <div>
            <h2 className="font-headline text-xl font-bold tracking-tight text-primary">{t("trafficStats.historyTitle")}</h2>
            <p className="mt-1 max-w-3xl text-sm text-on-surface-variant">{t("trafficStats.historySubtitle")}</p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {deviceHistoryRangeOptions.map((option) => {
              const active = option === range && !showCustom;
              return (
                <button
                  key={option}
                  type="button"
                  onClick={() => {
                    if (!loading) {
                      setShowCustom(false);
                      onRangeChange(option);
                    }
                  }}
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
              onClick={() => setShowCustom((value) => !value)}
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

        {showCustom ? (
          <div className="mt-3 flex flex-wrap items-end gap-3">
            <div>
              <label className="mb-1 block text-[10px] font-bold uppercase tracking-widest text-outline">{t("dashboard.dateFrom")}</label>
              <input
                type="datetime-local"
                value={localFrom}
                onChange={(event) => setLocalFrom(event.target.value)}
                className="rounded-lg border border-outline-variant/20 bg-surface-container-lowest/50 px-3 py-1.5 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
              />
            </div>
            <div>
              <label className="mb-1 block text-[10px] font-bold uppercase tracking-widest text-outline">{t("dashboard.dateTo")}</label>
              <input
                type="datetime-local"
                value={localTo}
                onChange={(event) => setLocalTo(event.target.value)}
                className="rounded-lg border border-outline-variant/20 bg-surface-container-lowest/50 px-3 py-1.5 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
              />
            </div>
            <button
              type="button"
              disabled={loading || !localFrom || !localTo}
              onClick={() => onApplyCustomRange(localFrom, localTo)}
              className="rounded-lg bg-primary/10 px-4 py-1.5 text-sm font-medium text-primary transition-colors hover:bg-primary/20 disabled:opacity-40"
            >
              {t("dashboard.applyRange")}
            </button>
          </div>
        ) : null}

        <div className="mt-4 grid gap-3 md:grid-cols-2 xl:grid-cols-4">
          <SummaryCard label={t("trafficStats.historyRangeTotal")} value={loading ? "..." : formatBytes(history?.totalBytes || 0)} />
          <SummaryCard label={t("trafficStats.historyTunneled")} value={loading ? "..." : formatBytes(history?.tunneledBytes || 0)} />
          <SummaryCard label={t("trafficStats.historyDirect")} value={loading ? "..." : formatBytes(history?.directBytes || 0)} />
          <SummaryCard label={t("trafficStats.historyLiveRate")} value={loading ? "..." : formatBytesPerSecond(liveRate)} />
        </div>

        <div className="mt-4 flex flex-wrap gap-3">
          <InlineMetric label={t("trafficStats.historyPeak")} value={loading ? "..." : formatBytes(history?.peakBytes || 0)} />
          <InlineMetric label={t("trafficStats.historyLatestBucket")} value={loading ? "..." : formatBytes(latestPoint?.bytes || 0)} />
          <InlineMetric label={t("trafficStats.historyLastSample")} value={loading ? "..." : (formatDateFull(history?.latestSampleAt) || t("common.notYet"))} />
        </div>
      </div>

      <div className="px-6 py-6">
        {loading && !history ? (
          <div className="rounded-2xl bg-surface-container p-6 text-sm text-on-surface-variant">...</div>
        ) : !hasData || !chart ? (
          <div className="rounded-2xl border border-dashed border-outline-variant/20 bg-surface-container p-6 text-sm text-on-surface-variant">
            <p className="font-medium text-on-surface">{t("trafficStats.historyEmpty")}</p>
            <p className="mt-2">{t("trafficStats.historyCollecting")}</p>
          </div>
        ) : (
          <DeviceHistoryChart chart={chart} history={history} device={device} range={range} t={t} />
        )}
      </div>
    </section>
  );
}

function SummaryCard({ label, value }) {
  return (
    <div className="rounded-xl border border-outline-variant/10 bg-surface-container-low px-5 py-3">
      <p className="mb-1 text-[10px] font-bold uppercase tracking-widest text-outline">{label}</p>
      <p className="font-headline text-2xl font-bold text-on-surface">{value}</p>
    </div>
  );
}

function InlineMetric({ label, value }) {
  return (
    <div className="rounded-full border border-outline-variant/20 bg-surface-container-high px-3 py-1.5 text-[11px] font-bold uppercase tracking-widest text-outline">
      {label}: <span className="ml-1 font-mono text-on-surface">{value}</span>
    </div>
  );
}

function PaginationControls({ page, pageSize, total, totalPages, onPageChange, t }) {
  if (!Number.isFinite(totalPages) || totalPages <= 1) {
    return null;
  }

  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(page * pageSize, total);

  return (
    <div className="flex flex-col gap-3 border-t border-outline-variant/10 bg-surface-container px-5 py-4 md:flex-row md:items-center md:justify-between">
      <div className="text-sm text-on-surface-variant">
        {t("trafficStats.pageStatus", {
          from: from.toLocaleString(),
          to: to.toLocaleString(),
          total: total.toLocaleString(),
        })}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <span className="rounded-full border border-outline-variant/20 bg-surface-container-high px-3 py-1 text-[11px] font-bold uppercase tracking-widest text-outline">
          {t("trafficStats.paginationPage", {
            page: page.toLocaleString(),
            totalPages: totalPages.toLocaleString(),
          })}
        </span>
        <button
          type="button"
          onClick={() => onPageChange(Math.max(1, page - 1))}
          disabled={page <= 1}
          className="rounded-xl border border-outline-variant/20 bg-surface-container-high px-4 py-2 text-sm font-medium text-on-surface transition-colors hover:bg-surface-variant disabled:opacity-50"
        >
          {t("trafficStats.paginationPrev")}
        </button>
        <button
          type="button"
          onClick={() => onPageChange(Math.min(totalPages, page + 1))}
          disabled={page >= totalPages}
          className="rounded-xl border border-outline-variant/20 bg-surface-container-high px-4 py-2 text-sm font-medium text-on-surface transition-colors hover:bg-surface-variant disabled:opacity-50"
        >
          {t("trafficStats.paginationNext")}
        </button>
      </div>
    </div>
  );
}

function RouteBadge({ item, t }) {
  return (
    <div className="flex flex-col gap-2">
      <span className={`inline-flex w-fit items-center gap-1 rounded-full px-2.5 py-1 text-[10px] font-bold uppercase tracking-widest ${
        item.viaTunnel
          ? "bg-secondary/10 text-secondary"
          : "bg-surface-container-high text-on-surface-variant"
      }`}>
        <span className={`h-1.5 w-1.5 rounded-full ${item.viaTunnel ? "bg-secondary" : "bg-outline-variant"}`} />
        {item.viaTunnel ? t("trafficStats.tunneled") : t("trafficStats.direct")}
      </span>
      <span className="text-xs text-on-surface-variant">
        {item.routeLabel || t("trafficStats.directRoute")}
      </span>
    </div>
  );
}

function buildDeviceHistoryChart(points) {
  if (!points || points.length === 0) {
    return null;
  }

  const maxValue = points.reduce(
    (max, point) => Math.max(max, point.bytes || 0, point.tunneledBytes || 0, point.directBytes || 0),
    0,
  );
  if (maxValue <= 0) {
    return null;
  }

  const width = 720;
  const height = 220;
  const padding = 18;
  const innerWidth = width - padding * 2;
  const innerHeight = height - padding * 2;

  const toCoord = (value, index) => {
    const x = padding + (points.length === 1 ? innerWidth / 2 : (index * innerWidth) / (points.length - 1));
    const y = padding + innerHeight - ((value || 0) / maxValue) * innerHeight;
    return { x, y };
  };

  const tunneled = points.map((point, index) => ({
    ...toCoord(point.tunneledBytes, index),
    at: point.at,
    bytes: point.tunneledBytes || 0,
  }));
  const direct = points.map((point, index) => ({
    ...toCoord(point.directBytes, index),
    at: point.at,
    bytes: point.directBytes || 0,
  }));
  const baseline = padding + innerHeight;

  const buildPath = (coords) => coords.map((coord, index) => `${index === 0 ? "M" : "L"} ${coord.x.toFixed(1)} ${coord.y.toFixed(1)}`).join(" ");
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
    maxValue,
    tunneled: { linePath: buildPath(tunneled), areaPath: buildArea(tunneled), coords: tunneled },
    direct: { linePath: buildPath(direct), areaPath: buildArea(direct), coords: direct },
    labels: uniqueDeviceHistoryLabels([points[0], points[Math.floor(points.length / 2)], points[points.length - 1]]),
    guides: [0, 0.33, 0.66, 1].map((ratio) => ({
      id: ratio,
      y: padding + innerHeight - innerHeight * ratio,
    })),
  };
}

function latestActiveDevicePoint(points) {
  if (!points || points.length === 0) {
    return null;
  }

  for (let index = points.length - 1; index >= 0; index -= 1) {
    const point = points[index];
    if ((point.bytes || 0) > 0 || (point.tunneledBytes || 0) > 0 || (point.directBytes || 0) > 0) {
      return point;
    }
  }

  return points[points.length - 1];
}

function uniqueDeviceHistoryLabels(labels) {
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

function DeviceHistoryChart({ chart, history, device, range, t }) {
  const svgRef = useRef(null);
  const tooltipPoints = chart.tunneled.coords.map((point, index) => ({
    at: point.at,
    payload: {
      at: point.at,
      tunneledBytes: point.bytes || 0,
      directBytes: chart.direct.coords[index]?.bytes || 0,
      bytes: (point.bytes || 0) + (chart.direct.coords[index]?.bytes || 0),
    },
    hits: [
      {
        x: chart.tunneled.coords[index]?.x || 0,
        y: chart.tunneled.coords[index]?.y ?? chart.height,
      },
      {
        x: chart.direct.coords[index]?.x || 0,
        y: chart.direct.coords[index]?.y ?? chart.height,
      },
    ],
  }));

  return (
    <div className="rounded-2xl border border-outline-variant/10 bg-surface-container p-4">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-4">
        <div>
          <p className="font-headline text-sm font-bold uppercase tracking-widest text-outline">{t("trafficStats.historyTitle")}</p>
          <p className="mt-1 text-xs text-on-surface-variant">
            {deviceLabel(device)} · {formatBytes(history?.totalBytes || 0)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-3 text-[10px] font-bold uppercase tracking-widest text-outline">
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: "rgba(0, 180, 130, 0.9)" }} />
            {t("trafficStats.historyTunneled")}
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-full" style={{ background: "rgba(0, 82, 204, 0.9)" }} />
            {t("trafficStats.historyDirect")}
          </span>
        </div>
      </div>
      <div className="relative">
        <svg ref={svgRef} viewBox={`0 0 ${chart.width} ${chart.height}`} className="h-56 w-full overflow-visible" style={{ touchAction: "none" }}>
          <defs>
            <linearGradient id="deviceHistoryTunnelArea" x1="0%" y1="0%" x2="0%" y2="100%">
              <stop offset="0%" stopColor="rgba(0, 180, 130, 0.35)" />
              <stop offset="100%" stopColor="rgba(0, 180, 130, 0.02)" />
            </linearGradient>
            <linearGradient id="deviceHistoryDirectArea" x1="0%" y1="0%" x2="0%" y2="100%">
              <stop offset="0%" stopColor="rgba(0, 82, 204, 0.25)" />
              <stop offset="100%" stopColor="rgba(0, 82, 204, 0.02)" />
            </linearGradient>
          </defs>
          {chart.guides.map((guide) => (
            <line key={guide.id} x1={chart.padding} y1={guide.y} x2={chart.width - chart.padding} y2={guide.y} stroke="rgba(116, 119, 138, 0.18)" strokeDasharray="6 8" />
          ))}
          <path d={chart.tunneled.areaPath} fill="url(#deviceHistoryTunnelArea)" />
          <path d={chart.tunneled.linePath} fill="none" stroke="rgba(0, 180, 130, 0.95)" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
          <path d={chart.direct.areaPath} fill="url(#deviceHistoryDirectArea)" />
          <path d={chart.direct.linePath} fill="none" stroke="rgba(0, 82, 204, 0.95)" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
          {chart.tunneled.coords.map((coord) => (
            <circle key={`tun-${coord.at}`} cx={coord.x} cy={coord.y} r="3.5" fill="rgba(0, 180, 130, 0.95)" />
          ))}
          {chart.direct.coords.map((coord) => (
            <circle key={`dir-${coord.at}`} cx={coord.x} cy={coord.y} r="3.5" fill="rgba(0, 82, 204, 0.95)" />
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
                  {t("trafficStats.historyTunneled")}
                </span>
                <span className="font-mono text-xs font-bold text-on-surface">{formatBytes(point.tunneledBytes || 0)}</span>
              </div>
              <div className="flex items-center justify-between gap-3">
                <span className="inline-flex items-center gap-1.5 text-[10px] text-outline">
                  <span className="h-1.5 w-1.5 rounded-full" style={{ background: "rgba(0, 82, 204, 0.9)" }} />
                  {t("trafficStats.historyDirect")}
                </span>
                <span className="font-mono text-xs font-bold text-on-surface">{formatBytes(point.directBytes || 0)}</span>
              </div>
              <div className="mt-1 border-t border-outline-variant/10 pt-1">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-[10px] text-outline">{t("trafficStats.historyRangeTotal")}</span>
                  <span className="font-mono text-xs font-bold text-on-surface">{formatBytes(point.bytes || 0)}</span>
                </div>
              </div>
            </div>
          )}
        />
      </div>
      <div className="mt-3 flex items-center justify-between gap-3 text-[10px] font-bold uppercase tracking-widest text-outline">
        {chart.labels.map((label) => (
          <span key={label.at}>{formatTrafficPointLabel(label.at, range)}</span>
        ))}
      </div>
    </div>
  );
}

function ChartTooltip({ svgRef, points, range, renderContent, mode = "proximity", threshold = 24 }) {
  const [active, setActive] = useState(null);
  const [pos, setPos] = useState({ x: 0, y: 0 });

  const handleMove = useCallback((event) => {
    const svg = svgRef.current;
    if (!svg || !points || points.length === 0) return;

    const rect = svg.getBoundingClientRect();
    const mouseX = event.clientX - rect.left;
    const mouseY = event.clientY - rect.top;
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
  }, [mode, points, svgRef, threshold]);

  const handleLeave = useCallback(() => setActive(null), []);

  useEffect(() => {
    const svg = svgRef.current;
    if (!svg) return undefined;
    svg.addEventListener("pointermove", handleMove);
    svg.addEventListener("pointerleave", handleLeave);
    return () => {
      svg.removeEventListener("pointermove", handleMove);
      svg.removeEventListener("pointerleave", handleLeave);
    };
  }, [handleLeave, handleMove, svgRef]);

  if (!active) {
    return null;
  }

  const tooltipWidth = 180;
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

function deviceLabel(device) {
  const name = (device.deviceName || "").trim();
  return name || device.sourceIp;
}

function deviceOptionLabel(device) {
  const name = (device.deviceName || "").trim();
  if (name) {
    return `${name} (${device.sourceIp})`;
  }
  return device.sourceIp;
}

function formatAutoRefreshInterval(value, t) {
  if (!Number.isFinite(value) || value <= 0) {
    return t("common.notYet");
  }

  const seconds = Math.round(value / 1000);
  return `${seconds} ${t("common.sec")}`;
}

import { useEffect, useState } from "react";
import { fetchJSON } from "../api.js";
import { DASHBOARD_REFRESH_OPTIONS, readDashboardRefreshInterval, writeDashboardRefreshInterval } from "../dashboardPreferences.js";
import { useI18n, languages } from "../i18n.jsx";
import Icon from "../components/Icon.jsx";
import InlineNotice from "../components/InlineNotice.jsx";
import { formatBytes, formatDate, formatLatencyMs } from "../utils.js";

const STATUS_REFRESH_MS = 10_000;

export default function SettingsPage() {
  const { t, lang, setLang } = useI18n();
  const [status, setStatus] = useState(null);
  const [config, setConfig] = useState(null);
  const [domainTraffic, setDomainTraffic] = useState({ domains: [], totalBytes: 0, updatedAt: "" });
  const [routing, setRouting] = useState(null);
  const [automation, setAutomation] = useState({ installService: false, autoRecover: false });
  const [error, setError] = useState("");
  const [saveError, setSaveError] = useState("");
  const [saveMessage, setSaveMessage] = useState("");
  const [saving, setSaving] = useState(false);
  const [automationError, setAutomationError] = useState("");
  const [automationMessage, setAutomationMessage] = useState("");
  const [savingAutomation, setSavingAutomation] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  const [dashboardRefreshMs, setDashboardRefreshMs] = useState(() => readDashboardRefreshInterval());

  const toggleTheme = () => {
    const next = !dark;
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
    localStorage.setItem("theme", next ? "dark" : "light");
  };

  useEffect(() => {
    let active = true;

    const loadInitial = async () => {
      try {
        const [nextStatus, nextConfig, nextDomainTraffic] = await Promise.all([
          fetchJSON("/api/status"),
          fetchJSON("/api/config"),
          fetchJSON("/api/traffic/domains?sort=bytes&limit=10"),
        ]);
        if (!active) return;

        setStatus(nextStatus);
        setConfig(nextConfig);
        setDomainTraffic(nextDomainTraffic || { domains: [], totalBytes: 0, updatedAt: "" });
        setRouting(nextConfig.routing || null);
        setAutomation(nextConfig.automation || { installService: false, autoRecover: false });
        setError("");
      } catch (err) {
        if (!active) return;
        setError(err.message);
      }
    };

    const refreshStatus = async () => {
      try {
        const [nextStatus, nextDomainTraffic] = await Promise.all([
          fetchJSON("/api/status"),
          fetchJSON("/api/traffic/domains?sort=bytes&limit=10"),
        ]);
        if (!active) return;
        setStatus(nextStatus);
        setDomainTraffic(nextDomainTraffic || { domains: [], totalBytes: 0, updatedAt: "" });
      } catch (err) {
        if (!active) return;
        setError(err.message);
      }
    };

    loadInitial();
    const timerId = window.setInterval(refreshStatus, STATUS_REFRESH_MS);

    return () => {
      active = false;
      window.clearInterval(timerId);
    };
  }, []);

  const refreshAll = async () => {
    setRefreshing(true);
    try {
      const [nextStatus, nextConfig, nextDomainTraffic] = await Promise.all([
        fetchJSON("/api/status"),
        fetchJSON("/api/config"),
        fetchJSON("/api/traffic/domains?sort=bytes&limit=10"),
      ]);

      setStatus(nextStatus);
      setConfig(nextConfig);
      setDomainTraffic(nextDomainTraffic || { domains: [], totalBytes: 0, updatedAt: "" });
      setRouting(nextConfig.routing || null);
      setAutomation(nextConfig.automation || { installService: false, autoRecover: false });
      setError("");
    } catch (err) {
      setError(err.message);
    } finally {
      setRefreshing(false);
    }
  };

  const providers = config?.providers ?? [];
  const rules = config?.rules ?? [];
  const subscriptionRuntime = status?.subscriptionRuntime ?? [];
  const topTrafficDomains = domainTraffic?.domains ?? [];

  const updateRoutingField = (field, value) => {
    setRouting((current) => (current ? { ...current, [field]: value } : current));
    setSaveError("");
    setSaveMessage("");
  };

  const updateAutomationField = (field, value) => {
    setAutomation((current) => ({ ...current, [field]: value }));
    setAutomationError("");
    setAutomationMessage("");
  };

  const saveRoutingSettings = async (event) => {
    event.preventDefault();
    if (!routing) return;

    setSaving(true);
    setSaveError("");
    setSaveMessage("");

    try {
      const saved = await fetchJSON("/api/config/routing", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          ...routing,
          tableNum: Number(routing.tableNum),
        }),
      });

      setConfig(saved);
      setRouting(saved.routing || null);
      setSaveMessage(t("settings.saved"));
      setError("");
    } catch (err) {
      setSaveError(err.message);
    } finally {
      setSaving(false);
    }
  };

  const saveAutomationSettings = async (event) => {
    event.preventDefault();

    setSavingAutomation(true);
    setAutomationError("");
    setAutomationMessage("");

    try {
      const saved = await fetchJSON("/api/config/automation", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(automation),
      });

      setConfig(saved);
      setAutomation(saved.automation || { installService: false, autoRecover: false });
      setAutomationMessage(t("settings.automationSaved"));
      setError("");
    } catch (err) {
      setAutomationError(err.message);
    } finally {
      setSavingAutomation(false);
    }
  };

  return (
    <div className="space-y-8">
      <header className="mb-2 flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <h1 className="mb-2 font-headline text-4xl font-bold tracking-tight text-primary">{t("settings.title")}</h1>
          <p className="max-w-2xl text-on-surface-variant">{t("settings.subtitle")}</p>
        </div>
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={refreshAll}
            disabled={refreshing}
            className="flex items-center gap-2 rounded-xl border border-outline-variant/20 bg-surface-container-high px-5 py-3 font-headline text-sm font-medium text-on-surface transition-colors hover:bg-surface-variant disabled:opacity-50"
          >
            <Icon name="refresh" className={`h-4 w-4 text-primary${refreshing ? " animate-spin" : ""}`} />
            {refreshing ? t("dashboard.refreshing") : t("dashboard.refresh")}
          </button>
        </div>
      </header>

      {error ? <InlineNotice tone="error" title={t("error.settings")} message={error} /> : null}

      <div className="grid grid-cols-1 gap-8 lg:grid-cols-12">
        <div className="space-y-8 lg:col-span-7">
          <Section icon="health_and_safety" iconColor="text-secondary" title={t("settings.health")}>
            <div className="space-y-4">
              {status?.runtimeOS === "windows" ? (
                <InlineNotice
                  tone="info"
                  title={t("settings.hostModeLocal")}
                  message={t("settings.hostModeLocalHint")}
                />
              ) : null}
              {/* Network */}
              <div>
                <p className="mb-2 text-[10px] font-bold uppercase tracking-widest text-outline">{t("settings.healthNetwork")}</p>
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  <HealthCard icon="language" label={t("settings.wan")} value={t(`common.wan.${status?.wan?.state || "unknown"}`)} ok={status?.wan?.state === "up"} />
                  <HealthCard icon="speed" label={t("settings.wanPing")} value={formatLatencyMs(status?.wan?.latencyMs, t)} />
                </div>
              </div>
              {/* Binaries */}
              <div>
                <p className="mb-2 text-[10px] font-bold uppercase tracking-widest text-outline">{t("settings.healthBinaries")}</p>
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  <HealthCard icon="vpn_key" label={t("settings.openvpn")} value={status?.binaries?.openvpn ? t("common.found") : t("common.notFound")} ok={status?.binaries?.openvpn} />
                  <HealthCard icon="hub" label={t("settings.singbox")} value={status?.binaries?.singbox ? t("common.found") : t("common.notFound")} ok={status?.binaries?.singbox} />
                </div>
              </div>
              {/* Files */}
              <div>
                <p className="mb-2 text-[10px] font-bold uppercase tracking-widest text-outline">{t("settings.healthFiles")}</p>
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  <HealthCard icon="description" label={t("settings.updateRoutes")} value={status?.files?.updateRoutes ? t("common.inPlace") : t("common.notFound")} ok={status?.files?.updateRoutes} />
                  <HealthCard icon="folder" label={t("settings.rootDir")} value={status?.projectDirectory || "-"} />
                  <HealthCard icon="database" label={t("settings.dataDir")} value={status?.dataDirectory || "-"} />
                  <HealthCard icon="computer" label={t("settings.host")} value={formatHostLabel(status)} />
                </div>
              </div>
            </div>
          </Section>

          <Section icon="alt_route" iconColor="text-tertiary" title={t("settings.rules")}>
            <div className="space-y-3">
              {rules.length === 0 ? (
                <p className="text-sm text-on-surface-variant">{t("settings.noRules")}</p>
              ) : (
                rules.map((rule) => {
                  const provider = providers.find((item) => item.id === rule.providerId);
                  return (
                    <div key={rule.id} className="flex items-center justify-between rounded-lg bg-surface-container p-3">
                      <div>
                        <p className="text-sm font-medium text-on-surface">{rule.name}</p>
                        <p className="text-xs text-on-surface-variant">
                          {provider?.name || rule.providerId} · {rule.domains.length} {t("settings.domainsCount")}
                        </p>
                      </div>
                      <span
                        className={`rounded px-2 py-0.5 text-[10px] font-bold uppercase tracking-widest ${
                          rule.enabled ? "bg-secondary/10 text-secondary" : "bg-outline-variant/20 text-outline"
                        }`}
                      >
                        {rule.enabled ? t("settings.ruleActive") : t("settings.ruleOff")}
                      </span>
                    </div>
                  );
                })
              )}
            </div>
          </Section>

          <Section icon="lan" iconColor="text-secondary" title={t("settings.subscriptionRuntime")}>
            {subscriptionRuntime.length === 0 ? (
              <p className="text-sm text-on-surface-variant">{t("settings.subscriptionRuntimeEmpty")}</p>
            ) : (
              <div className="space-y-3">
                {subscriptionRuntime.map((instance) => (
                  <div key={instance.key} className="rounded-lg border border-outline-variant/10 bg-surface-container p-4">
                    <div className="mb-3 flex items-start justify-between gap-3">
                      <div>
                        <p className="font-headline text-sm font-bold text-on-surface">{instance.location}</p>
                        <p className="text-xs text-on-surface-variant">{instance.providerName}</p>
                      </div>
                      <span
                        className={`rounded-full px-2.5 py-1 text-[10px] font-bold uppercase tracking-widest ${
                          instance.status === "running" ? "bg-secondary/10 text-secondary" : "bg-error/10 text-error"
                        }`}
                      >
                        {instance.status === "running" ? t("settings.runtimeRunning") : t("settings.runtimeStopped")}
                      </span>
                    </div>

                    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                      <RuntimeMeta label="tun" value={instance.interfaceName} />
                      <RuntimeMeta label="fwmark" value={instance.fwMark} />
                      <RuntimeMeta label="table" value={String(instance.tableNum)} />
                      <RuntimeMeta label="ipset" value={instance.ipSetName} />
                      <RuntimeMeta label={t("settings.runtimeDomains")} value={String(instance.domainCount)} />
                      <RuntimeMeta label="pid" value={instance.pid ? String(instance.pid) : "-"} />
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Section>

          <Section icon="dns" iconColor="text-primary" title={t("settings.domains")}>
            {topTrafficDomains.length === 0 ? (
              <p className="text-sm text-on-surface-variant">{t("settings.domainsEmpty")}</p>
            ) : (
              <div className="overflow-hidden rounded-lg border border-outline-variant/10">
                <table className="w-full text-left">
                  <thead>
                    <tr className="border-b border-outline-variant/10 bg-surface-container-high">
                      <th className="px-4 py-2.5 text-[10px] font-bold uppercase tracking-widest text-outline">#</th>
                      <th className="px-4 py-2.5 text-[10px] font-bold uppercase tracking-widest text-outline">{t("settings.domainColumn")}</th>
                      <th className="px-4 py-2.5 text-right text-[10px] font-bold uppercase tracking-widest text-outline">{t("trafficStats.traffic")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {topTrafficDomains.map((item, idx) => (
                      <tr key={item.domain} className="border-b border-outline-variant/5 bg-surface-container transition-colors last:border-b-0 hover:bg-surface-container-high">
                        <td className="px-4 py-2 text-xs text-outline-variant">
                          <RankBadge rank={idx + 1} />
                        </td>
                        <td className="px-4 py-2 font-mono text-sm text-on-surface">{item.domain}</td>
                        <td className="px-4 py-2 text-right font-mono text-sm text-on-surface">{formatBytes(item.bytes || 0)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Section>
        </div>

        <div className="space-y-8 lg:col-span-5">
          <Section icon="analytics" iconColor="text-primary" title={t("settings.stats")}>
            <div className="space-y-3">
              <StatRow label={t("settings.providersCount")} value={providers.length} />
              <StatRow label={t("settings.activeRules")} value={rules.filter((rule) => rule.enabled).length} />
              <StatRow label={t("settings.totalDomains")} value={status?.domainsCount ?? 0} />
              <StatRow label={t("settings.lastApply")} value={formatDate(status?.lastAppliedAt || config?.lastAppliedAt) || t("common.notYet")} />
              <StatRow label={t("settings.lastUpdate")} value={formatDate(config?.updatedAt) || "-"} />
            </div>
          </Section>

          <Section icon="settings_ethernet" iconColor="text-tertiary" title={t("settings.network")}>
            <form className="space-y-4" onSubmit={saveRoutingSettings}>
              <p className="text-sm text-on-surface-variant">{t("settings.routingHint")}</p>

              {saveError ? <InlineNotice tone="error" title={t("error.settingsSave")} message={saveError} /> : null}
              {saveMessage ? <InlineNotice tone="info" title={t("settings.routingProfile")} message={saveMessage} /> : null}

              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <TextInput label={t("settings.lanIface")} hint={t("hint.lanIface")} value={routing?.lanIface || ""} onChange={(value) => updateRoutingField("lanIface", value)} />
                <TextInput label={t("settings.vpnIface")} hint={t("hint.vpnIface")} value={routing?.vpnIface || ""} onChange={(value) => updateRoutingField("vpnIface", value)} />
                <SelectInput
                  label={t("settings.routeMode")}
                  hint={t("hint.routeMode")}
                  value={routing?.vpnRouteMode || "gateway"}
                  onChange={(value) => updateRoutingField("vpnRouteMode", value)}
                  options={[
                    { value: "gateway", label: t("settings.routeModeGateway") },
                    { value: "dev", label: t("settings.routeModeDev") },
                  ]}
                />
                <TextInput
                  label={t("settings.vpnGateway")}
                  hint={t("hint.vpnGateway")}
                  value={routing?.vpnGateway || ""}
                  onChange={(value) => updateRoutingField("vpnGateway", value)}
                  placeholder="10.8.0.1"
                />
                <TextInput
                  label={t("settings.tableNum")}
                  hint={t("hint.tableNum")}
                  type="number"
                  value={routing?.tableNum ?? ""}
                  onChange={(value) => updateRoutingField("tableNum", value)}
                />
                <TextInput label={t("settings.fwMark")} hint={t("hint.fwMark")} value={routing?.fwMark || ""} onChange={(value) => updateRoutingField("fwMark", value)} placeholder="0x1" />
                <TextInput label={t("settings.fwZoneChain")} hint={t("hint.fwZoneChain")} value={routing?.fwZoneChain || ""} onChange={(value) => updateRoutingField("fwZoneChain", value)} />
                <TextInput label={t("settings.ipSetName")} hint={t("hint.ipSetName")} value={routing?.ipSetName || ""} onChange={(value) => updateRoutingField("ipSetName", value)} />
              </div>

              <TextInput
                label={t("settings.dnsMasqConfigFile")}
                hint={t("hint.dnsMasqConfigFile")}
                value={routing?.dnsMasqConfigFile || ""}
                onChange={(value) => updateRoutingField("dnsMasqConfigFile", value)}
              />

              <ToggleField label={t("settings.masquerade")} hint={t("hint.masquerade")} checked={Boolean(routing?.vpnMasquerade)} onChange={(value) => updateRoutingField("vpnMasquerade", value)} />

              <button
                type="submit"
                disabled={saving || !routing}
                className="rounded-xl bg-primary px-5 py-2.5 font-headline text-sm font-bold text-on-primary transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
              >
                {saving ? t("settings.saving") : t("settings.saveRouting")}
              </button>
            </form>
          </Section>

          <Section icon="autorenew" iconColor="text-secondary" title={t("settings.automation")}>
            <form className="space-y-4" onSubmit={saveAutomationSettings}>
              <p className="text-sm text-on-surface-variant">{t("settings.automationHint")}</p>

              {automationError ? <InlineNotice tone="error" title={t("error.automationSave")} message={automationError} /> : null}
              {automationMessage ? <InlineNotice tone="info" title={t("settings.automationProfile")} message={automationMessage} /> : null}

              <ToggleField
                label={t("settings.installService")}
                checked={Boolean(automation.installService)}
                onChange={(value) => updateAutomationField("installService", value)}
              />
              <ToggleField
                label={t("settings.autoRecover")}
                checked={Boolean(automation.autoRecover)}
                onChange={(value) => updateAutomationField("autoRecover", value)}
              />

              <div className="flex items-center justify-between gap-4 rounded-lg bg-surface-container p-3">
                <div>
                  <span className="block text-sm text-on-surface">{t("settings.trafficCleanup")}</span>
                  <span className="mt-1 block text-xs text-on-surface-variant">{t("settings.trafficCleanupHint")}</span>
                </div>
                <select
                  value={String(automation.trafficCleanupDays || 0)}
                  onChange={(event) => updateAutomationField("trafficCleanupDays", Number.parseInt(event.target.value, 10))}
                  className="cursor-pointer rounded-lg border-none bg-surface-container-high px-3 py-1.5 font-headline text-sm font-bold text-primary focus:ring-1 focus:ring-primary"
                >
                  <option value="0">{t("settings.trafficCleanupOff")}</option>
                  <option value="7">{t("settings.trafficCleanup7d")}</option>
                  <option value="14">{t("settings.trafficCleanup14d")}</option>
                  <option value="30">{t("settings.trafficCleanup30d")}</option>
                  <option value="60">{t("settings.trafficCleanup60d")}</option>
                  <option value="90">{t("settings.trafficCleanup90d")}</option>
                </select>
              </div>

              <button
                type="submit"
                disabled={savingAutomation}
                className="rounded-xl bg-primary px-5 py-2.5 font-headline text-sm font-bold text-on-primary transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
              >
                {savingAutomation ? t("settings.saving") : t("settings.saveAutomation")}
              </button>
            </form>
          </Section>

          <Section icon="palette" iconColor="text-on-surface-variant" title={t("settings.interface")}>
            <div className="space-y-4">
              <div className="flex items-center justify-between rounded-lg bg-surface-container p-3">
                <span className="text-sm text-on-surface">{t("settings.darkTheme")}</span>
                <div
                  onClick={toggleTheme}
                  className={`relative h-6 w-11 cursor-pointer rounded-full transition-colors ${dark ? "bg-secondary" : "bg-outline-variant"}`}
                >
                  <div className={`absolute top-[2px] h-5 w-5 rounded-full bg-white transition-transform ${dark ? "right-[2px]" : "left-[2px]"}`} />
                </div>
              </div>
              <div className="flex items-center justify-between rounded-lg bg-surface-container p-3">
                <span className="text-sm text-on-surface">{t("settings.language")}</span>
                <select
                  value={lang}
                  onChange={(event) => setLang(event.target.value)}
                  className="cursor-pointer rounded-lg border-none bg-surface-container-high px-3 py-1.5 font-headline text-sm font-bold text-primary focus:ring-1 focus:ring-primary"
                >
                  {languages.map((language) => (
                    <option key={language.code} value={language.code}>
                      {language.label}
                    </option>
                  ))}
                </select>
              </div>
              <div className="flex items-center justify-between gap-4 rounded-lg bg-surface-container p-3">
                <div>
                  <span className="block text-sm text-on-surface">{t("settings.dashboardRefresh")}</span>
                  <span className="mt-1 block text-xs text-on-surface-variant">{t("settings.dashboardRefreshHint")}</span>
                </div>
                <select
                  value={String(dashboardRefreshMs)}
                  onChange={(event) => setDashboardRefreshMs(writeDashboardRefreshInterval(Number.parseInt(event.target.value, 10)))}
                  className="cursor-pointer rounded-lg border-none bg-surface-container-high px-3 py-1.5 font-headline text-sm font-bold text-primary focus:ring-1 focus:ring-primary"
                >
                  {DASHBOARD_REFRESH_OPTIONS.map((option) => (
                    <option key={option} value={option}>
                      {t(`settings.dashboardRefreshOption.${option === 0 ? "off" : option}`)}
                    </option>
                  ))}
                </select>
              </div>
            </div>
          </Section>

          {status?.lastError || config?.lastError ? (
            <div className="rounded-xl border border-error/20 bg-error-container/20 p-5">
              <div className="mb-2 flex items-center gap-2">
                <Icon name="error" className="h-5 w-5 text-error" />
                <h3 className="font-headline font-bold text-error">{t("settings.lastError")}</h3>
              </div>
              <p className="text-sm text-error/80">{status?.lastError || config?.lastError}</p>
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function Section({ icon, iconColor, title, children }) {
  return (
    <section className="rounded-xl border-l-2 border-outline-variant/20 bg-surface-container-low p-6 shadow-lg">
      <div className="mb-5 flex items-center gap-3">
        <Icon name={icon} className={`h-5 w-5 ${iconColor}`} />
        <h2 className="font-headline text-lg font-bold text-on-surface">{title}</h2>
      </div>
      {children}
    </section>
  );
}

function HealthCard({ icon, label, value, ok }) {
  const statusColor = ok === true ? "text-secondary" : ok === false ? "text-error" : "text-on-surface";
  const dotColor = ok === true ? "bg-secondary" : ok === false ? "bg-error" : "bg-outline-variant";
  return (
    <div className="flex items-start gap-3 rounded-lg bg-surface-container p-3">
      <Icon name={icon} className={`mt-0.5 h-4 w-4 shrink-0 ${statusColor}`} />
      <div className="min-w-0">
        <p className="text-[10px] uppercase tracking-widest text-outline">{label}</p>
        <div className="mt-0.5 flex items-center gap-2">
          {ok !== undefined && <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dotColor}`} />}
          <span className={`truncate text-sm font-medium ${statusColor}`}>{value}</span>
        </div>
      </div>
    </div>
  );
}

function StatRow({ label, value }) {
  return (
    <div className="flex items-center justify-between rounded-lg bg-surface-container p-3">
      <span className="text-sm text-on-surface-variant">{label}</span>
      <span className="text-sm font-bold text-on-surface">{value}</span>
    </div>
  );
}

function RuntimeMeta({ label, value }) {
  return (
    <div className="rounded-lg bg-surface-container-high px-3 py-2">
      <p className="mb-1 text-[10px] font-bold uppercase tracking-widest text-outline">{label}</p>
      <p className="font-mono text-sm text-on-surface">{value}</p>
    </div>
  );
}

function RankBadge({ rank }) {
  const medalClasses = {
    1: "border-amber-300/40 bg-amber-400/15 text-amber-200",
    2: "border-slate-300/40 bg-slate-300/10 text-slate-200",
    3: "border-orange-300/40 bg-orange-400/15 text-orange-200",
  };

  return (
    <span
      className={`inline-flex h-7 min-w-7 items-center justify-center rounded-full border px-2 text-[11px] font-bold ${
        medalClasses[rank] || "border-outline-variant/20 bg-surface-container-high text-outline-variant"
      }`}
    >
      {rank}
    </span>
  );
}

function TextInput({ label, value, onChange, type = "text", placeholder = "", hint = "" }) {
  return (
    <label className="block">
      <span className="mb-1.5 flex items-center gap-1.5 text-[11px] font-bold uppercase tracking-widest text-outline">
        {label}
        {hint && <HintIcon hint={hint} />}
      </span>
      <input
        type={type}
        value={value}
        placeholder={placeholder}
        onChange={(event) => onChange(event.target.value)}
        className="w-full rounded-xl border border-outline-variant/20 bg-surface-container px-3 py-2.5 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
      />
    </label>
  );
}

function SelectInput({ label, value, onChange, options, hint = "" }) {
  return (
    <label className="block">
      <span className="mb-1.5 flex items-center gap-1.5 text-[11px] font-bold uppercase tracking-widest text-outline">
        {label}
        {hint && <HintIcon hint={hint} />}
      </span>
      <select
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="w-full rounded-xl border border-outline-variant/20 bg-surface-container px-3 py-2.5 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function ToggleField({ label, checked, onChange, hint = "" }) {
  return (
    <label className="flex items-center justify-between rounded-lg bg-surface-container p-3">
      <span className="flex items-center gap-1.5 text-sm text-on-surface">
        {label}
        {hint && <HintIcon hint={hint} />}
      </span>
      <input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} className="h-4 w-4 accent-primary" />
    </label>
  );
}

function HintIcon({ hint }) {
  return (
    <span className="group relative inline-flex cursor-help" onClick={(e) => e.preventDefault()}>
      <Icon name="help_outline" className="h-3.5 w-3.5 text-outline-variant transition-colors group-hover:text-primary" />
      <span className="pointer-events-none absolute bottom-full left-1/2 z-50 mb-2 w-64 -translate-x-1/2 rounded-xl border border-primary/30 bg-surface-container-highest px-4 py-3 text-[11px] font-normal normal-case leading-relaxed tracking-normal text-on-surface opacity-0 shadow-2xl shadow-black/40 ring-1 ring-white/5 backdrop-blur-sm transition-opacity group-hover:pointer-events-auto group-hover:opacity-100">
        {hint}
        <span className="absolute top-full left-1/2 -translate-x-1/2 border-4 border-transparent border-t-primary/30" />
      </span>
    </span>
  );
}

function formatHostLabel(status) {
  if (!status) return "-";

  const parts = [status.hostName, status.runtimeOS].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : "-";
}

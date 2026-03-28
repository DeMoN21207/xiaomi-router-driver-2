import { useDeferredValue, useEffect, useMemo, useState, useRef, useCallback } from "react";
import { fetchJSON } from "../api.js";
import { parseDomainInput } from "../domainInput.js";
import { useI18n } from "../i18n.jsx";
import Icon from "../components/Icon.jsx";
import InlineNotice from "../components/InlineNotice.jsx";
import { formatLatencyMs, statusToneClasses } from "../utils.js";

const defaultForm = { name: "", type: "openvpn", source: "", enabled: true };

export default function ConnectionsPage() {
  const { t } = useI18n();
  const [config, setConfig] = useState(null);
  const [status, setStatus] = useState(null);
  const [form, setForm] = useState(defaultForm);
  const [showForm, setShowForm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [pageError, setPageError] = useState("");
  const [toast, setToast] = useState(null);
  const toastTimer = useRef(null);
  const [probing, setProbing] = useState(false);
  const [probeResult, setProbeResult] = useState(null);
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef(null);

  const providers = config?.providers ?? [];
  const rules = config?.rules ?? [];
  const runtimeMap = new Map((status?.providers ?? []).map((p) => [p.id, p]));

  const showToast = useCallback((message, error = false) => {
    setToast({ message, error });
    clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(null), 2800);
  }, []);

  useEffect(() => () => clearTimeout(toastTimer.current), []);

  const refresh = useCallback(async () => {
    try {
      const [nextConfig, nextStatus] = await Promise.all([
        fetchJSON("/api/config"),
        fetchJSON("/api/status"),
      ]);
      setConfig(nextConfig);
      setStatus(nextStatus);
      setPageError("");
    } catch (error) {
      setPageError(error.message);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function uploadOvpn(event) {
    const file = event.target.files?.[0];
    if (!file) return;
    setUploading(true);
    try {
      const body = new FormData();
      body.append("file", file);
      const result = await fetchJSON("/api/providers/upload", { method: "POST", body });
      setForm((prev) => ({ ...prev, source: result.path }));
      setProbeResult(null);
      showToast(t("connections.uploaded"));
    } catch (error) {
      showToast(error.message, true);
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  }

  async function probeSource() {
    if (!form.source.trim()) return;
    setProbing(true);
    setProbeResult(null);
    try {
      const result = await fetchJSON("/api/providers/probe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ type: form.type, source: form.source }),
      });
      setProbeResult(result);
    } catch (error) {
      setProbeResult({ locations: [], error: error.message });
    } finally {
      setProbing(false);
    }
  }

  async function submit(event) {
    event.preventDefault();
    setBusy(true);
    try {
      const payload = {
        name: form.name,
        type: form.type,
        source: form.source,
        enabled: form.enabled,
      };
      await fetchJSON("/api/providers", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      setForm(defaultForm);
      setProbeResult(null);
      setShowForm(false);
      setPageError("");
      showToast(t("connections.added"));
      await refresh();
    } catch (error) {
      const message = error.message;
      setPageError(message);
      showToast(message, true);
    } finally {
      setBusy(false);
    }
  }

  async function remove(id) {
    setBusy(true);
    try {
      await fetchJSON(`/api/providers/${encodeURIComponent(id)}`, { method: "DELETE" });
      setPageError("");
      showToast(t("connections.deleted"));
      await refresh();
    } catch (error) {
      const message = error.message;
      setPageError(message);
      showToast(message, true);
    } finally {
      setBusy(false);
    }
  }

  async function updateProvider(provider, patch) {
    setBusy(true);
    try {
      await fetchJSON(`/api/providers/${encodeURIComponent(provider.id)}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: provider.name,
          type: provider.type,
          source: provider.source,
          selectedLocation: provider.selectedLocation || "",
          enabled: patch.enabled ?? provider.enabled,
        }),
      });
      await fetchJSON("/api/rules/apply", { method: "POST" });
      setPageError("");
      showToast(patch.enabled ? t("connections.providerEnabled") : t("connections.providerDisabled"));
      await refresh();
    } catch (error) {
      const message = error.message;
      setPageError(message);
      showToast(message, true);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-8">
      <header className="flex flex-col justify-between gap-6 md:flex-row md:items-end">
        <div>
          <h1 className="font-headline text-4xl font-bold tracking-tight text-on-surface">{t("connections.title")}</h1>
          <p className="mt-2 max-w-2xl text-on-surface-variant">{t("connections.subtitle")}</p>
        </div>
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={() => {
              const allDomains = [];
              for (const rule of rules) {
                const provider = providers.find((p) => p.id === rule.providerId);
                const label = rule.selectedLocation || rule.name || "unknown";
                const provName = provider?.name || "";
                allDomains.push(`# ${provName} — ${label} (${rule.domains.length})`);
                for (const d of rule.domains) allDomains.push(d);
                allDomains.push("");
              }
              if (allDomains.length === 0) return;
              const blob = new Blob([allDomains.join("\n") + "\n"], { type: "text/plain" });
              const url = URL.createObjectURL(blob);
              const a = document.createElement("a");
              a.href = url;
              a.download = "all-domains-export.txt";
              a.click();
              URL.revokeObjectURL(url);
            }}
            className="flex items-center gap-2 rounded-xl border border-outline-variant/20 bg-surface-container-high px-5 py-3 font-headline text-sm font-medium text-on-surface transition-colors hover:bg-surface-variant"
          >
            <Icon name="download" className="h-4 w-4 text-primary" />
            {t("connections.exportAll")}
          </button>
          <button
            type="button"
            onClick={() => setShowForm((value) => !value)}
            className="flex items-center gap-2 rounded-xl bg-gradient-to-r from-primary to-primary-container px-6 py-3 font-bold text-on-primary shadow-lg shadow-primary/10 transition-all hover:brightness-110 active:scale-95"
          >
            <Icon name={showForm ? "close" : "add"} className="h-5 w-5" />
            {showForm ? t("connections.close") : t("connections.add")}
          </button>
        </div>
      </header>

      {pageError ? <InlineNotice tone="error" title={t("error.connections")} message={pageError} /> : null}

      {showForm && (
        <form onSubmit={submit} className="space-y-5 rounded-xl border border-outline-variant/10 bg-surface-container-low p-6">
          <h2 className="font-headline text-lg font-bold text-on-surface">{t("connections.newProvider")}</h2>
          <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
            <FormField label={t("connections.name")} hint={t("connections.nameHint")}>
              <input
                className="w-full rounded-lg border-none bg-surface-container-highest px-4 py-3 text-sm text-on-surface focus:ring-1 focus:ring-primary"
                value={form.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
                placeholder={t("connections.namePlaceholder")}
              />
            </FormField>
            <FormField label={t("connections.type")} hint={t("connections.typeHint")}>
              <select
                className="w-full rounded-lg border-none bg-surface-container-highest px-4 py-3 text-sm text-on-surface focus:ring-1 focus:ring-primary"
                value={form.type}
                onChange={(event) => setForm({ ...form, type: event.target.value })}
              >
                <option value="openvpn">{t("common.openvpn")}</option>
                <option value="subscription">{t("common.subscription")}</option>
              </select>
            </FormField>
            <FormField
              label={t("connections.source")}
              hint={t(form.type === "openvpn" ? "connections.sourceHintOpenvpn" : "connections.sourceHintSubscription")}
            >
              <div className="flex gap-2">
                <input
                  className="w-full rounded-lg border-none bg-surface-container-highest px-4 py-3 font-mono text-sm text-on-surface focus:ring-1 focus:ring-primary"
                  value={form.source}
                  onChange={(event) => { setForm({ ...form, source: event.target.value }); setProbeResult(null); }}
                  placeholder={form.type === "openvpn" ? "profiles/de.ovpn" : "https://provider/sublink/..."}
                />
                {form.type === "openvpn" && (
                  <>
                    <input ref={fileInputRef} type="file" accept=".ovpn" onChange={uploadOvpn} className="hidden" />
                    <button
                      type="button"
                      onClick={() => fileInputRef.current?.click()}
                      disabled={uploading}
                      className="shrink-0 rounded-lg bg-surface-container-highest px-4 py-3 text-sm font-medium text-primary transition-colors hover:bg-surface-container-high disabled:opacity-40"
                      title={t("connections.upload")}
                    >
                      <Icon name="upload_file" className="h-5 w-5" />
                    </button>
                  </>
                )}
                <button
                  type="button"
                  onClick={probeSource}
                  disabled={probing || !form.source.trim()}
                  className="shrink-0 rounded-lg bg-surface-container-highest px-4 py-3 text-sm font-medium text-primary transition-colors hover:bg-surface-container-high disabled:opacity-40"
                >
                  {probing ? t("connections.probing") : t("connections.probe")}
                </button>
              </div>
            </FormField>
          </div>
          {probeResult && (
            <div className="rounded-lg border border-outline-variant/10 bg-surface-container p-4">
              <h3 className="mb-2 text-xs font-bold uppercase tracking-widest text-outline">
                {t("connections.probeLocations")}
                {probeResult.locations?.length > 0 && (
                  <span className="ml-2 font-mono text-primary">{probeResult.locations.length} {t("connections.probeCount")}</span>
                )}
              </h3>
              {probeResult.error && (
                <p className="mb-2 text-sm text-error">{probeResult.error}</p>
              )}
              {probeResult.locations?.length > 0 ? (
                <LocationsBrowser locations={probeResult.locations} t={t} />
              ) : !probeResult.error ? (
                <p className="text-sm text-on-surface-variant">{t("connections.probeEmpty")}</p>
              ) : null}
            </div>
          )}
          <div className="flex items-center justify-between gap-4 max-sm:flex-col max-sm:items-start">
            <label className="flex cursor-pointer items-center gap-3">
              <input
                type="checkbox"
                checked={form.enabled}
                onChange={(event) => setForm({ ...form, enabled: event.target.checked })}
                className="h-5 w-5 rounded border-outline-variant bg-surface-container-highest text-primary focus:ring-0"
              />
              <span className="text-sm font-medium">{t("connections.enabled")}</span>
            </label>
            <button
              type="submit"
              disabled={busy}
              className="rounded-xl bg-gradient-to-r from-primary to-primary-container px-8 py-2.5 text-sm font-bold text-on-primary shadow-lg shadow-primary/10 transition-all active:scale-95 disabled:opacity-50"
            >
              {t("connections.submit")}
            </button>
          </div>
        </form>
      )}

      <div className="space-y-6">
        {providers.map((provider) => {
          const runtime = runtimeMap.get(provider.id);
          const isOnline = provider.enabled && runtime?.health === "ready";
          const tone = !provider.enabled ? "outline" : isOnline ? "secondary" : runtime?.health === "warning" ? "tertiary" : "error";
          const statusLabel = !provider.enabled ? t("connections.off") : isOnline ? t("connections.online") : t("connections.problem");
          const toneClasses = statusToneClasses(tone);
          const providerRules = rules.filter((r) => r.providerId === provider.id);

          return (
            <ProviderCard
              key={provider.id}
              provider={provider}
              providers={providers}
              rules={rules}
              toneClasses={toneClasses}
              statusLabel={statusLabel}
              isOnline={isOnline}
              providerRules={providerRules}
              busy={busy}
              onDelete={() => remove(provider.id)}
              onToggleEnabled={(nextEnabled) => updateProvider(provider, { enabled: nextEnabled })}
              onDomainsChange={refresh}
              showToast={showToast}
              setPageError={setPageError}
              t={t}
            />
          );
        })}
      </div>

      {providers.length === 0 && (
        <div className="rounded-xl bg-surface-container-low p-12 text-center">
          <Icon name="vpn_lock" className="mx-auto mb-4 h-12 w-12 text-outline-variant" />
          <p className="text-on-surface-variant">{t("connections.empty")}</p>
        </div>
      )}

      {toast && (
        <div
          className={`fixed right-6 bottom-6 z-50 rounded-xl px-5 py-3 text-sm font-medium shadow-2xl ${
            toast.error ? "bg-error-container text-on-error" : "bg-secondary-container text-on-secondary"
          }`}
        >
          {toast.message}
        </div>
      )}
    </div>
  );
}

function ProviderCard({ provider, providers, rules, toneClasses, statusLabel, isOnline, providerRules, busy, onDelete, onToggleEnabled, onDomainsChange, showToast, setPageError, t }) {
  const [expanded, setExpanded] = useState(false);
  const [saving, setSaving] = useState(false);
  const [probing, setProbing] = useState(false);
  const [refreshingLatency, setRefreshingLatency] = useState(false);
  const [probeLocations, setProbeLocations] = useState(null);
  const [sourceVisible, setSourceVisible] = useState(false);
  const [editingSource, setEditingSource] = useState(false);
  const [sourceDraft, setSourceDraft] = useState(provider.source);
  const [savingSource, setSavingSource] = useState(false);
  const [newRouteLoc, setNewRouteLoc] = useState("");
  const probedRef = useRef(false);

  const totalDomains = providerRules.reduce((sum, r) => sum + r.domains.length, 0);

  async function probeProvider() {
    setProbing(true);
    setProbeLocations(null);
    try {
      const result = await fetchJSON("/api/providers/probe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ type: provider.type, source: provider.source }),
      });
      setProbeLocations(result.locations || []);
    } catch (error) {
      showToast(error.message, true);
    } finally {
      setProbing(false);
    }
  }

  // Auto-probe locations when expanding a provider
  useEffect(() => {
    if (expanded && !probeLocations && !probedRef.current) {
      probedRef.current = true;
      probeProvider();
    }
  }, [expanded]);

  async function applyAndRefresh() {
    try {
      await fetchJSON("/api/rules/apply", { method: "POST" });
    } finally {
      await onDomainsChange();
    }
  }

  async function addRoute() {
    if (!newRouteLoc) return;
    setSaving(true);
    try {
      await fetchJSON("/api/rules", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: `${provider.name} — ${newRouteLoc}`,
          providerId: provider.id,
          selectedLocation: newRouteLoc,
          domains: "",
          enabled: true,
        }),
      });
      setNewRouteLoc("");
      showToast(t("connections.routeAdded"));
      await applyAndRefresh();
    } catch (error) {
      setPageError(error.message);
      showToast(error.message, true);
    } finally {
      setSaving(false);
    }
  }

  async function refreshLatency() {
    if (!probeLocations || probeLocations.length === 0) return;

    setRefreshingLatency(true);
    try {
      const result = await fetchJSON("/api/providers/latency", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ locations: probeLocations }),
      });
      setProbeLocations(result.locations || []);
      showToast(t("connections.pingUpdated"));
    } catch (error) {
      setPageError(error.message);
      showToast(error.message, true);
    } finally {
      setRefreshingLatency(false);
    }
  }

  async function deleteRule(ruleId) {
    setSaving(true);
    try {
      await fetchJSON(`/api/rules/${encodeURIComponent(ruleId)}`, { method: "DELETE" });
      await applyAndRefresh();
    } catch (error) {
      setPageError(error.message);
      showToast(error.message, true);
    } finally {
      setSaving(false);
    }
  }

  // Locations already used in rules (for highlighting in chips)
  const usedLocations = new Set(providerRules.map((r) => r.selectedLocation).filter(Boolean));

  return (
    <div className="overflow-hidden rounded-xl border border-transparent bg-surface-container-low shadow-xl transition-all duration-300 hover:border-primary/20">
      <div className="flex items-center gap-4 p-5">
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="flex min-w-0 flex-1 items-center justify-between gap-4 rounded-xl px-1 py-1 text-left transition-colors hover:bg-surface-container/50"
        >
        <div className="min-w-0">
          <h3 className="truncate font-headline text-lg font-bold text-primary">{provider.name}</h3>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <span className="rounded bg-surface-container-highest px-2 py-0.5 font-mono text-[10px] text-on-primary-container">
              {t(`common.${provider.type}`)}
            </span>
            {providerRules.length > 0 && (
              <span className="rounded bg-surface-container-highest px-2 py-0.5 text-[10px] text-on-surface-variant">
                {providerRules.length} {t("connections.routes")} · {totalDomains} {t("connections.domainsFor")}
              </span>
            )}
            {providerRules.map((rule) => rule.selectedLocation && (
              <span key={rule.id} className="flex items-center gap-1 rounded bg-primary/10 px-2 py-0.5 text-[10px] text-primary">
                <Icon name="location_on" className="h-3 w-3" />
                {rule.selectedLocation}
              </span>
            ))}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-3">
          <span className={`flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[10px] font-bold uppercase tracking-widest ${toneClasses.badge}`}>
            <span className={`h-2 w-2 rounded-full ${toneClasses.dot} ${isOnline ? "status-glow-success" : ""}`} />
            {statusLabel}
          </span>
          <Icon name={expanded ? "expand_less" : "expand_more"} className="h-5 w-5 text-outline-variant" />
        </div>
      </button>

        <div className="flex shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={() => onToggleEnabled(!provider.enabled)}
            disabled={busy || saving}
            className={`rounded-full border px-3 py-1.5 text-xs font-bold uppercase tracking-wide transition-colors disabled:opacity-40 ${
              provider.enabled
                ? "border-secondary/30 bg-secondary/10 text-secondary hover:bg-secondary/15"
                : "border-outline-variant/20 bg-surface-container-highest text-on-surface-variant hover:border-primary/30 hover:text-on-surface"
            }`}
            title={provider.enabled ? t("connections.disableProvider") : t("connections.enableProvider")}
          >
            {provider.enabled ? t("connections.providerActive") : t("connections.providerInactive")}
          </button>
          <button
            type="button"
            onClick={onDelete}
            disabled={busy || saving}
            className="rounded-full border border-outline-variant/20 p-2 text-on-surface-variant transition-colors hover:border-error/30 hover:bg-error-container/20 hover:text-error disabled:opacity-40"
            title={t("common.deleteProvider")}
          >
            <Icon name="delete" className="h-4 w-4" />
          </button>
        </div>
      </div>

      {/* Expanded */}
      {expanded && (
        <div className="border-t border-outline-variant/10 p-5 pt-4">
          {/* Source info */}
          <div className="mb-5">
            <span className="mb-1 block text-[10px] uppercase tracking-widest text-on-surface-variant">{t("connections.source")}</span>
            {editingSource ? (
              <div className="flex gap-2">
                <input
                  className="w-full rounded-lg border border-outline-variant/20 bg-surface-container-lowest/50 px-3 py-2 font-mono text-sm text-secondary outline-none transition-colors focus:border-primary/40"
                  value={sourceDraft}
                  onChange={(e) => setSourceDraft(e.target.value)}
                  disabled={savingSource}
                />
                <button
                  type="button"
                  disabled={savingSource || sourceDraft.trim() === provider.source}
                  onClick={async () => {
                    setSavingSource(true);
                    try {
                      await fetchJSON(`/api/providers/${encodeURIComponent(provider.id)}`, {
                        method: "PUT",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ name: provider.name, type: provider.type, source: sourceDraft.trim(), enabled: provider.enabled }),
                      });
                      showToast(t("connections.sourceUpdated"));
                      setEditingSource(false);
                      probedRef.current = false;
                      setProbeLocations(null);
                      await onDomainsChange();
                    } catch (err) {
                      showToast(err.message, true);
                    } finally {
                      setSavingSource(false);
                    }
                  }}
                  className="shrink-0 rounded-lg bg-primary/10 px-3 py-2 text-sm font-medium text-primary transition-colors hover:bg-primary/20 disabled:opacity-40"
                >
                  <Icon name="check" className="h-4 w-4" />
                </button>
                <button
                  type="button"
                  onClick={() => { setEditingSource(false); setSourceDraft(provider.source); }}
                  disabled={savingSource}
                  className="shrink-0 rounded-lg bg-surface-container-highest px-3 py-2 text-sm text-on-surface-variant transition-colors hover:text-error disabled:opacity-40"
                >
                  <Icon name="close" className="h-4 w-4" />
                </button>
              </div>
            ) : (
              <div className="flex items-center gap-2">
                <code className="block w-full overflow-hidden text-ellipsis rounded-lg bg-surface-container-lowest/50 px-3 py-2 font-mono text-sm text-secondary">
                  {sourceVisible ? provider.source : "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022"}
                </code>
                <button
                  type="button"
                  onClick={() => setSourceVisible((v) => !v)}
                  className="shrink-0 rounded-lg bg-surface-container-highest p-2 text-on-surface-variant transition-colors hover:text-primary"
                  title={sourceVisible ? t("connections.hideSource") : t("connections.showSource")}
                >
                  <Icon name={sourceVisible ? "visibility_off" : "visibility"} className="h-4 w-4" />
                </button>
                <button
                  type="button"
                  onClick={() => { setEditingSource(true); setSourceDraft(provider.source); }}
                  className="shrink-0 rounded-lg bg-surface-container-highest p-2 text-on-surface-variant transition-colors hover:text-primary"
                  title={t("connections.editSource")}
                >
                  <Icon name="edit" className="h-4 w-4" />
                </button>
              </div>
            )}
          </div>

          {/* Loading indicator while probing */}
          {probing && (
            <div className="mb-5 flex items-center gap-2 text-sm text-on-surface-variant">
              <Icon name="travel_explore" className="h-4 w-4 animate-spin text-primary" />
              {t("connections.probing")}
            </div>
          )}

          {/* Location chips + add route */}
          {probeLocations && probeLocations.length > 0 && (
            <div className="mb-5 rounded-lg border border-outline-variant/10 bg-surface-container p-3">
              <h4 className="mb-2 text-xs font-bold uppercase tracking-widest text-outline">
                {t("connections.probeLocations")}
                <span className="ml-2 font-mono text-primary">{probeLocations.length}</span>
              </h4>
              <LocationsBrowser
                locations={probeLocations}
                selectedLocation={newRouteLoc}
                onSelect={setNewRouteLoc}
                usedLocations={usedLocations}
                disabled={saving}
                t={t}
              />
              <div className="mt-3 flex items-center justify-between gap-3 max-sm:flex-col max-sm:items-stretch">
                <div className="min-h-[20px] text-sm text-on-surface-variant">
                  {newRouteLoc ? (
                    <span>
                      {t("connections.selectedLocation")} <span className="font-medium text-on-surface">{newRouteLoc}</span>
                    </span>
                  ) : (
                    t("connections.addRoutePlaceholder")
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-2 max-sm:flex-col max-sm:items-stretch">
                  <button
                    type="button"
                    onClick={refreshLatency}
                    disabled={saving || probing || refreshingLatency || probeLocations.length === 0}
                    className="rounded-lg border border-outline-variant/20 bg-surface-container-highest px-4 py-2 text-sm font-medium text-on-surface transition-colors hover:border-primary/30 hover:text-primary disabled:opacity-40"
                  >
                    {refreshingLatency ? t("connections.refreshingPing") : t("connections.refreshPing")}
                  </button>
                  <button
                    type="button"
                    onClick={addRoute}
                    disabled={saving || !newRouteLoc}
                    className="rounded-lg bg-primary/10 px-4 py-2 text-sm font-medium text-primary transition-colors hover:bg-primary/20 disabled:opacity-40"
                  >
                    {t("connections.addRoute")}
                  </button>
                </div>
              </div>
            </div>
          )}

          {/* Rules (routes) — each with its own location + domains */}
          <div className="space-y-4">
            {providerRules.length === 0 && (
              <p className="text-sm text-on-surface-variant">{t("connections.noRoutes")}</p>
            )}
            {providerRules.map((rule) => (
              <RuleBlock
                key={rule.id}
                rule={rule}
                provider={provider}
                allRules={rules}
                allProviders={providers}
                saving={saving}
                setSaving={setSaving}
                applyAndRefresh={applyAndRefresh}
                onDeleteRule={() => deleteRule(rule.id)}
                showToast={showToast}
                setPageError={setPageError}
                t={t}
              />
            ))}
          </div>

          {/* Delete provider */}
          <div className="mt-4 flex justify-end border-t border-outline-variant/10 pt-4">
            <button
              type="button"
              onClick={onDelete}
              disabled={busy}
              className="flex items-center gap-2 rounded-lg px-3 py-2 text-sm text-on-surface-variant transition-colors hover:bg-error-container/20 hover:text-error disabled:opacity-50"
              title={t("common.deleteProvider")}
            >
              <Icon name="delete" className="h-4 w-4" />
              {t("common.deleteProvider")}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function LocationsBrowser({ locations, selectedLocation = "", onSelect, usedLocations, disabled = false, t }) {
  const [query, setQuery] = useState("");
  const [sortKey, setSortKey] = useState("");
  const [sortDirection, setSortDirection] = useState("asc");
  const deferredQuery = useDeferredValue(query);
  const normalizedQuery = deferredQuery.trim().toLowerCase();
  const selectedSet = usedLocations ?? new Set();

  const filteredLocations = useMemo(() => {
    if (!normalizedQuery) {
      return locations;
    }

    return locations.filter((loc) => {
      const haystack = [loc.name, loc.address, loc.type].filter(Boolean).join(" ").toLowerCase();
      return haystack.includes(normalizedQuery);
    });
  }, [locations, normalizedQuery]);

  const visibleLocations = useMemo(() => {
    if (!sortKey) {
      return filteredLocations;
    }

    return [...filteredLocations].sort((left, right) => compareLocations(left, right, sortKey, sortDirection));
  }, [filteredLocations, sortDirection, sortKey]);

  function toggleSort(nextSortKey) {
    if (sortKey === nextSortKey) {
      setSortDirection((currentDirection) => (currentDirection === "asc" ? "desc" : "asc"));
      return;
    }

    setSortKey(nextSortKey);
    setSortDirection("asc");
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3 max-sm:flex-col max-sm:items-stretch">
        <label className="relative block flex-1">
          <Icon name="search" className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-outline-variant" />
          <input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("connections.locationSearchPlaceholder")}
            className="w-full rounded-lg border border-outline-variant/15 bg-surface-container-highest py-2 pl-9 pr-3 text-sm text-on-surface outline-none transition-colors focus:border-primary/40"
          />
        </label>
        <div className="shrink-0 rounded-full bg-surface-container-highest px-3 py-1 text-xs font-medium text-on-surface-variant">
          {t("connections.locationsShowing", { visible: String(filteredLocations.length), total: String(locations.length) })}
        </div>
      </div>

      {visibleLocations.length > 0 ? (
        <>
          <div className="overflow-hidden rounded-lg border border-outline-variant/10">
            <div className="grid grid-cols-[110px_minmax(180px,1fr)_minmax(220px,1.3fr)_110px] gap-px bg-outline-variant/10 text-[11px] font-bold uppercase tracking-widest text-outline">
              <div className="bg-surface-container-high px-3 py-2">{t("connections.locationTypeColumn")}</div>
              <SortHeader
                label={t("connections.locationAddressColumn")}
                columnKey="address"
                activeSortKey={sortKey}
                direction={sortDirection}
                onToggle={toggleSort}
              />
              <SortHeader
                label={t("connections.locationNameColumn")}
                columnKey="name"
                activeSortKey={sortKey}
                direction={sortDirection}
                onToggle={toggleSort}
              />
              <SortHeader
                label={t("connections.locationPingColumn")}
                columnKey="ping"
                activeSortKey={sortKey}
                direction={sortDirection}
                onToggle={toggleSort}
                align="right"
              />
            </div>
            <div className="max-h-72 overflow-auto bg-surface-container-lowest/40">
              {visibleLocations.map((loc) => {
                const active = selectedLocation === loc.name;
                const alreadyUsed = selectedSet.has(loc.name);
                const interactive = typeof onSelect === "function";

                return (
                  <button
                    key={`row:${loc.type || "location"}:${loc.name}:${loc.address || ""}`}
                    type="button"
                    onClick={interactive ? () => onSelect(loc.name) : undefined}
                    disabled={!interactive || disabled}
                    className={`grid w-full grid-cols-[110px_minmax(180px,1fr)_minmax(220px,1.3fr)_110px] gap-px border-t border-outline-variant/10 text-left text-sm transition-colors ${
                      interactive ? "hover:bg-surface-container/70 disabled:cursor-default disabled:hover:bg-transparent" : "cursor-default"
                    } ${active ? "bg-primary/8" : "bg-transparent"}`}
                  >
                    <div className="px-3 py-2.5 text-on-surface-variant">
                      <span className="rounded bg-surface-container-highest px-2 py-0.5 text-[11px] font-medium uppercase tracking-wide text-on-surface">
                        {formatLocationType(loc.type)}
                      </span>
                    </div>
                    <div className="px-3 py-2.5 font-mono text-xs text-secondary">{loc.address || "—"}</div>
                    <div className="px-3 py-2.5 text-on-surface">
                      <div className="flex items-center gap-2">
                        <span className="truncate">{loc.name}</span>
                        {active ? (
                          <span className="rounded-full bg-primary/10 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wide text-primary">
                            {t("connections.locationSelected")}
                          </span>
                        ) : null}
                        {alreadyUsed ? (
                          <span className="rounded-full bg-secondary/10 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wide text-secondary">
                            {t("connections.locationUsed")}
                          </span>
                        ) : null}
                      </div>
                    </div>
                    <div
                      className="px-3 py-2.5 text-right font-mono text-xs text-on-surface-variant"
                      title={loc.latencyError || undefined}
                    >
                      {formatLocationLatency(loc, t)}
                    </div>
                  </button>
                );
              })}
            </div>
          </div>
        </>
      ) : (
        <div className="rounded-lg border border-dashed border-outline-variant/20 bg-surface-container-highest/40 px-4 py-5 text-sm text-on-surface-variant">
          {t("connections.locationNoMatch")}
        </div>
      )}
    </div>
  );
}

function SortHeader({ label, columnKey, activeSortKey, direction, onToggle, align = "left" }) {
  const active = activeSortKey === columnKey;

  return (
    <button
      type="button"
      onClick={() => onToggle(columnKey)}
      className={`flex items-center gap-1.5 bg-surface-container-high px-3 py-2 text-left transition-colors hover:bg-surface-container-highest ${
        align === "right" ? "justify-end text-right" : "justify-between"
      }`}
    >
      <span>{label}</span>
      <span className="relative flex flex-col items-center">
        <Icon
          name="arrow_drop_up"
          className={`h-3.5 w-3.5 -mb-1 transition-colors ${
            active && direction === "asc" ? "text-primary" : "text-outline-variant/40"
          }`}
        />
        <Icon
          name="arrow_drop_down"
          className={`h-3.5 w-3.5 -mt-1 transition-colors ${
            active && direction === "desc" ? "text-primary" : "text-outline-variant/40"
          }`}
        />
      </span>
    </button>
  );
}

function compareLocations(left, right, sortKey, sortDirection) {
  const direction = sortDirection === "desc" ? -1 : 1;

  if (sortKey === "ping") {
    const leftMissing = !(left?.latencyMs > 0);
    const rightMissing = !(right?.latencyMs > 0);

    if (leftMissing !== rightMissing) {
      return leftMissing ? 1 : -1;
    }

    if (!leftMissing && left.latencyMs !== right.latencyMs) {
      return (left.latencyMs - right.latencyMs) * direction;
    }

    return compareText(left?.name, right?.name);
  }

  if (sortKey === "address") {
    const byAddress = compareText(left?.address, right?.address);
    if (byAddress !== 0) {
      return byAddress * direction;
    }

    return compareText(left?.name, right?.name);
  }

  if (sortKey === "name") {
    const byName = compareText(left?.name, right?.name);
    if (byName !== 0) {
      return byName * direction;
    }

    return compareText(left?.address, right?.address);
  }

  return 0;
}

function compareText(left, right) {
  return String(left || "").localeCompare(String(right || ""), undefined, {
    numeric: true,
    sensitivity: "base",
  });
}

function formatLocationLatency(location, t) {
  if (location?.latencyMs > 0) {
    return formatLatencyMs(location.latencyMs, t);
  }

  return "—";
}

function formatLocationType(type) {
  const normalized = String(type || "").trim().toLowerCase();
  if (!normalized) {
    return "node";
  }
  return normalized;
}

function formatRuleConflictLabel(rule, provider) {
  const location = String(rule?.selectedLocation || "").trim();
  const name = location || String(rule?.name || "").trim();
  const providerName = String(provider?.name || "").trim();

  if (providerName && name) {
    return `${providerName} / ${name}`;
  }
  if (providerName) {
    return providerName;
  }
  if (name) {
    return name;
  }
  if (rule?.id) {
    return String(rule.id);
  }
  return "unknown route";
}

function findRuleDomainConflict(rule, provider, domains, allRules, allProviders) {
  if (!rule?.enabled || !provider?.enabled) {
    return "";
  }

  const providersById = new Map((allProviders || []).map((item) => [item.id, item]));
  const candidateDomains = new Set((domains || []).filter(Boolean));
  if (candidateDomains.size === 0) {
    return "";
  }

  for (const existing of allRules || []) {
    if (!existing?.enabled || existing.id === rule.id) {
      continue;
    }
    const existingProvider = providersById.get(existing.providerId);
    if (!existingProvider?.enabled) {
      continue;
    }
    for (const domain of existing.domains || []) {
      if (candidateDomains.has(domain)) {
        return `domain "${domain}" is already assigned to "${formatRuleConflictLabel(existing, existingProvider)}" and cannot also be used in "${formatRuleConflictLabel(rule, provider)}"`;
      }
    }
  }

  return "";
}

function RuleBlock({ rule, provider, allRules, allProviders, saving, setSaving, applyAndRefresh, onDeleteRule, showToast, setPageError, t }) {
  const [domainInput, setDomainInput] = useState("");
  const fileInputRef = useRef(null);

  async function saveDomains(merged) {
    const conflict = findRuleDomainConflict(rule, provider, merged, allRules, allProviders);
    if (conflict) {
      setPageError(conflict);
      showToast(conflict, true);
      return;
    }

    setSaving(true);
    try {
      await fetchJSON(`/api/rules/${encodeURIComponent(rule.id)}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: rule.name, providerId: provider.id, selectedLocation: rule.selectedLocation, domains: merged.join(","), enabled: rule.enabled }),
      });
      setDomainInput("");
      await applyAndRefresh();
      showToast(t("connections.domainsUpdated"));
    } catch (error) {
      setPageError(error.message);
      showToast(error.message, true);
    } finally {
      setSaving(false);
    }
  }

  async function addDomains(event) {
    event.preventDefault();
    const raw = domainInput.trim();
    if (!raw) return;
    const merged = [...new Set([...rule.domains, ...parseDomainInput(raw)])];
    await saveDomains(merged);
  }

  function handleFileImport(event) {
    const file = event.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = async (e) => {
      const text = e.target?.result || "";
      const imported = parseDomainInput(text);
      if (imported.length === 0) return;
      const merged = [...new Set([...rule.domains, ...imported])];
      await saveDomains(merged);
    };
    reader.readAsText(file);
    event.target.value = "";
  }

  async function removeDomain(domain) {
    setSaving(true);
    try {
      const remaining = rule.domains.filter((d) => d !== domain);
      if (remaining.length === 0) {
        await fetchJSON(`/api/rules/${encodeURIComponent(rule.id)}`, { method: "DELETE" });
      } else {
        await fetchJSON(`/api/rules/${encodeURIComponent(rule.id)}`, {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: rule.name, providerId: provider.id, selectedLocation: rule.selectedLocation, domains: remaining.join(","), enabled: rule.enabled }),
        });
      }
      await applyAndRefresh();
    } catch (error) {
      setPageError(error.message);
      showToast(error.message, true);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="rounded-lg border border-outline-variant/10 bg-surface-container p-4">
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Icon name="location_on" className="h-4 w-4 text-primary" />
          <span className="font-headline text-sm font-bold text-on-surface">{rule.selectedLocation || rule.name}</span>
          <span className="text-xs text-on-surface-variant">· {rule.domains.length} {t("connections.domainsFor")}</span>
        </div>
        <button
          type="button"
          onClick={onDeleteRule}
          disabled={saving}
          className="text-outline-variant transition-colors hover:text-error disabled:opacity-40"
          title={t("connections.deleteRoute")}
        >
          <Icon name="close" className="h-4 w-4" />
        </button>
      </div>

      <form onSubmit={addDomains} className="mb-3 space-y-2">
        <textarea
          className="w-full resize-y rounded-lg border-none bg-surface-container-highest px-3 py-2 font-mono text-sm text-on-surface placeholder:text-outline-variant focus:ring-1 focus:ring-primary"
          rows={2}
          value={domainInput}
          onChange={(e) => setDomainInput(e.target.value)}
          placeholder={t("connections.domainsPlaceholder")}
          disabled={saving}
        />
        <div className="flex gap-2">
          <button
            type="submit"
            disabled={saving || !domainInput.trim()}
            className="flex items-center gap-1.5 rounded-lg bg-primary/10 px-4 py-2 text-sm font-medium text-primary transition-colors hover:bg-primary/20 disabled:opacity-40"
          >
            <Icon name="add" className="h-4 w-4" />
            {t("connections.addDomains")}
          </button>
          <button
            type="button"
            disabled={saving}
            onClick={() => fileInputRef.current?.click()}
            className="flex items-center gap-1.5 rounded-lg bg-outline-variant/10 px-4 py-2 text-sm font-medium text-on-surface-variant transition-colors hover:bg-outline-variant/20 disabled:opacity-40"
          >
            <Icon name="upload_file" className="h-4 w-4" />
            {t("connections.importFile")}
          </button>
          <button
            type="button"
            onClick={() => {
              const sample = "# Пример списка доменов для импорта\n# Строки с # игнорируются\n\nyoutube.com\ngooglevideo.com\nyoutu.be\nnetflix.com\nnflxvideo.net\ntwitch.tv\ndiscord.com\ngateway.discord.gg\nspotify.com\naudio-ak-spotify-com.akamaized.net\n";
              const blob = new Blob([sample], { type: "text/plain" });
              const url = URL.createObjectURL(blob);
              const a = document.createElement("a");
              a.href = url;
              a.download = "domains-example.txt";
              a.click();
              URL.revokeObjectURL(url);
            }}
            className="flex items-center gap-1.5 rounded-lg bg-outline-variant/10 px-4 py-2 text-sm font-medium text-outline-variant transition-colors hover:bg-outline-variant/20"
          >
            <Icon name="download" className="h-4 w-4" />
            {t("connections.downloadExample")}
          </button>
          <input ref={fileInputRef} type="file" accept=".txt,.csv,.lst,.list" className="hidden" onChange={handleFileImport} />
        </div>
      </form>

      {rule.domains.length > 0 && (
        <>
          <div className="mb-2 flex gap-2">
            <button
              type="button"
              disabled={saving}
              onClick={() => {
                const blob = new Blob([rule.domains.join("\n") + "\n"], { type: "text/plain" });
                const url = URL.createObjectURL(blob);
                const a = document.createElement("a");
                a.href = url;
                a.download = `${(rule.selectedLocation || rule.name || "domains").replace(/\s+/g, "_")}.txt`;
                a.click();
                URL.revokeObjectURL(url);
              }}
              className="flex items-center gap-1.5 rounded-lg bg-outline-variant/10 px-3 py-1.5 text-xs font-medium text-on-surface-variant transition-colors hover:bg-outline-variant/20 disabled:opacity-40"
            >
              <Icon name="download" className="h-3.5 w-3.5" />
              {t("connections.exportDomains")}
            </button>
            <button
              type="button"
              disabled={saving}
              onClick={async () => {
                if (!window.confirm(t("connections.clearAllConfirm"))) return;
                setSaving(true);
                try {
                  await fetchJSON(`/api/rules/${encodeURIComponent(rule.id)}`, { method: "DELETE" });
                  await applyAndRefresh();
                  showToast(t("connections.domainsCleared"));
                } catch (error) {
                  setPageError(error.message);
                  showToast(error.message, true);
                } finally {
                  setSaving(false);
                }
              }}
              className="flex items-center gap-1.5 rounded-lg bg-error/10 px-3 py-1.5 text-xs font-medium text-error transition-colors hover:bg-error/20 disabled:opacity-40"
            >
              <Icon name="delete_sweep" className="h-3.5 w-3.5" />
              {t("connections.clearAll")}
            </button>
          </div>
          <div className="flex flex-wrap gap-1.5">
            {rule.domains.map((domain) => (
              <span key={domain} className="flex items-center gap-1 rounded-lg bg-surface-container-highest px-2.5 py-1 font-mono text-xs text-secondary">
                {domain}
                <button
                  type="button"
                  onClick={() => removeDomain(domain)}
                  disabled={saving}
                  className="text-outline-variant transition-colors hover:text-error disabled:opacity-40"
                >
                  <Icon name="close" className="h-3 w-3" />
                </button>
              </span>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

function FormField({ label, hint, children }) {
  return (
    <label className="block">
      <span className="mb-1.5 block font-headline text-xs uppercase tracking-widest text-outline">{label}</span>
      {hint ? <span className="mb-2 block text-[10px] text-outline-variant">{hint}</span> : null}
      {children}
    </label>
  );
}

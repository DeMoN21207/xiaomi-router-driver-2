import { useEffect, useState, useRef, useCallback } from "react";
import { fetchJSON } from "../api.js";
import { useI18n } from "../i18n.jsx";
import Icon from "../components/Icon.jsx";
import InlineNotice from "../components/InlineNotice.jsx";

const defaultForm = { name: "", source: "", selectedLocation: "", enabled: true };

export default function SubscriptionsPage() {
  const { t } = useI18n();
  const [config, setConfig] = useState(null);
  const [error, setError] = useState("");
  const [showForm, setShowForm] = useState(false);
  const [form, setForm] = useState(defaultForm);
  const [busy, setBusy] = useState(false);
  const [toast, setToast] = useState(null);
  const toastTimer = useRef(null);

  const showToast = useCallback((message, isError = false) => {
    setToast({ message, error: isError });
    clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(null), 2800);
  }, []);

  useEffect(() => () => clearTimeout(toastTimer.current), []);

  const refresh = useCallback(async () => {
    try {
      const data = await fetchJSON("/api/config");
      setConfig(data);
      setError("");
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function submit(event) {
    event.preventDefault();
    setBusy(true);
    try {
      await fetchJSON("/api/providers", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...form, type: "subscription" }),
      });
      setForm(defaultForm);
      setShowForm(false);
      showToast(t("subscriptions.added"));
      await refresh();
    } catch (err) {
      showToast(err.message, true);
    } finally {
      setBusy(false);
    }
  }

  async function remove(id) {
    setBusy(true);
    try {
      await fetchJSON(`/api/providers/${encodeURIComponent(id)}`, { method: "DELETE" });
      showToast(t("subscriptions.deleted"));
      await refresh();
    } catch (err) {
      showToast(err.message, true);
    } finally {
      setBusy(false);
    }
  }

  const providers = (config?.providers ?? []).filter((provider) => provider.type === "subscription");

  return (
    <div className="space-y-8">
      <header className="flex flex-col justify-between gap-6 md:flex-row md:items-end">
        <div>
          <h1 className="mb-2 font-headline text-4xl font-bold tracking-tight text-on-surface">{t("subscriptions.title")}</h1>
          <p className="max-w-lg text-on-surface-variant">{t("subscriptions.subtitle")}</p>
        </div>
        <div className="flex gap-3">
          <button
            type="button"
            onClick={() => setShowForm((v) => !v)}
            className="flex items-center gap-2 rounded-xl bg-gradient-to-r from-primary to-primary-container px-6 py-2.5 font-bold text-on-primary shadow-lg shadow-primary/10 transition-all hover:brightness-110 active:scale-95"
          >
            <Icon name={showForm ? "close" : "add"} className="h-5 w-5" />
            {showForm ? t("subscriptions.close") : t("subscriptions.add")}
          </button>
          <button
            type="button"
            className="flex items-center gap-2 rounded-xl border border-outline-variant/30 bg-surface-container-high px-5 py-2.5 font-headline text-sm font-medium text-on-surface transition-colors hover:bg-surface-variant"
          >
            <Icon name="cloud_sync" className="h-5 w-5 text-primary" />
            {t("subscriptions.sync")}
          </button>
        </div>
      </header>

      {error ? <InlineNotice tone="error" title={t("error.subscriptions")} message={error} /> : null}

      {showForm && (
        <form onSubmit={submit} className="space-y-5 rounded-xl border border-outline-variant/10 bg-surface-container-low p-6">
          <h2 className="font-headline text-lg font-bold text-on-surface">{t("subscriptions.newTitle")}</h2>
          <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
            <FormField label={t("subscriptions.name")} hint={t("subscriptions.nameHint")}>
              <input
                className="w-full rounded-lg border-none bg-surface-container-highest px-4 py-3 text-sm text-on-surface focus:ring-1 focus:ring-primary"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder={t("subscriptions.namePlaceholder")}
                required
              />
            </FormField>
            <FormField label={t("subscriptions.source")} hint={t("subscriptions.sourceHint")}>
              <input
                className="w-full rounded-lg border-none bg-surface-container-highest px-4 py-3 font-mono text-sm text-on-surface focus:ring-1 focus:ring-primary"
                value={form.source}
                onChange={(e) => setForm({ ...form, source: e.target.value })}
                placeholder={t("subscriptions.sourcePlaceholder")}
                required
              />
            </FormField>
            <FormField label={t("subscriptions.location")} hint={t("subscriptions.locationHint")}>
              <input
                className="w-full rounded-lg border-none bg-surface-container-highest px-4 py-3 text-sm text-on-surface focus:ring-1 focus:ring-primary"
                value={form.selectedLocation}
                onChange={(e) => setForm({ ...form, selectedLocation: e.target.value })}
                placeholder={t("subscriptions.locationPlaceholder")}
              />
            </FormField>
          </div>
          <div className="flex items-center justify-between gap-4 max-sm:flex-col max-sm:items-start">
            <label className="flex cursor-pointer items-center gap-3">
              <input
                type="checkbox"
                checked={form.enabled}
                onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
                className="h-5 w-5 rounded border-outline-variant bg-surface-container-highest text-primary focus:ring-0"
              />
              <span className="text-sm font-medium">{t("subscriptions.enabled")}</span>
            </label>
            <button
              type="submit"
              disabled={busy}
              className="rounded-xl bg-gradient-to-r from-primary to-primary-container px-8 py-2.5 text-sm font-bold text-on-primary shadow-lg shadow-primary/10 transition-all active:scale-95 disabled:opacity-50"
            >
              {t("subscriptions.submit")}
            </button>
          </div>
        </form>
      )}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        {providers.map((provider) => (
          <div key={provider.id} className="group relative overflow-hidden rounded-2xl border-l-4 border-secondary bg-surface-container-low p-6 shadow-lg">
            <div className="absolute top-0 right-0 p-4 opacity-10 transition-opacity group-hover:opacity-20">
              <Icon name="security" className="h-20 w-20" />
            </div>
            <div className="mb-5 flex items-start justify-between">
              <div>
                <div className="mb-1 flex items-center gap-2">
                  <h3 className="font-headline text-xl font-bold tracking-tight text-primary">{provider.name}</h3>
                  <span
                    className={`rounded-md border px-2 py-0.5 text-[10px] font-black uppercase tracking-widest ${
                      provider.enabled
                        ? "border-secondary/20 bg-secondary/10 text-secondary"
                        : "border-outline-variant/20 bg-outline-variant/10 text-outline"
                    }`}
                  >
                    {provider.enabled ? t("subscriptions.active") : t("subscriptions.inactive")}
                  </span>
                </div>
                <div className="flex items-center gap-2 text-on-surface-variant">
                  <Icon name="link" className="h-4 w-4" />
                  <span className="max-w-[300px] truncate font-mono text-xs tracking-tight">{provider.source}</span>
                </div>
              </div>
            </div>
            <div className="mt-4 grid grid-cols-2 gap-4">
              <div>
                <span className="mb-1 block text-[10px] uppercase tracking-widest text-on-surface-variant">{t("subscriptions.location")}</span>
                <p className="text-sm font-medium">{provider.selectedLocation || t("subscriptions.auto")}</p>
              </div>
              <div>
                <span className="mb-1 block text-[10px] uppercase tracking-widest text-on-surface-variant">{t("subscriptions.status")}</span>
                <p className={`text-sm font-bold ${provider.enabled ? "text-secondary" : "text-outline"}`}>
                  {provider.enabled ? t("subscriptions.connected") : t("subscriptions.disconnected")}
                </p>
              </div>
            </div>
            <div className="mt-6 flex gap-3 border-t border-outline-variant/20 pt-4">
              <button
                type="button"
                className="flex items-center gap-1.5 font-headline text-xs font-bold uppercase tracking-wider text-primary transition-colors hover:text-on-surface"
              >
                <Icon name="sync" className="h-4 w-4" />
                {t("subscriptions.update")}
              </button>
              <button
                type="button"
                className="flex items-center gap-1.5 font-headline text-xs font-bold uppercase tracking-wider text-on-surface-variant transition-colors hover:text-primary"
              >
                <Icon name="edit" className="h-4 w-4" />
                {t("subscriptions.edit")}
              </button>
              <button
                type="button"
                onClick={() => remove(provider.id)}
                disabled={busy}
                className="ml-auto flex items-center gap-1.5 font-headline text-xs font-bold uppercase tracking-wider text-on-surface-variant transition-colors hover:text-error disabled:opacity-50"
              >
                <Icon name="delete" className="h-4 w-4" />
                {t("common.delete")}
              </button>
            </div>
          </div>
        ))}
      </div>

      {providers.length === 0 && !showForm ? (
        <div className="space-y-3 rounded-xl bg-surface-container-low p-12 text-center">
          <Icon name="payments" className="mx-auto h-12 w-12 text-outline-variant" />
          <p className="text-on-surface-variant">{t("subscriptions.empty")}</p>
        </div>
      ) : null}

      {toast ? (
        <div
          className={`fixed right-6 bottom-6 z-50 rounded-xl px-5 py-3 text-sm font-medium shadow-2xl ${
            toast.error ? "bg-error-container text-on-error" : "bg-secondary-container text-on-secondary"
          }`}
        >
          {toast.message}
        </div>
      ) : null}
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

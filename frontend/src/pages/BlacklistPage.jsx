import { useCallback, useEffect, useRef, useState } from "react";
import { fetchJSON } from "../api.js";
import { useI18n } from "../i18n.jsx";
import Icon from "../components/Icon.jsx";
import InlineNotice from "../components/InlineNotice.jsx";

export default function BlacklistPage() {
  const { t } = useI18n();
  const [entries, setEntries] = useState([]);
  const [domainCount, setDomainCount] = useState(0);
  const [ipCount, setIpCount] = useState(0);
  const [textInput, setTextInput] = useState("");
  const [error, setError] = useState("");
  const [toast, setToast] = useState("");
  const [toastError, setToastError] = useState(false);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const fileInputRef = useRef(null);

  const showToast = useCallback((msg, isError = false) => {
    setToast(msg);
    setToastError(isError);
    setTimeout(() => setToast(""), 3000);
  }, []);

  const refresh = useCallback(async () => {
    try {
      const data = await fetchJSON("/api/blacklist");
      setEntries(data.entries || []);
      setDomainCount(data.domainCount || 0);
      setIpCount(data.ipCount || 0);
      setError("");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  function parseDomains(raw) {
    return raw.split(/\n/).map((line) => line.replace(/#.*$/, "")).join(",").split(/[\s,;]+/).map((d) => d.trim()).filter(Boolean);
  }

  async function addEntries(event) {
    event.preventDefault();
    const raw = textInput.trim();
    if (!raw) return;
    setBusy(true);
    try {
      await fetchJSON("/api/blacklist", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ entries: parseDomains(raw).join(",") }),
      });
      setTextInput("");
      await refresh();
      showToast(t("blacklist.updated"));
    } catch (err) {
      showToast(err.message, true);
    } finally {
      setBusy(false);
    }
  }

  async function deleteEntry(value) {
    setBusy(true);
    try {
      await fetchJSON(`/api/blacklist?value=${encodeURIComponent(value)}`, { method: "DELETE" });
      await refresh();
    } catch (err) {
      showToast(err.message, true);
    } finally {
      setBusy(false);
    }
  }

  async function applyBlacklist() {
    setBusy(true);
    try {
      await fetchJSON("/api/blacklist/apply", { method: "POST" });
      showToast(t("blacklist.applied"));
    } catch (err) {
      showToast(err.message, true);
    } finally {
      setBusy(false);
    }
  }

  function handleFileImport(event) {
    const file = event.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = async (e) => {
      const text = e.target?.result || "";
      const imported = parseDomains(text);
      if (imported.length === 0) return;
      setBusy(true);
      try {
        await fetchJSON("/api/blacklist", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ entries: imported.join(",") }),
        });
        await refresh();
        showToast(t("blacklist.updated"));
      } catch (err) {
        showToast(err.message, true);
      } finally {
        setBusy(false);
      }
    };
    reader.readAsText(file);
    event.target.value = "";
  }

  return (
    <div className="space-y-8">
      <div className="flex flex-col justify-between gap-4 md:flex-row md:items-end">
        <div>
          <h1 className="font-headline text-3xl font-bold tracking-tight text-primary md:text-4xl">{t("blacklist.title")}</h1>
          <p className="mt-1 text-on-surface-variant">{t("blacklist.subtitle")}</p>
        </div>
        <button
          type="button"
          onClick={applyBlacklist}
          disabled={busy}
          className="flex items-center gap-2 rounded-xl border border-error/30 bg-error/10 px-5 py-2.5 font-headline text-sm font-medium text-error transition-colors hover:bg-error/20 disabled:opacity-50"
        >
          <Icon name="shield" className="h-4 w-4" />
          {t("blacklist.apply")}
        </button>
      </div>

      {error ? <InlineNotice tone="error" title={t("blacklist.error")} message={error} /> : null}

      {toast && (
        <div className={`rounded-xl px-4 py-3 text-sm font-medium ${toastError ? "bg-error/10 text-error" : "bg-secondary/10 text-secondary"}`}>
          {toast}
        </div>
      )}

      <div className="rounded-2xl border border-outline-variant/10 bg-surface-container-lowest p-6">
        <form onSubmit={addEntries} className="space-y-3">
          <textarea
            className="w-full resize-y rounded-lg border-none bg-surface-container-highest px-3 py-2 font-mono text-sm text-on-surface placeholder:text-outline-variant focus:ring-1 focus:ring-primary"
            rows={4}
            value={textInput}
            onChange={(e) => setTextInput(e.target.value)}
            placeholder={t("blacklist.placeholder")}
            disabled={busy}
          />
          <div className="flex flex-wrap gap-2">
            <button
              type="submit"
              disabled={busy || !textInput.trim()}
              className="flex items-center gap-1.5 rounded-lg bg-primary/10 px-4 py-2 text-sm font-medium text-primary transition-colors hover:bg-primary/20 disabled:opacity-40"
            >
              <Icon name="add" className="h-4 w-4" />
              {t("blacklist.add")}
            </button>
            <button
              type="button"
              disabled={busy}
              onClick={() => fileInputRef.current?.click()}
              className="flex items-center gap-1.5 rounded-lg bg-outline-variant/10 px-4 py-2 text-sm font-medium text-on-surface-variant transition-colors hover:bg-outline-variant/20 disabled:opacity-40"
            >
              <Icon name="upload_file" className="h-4 w-4" />
              {t("blacklist.importFile")}
            </button>
            <button
              type="button"
              onClick={() => {
                const sample = "# Пример черного списка\n# Домены (будут резолвиться в 0.0.0.0)\nmalware-site.com\ntracker.example.org\nads.spammer.net\n\n# IP-адреса (будут заблокированы через iptables)\n203.0.113.50\n198.51.100.0/24\n";
                const blob = new Blob([sample], { type: "text/plain" });
                const url = URL.createObjectURL(blob);
                const a = document.createElement("a");
                a.href = url;
                a.download = "blacklist-example.txt";
                a.click();
                URL.revokeObjectURL(url);
              }}
              className="flex items-center gap-1.5 rounded-lg bg-outline-variant/10 px-4 py-2 text-sm font-medium text-outline-variant transition-colors hover:bg-outline-variant/20"
            >
              <Icon name="download" className="h-4 w-4" />
              {t("blacklist.downloadExample")}
            </button>
            <input ref={fileInputRef} type="file" accept=".txt,.csv,.lst,.list" className="hidden" onChange={handleFileImport} />
          </div>
        </form>
      </div>

      {!loading && entries.length > 0 && (
        <div className="space-y-4">
          <div className="flex items-center gap-4">
            <span className="rounded-full bg-primary/10 px-3 py-1 text-xs font-bold text-primary">
              {domainCount} {t("blacklist.domains")}
            </span>
            <span className="rounded-full bg-error/10 px-3 py-1 text-xs font-bold text-error">
              {ipCount} {t("blacklist.ips")}
            </span>
          </div>

          <div className="flex flex-wrap gap-1.5">
            {entries.map((entry) => (
              <span
                key={entry.value}
                className={`flex items-center gap-1 rounded-lg px-2.5 py-1 font-mono text-xs ${
                  entry.type === "ip"
                    ? "bg-error/10 text-error"
                    : "bg-primary/10 text-primary"
                }`}
              >
                {entry.type === "ip" && <Icon name="language" className="h-3 w-3 opacity-60" />}
                {entry.type === "domain" && <Icon name="dns" className="h-3 w-3 opacity-60" />}
                {entry.value}
                <button
                  type="button"
                  onClick={() => deleteEntry(entry.value)}
                  disabled={busy}
                  className="ml-0.5 text-outline-variant transition-colors hover:text-error disabled:opacity-40"
                >
                  <Icon name="close" className="h-3 w-3" />
                </button>
              </span>
            ))}
          </div>
        </div>
      )}

      {!loading && entries.length === 0 && (
        <div className="rounded-2xl border border-outline-variant/10 bg-surface-container-lowest p-8 text-center">
          <Icon name="shield" className="mx-auto mb-2 h-10 w-10 text-outline-variant/30" />
          <p className="text-sm text-outline-variant">{t("blacklist.empty")}</p>
        </div>
      )}
    </div>
  );
}

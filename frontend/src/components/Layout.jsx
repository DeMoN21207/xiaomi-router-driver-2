import { useState, useEffect, useRef } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useI18n } from "../i18n.jsx";
import Icon from "./Icon.jsx";
import GlobalProgress from "./GlobalProgress.jsx";

const navItems = [
  { to: "/", icon: "dashboard", labelKey: "nav.dashboard" },
  { to: "/connections", icon: "vpn_lock", labelKey: "nav.connections" },
  { to: "/traffic", icon: "bar_chart", labelKey: "nav.traffic" },
  { to: "/blacklist", icon: "block", labelKey: "nav.blacklist" },
  { to: "/events", icon: "history", labelKey: "nav.events" },
  { to: "/settings", icon: "settings", labelKey: "nav.settings" },
];

const subtitleKeys = {
  "/": "topbar.management",
  "/connections": "topbar.connections",
  "/traffic": "topbar.traffic",
  "/blacklist": "topbar.blacklist",
  "/events": "topbar.events",
  "/settings": "topbar.settings",
};

export default function Layout() {
  const location = useLocation();
  const { t } = useI18n();
  const subtitleKey = subtitleKeys[location.pathname];
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem("sidebarCollapsed") === "1";
    } catch {
      return false;
    }
  });

  useEffect(() => {
    try {
      localStorage.setItem("sidebarCollapsed", collapsed ? "1" : "0");
    } catch {
      /* ignore */
    }
  }, [collapsed]);

  const sidebarWidth = collapsed ? "w-16" : "w-64";
  const contentOffset = collapsed ? "md:ml-16" : "md:ml-64";
  const topbarOffset = collapsed ? "md:left-16" : "md:left-64";

  return (
    <div className="min-h-screen bg-surface text-on-surface">
      <GlobalProgress />
      {/* Sidebar */}
      <aside className={`hidden md:flex h-screen ${sidebarWidth} fixed left-0 top-0 z-40 bg-surface-container-low shadow-2xl shadow-black/40 flex-col pt-20 pb-6 ${collapsed ? "px-2" : "px-4"} transition-[width] duration-200`}>
        {/* Collapse toggle — vertically centered on the right edge */}
        <button
          type="button"
          onClick={() => setCollapsed((v) => !v)}
          title={collapsed ? t("nav.expand") : t("nav.collapse")}
          className="absolute top-1/2 -right-3 -translate-y-1/2 z-50 flex h-7 w-7 items-center justify-center rounded-full border border-outline-variant/30 bg-surface-container-high text-on-surface-variant shadow-lg transition-colors hover:border-primary/40 hover:text-primary"
        >
          <Icon name={collapsed ? "chevron_right" : "chevron_left"} className="h-4 w-4" />
        </button>

        <div className={`mb-8 ${collapsed ? "px-0 text-center" : "px-4"}`}>
          {collapsed ? (
            <div className="mx-auto flex h-9 w-9 items-center justify-center rounded-lg bg-primary/15 font-headline text-sm font-black text-primary">
              RV
            </div>
          ) : (
            <h1 className="font-headline font-black text-primary text-lg tracking-tight">
              RouteVPN Manager
            </h1>
          )}
        </div>

        <nav className="flex-1 space-y-1">
          {navItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === "/"}
              title={collapsed ? t(item.labelKey) : undefined}
              className={({ isActive }) =>
                `flex items-center ${collapsed ? "justify-center px-2" : "px-4"} py-3 my-1 font-headline uppercase tracking-wider text-xs transition-all duration-200 ${
                  isActive
                    ? "bg-surface-container-high text-primary border-r-4 border-secondary"
                    : "text-outline hover:bg-surface-container-high hover:text-white" + (collapsed ? "" : " hover:translate-x-1")
                }`
              }
            >
              <Icon name={item.icon} className={`${collapsed ? "" : "mr-3"} h-5 w-5 shrink-0`} />
              {!collapsed && t(item.labelKey)}
            </NavLink>
          ))}
        </nav>

        <SidebarClock collapsed={collapsed} />
      </aside>

      {/* Top Bar */}
      <header className={`fixed top-0 right-0 left-0 ${topbarOffset} z-50 bg-surface flex justify-between items-center px-6 md:px-8 py-3 h-16 border-b border-outline-variant/10 transition-[left] duration-200`}>
        <div className="flex items-center gap-4">
          {subtitleKey ? (
            <span className="font-headline text-xl font-bold text-primary tracking-widest uppercase">
              {t(subtitleKey)}
            </span>
          ) : (
            <span className="font-headline text-xl font-bold text-primary tracking-widest">
              RouteVPN
            </span>
          )}
        </div>

      </header>

      {/* Main Content */}
      <main className={`${contentOffset} pt-24 pb-12 px-6 md:px-8 min-h-screen transition-[margin] duration-200`}>
        <div className="max-w-7xl mx-auto">
          <Outlet />
        </div>
      </main>
    </div>
  );
}

function SidebarClock({ collapsed }) {
  const [now, setNow] = useState(() => new Date());
  const startRef = useRef(Date.now());

  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(id);
  }, []);

  if (collapsed) return null;

  const elapsed = Math.floor((now.getTime() - startRef.current) / 1000);
  const h = String(Math.floor(elapsed / 3600)).padStart(2, "0");
  const m = String(Math.floor((elapsed % 3600) / 60)).padStart(2, "0");
  const s = String(elapsed % 60).padStart(2, "0");

  const date = now.toLocaleDateString("ru-RU", { day: "2-digit", month: "2-digit", year: "numeric" });
  const time = now.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" });

  const dayOfWeek = now.toLocaleDateString("ru-RU", { weekday: "long" });

  return (
    <div className="border-t border-outline-variant/10 px-4 pt-4 space-y-1">
      <div className="font-mono text-sm font-semibold text-on-surface tracking-tight">{time}</div>
      <div className="text-xs text-on-surface-variant">{date}, {dayOfWeek}</div>
      <div className="flex items-center gap-1.5 pt-1">
        <Icon name="timer" className="h-3.5 w-3.5 text-primary/70" />
        <span className="font-mono text-xs text-primary/70">{h}:{m}:{s}</span>
      </div>
    </div>
  );
}

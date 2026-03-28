import { useState, useEffect, useRef } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useI18n } from "../i18n.jsx";
import Icon from "./Icon.jsx";

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

  return (
    <div className="min-h-screen bg-surface text-on-surface">
      {/* Sidebar */}
      <aside className="hidden md:flex h-screen w-64 fixed left-0 top-0 z-40 bg-surface-container-low shadow-2xl shadow-black/40 flex-col pt-20 pb-6 px-4">
        <div className="mb-8 px-4">
          <h1 className="font-headline font-black text-primary text-lg tracking-tight">
            RouteVPN Manager
          </h1>
        </div>

        <nav className="flex-1 space-y-1">
          {navItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === "/"}
              className={({ isActive }) =>
                `flex items-center px-4 py-3 my-1 font-headline uppercase tracking-wider text-xs transition-all duration-200 ${
                  isActive
                    ? "bg-surface-container-high text-primary border-r-4 border-secondary"
                    : "text-outline hover:bg-surface-container-high hover:text-white hover:translate-x-1"
                }`
              }
            >
              <Icon name={item.icon} className="mr-3 h-5 w-5" />
              {t(item.labelKey)}
            </NavLink>
          ))}
        </nav>

        <SidebarClock />
      </aside>

      {/* Top Bar */}
      <header className="fixed top-0 right-0 left-0 md:left-64 z-50 bg-surface flex justify-between items-center px-6 md:px-8 py-3 h-16 border-b border-outline-variant/10">
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
      <main className="md:ml-64 pt-24 pb-12 px-6 md:px-8 min-h-screen">
        <div className="max-w-7xl mx-auto">
          <Outlet />
        </div>
      </main>
    </div>
  );
}

function SidebarClock() {
  const [now, setNow] = useState(() => new Date());
  const startRef = useRef(Date.now());

  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(id);
  }, []);

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

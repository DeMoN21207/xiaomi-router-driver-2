const iconMap = {
  dashboard: (
    <>
      <path d="M4 4h7v7H4z" />
      <path d="M13 4h7v4h-7z" />
      <path d="M13 10h7v10h-7z" />
      <path d="M4 13h7v7H4z" />
    </>
  ),
  vpn_lock: (
    <>
      <path d="M7 11V8a5 5 0 0 1 10 0v3" />
      <rect x="5" y="11" width="14" height="10" rx="2" />
      <path d="M12 15v2" />
      <circle cx="12" cy="15" r="1" />
    </>
  ),
  payments: (
    <>
      <rect x="3" y="6" width="18" height="12" rx="2" />
      <path d="M3 10h18" />
      <path d="M7 15h3" />
    </>
  ),
  history: (
    <>
      <path d="M3 12a9 9 0 1 0 3-6.7" />
      <path d="M3 4v4h4" />
      <path d="M12 8v5l3 2" />
    </>
  ),
  settings: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.7 1.7 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.82-.33 1.7 1.7 0 0 0-1 1.54V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1-1.54 1.7 1.7 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .33-1.82 1.7 1.7 0 0 0-1.54-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.54-1 1.7 1.7 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.82.33h.01a1.7 1.7 0 0 0 1-1.54V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.54 1.7 1.7 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.33 1.82v.01a1.7 1.7 0 0 0 1.54 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.54 1Z" />
    </>
  ),
  help_outline: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M9.5 9a2.5 2.5 0 1 1 4.2 1.8c-.9.8-1.7 1.3-1.7 2.7" />
      <path d="M12 17h.01" />
    </>
  ),
  terminal: (
    <>
      <path d="m4 6 5 6-5 6" />
      <path d="M12 18h8" />
    </>
  ),
  language: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M3 12h18" />
      <path d="M12 3a15 15 0 0 1 0 18" />
      <path d="M12 3a15 15 0 0 0 0 18" />
    </>
  ),
  notifications: (
    <>
      <path d="M6 8a6 6 0 1 1 12 0c0 6 2 7 2 7H4s2-1 2-7" />
      <path d="M10 18a2 2 0 0 0 4 0" />
    </>
  ),
  account_circle: (
    <>
      <circle cx="12" cy="8" r="4" />
      <path d="M4 20a8 8 0 0 1 16 0" />
    </>
  ),
  cloud_sync: (
    <>
      <path d="M7 18a4 4 0 1 1 .5-8A5.5 5.5 0 0 1 18 8.5 4.5 4.5 0 0 1 17 18Z" />
      <path d="M10 14h4" />
      <path d="m12 12 2 2-2 2" />
      <path d="M14 10h-4" />
      <path d="m12 8-2 2 2 2" />
    </>
  ),
  security: (
    <>
      <path d="M12 3 5 6v5c0 4.5 2.6 8.5 7 10 4.4-1.5 7-5.5 7-10V6Z" />
      <path d="m9.5 12 1.8 1.8 3.2-3.3" />
    </>
  ),
  link: (
    <>
      <path d="M10 14a4 4 0 0 1 0-6l2-2a4 4 0 0 1 6 6l-1 1" />
      <path d="M14 10a4 4 0 0 1 0 6l-2 2a4 4 0 1 1-6-6l1-1" />
    </>
  ),
  sync: (
    <>
      <path d="M20 7v5h-5" />
      <path d="M4 17v-5h5" />
      <path d="M7 17a7 7 0 0 0 11-3" />
      <path d="M17 7A7 7 0 0 0 6 10" />
    </>
  ),
  edit: (
    <>
      <path d="M4 20h4l10-10-4-4L4 16Z" />
      <path d="m12 6 4 4" />
    </>
  ),
  error: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 8v5" />
      <path d="M12 16h.01" />
    </>
  ),
  refresh: (
    <>
      <path d="M21 12a9 9 0 1 1-3-6.7" />
      <path d="M21 4v6h-6" />
    </>
  ),
  play_arrow: <path d="m8 6 10 6-10 6z" />,
  add: (
    <>
      <path d="M12 5v14" />
      <path d="M5 12h14" />
    </>
  ),
  close: (
    <>
      <path d="m6 6 12 12" />
      <path d="m18 6-12 12" />
    </>
  ),
  delete: (
    <>
      <path d="M5 7h14" />
      <path d="M9 7V5h6v2" />
      <path d="M8 7v12h8V7" />
      <path d="M10 11v5" />
      <path d="M14 11v5" />
    </>
  ),
  health_and_safety: (
    <>
      <path d="M12 3 5 6v5c0 4.5 2.6 8.5 7 10 4.4-1.5 7-5.5 7-10V6Z" />
      <path d="M12 8v6" />
      <path d="M9 11h6" />
    </>
  ),
  alt_route: (
    <>
      <path d="M6 6h8" />
      <path d="M14 6a4 4 0 0 1 4 4v1" />
      <path d="M18 11v7" />
      <path d="M18 18h-8" />
      <path d="M10 18a4 4 0 0 1-4-4v-1" />
      <path d="M6 13V6" />
    </>
  ),
  dns: (
    <>
      <rect x="5" y="4" width="14" height="6" rx="2" />
      <rect x="5" y="14" width="14" height="6" rx="2" />
      <path d="M8 7h.01" />
      <path d="M8 17h.01" />
      <path d="M12 7h4" />
      <path d="M12 17h4" />
    </>
  ),
  analytics: (
    <>
      <path d="M5 19V9" />
      <path d="M12 19V5" />
      <path d="M19 19v-7" />
    </>
  ),
  settings_ethernet: (
    <>
      <path d="M7 7h10v5H7z" />
      <path d="M9 12v4" />
      <path d="M15 12v4" />
      <path d="M4 16h16" />
      <path d="M12 16v4" />
    </>
  ),
  palette: (
    <>
      <path d="M12 3a9 9 0 1 0 0 18h1a2 2 0 0 0 0-4h-1a2 2 0 1 1 0-4h3a6 6 0 0 0 0-12Z" />
      <circle cx="7.5" cy="10" r="1" />
      <circle cx="10" cy="7.5" r="1" />
      <circle cx="14" cy="7.5" r="1" />
    </>
  ),
  wifi: (
    <>
      <path d="M4.5 9a11 11 0 0 1 15 0" />
      <path d="M7.5 12a7 7 0 0 1 9 0" />
      <path d="M10.5 15a3 3 0 0 1 3 0" />
      <path d="M12 19h.01" />
    </>
  ),
  schedule: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v6l4 2" />
    </>
  ),
  info: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 10v5" />
      <path d="M12 7h.01" />
    </>
  ),
  warning: (
    <>
      <path d="M12 4 3 20h18Z" />
      <path d="M12 9v5" />
      <path d="M12 17h.01" />
    </>
  ),
  bar_chart: (
    <>
      <path d="M4 20V10" />
      <path d="M9 20V4" />
      <path d="M14 20v-8" />
      <path d="M19 20v-4" />
    </>
  ),
  delete_sweep: (
    <>
      <path d="M4 7h4" />
      <path d="M4 12h6" />
      <path d="M4 17h8" />
      <path d="M15 8l6 6" />
      <path d="M21 8l-6 6" />
    </>
  ),
  search: (
    <>
      <circle cx="10.5" cy="10.5" r="6.5" />
      <path d="m21 21-4.3-4.3" />
    </>
  ),
  expand_more: <path d="m7 10 5 5 5-5" />,
  hourglass_empty: (
    <>
      <path d="M7 4h10v3a5 5 0 0 1-5 5 5 5 0 0 1-5-5V4Z" />
      <path d="M7 20h10v-3a5 5 0 0 0-5-5 5 5 0 0 0-5 5v3Z" />
    </>
  ),
  timer: (
    <>
      <circle cx="12" cy="13" r="8" />
      <path d="M12 9v4l2.5 1.5" />
      <path d="M10 2h4" />
    </>
  ),
  calendar_today: (
    <>
      <rect x="4" y="5" width="16" height="16" rx="2" />
      <path d="M8 3v4" />
      <path d="M16 3v4" />
      <path d="M4 10h16" />
    </>
  ),
  filter_list: (
    <>
      <path d="M4 6h16" />
      <path d="M7 12h10" />
      <path d="M10 18h4" />
    </>
  ),
  download: (
    <>
      <path d="M12 5v10" />
      <path d="m7 12 5 5 5-5" />
      <path d="M5 19h14" />
    </>
  ),
  upload_file: (
    <>
      <path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z" />
      <path d="M14 3v4h4" />
      <path d="M12 17v-6" />
      <path d="m9 14 3-3 3 3" />
    </>
  ),
  block: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="m5.7 5.7 12.6 12.6" />
    </>
  ),
  shield: (
    <>
      <path d="M12 3 4 7v5c0 5 3.3 9.5 8 11 4.7-1.5 8-6 8-11V7Z" />
    </>
  ),
  date_range: (
    <>
      <rect x="4" y="5" width="16" height="16" rx="2" />
      <path d="M8 3v4" />
      <path d="M16 3v4" />
      <path d="M4 10h16" />
      <path d="M8 14h2" />
      <path d="M14 14h2" />
    </>
  ),
  pending: <circle cx="12" cy="12" r="9" />,
};

export default function Icon({ name, className = "" }) {
  const icon = iconMap[name] ?? iconMap.pending;

  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
    >
      {icon}
    </svg>
  );
}

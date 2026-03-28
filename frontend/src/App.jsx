import { Routes, Route, Navigate } from "react-router-dom";
import Layout from "./components/Layout.jsx";
import DashboardPage from "./pages/DashboardPage.jsx";
import ConnectionsPage from "./pages/ConnectionsPage.jsx";
import EventsPage from "./pages/EventsPage.jsx";
import SettingsPage from "./pages/SettingsPage.jsx";
import TrafficStatsPage from "./pages/TrafficStatsPage.jsx";
import BlacklistPage from "./pages/BlacklistPage.jsx";

export default function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<DashboardPage />} />
        <Route path="connections" element={<ConnectionsPage />} />
        <Route path="traffic" element={<TrafficStatsPage />} />
        <Route path="blacklist" element={<BlacklistPage />} />
        <Route path="events" element={<EventsPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

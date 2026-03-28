import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { I18nProvider } from "./i18n.jsx";
import App from "./App.jsx";
import "./styles.css";

// Restore theme from localStorage (default: dark)
const savedTheme = localStorage.getItem("theme");
document.documentElement.classList.toggle("dark", savedTheme !== "light");

ReactDOM.createRoot(document.getElementById("root")).render(
  <React.StrictMode>
    <BrowserRouter>
      <I18nProvider>
        <App />
      </I18nProvider>
    </BrowserRouter>
  </React.StrictMode>,
);

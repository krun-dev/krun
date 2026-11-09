import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./index.css";
import { restoreRepositoryReturn } from "./lib/repository-authorization";

restoreRepositoryReturn();
try {
  document.documentElement.classList.toggle("dark", localStorage.getItem("krun.theme") === "dark");
} catch {
  document.documentElement.classList.remove("dark");
}
ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);

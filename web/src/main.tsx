import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
const Docs = React.lazy(() =>
  import("./components/docs").then((module) => ({ default: module.Docs })),
);
import "./index.css";
try {
  document.documentElement.classList.toggle("dark", localStorage.getItem("krun.theme") !== "light");
} catch {
  document.documentElement.classList.add("dark");
}
ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    {window.location.pathname === "/docs" ? (
      <React.Suspense fallback={null}>
        <Docs />
      </React.Suspense>
    ) : (
      <App />
    )}
  </React.StrictMode>,
);

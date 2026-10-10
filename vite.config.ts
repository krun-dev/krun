import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
export default defineConfig({
  root: "web",
  base: "/console/",
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "web/src") } },
  build: { outDir: "../dist/console", emptyOutDir: true },
  server: {
    proxy: {
      "/auth": "http://127.0.0.1:8080",
      "/api": "http://127.0.0.1:8080",
    },
  },
});

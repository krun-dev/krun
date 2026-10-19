import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
export default defineConfig({
  root: "web",
  base: "/",
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "web/src") } },
  build: { outDir: "../dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": process.env.KRUN_API_TARGET || "http://127.0.0.1:8080",
    },
  },
});

import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import pageActions from "starlight-page-actions";
import llmsTxt from "starlight-llms-txt";

export default defineConfig({
  site: "https://krun.invalid",
  output: "static",
  outDir: "../dist/starlight",
  build: { format: "file", inlineStylesheets: "never" },
  trailingSlash: "never",
  vite: {
    build: { assetsInlineLimit: 0 },
    plugins: [
      {
        // Starlight imports Astro virtual modules; keep them inside Vite in this subproject.
        name: "krun-starlight-no-external",
        enforce: "post",
        configEnvironment(name) {
          if (["astro", "ssr", "prerender"].includes(name)) {
            return { resolve: { noExternal: [/^@astrojs\/starlight/] } };
          }
        },
      },
    ],
  },
  integrations: [
    starlight({
      title: "krun 文档",
      defaultLocale: "root",
      locales: { root: { label: "简体中文", lang: "zh-CN" } },
      customCss: ["./src/styles/custom.css"],
      components: {
        PageTitle: "./src/components/PageTitle.astro",
        SiteTitle: "./src/components/SiteTitle.astro",
        ThemeProvider: "./src/components/ThemeProvider.astro",
        ThemeSelect: "./src/components/ThemeSelect.astro",
        Sidebar: "./src/components/Sidebar.astro",
        Footer: "./src/components/Footer.astro",
      },
      sidebar: [
        { label: "返回控制台", link: "/" },
        { label: "开始使用", items: ["docs", { autogenerate: { directory: "guides" } }] },
      ],
      head: [{ tag: "script", attrs: { src: "/docs-enhance.js", defer: true } }],
      plugins: [pageActions({ position: "page-title" }), llmsTxt()],
    }),
  ],
});

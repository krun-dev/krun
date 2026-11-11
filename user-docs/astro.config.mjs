import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";
import pageActions from "starlight-page-actions";
import llmsTxt from "starlight-llms-txt";
import internalDocs from "./plugins/internal-docs.mjs";

export default defineConfig({
  site: "https://krun.invalid",
  base: "/docs",
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
      favicon: "/favicon.png",
      tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 4 },
      expressiveCode: {
        plugins: [
          {
            name: "Shell prompts",
            hooks: {
              postprocessRenderedLine({ codeBlock, line, lineIndex, renderData }) {
                if (!["sh", "bash", "zsh"].includes(codeBlock.language)) return;
                const previousLine =
                  lineIndex > 0 ? codeBlock.getLine(lineIndex - 1)?.text.trimEnd() : undefined;
                if (!/^[^\s#]/.test(line.text) || previousLine?.endsWith("\\")) return;
                renderData.lineAst.properties.className.push("krun-shell-command");
              },
            },
          },
        ],
      },
      components: {
        PageTitle: "./src/components/PageTitle.astro",
        TwoColumnContent: "./src/components/SingleColumnContent.astro",
        SiteTitle: "./src/components/SiteTitle.astro",
        ThemeProvider: "./src/components/ThemeProvider.astro",
        ThemeSelect: "./src/components/ThemeSelect.astro",
        Sidebar: "./src/components/Sidebar.astro",
        Footer: "./src/components/Footer.astro",
      },
      sidebar: [
        { label: "开始使用", items: ["index", "guides/concepts", "guides/organizations"] },
        {
          label: "组件",
          items: [
            {
              label: "计算 · Workers",
              items: [
                { label: "概览", slug: "guides/component-workers" },
                "guides/deploy-project",
                "guides/deploy-local-project",
                {
                  label: "Git 仓库集成",
                  items: [
                    { label: "连接与构建", slug: "guides/git-repositories" },
                    "guides/gitlab-integration",
                    "guides/github-integration",
                  ],
                },
                "guides/workers-runtime-compatibility",
              ],
            },
            "guides/component-object-storage",
            { label: "数据库", items: ["guides/workers-hyperdrive"] },
          ],
        },
        {
          label: "命令行工具",
          items: [
            "guides/krun-cli",
            {
              label: "Wrangler 使用指南",
              collapsed: true,
              items: [
                { label: "部署 Workers", slug: "guides/cloudflare-workers" },
                { label: "配置兼容范围", slug: "guides/wrangler-configuration" },
              ],
            },
          ],
        },
        {
          label: "示例",
          items: [
            "guides/worker-examples",
            "guides/examples-r2-workers",
            "guides/examples-postgres-todo",
            "guides/examples-hono",
            "guides/examples-astro",
            "guides/examples-static-site",
          ],
        },
      ],
      head: [
        { tag: "script", attrs: { src: "/docs/docs-enhance.js", defer: true } },
        {
          tag: "link",
          attrs: { rel: "apple-touch-icon", href: "/docs/apple-touch-icon.png" },
        },
      ],
      plugins: [pageActions({ position: "page-title" }), llmsTxt(), internalDocs()],
    }),
  ],
});

---
title: 静态页面
description: 将一隅 HTML / CSS / SVG 小站作为 assets-only 项目发布到 krun Workers。
---

[一隅 · 静态小站源码](https://github.com/krun-dev/krun-examples/tree/main/examples/static-site)是一个运行在 **krun Workers** 上的植物插画风格小站，包含响应式首页、可展开的日常手记和自定义 404 页面。

页面只有 HTML、CSS 和本地 SVG，没有浏览器 JavaScript、API 或外部字体依赖。手记通过原生 HTML `details` 元素展开，关闭 JavaScript 后仍可用。

## 在 Workers 上部署

按[通用部署步骤](/docs/guides/worker-examples.html#在控制台构建并发布)关联 `https://github.com/krun-dev/krun-examples`，分支选择 `main`，项目目录填写 **`examples/static-site`**。无需变量、Secret、对象桶或任何 binding，构建成功后发布到目标环境。

项目的 `wrangler.jsonc` 只声明静态资产，没有 `main`：

```jsonc
{
  "name": "static-site-demo",
  "compatibility_date": "2026-09-28",
  "assets": {
    "directory": "./public",
    "html_handling": "auto-trailing-slash",
    "not_found_handling": "404-page"
  }
}
```

`public/` 就是完整静态产物，`npm run build` 不编译业务 Worker。平台将资产打包为不可变制品，生成内部入口，通过 Workers 版本与环境发布链路部署，由 workerd runtime 服务请求。运行时无需 Node.js，也无需单独配置 OSS 静态站点托管。

## 验证部署

将以下路径追加到目标环境地址：

| 路径 | 预期结果 |
| --- | --- |
| `/` | 显示「一隅」首页，植物插画和样式正常；可展开日常手记，窄屏布局正常 |
| `/not-found` | 显示「这里留了一点空白」，返回 HTTP 404 |

此例使用 `404-page`，不启用 SPA fallback，也不依赖前端路由。更多资产配置见 [Wrangler 配置兼容范围](/docs/guides/wrangler-configuration.html#静态资产配置)。

## 修改页面与本地预览

直接修改 `public/index.html` 的文字、`public/styles.css` 的样式或 `public/garden.svg` 的插画，然后重新构建并发布。新增静态文件也放在 `public/`。

在项目目录执行：

```sh
npm ci
npm run check
npm run dev
```

`check` 使用 Wrangler dry-run 检查资产，`dev` 启动本地 Wrangler。使用 Wrangler 直接部署到 krun 的终端配置见[部署指南](/docs/guides/cloudflare-workers.html)。

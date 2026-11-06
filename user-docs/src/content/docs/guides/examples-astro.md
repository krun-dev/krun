---
title: Astro
description: 使用 Astro Cloudflare adapter 在 krun Workers 上渲染页面与 JSON API。
---

[Astro 示例源码](https://github.com/krun-dev/krun-examples/tree/main/examples/astro)使用 Astro Cloudflare adapter 生成模块 Worker。首页根据查询参数按请求渲染，同时提供 JSON API。此示例是 **Workers 服务端渲染**，不需要部署 Node.js 服务，也不使用 session、KV 或图片优化。

## 在 Workers 上部署

按[通用部署步骤](/docs/guides/worker-examples.html#在控制台构建并发布)关联 `https://github.com/krun-dev/krun-examples`，分支选择 `main`，项目目录填写 **`examples/astro`**，构建成功后发布到目标环境，无需额外资源绑定。

项目的 `wrangler.jsonc` 同时声明构建命令、生成的 Worker 入口与静态资产：

```jsonc
{
  "name": "astro-demo",
  "main": "./dist/_worker.js/index.js",
  "compatibility_date": "2026-09-28",
  "compatibility_flags": ["nodejs_compat"],
  "build": { "command": "npm run build" },
  "assets": { "directory": "./dist", "binding": "ASSETS" }
}
```

Wrangler 调用 Astro 构建，再将 Worker 和静态资产打包为 krun 制品。`ASSETS` 是静态资产 binding，不需要在控制台创建同名对象桶。普通框架仓库需要先配置好 Workers adapter；平台不会自动安装或补写 adapter。

## 验证部署

将以下路径追加到目标环境地址：

| 路径 | 预期结果 |
| --- | --- |
| `/?name=krun` | 显示「Astro on krun」和「你好，krun！」；更换 `name` 后页面显示对应问候 |
| `/api/hello?name=krun` | 返回 `{"message":"Hello, krun!","framework":"Astro"}` |

首页在 Worker 中按请求渲染。若只想部署 HTML/CSS 文件，可选择[静态页面示例](/docs/guides/examples-static-site.html)。

## 本地运行

在项目目录执行：

```sh
npm ci
npm run check
npx wrangler@4.155.0 dev --local
```

`check` 会执行配置中的 Astro 构建并进行 Wrangler dry-run。部署后的最终行为仍需在 krun 目标环境验证。

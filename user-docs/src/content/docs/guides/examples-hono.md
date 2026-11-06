---
title: Hono
description: 在 krun Workers 上使用 Hono 提供页面与 JSON API。
---

[Hono 示例源码](https://github.com/krun-dev/krun-examples/tree/main/examples/hono)使用 Hono 定义路由，提供一个 HTML 首页和问候 JSON API，适合作为轻量 HTTP 服务的起点。不需要对象桶、Secret 或其他资源绑定。

## 在 Workers 上部署

按[通用部署步骤](/docs/guides/worker-examples.html#在控制台构建并发布)关联 `https://github.com/krun-dev/krun-examples`，分支选择 `main`，项目目录填写 **`examples/hono`**，构建成功后发布到目标环境。

项目的 `wrangler.jsonc` 指定模块入口：

```jsonc
{
  "name": "hono-demo",
  "main": "src/index.js",
  "compatibility_date": "2026-09-28"
}
```

入口导出 Hono 应用，由 krun workerd runtime 处理请求，运行时无需 Node.js。仓库构建的目标是控制台选择的 Worker，配置中的 `name` 不会替代该选择。

## 验证部署

将以下路径追加到目标环境地址：

| 路径 | 预期结果 |
| --- | --- |
| `/` | 显示「Hono on krun」页面 |
| `/api/hello?name=krun` | 返回 `{"message":"Hello, krun!","framework":"Hono"}` |

可在源码中修改路由或问候内容，再重新构建并发布。这里的 `/api/hello` 是业务 Worker 内的 API 路径。

## 本地运行

在项目目录执行：

```sh
npm ci
npm run check
npm run dev
```

`check` 使用 Wrangler dry-run 检查构建，`dev` 启动本地 Wrangler。部署后的最终行为仍需在 krun 目标环境验证。

---
title: Workers 代码兼容范围
description: 面向开发者和编码 Agent 的 krun Workers API 使用范围。
sidebar:
  order: 3
---

krun 基于 workerd 运行模块格式的 JavaScript / TypeScript Worker。代码写法参考 Cloudflare Workers 文档，可用能力及限制以 krun 组件文档为准。使用相同运行时不代表兼容整个 Cloudflare 平台。

本页提供开发者阅读列表和可直接交给编码 Agent 的 Markdown 列表，无需读取 Cloudflare 完整的 `llms.txt`。

## 开发者阅读列表

先了解 krun 提供的能力，再按任务查询官方 API 写法：

| 阅读时机 | 文档 | 关注内容 |
| --- | --- | --- |
| 开始编写前 | [计算 · Workers](/docs/guides/component-workers.html) | 请求入口、变量、Secret、静态资产、Cache API 和网络限制 |
| 使用对象存储时 | [存储 · 对象存储](/docs/guides/component-object-storage.html) | R2 binding 的可用方法、参数、分页和分片上传限制 |
| 构建和发布时 | [使用 Wrangler 部署 Workers](/docs/guides/cloudflare-workers.html) | 项目配置、构建、发布和目标环境验证 |
| 首次编写入口时 | [Cloudflare Fetch Handler](https://developers.cloudflare.com/workers/runtime-apis/handlers/fetch/) | 模块入口、接收 Request、通过 env 读取 binding、返回 Response |
| 处理请求时 | [Cloudflare Request](https://developers.cloudflare.com/workers/runtime-apis/request/) | 标准请求属性、请求头和 body 读取 |
| 生成响应时 | [Cloudflare Response](https://developers.cloudflare.com/workers/runtime-apis/response/) | 状态码、响应头、响应 body 和重定向 |

官方页面中的 Cloudflare 专属扩展（例如 `request.cf`）、其他产品和网络能力不构成 krun 的支持承诺。R2、Cache、静态资产和 Secret 的具体使用范围直接查询上面的 krun 组件文档。

## 给编码 Agent 的列表

复制下面整段 Markdown 给 Agent。平台链接使用当前控制台地址；如果查看原文时仍看到 `{{KRUN_ORIGIN}}`，请先替换为你的控制台 origin。也可以使用页面顶部的「复制 Markdown」复制本页，平台地址会自动补全。

```markdown
# krun Worker 编码指南

为 krun 编写模块格式的 JavaScript / TypeScript Worker。
使用 export default { async fetch(request, env) { … } } 接收 HTTP 请求并返回 Response，优先使用标准 Web API。

## 必读

- [krun Workers 能力与限制]({{KRUN_ORIGIN}}/docs/guides/component-workers.md)
- [Cloudflare Fetch Handler：模块入口写法](https://developers.cloudflare.com/workers/runtime-apis/handlers/fetch/index.md)

## 按需读取

- 处理请求：[Request](https://developers.cloudflare.com/workers/runtime-apis/request/index.md)，只参考标准请求属性和 body 读取。
- 生成响应：[Response](https://developers.cloudflare.com/workers/runtime-apis/response/index.md)，只参考标准响应属性和方法。
- 使用对象存储：[krun R2 binding 方法与限制]({{KRUN_ORIGIN}}/docs/guides/component-object-storage.md)。
- 构建和发布：[krun Wrangler 部署流程]({{KRUN_ORIGIN}}/docs/guides/cloudflare-workers.md)。

## 编码约束

- 以 krun 组件文档明确列出的 binding、方法和参数范围为准；官方文档用于查询写法，不代表 krun 支持其中全部能力。
- 先检查项目实际配置的 binding 名称、compatibility date 和 flags；不要自行升级兼容日期或添加未经验证的 flags。
- 业务 Worker 没有默认的公网出站权限，不要依赖全局 fetch(url) 访问外部服务；它与接收请求的 fetch handler 不同。
- 不要假设支持 request.cf、KV、D1、Durable Objects、Queues、Service bindings 或定时触发器。
- R2 列表处理分页，对象读取处理 null 和无 body；Cache miss 必须有回退，不能依赖跨实例共享或持久化缓存。
- 不要假设完整 Node.js 环境或任意 npm 包可用；兼容性取决于平台固定的运行时、项目 date/flags 及实际使用的 API。
- 需要文档未列出的能力时先说明需求，不要假设平台支持。完成后在 krun 目标环境验证，构建成功不代表运行能力全部可用。
```

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
| 迁移或修改项目配置时 | [Wrangler 配置兼容范围](/docs/guides/wrangler-configuration.html) | 配置文件、字段、binding 和仓库构建与直接部署的差异 |
| 首次编写入口时 | [Cloudflare Fetch Handler](https://developers.cloudflare.com/workers/runtime-apis/handlers/fetch/) | 模块入口、接收 Request、通过 env 读取 binding、返回 Response |
| 处理请求时 | [Cloudflare Request](https://developers.cloudflare.com/workers/runtime-apis/request/) | 标准请求属性、请求头和 body 读取 |
| 生成响应时 | [Cloudflare Response](https://developers.cloudflare.com/workers/runtime-apis/response/) | 状态码、响应头、响应 body 和重定向 |
| 理解 binding 时 | [Cloudflare Bindings](https://developers.cloudflare.com/workers/runtime-apis/bindings/#what-is-a-binding) | binding 名称、实际资源与 `env` 的关系；只参考 krun 已支持的绑定类型 |
| 学习桶绑定和读写时 | [Cloudflare Use R2 from Workers](https://developers.cloudflare.com/r2/api/workers/workers-api-usage/) | `binding`、`bucket_name` 与代码访问方式；建桶、环境绑定和发布按 krun 流程操作 |
| 查询 R2 方法时 | [Cloudflare R2 Workers API reference](https://developers.cloudflare.com/r2/api/workers/workers-api-reference/) | 仅查询 krun 对象存储文档明确支持的方法和参数 |

**krun 只兼容组件文档明确列出的 API、参数及用法。官方参考中的同名 API 不代表所有选项和行为都受支持。** Cloudflare 专属扩展（例如 `request.cf`）、其他产品和网络能力不构成 krun 的支持承诺。

- **R2**：支持 `head`、`get`、`put`、`delete`、`list`、`createMultipartUpload`、`resumeMultipartUpload`，以及上传对象的 `uploadPart`、`complete`、`abort`。`list` 每页最多 64 项，批量 `delete` 最多 1000 项；不支持 `startAfter`、SSE-C、PUT checksum 或组合条件。其他参数及返回值限制见 [对象存储](/docs/guides/component-object-storage.html)。
- **Hyperdrive**：支持 PostgreSQL。Worker 把 `env.<绑定名>.connectionString` 交给 PostgreSQL 驱动。配置与绑定见 [PostgreSQL 托管接入](/docs/guides/workers-hyperdrive.html)。
- **Cache**：支持 `caches.default`、`caches.open` 和 cache 的 `put`、`match`、`delete`。缓存只在单个 runtime 进程内生效；不支持 `ignoreSearch`、`ignoreVary`、SWR 或 Cloudflare CDN 缓存语义，详见 [Workers](/docs/guides/component-workers.html)。
- **绑定与发布**：绑定名称需与代码中的 `env` 属性一致，桶名是实际资源名称。控制台仓库构建需要在目标环境手动配置桶绑定，保存后发布；仓库配置不会自动创建桶或修改环境绑定。

## 给编码 Agent 的列表

复制下面整段 Markdown 给 Agent。平台链接使用当前控制台地址；如果查看原文时仍看到 `{{KRUN_ORIGIN}}`，请先替换为你的控制台 origin。也可以使用页面顶部的「复制 Markdown」复制本页，平台地址会自动补全。

下列 Cloudflare 官方链接直接指向各篇文档的 `index.md` 原文，适合 LLM 按任务读取；无需抓取网页导航或完整 `llms.txt`。官方 Markdown 同样包含 krun 未支持的能力，读取时必须遵守下面的编码约束。

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
- 理解绑定：[Cloudflare Bindings](https://developers.cloudflare.com/workers/runtime-apis/bindings/index.md)，只参考 binding、资源和 env 的关系，不扩展 krun 支持的绑定类型。
- 学习桶绑定和读写：[Cloudflare Use R2 from Workers](https://developers.cloudflare.com/r2/api/workers/workers-api-usage/index.md)，参考 binding / bucket_name 和代码写法；资源创建和发布遵循 krun 流程。
- 查询 R2 方法：[Cloudflare R2 Workers API reference](https://developers.cloudflare.com/r2/api/workers/workers-api-reference/index.md)，先读 krun 对象存储文档，再查询其中明确支持的方法和参数。
- 构建和发布：[krun Wrangler 部署流程]({{KRUN_ORIGIN}}/docs/guides/cloudflare-workers.md)。
- 配置项目：[krun Wrangler 配置兼容范围]({{KRUN_ORIGIN}}/docs/guides/wrangler-configuration.md)，按实际发布方式选择配置；仓库构建不会自动导入 vars、R2 binding 或 env 配置段。

## 编码约束

- 以 krun 组件文档明确列出的 binding、方法和参数范围为准；官方文档用于查询写法，不代表 krun 支持其中全部能力。
- 先检查项目实际配置的 binding 名称、compatibility date 和 flags；不要自行升级兼容日期或添加未经验证的 flags。
- 业务 Worker 没有默认的公网出站权限，不要依赖全局 fetch(url) 访问外部服务；它与接收请求的 fetch handler 不同。
- 服务绑定使用 `env.SERVICE.fetch(request)`，调用同一组织内目标 Worker 的默认 HTTP 入口。只使用项目已配置的绑定。
- 不要假设支持 request.cf、KV、D1、Durable Objects、Queues 或定时触发器。
- R2 列表处理分页，对象读取处理 null 和无 body；Cache miss 必须有回退，不能依赖跨实例共享或持久化缓存。
- R2 list 每页最多 64 项，批量 delete 最多 1000 项；不使用 startAfter、SSE-C、PUT checksum 或组合条件，不因官方文档列出某个参数就假设 krun 支持。
- binding 名称与 env 属性一致，区分大小写；bucket_name 是实际桶名。控制台仓库构建需要在目标环境手动配置绑定并发布，不能只修改仓库中的 wrangler.jsonc。
- 不要假设完整 Node.js 环境或任意 npm 包可用；兼容性取决于平台固定的运行时、项目 date/flags 及实际使用的 API。
- 需要文档未列出的能力时先说明需求，不要假设平台支持。完成后在 krun 目标环境验证，构建成功不代表运行能力全部可用。
```

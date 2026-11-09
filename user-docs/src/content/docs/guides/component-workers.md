---
title: 计算 · Workers
description: 了解 Workers 兼容的运行时入口、binding 和 Cache API。
---

krun 使用 workerd 运行模块格式的 JavaScript 和 TypeScript Workers。入口通过 `fetch(request, env)` 接收请求并返回 `Response`：

**仅兼容本页及[对象存储文档](/docs/guides/component-object-storage.html)列出的 API 和用法，很多 Cloudflare Workers API 和产品不支持。** 不要假设未列出的能力可用。

```js
export default {
  async fetch(request, env) {
    return new Response(`Hello, ${env.NAME}!`);
  },
};
```

运行时使用项目配置的 compatibility date 和 flags。业务 Worker 没有默认的公网出站权限，不要依赖全局 `fetch()` 调用外部服务；需要读取静态资源或对象存储时，使用已配置的 binding。

## 可用的 binding

通过 `fetch(request, env)` 的 `env` 参数读取配置的 binding。只有实际配置并发布的 binding 才存在。

| binding | 代码中的用法 | 可用范围 |
| --- | --- | --- |
| 普通变量 | `env.NAME` | 字符串或 JSON 值；JSON 保持对象、数组、数字、布尔和 null 类型 |
| Secret | `env.API_TOKEN` | 字符串；通过控制台或 Wrangler 设置，不要写入源码 |
| 版本元数据 | `env.VERSION` | 配置后可读取 `id`、`tag`、`timestamp` |
| 静态资产 | `env.ASSETS.fetch(request)` | 读取当前版本打包的静态文件；需配置 `assets.binding` |
| R2 桶 | `env.BUCKET` | 需配置 `r2_buckets` 并绑定已启用的逻辑桶；方法见 [对象存储](/docs/guides/component-object-storage.html) |

这些名称只是示例，实际名称由项目或环境配置决定。不要使用 KV、D1、Durable Objects、Queue 或 Service binding，也不要假设未列出的 binding 可用。

## Cache API

可以使用 `caches.default`、`caches.open(name)` 和 cache 的 `put(request, response)`、`match(request)`、`delete(request)`。仅把缓存用于可丢失的加速数据：它是单个 runtime 进程内的有界缓存，重启或请求落到其他实例都可能 miss，`put` 成功也不保证实际存入。

缓存按组织、Worker、环境和名称隔离；支持常见 TTL、Vary、条件读取和单段 Range。不要依赖跨实例共享、全局清理、`ignoreSearch`、`ignoreVary`、SWR 或 Cloudflare CDN 缓存。`put` 不接受非 GET、206 响应或 `Vary: *`。

## 构建和发布

支持 Wrangler 配置、JavaScript/TypeScript 模块、静态资产和已配置的自定义构建；仓库构建支持 npm、pnpm、Yarn 或 Bun 锁文件。通过控制台从仓库构建，见 [Git 仓库集成](/docs/guides/git-repositories.html)。通过 CLI 部署，见 [使用 Wrangler 部署 Workers](/docs/guides/cloudflare-workers.html)。

如需限定编码 Agent 的使用范围，参照 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html)。

## 命名配置版本

在 Worker 中选择环境并进入「配置版本」，可以从环境草稿或当前生效配置保存一份带名称、
说明的配置，例如“XXX 测试”。通过「复制并修改」创建新版本，普通变量、存储绑定和
Secret 引用一起保存；保存不会改变当前运行。

选择配置后点击「发布并生效」，复用当前代码制品并确认发布。恢复旧配置也使用当前代码；
要连同代码回滚，请使用「版本与部署」。首次部署需先完成一次构建，再选择该构建发布。
当前生效标记会在运行时确认后更新。非活动配置历史保留 30 天，当前和发布中的配置继续保留。

「环境管理 → 创建预览环境 → 新建预览默认配置」只决定之后新建 Preview 的初始值，已有环境各自独立。
这些控制台操作保持 Cloudflare Version、Deployment 和 Previews Base API 兼容。

---
title: 存储 · 对象存储
description: 了解对象存储兼容的 Cloudflare R2 binding 方法和使用限制。
---

krun 对象存储提供 Cloudflare R2 binding 的常用 API 子集，供 Workers 通过 `env` 调用。

**仅兼容本页列出的 API 及用法，很多 Cloudflare R2 API 和参数不支持。** 不要假设未列出的能力可用。

## R2 桶

使用 Wrangler 发布时，可以在 `wrangler.jsonc` 配置：

```jsonc
{
  "r2_buckets": [
    { "binding": "BUCKET", "bucket_name": "my-bucket" }
  ]
}
```

桶需先在当前组织中创建并启用。控制台仓库构建不会自动从配置文件修改环境中的桶绑定；使用仓库构建时，还需在控制台环境配置中绑定桶，然后发布。

| 方法 | 可用行为 |
| --- | --- |
| `head(key)` | 返回对象元数据；对象不存在时返回 `null` |
| `get(key, options)` | 返回对象及 body；不存在时返回 `null`，条件不满足时可能只返回元数据 |
| `put(key, value, options)` | 写入字符串、`ArrayBuffer`、typed array、`Blob`、`null` 或 `ReadableStream`；条件写入不满足时可返回 `null` |
| `delete(key)` / `delete(keys)` | 删除一个或多个对象；数组最多 1000 项，批量删除不保证原子性 |
| `list(options)` | 支持 `prefix`、`delimiter`、`limit`、`include`、`cursor`；`delimiter` 仅支持空值或 `/`，每页最多 64 项 |
| `createMultipartUpload(key, options)` | 创建分片上传，返回上传对象 |
| `resumeMultipartUpload(key, uploadId)` | 同步恢复分片上传，返回上传对象 |
| 上传对象的 `uploadPart()`、`complete()`、`abort()` | 上传分片、完成或取消上传；`complete()` 的分片清单须按编号升序 |

`get()` 返回的 body 可通过 `body` 流、`arrayBuffer()`、`text()`、`json()` 或 `blob()` 读取；对象支持 `writeHttpMetadata(headers)`。读取前应判断对象是否存在，以及条件请求是否返回 body。

```js
export default {
  async fetch(request, env) {
    const object = await env.BUCKET.get("hello.txt");
    if (!object || !object.body) {
      return new Response("Not found", { status: 404 });
    }

    const headers = new Headers();
    object.writeHttpMetadata(headers);
    headers.set("etag", object.httpEtag);
    return new Response(object.body, { headers });
  },
};
```

分页时根据 `truncated` 和返回的 `cursor` 继续请求，不要假设一次列出所有对象。Range 支持单段 offset/length、suffix 或单段 HTTP bytes range；条件读取支持单个 ETag 或日期条件，条件写入支持 If-Match 或 If-None-Match `*`。不要使用 `startAfter`、SSE-C、PUT checksum、组合条件或其他条件写入；不要依赖 `checksums.md5`。key 不使用点路径段或反斜线。

分片编号为 1–10000；除最后一片外，各片必须等大且至少 5 MiB，单片最多 5 GiB。`uploadId` 是 krun 的 opaque token，不能与原生 S3 multipart upload ID 混用。对象需通过 krun R2 binding 写入；直接通过 S3 写入且缺少平台元数据的对象不能作为兼容对象读取。

## 官方使用参考

**以下官方文档用于理解绑定关系和代码写法，krun 仅支持本页明确列出的 API、参数及用法。** 不要将 Cloudflare 的完整 R2 能力或部署流程直接套用到 krun。

| 内容 | 开发者阅读 | LLM 读取官方 Markdown |
| --- | --- | --- |
| binding、资源与 `env` 的关系 | [Bindings](https://developers.cloudflare.com/workers/runtime-apis/bindings/#what-is-a-binding) | [Bindings 原文](https://developers.cloudflare.com/workers/runtime-apis/bindings/index.md) |
| 桶绑定、`binding` / `bucket_name` 和对象读写 | [Use R2 from Workers](https://developers.cloudflare.com/r2/api/workers/workers-api-usage/) | [使用指南原文](https://developers.cloudflare.com/r2/api/workers/workers-api-usage/index.md) |
| R2 方法的参数和返回值 | [Workers API reference](https://developers.cloudflare.com/r2/api/workers/workers-api-reference/) | [API 参考原文](https://developers.cloudflare.com/r2/api/workers/workers-api-reference/index.md) |

Agent 先读取本页确认可用范围，再按任务读取上面的官方 Markdown；无需读取 Cloudflare 全量 `llms.txt`。完整的开发者阅读列表和可复制 Agent 指令见 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html)。

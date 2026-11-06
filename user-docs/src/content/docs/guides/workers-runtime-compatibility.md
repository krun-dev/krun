---
title: Workers 代码兼容范围
description: 面向开发者和编码 Agent 的 krun Workers API 使用范围。
sidebar:
  order: 3
---

编写代码时，以各组件文档明确列出的能力及其限制为准：

- [计算 · Workers](/docs/guides/component-workers.html)：请求入口、变量、Secret、静态资产和 Cache API。
- [存储 · 对象存储](/docs/guides/component-object-storage.html)：R2 binding、对象读写、列表和分片上传。

## 交给编码 Agent 的约束

将上述两篇组件文档提供给 Agent，并附上以下指令：

> 请为 krun 编写模块格式的 JavaScript 或 TypeScript Worker。只使用两篇组件文档明确列出的 binding、方法和参数范围；先检查项目实际配置的 binding 名称。R2 列表必须处理分页，对象读取必须处理 `null` 和无 body，Cache miss 必须能回退。不要使用文档未列出的 Cloudflare 产品或依赖公网出站请求；需要其他能力时先说明需求，不要假设平台支持。

运行时使用固定的 workerd 版本和项目声明的 compatibility date/flags；支持的 API 子集不代表整个 Cloudflare 平台完全兼容。完成后通过 [Wrangler 部署流程](/docs/guides/cloudflare-workers.html) 构建并在目标环境验证。

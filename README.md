<p align="center">
  <img src=".github/brand/krun-symbol.png" alt="krun" width="112" height="112" />
</p>

# krun

[English](README.en.md) · 简体中文

krun 是一个自托管应用平台，目标是在自己的基础设施上提供类似 Cloudflare Workers、D1 和 R2 的计算与存储能力，并兼容其中部分 API。在这些基础能力之上，krun 希望支持多种类型的应用运行时，让团队能够部署和管理不同形态的应用。

项目尚未上线，目前支持 Workers 运行与管理、对象存储、PostgreSQL 托管接入，以及部分 Cloudflare Workers API 和 Wrangler 工作流。更多类型的应用运行时仍是项目目标。管理 API 和 CLI 的支持范围见 [Cloudflare 协议适配](docs/cloudflare-api.md)。

## 当前能力

- 使用控制台创建 Worker、构建和发布版本，管理环境、域名与资源池。
- 使用部分兼容的 Cloudflare API 和 Wrangler 工作流部署 Workers。
- 使用 krun CLI 或原版 Wrangler 进行双版本百分比灰度、晋级和回滚。 [krun CLI 指南](user-docs/src/content/docs/guides/krun-cli.md#双版本灰度)
- 为 Worker 绑定基于 S3 兼容存储的逻辑桶，并在控制台管理对象。
- 通过组织、成员、角色和 API Key 管理访问权限，使用 CLI 管理身份和组织。

## Workers bindings 兼容范围

krun 基于 workerd 运行模块格式的 JavaScript 和 TypeScript Worker。下表列出可用的 Cloudflare Workers bindings 及其主要限制。

| Binding | 兼容范围 |
| --- | --- |
| 环境变量（`plain_text`、`json`） | 通过 `env` 读取字符串和 JSON 值。值随版本冻结，Wrangler 支持 `--keep-vars`。 |
| Secret（`secret_text`） | 通过 `env` 读取 Secret 字符串，支持控制台和 Wrangler 管理。管理接口只返回脱敏元数据。 |
| 版本元数据（`version_metadata`） | 通过指定的 `env` binding 读取 `id`、`tag`、`timestamp`。 |
| 静态资产（`assets`） | 支持 `env.ASSETS.fetch(request)`（名称可配置）、纯静态部署和常用路由模式，包括 SPA 回退。重写限站内路径。 [资产配置](user-docs/src/content/docs/guides/wrangler-configuration.md#静态资产配置) |
| R2（`r2_bucket`） | 支持 S3 兼容逻辑桶的 `head`、`get`、`put`、`delete`、`list` 和分片上传，覆盖常用 Range 和条件请求。每页最多 64 项，批量删除最多 1000 项。`startAfter`、SSE-C、PUT checksum 和组合条件超出兼容范围。 [R2 方法与限制](docs/workers-r2.md#已实现的-runtime-api) |
| Hyperdrive（`hyperdrive`） | 支持 PostgreSQL 配置管理和 `env.DB.connectionString`（名称可配置）。Worker 通过 workerd 按池出网规则直连 PostgreSQL。兼容范围涵盖配置和绑定，查询缓存与平台连接池超出范围。 [PostgreSQL 托管接入](user-docs/src/content/docs/guides/workers-hyperdrive.md) |
| 服务绑定（`service`） | 通过 `env.BINDING.fetch(request)` 调用同一组织内 Worker 的默认 HTTP 入口，支持同池和跨池。Wrangler `services` 跟随目标发布，krun 原生配置可固定目标版本。 [Cloudflare 协议适配](docs/cloudflare-api.md#服务绑定) |

KV、D1、Durable Objects 和 Queues 目前未实现。全局 Cache API 提供 `caches.default`、`caches.open()` 和 `put`、`match`、`delete`，缓存只在单个运行时进程内生效。 [Cache API](docs/workers-r2.md#cache-api)

控制台从仓库构建时，在 krun 的目标环境里配置变量、Secret、版本元数据、R2 和 Hyperdrive。服务绑定可通过环境 API 配置。静态资产随构建产物发布。Wrangler 直接部署支持在上传时提交配置中的绑定，Secret 通过 `wrangler secret put` 管理。 [Wrangler 配置兼容范围](user-docs/src/content/docs/guides/wrangler-configuration.md)

各 binding 的测试用例和验证环境见 [Workers bindings 兼容测试表](docs/verification.md#workers-bindings-兼容测试)。五个真实 R2 项目的测试场景见 [R2 开源项目验证](docs/workers-r2.md#开源项目验证)。

## 架构与部署

krun 由 React + Vite 静态控制台、Go 控制面和基于 workerd 的 Workers 运行时组成。控制台构建后作为静态文件托管；生产环境需要在同一 origin 下将 `/api/*` 转发给 Go 控制面。部署要求见 [部署文档](docs/deployment.md)。

本地开发采用 local-first 拓扑：控制面、PostgreSQL、builder 和前端运行在开发机上，集群运行 serving runtime、独立 validator 和 kgateway。配置和启动步骤见 [本地开发](docs/local-dev.md)。

## 文档

- [用户指南与 Quick Start](user-docs/src/content/docs/index.md)
- [开发与架构文档索引](docs/README.md)
- [Worker 示例](examples/workers-fullstack/README.md)

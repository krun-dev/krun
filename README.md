<p align="center">
  <img src=".github/brand/krun-symbol.png" alt="krun" width="112" height="112" />
</p>

# krun

krun 是一个自托管应用平台，目标是在自己的基础设施上提供类似 Cloudflare Workers、D1 和 R2 的计算与存储能力，并兼容其中部分 API。在这些基础能力之上，krun 希望支持多种类型的应用运行时，让团队能够部署和管理不同形态的应用。

项目尚未上线，目前主要实现 Workers 运行与管理、R2 兼容的对象存储绑定，以及部分 Cloudflare Workers API 和 Wrangler 工作流。D1 和更多类型的应用运行时是项目目标，尚未实现；krun 也不承诺完整兼容 Cloudflare 的产品或 API。具体支持范围见 [Cloudflare 协议适配](docs/cloudflare-api.md)和 [R2 binding](docs/workers-r2.md)。

## 当前能力

- 使用控制台创建 Worker、构建和发布版本，管理环境、域名与资源池。
- 使用部分兼容的 Cloudflare API 和 Wrangler 工作流部署 Workers。
- 为 Worker 绑定基于 S3 兼容存储的逻辑桶，并在控制台管理对象。
- 通过组织、成员、角色和 API Key 管理访问权限，使用 CLI 管理身份和组织。

## 架构与部署

krun 由 React + Vite 静态控制台、Go 控制面和基于 workerd 的 Workers 运行时组成。控制台构建后作为静态文件托管；生产环境需要在同一 origin 下将 `/api/*` 转发给 Go 控制面。部署要求见 [部署文档](docs/deployment.md)。

本地开发采用 local-first 拓扑：控制面、PostgreSQL、builder 和前端运行在开发机上，集群运行 serving runtime、独立 validator 和 kgateway。配置和启动步骤见 [本地开发](docs/local-dev.md)。

## 文档

- [用户指南与 Quick Start](user-docs/src/content/docs/index.md)
- [开发与架构文档索引](docs/README.md)
- [Worker 示例](examples/workers-fullstack/README.md)

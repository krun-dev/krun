# krun 当前基线

本目录仅记录已实现行为、运行契约与复现方式。主配置以
[config/example.yaml](../config/example.yaml) 为准，数据库以
[internal/storage/schema.sql](../internal/storage/schema.sql) 为唯一建表定义。
项目尚未上线；模型变化直接更新完整基线，不维护升级链或旧格式兼容。

面向用户的文档使用 Starlight，源码位于 [`user-docs/src/content/docs/`](../user-docs/src/content/docs/)。
`npm run build` 将文档静态构建并合并到控制台产物；`npm run docs:dev` 可单独预览
文档，完整的同源路由和 CLI 下载请使用构建产物预览。

Quick Start 位于 `index.md`，访问入口为 `/docs/`。新增文档放在 `guides/`，使用
Markdown 和 Starlight 的标题 frontmatter；在 `user-docs/astro.config.mjs` 配置导航，
章节目录、搜索和 `llms.txt` 自动生成。
文中 `{{KRUN_ORIGIN}}` 在浏览器显示和复制时替换为当前 origin，Markdown 原文保留
占位符。Go 和 OSS 部署时不需要文档服务进程。
| 内容 | 入口 |
| --- | --- |
| 代码分层与模块边界 | [架构](architecture.md) |
| 开发拓扑与启动 | [local-first](local-dev.md) |
| 静态控制台、同源 API 与部署 | [部署](deployment.md) |
| 公司 Kubernetes / kgateway 部署包 | [公司内网部署](../deploy/production/README.md) |
| 认证、授权与 CLI | [认证 API](auth-api.md) |
| 组织定向邀请与链接人数限制 | [组织邀请](org-invitation-links.md) |
| 身份提供商本地联调 | [SSO](sso-local-testing.md) |
| 存储、CAS、保留期与初始化 | [存储](storage.md) |
| S3 兼容对象存储与访问模式 | [对象存储](object-storage.md) |
| TOS STS / static 凭据 provider 与缓存 | [存储凭据](storage-credentials.md) |
| Workers R2 binding、逻辑桶与兼容范围 | [R2 binding](workers-r2.md) |
| 对象浏览、浏览器直传和自有 CDN 域名 | [文件与分发](bucket-objects.md) |
| Worker 构建、发布、回滚与内部 API | [后端](workers-backend.md) |
| 控制台操作 | [控制台](workers-console.md) |
| 资源池与 Network Profile | [资源池](workers-pools.md) |
| 域名、DNS/TLS 与共享路由 | [域名](workers-domains.md) |
| 仓库连接与构建 | [仓库](workers-repositories.md) |
| Cloudflare Workers API 与 Wrangler | [协议适配](cloudflare-api.md) |
| Wrangler 构建支持范围 | [构建兼容](workers-build-compatibility.md) |
| 静态资产与缓存 | [资产](workers-assets.md) |
| 自动化验证 | [验证](verification.md) |
| 规模边界与压测复现 | [规模](performance-scaling.md) |

开发和测试库需要显式重建时，先停止所有连接并确认它是可丢弃的专用数据库；启动器
不会自动清空数据库或对象存储。现有库 fingerprint 与源码基线不同会拒绝启动。

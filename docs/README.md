# krun 当前基线

本目录仅记录已实现行为、运行契约与复现方式。主配置以
[config/example.yaml](../config/example.yaml) 为准，数据库以
[internal/control/schema.go](../internal/control/schema.go) 为唯一建表定义。
项目尚未上线；模型变化直接更新完整基线，不维护升级链或旧格式兼容。

| 内容 | 入口 |
| --- | --- |
| 开发拓扑与启动 | [local-first](local-dev.md) |
| 静态控制台、同源 API 与部署 | [部署](deployment.md) |
| 认证、授权与 CLI | [认证 API](auth-api.md) |
| 身份提供商本地联调 | [SSO](sso-local-testing.md) |
| 存储、CAS、保留期与初始化 | [存储](storage.md) |
| Worker 构建、发布、回滚与内部 API | [后端](workers-backend.md) |
| 控制台操作 | [控制台](workers-console.md) |
| 资源池与 Network Profile | [资源池](workers-pools.md) |
| 域名、DNS/TLS 与共享路由 | [域名](workers-domains.md) |
| 仓库连接与构建 | [仓库](workers-repositories.md) |
| Wrangler 构建支持范围 | [构建兼容](workers-build-compatibility.md) |
| 静态资产与缓存 | [资产](workers-assets.md) |
| 自动化验证 | [验证](verification.md) |
| 规模边界与压测复现 | [规模](performance-scaling.md) |

开发和测试库需要显式重建时，先停止所有连接并确认它是可丢弃的专用数据库；启动器
不会自动清空数据库或对象存储。现有库 fingerprint 与源码基线不同会拒绝启动。

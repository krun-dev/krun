---
title: 示例总览与部署
description: 按 Workers + R2、PostgreSQL、Hono、Astro 和静态页面选择示例，构建并发布到 krun Workers。
---

[krun-examples](https://github.com/krun-dev/krun-examples) 提供可独立构建的参考项目。每个项目有自己的 Wrangler 配置和锁文件，可以在控制台选择项目目录构建并发布。这些示例均部署到 **krun Workers**；静态页面使用 assets-only 配置，由 Workers runtime 服务资产，无需用户编写 Worker 入口。

| 分类 | 示例与用途 | 项目目录 | 额外资源 |
| --- | --- | --- | --- |
| [Workers + R2](/docs/guides/examples-r2-workers.html) | 拾光照片墙、拾物文件工作台：Worker API 读写对象存储 | `examples/photo-wall`、`examples/object-storage` | 分别绑定 `PHOTOS`、`FILES` 对象桶 |
| [Workers + PG](/docs/guides/examples-postgres-todo.html) | 留白 · TODO：静态前端、Worker API 和 PostgreSQL 持久化 | `examples/postgres-todo` | PostgreSQL 和 Hyperdrive 绑定 `DB` |
| [Hono](/docs/guides/examples-hono.html) | 轻量页面和 JSON API | `examples/hono` | 无 |
| [Astro](/docs/guides/examples-astro.html) | 服务端渲染页面和 JSON API | `examples/astro` | 无 |
| [静态页面](/docs/guides/examples-static-site.html) | 一隅：HTML / CSS / SVG 小站，含移动端布局和 404 页面 | `examples/static-site` | 无 |

## 准备工作

选择有权限的组织，并确认自己可以创建 Worker、构建代码和发布目标环境。对象存储示例还需要创建和绑定桶的权限。仓库来源需要启用 GitHub；如无法选择 GitHub，请联系平台管理员。

## 在控制台构建并发布

1. 打开控制台，选择组织，进入 **Workers**，创建一个 Worker。选择适合业务的网络访问范围。
2. 在 Worker 的环境配置中绑定仓库：来源选择 GitHub，仓库地址填写 `https://github.com/krun-dev/krun-examples`，项目目录填写上表中所选示例的目录。分支通常填写 `main`，Workers + PG使用[示例专页](/docs/guides/examples-postgres-todo.html#绑定并发布)列出的分支。
3. 如果选择 Workers + R2，先按[分类文档](/docs/guides/examples-r2-workers.html#配置对象存储)创建桶并配置 R2 绑定；Workers + PG 按[专页](/docs/guides/examples-postgres-todo.html)创建临时库、建表并绑定 `DB`。Hono、Astro 和静态页面不需要额外资源绑定。
4. 在 **构建** 中点击 **构建新版本**，选择目标环境并开始构建。构建失败时查看构建日志。
5. 构建成功后，选择该构建和目标环境发布，等待发布实际生效。打开目标环境显示的访问地址验证；需要从当前网络能够访问该地址。

平台按项目目录中的 `wrangler.jsonc` 和锁文件安装依赖并构建；Astro 和 Object Studio 还会执行各自配置的构建命令。要修改示例，请先将仓库复制到自己的代码仓库，在绑定时填写自己的仓库地址和分支，再构建发布。

## 验证示例

每个分类页都列出源码、项目配置、资源要求与验证步骤。发布后使用目标环境显示的 Worker 地址，按照对应分类页验证页面、API 或文件读写。

如果控制台内调用不可用，可以在有权访问 Worker 的网络中直接打开环境地址；若地址也不可访问，请检查发布状态、域名绑定和网络访问范围。

## 无浏览器自动化部署（Agent / CI）

控制台仓库构建需要 GitHub 授权等浏览器交互；无人值守环境改用 krun CLI 管理资源、原版 Wrangler 发布代码，全程不需要浏览器：

1. 安装 [krun CLI](/docs/guides/krun-cli.html)，用 `krun login --token-stdin`（读入 API Key）或环境变量 `KRUN_ENDPOINT` + `KRUN_TOKEN` 完成非交互登录；`krun config set org ORG_ID` 选定组织。
2. 每个示例先 `krun workers create --name NAME` 创建 Worker 资源。
3. 按 [Wrangler 部署指南](/docs/guides/cloudflare-workers.html#创建可部署的-api-key) 创建策略覆盖部署动作的 API Key，配置 `CLOUDFLARE_API_BASE_URL`、`CLOUDFLARE_ACCOUNT_ID`、`CLOUDFLARE_API_TOKEN` 三个环境变量。
4. 克隆示例仓库，进入示例目录执行 `npm ci && npx wrangler@4.155.0 deploy`。
5. R2 示例部署前：`krun r2 bucket create BUCKET` 建桶，`krun workers environments update --worker NAME --env production --r2-bindings-file bindings.yaml` 绑定（文件格式为 `绑定名: 桶名` 的映射），并把该示例 `wrangler.jsonc` 里的 `bucket_name` 改成实际桶名——配置里的桶名若与平台桶不一致，Wrangler 会尝试自动建桶并失败。
6. Workers + PG 示例：把 `wrangler.jsonc` 中 `hyperdrive[0].id` 的全零占位值替换为实际配置 ID（保留占位值直接部署会上传无效绑定）。创建 Hyperdrive 配置可以用 `npx wrangler@4.155.0 hyperdrive create NAME --connection-string "$ORIGIN_DSN"`。

每篇示例与指南页都可以直接抓取 Markdown 原文：在页面 URL 后加 `.md.txt`。给编码 Agent 的完整入口清单见 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html#给编码-agent-的列表)。

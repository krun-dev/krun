---
title: 基础概念
description: 了解 krun 中用于团队协作、运行代码和管理资源的基本概念。
sidebar:
  order: 1
---

## 组织与权限

组织是团队协作和资源管理的边界。管理员将成员加入组织，并通过角色分配操作权限。创建组织、邀请成员和配置角色的步骤见[组织与权限指南](/docs/guides/organizations.html)。

## Workers

Worker 是运行应用代码的服务。可以在控制台中上传源码或连接仓库，完成构建后发布到目标环境。

krun 使用开源的 Cloudflare Workers 运行时 workerd 执行代码，并提供组织、权限、构建、发布和资源管理。Worker 代码通过 binding（绑定）使用获准的资源，例如用 `env.FILES` 访问配置好的对象存储桶。具体可用的 API 和 binding 见 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html)。

## Wrangler 和 krun CLI

[Wrangler](https://developers.cloudflare.com/workers/wrangler/) 是 Cloudflare 提供的命令行工具，用于开发、构建和部署 Workers。它读取项目中的 `wrangler.jsonc`、`wrangler.json` 或 `wrangler.toml`，了解代码入口、兼容日期等配置。通过配置 krun 的兼容 API 地址和 krun API Key，也可以使用已支持的 Wrangler 命令将项目发布到 krun；无需将代码发布到 Cloudflare。步骤见 [使用 Wrangler 部署 Workers](/docs/guides/cloudflare-workers.html)。

krun CLI 是操作 krun 自身的命令行工具，用于登录、管理组织和对象存储，以及构建、发布和调用 Worker。它使用 krun 控制台地址和登录凭据或 API Key；它不是执行 Worker 代码的运行时。用法见 [krun CLI 使用指南](/docs/guides/krun-cli.html)。

| 需要做什么 | 使用方式 |
| --- | --- |
| 在项目目录构建并直接发布已有 Wrangler 项目 | 配置 krun 兼容 API 地址和凭据后使用 `wrangler deploy`；支持范围见 [Wrangler 配置兼容范围](/docs/guides/wrangler-configuration.html) |
| 上传本地静态产物或 Worker 项目 | 在控制台拖入目录或 ZIP，也可运行 `krun deploy --path ./project --worker WORKER_ID --env production`。见 [部署本地项目](/docs/guides/deploy-local-project.html) |
| 从已绑定的 Git 仓库构建并发布到 krun 环境 | 使用 `krun deploy --worker WORKER_ID --env production`，也可在控制台操作 |
| 管理组织、对象存储和 krun 环境 | 使用 krun 控制台或 krun CLI |

用 Wrangler 直接部署时，Wrangler 在本地构建并上传。从控制台或 krun CLI 上传项目、从仓库构建时，krun 构建器读取项目配置，变量和资源绑定在目标 krun 环境设置。Wrangler 的 `--env` 会按 Wrangler 配置解析 Script 名称，krun CLI 的 `--env` 选择同一个 Worker 的目标环境。[配置兼容范围](/docs/guides/wrangler-configuration.html)

## 构建、环境与版本

构建把源码转换为可发布的产物。环境用于组织不同的运行目标。版本固定代码产物及创建时的环境变量、Secret 版本引用和存储绑定；部署将指定版本应用到环境。

通常直接发布成功构建即可，平台会同时创建版本。若需要提前固定内容、稍后上线，在 Worker 中选择目标环境，打开“构建”中对应构建的“更多构建操作”，选择“创建待部署版本”，核对配置并创建。

创建后会自动进入“版本与流量”，显示新版本的标签、创建时间、关联构建和冻结配置。创建版本不会改变线上流量，也不会生成访问地址。稍后在同一环境的“版本与流量”中选择“部署此版本”并确认，才会启动发布；部署使用创建时的配置，不受后续环境配置修改影响。

## 对象存储

对象存储用于管理文件。有相应权限时，可以在控制台浏览桶、上传和下载文件，也可以为 Worker 配置存储绑定。

## API Key

API Key 是用于自动化的凭据。它的权限受策略和创建者权限限制，请妥善保管，并在不再使用时撤销。

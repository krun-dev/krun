---
title: 部署本地项目
description: 从浏览器、krun CLI 或 Wrangler 发布本地静态站点和 Worker 项目，无需 Git 仓库。
---

静态站点和 Worker 项目都可以从本地目录发布到 krun Workers。三种入口共用 Worker、环境、版本历史和回滚。

| 入口 | 适用场景 |
| --- | --- |
| Workers 控制台 | 从浏览器拖入目录或 ZIP，查看识别和构建结果后发布 |
| krun CLI | 用一条命令上传目录或 ZIP，由 krun 构建并发布到指定环境 |
| 官方 Wrangler CLI | 使用已有的 Wrangler 项目或 CI，在本机打包并上传到 krun |

## 准备上传内容

- **静态产物**可以是已经构建好的 `dist/`、`out/` 或 `public/`。目录根部应有 `index.html`，并包含页面引用的 CSS、JavaScript 和图片。Vite 等项目先在本地执行构建，再上传产物目录。
- **Worker 项目**的根目录需要有 `wrangler.jsonc`、`wrangler.json` 或 `wrangler.toml`。配置可以声明 JS/TS 入口 `main`、静态资产 `assets`，或两者。项目有依赖时一并上传 `package.json` 和 npm、pnpm、Yarn 或 Bun 锁文件。环境变量、Secret 和资源绑定在目标 krun 环境设置。[Wrangler 配置兼容范围](/docs/guides/wrangler-configuration.html)

krun 优先检查 Wrangler 配置，其次识别静态产物。ZIP 可以直接包含项目文件，也可以包一层目录。若提示存在多个项目，填写相对于上传目录或 ZIP 根部的项目目录，例如 `repo/apps/site`。

如果单页应用需要在直接访问子路由时返回首页，可在 Wrangler 配置中设置 `assets.not_found_handling = "single-page-application"`，并让 `assets.directory` 指向构建产物。[静态资产配置](/docs/guides/wrangler-configuration.html#静态资产配置)

请勿上传 `.env`、`.dev.vars` 或 `.npmrc` 等本地凭据文件。项目内文件名使用可打印 ASCII 字符。每个项目最多 4,096 个文件、总计 24 MiB、单个文件 8 MiB。ZIP 本身最多 32 MiB，整个 ZIP 解包后最多 64 MiB。静态资产最多 500 个文件、总计 20 MiB、单个文件 5 MiB。

## 在 Workers 控制台上传

1. 选择组织，打开 **Workers**，创建或打开要更新的 Worker。
2. 选择目标环境，进入 **构建与发布 → 部署项目 → 上传部署**，拖入目录或 ZIP，也可以使用文件选择按钮。
3. 查看识别出的类型和构建状态。构建成功后，点击 **发布**，核对目标环境并确认。
4. 等运行时确认后，打开该环境的访问地址。更新站点时重新上传，历史版本可在 **版本与部署** 中回滚。

上传和构建需要目标环境的构建权限，确认发布需要发布权限。

「部署项目」也提供 **Git 仓库** 入口，适合在 GitHub 或 GitLab 持续维护的项目，可以按分支、Tag 或提交构建。同一个 Worker 可同时使用 Git 仓库和本地上传。[Git 仓库集成](/docs/guides/git-repositories.html)

## 使用 krun CLI 上传并部署

先完成 [krun CLI 登录](/docs/guides/krun-cli.html)，并使用现有 Worker 的 ID，或者先创建一个 Worker。以下命令分别发布本地静态目录和 Worker 项目 ZIP。

```sh
krun deploy --org ORG_ID --worker STATIC_WORKER_ID --env production --path ./dist
krun deploy --org ORG_ID --worker APP_WORKER_ID --env production --path ./worker.zip
```

CLI 等待构建成功并提交发布，默认继续等待运行时确认。使用 `--wait=false` 可在提交后返回。ZIP 包含多个项目时，可以加 `--project-root repo/apps/site`。构建失败时，用命令输出的 Build ID 查看日志。[构建并部署 Worker](/docs/guides/krun-cli.html#构建并部署-worker)

## 使用官方 Wrangler CLI

按 Wrangler 部署指南配置 krun 兼容 API 地址、组织和凭据，然后在带 Wrangler 配置的项目目录运行 `npx wrangler@4.155.0 deploy`。已有纯静态目录时，也可以用 `--assets` 指定产物目录。

```sh
npx wrangler@4.155.0 deploy --assets ./dist --name my-static-site --compatibility-date 2026-09-28
```

`--name` 是组织内的 Worker 定位符。命令会将产物上传到 krun，发布结果可以在控制台查看。[配置 Wrangler 并部署](/docs/guides/cloudflare-workers.html)

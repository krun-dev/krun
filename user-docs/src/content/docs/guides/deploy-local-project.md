---
title: 上传部署
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
2. 选择目标环境，进入 **构建与发布 → 部署项目 → 上传部署**。将目录或 ZIP 拖进虚线框，也可以点击 **选择文件夹** 或 **选择 ZIP**。

   [![构建与发布页的部署项目区，拖入文件时右侧上传部署卡片以绿色虚线高亮](/docs/screenshots/local-upload/upload-entry.png)](/docs/screenshots/local-upload/upload-entry.png)

3. 查看识别出的项目类型和构建状态。图中的 `upload-demo-site.zip` 被识别为「静态网站」。看到「构建成功」后，点击 **发布构建版本**。

   [![上传部署卡片显示 ZIP 文件名、静态网站类型、构建成功状态和发布构建版本按钮](/docs/screenshots/local-upload/upload-build.png)](/docs/screenshots/local-upload/upload-build.png)

4. 在弹窗核对目标环境，点击 **确认发布**。

   [![发布构建版本弹窗选中 production，底部是确认发布按钮](/docs/screenshots/local-upload/upload-publish.png)](/docs/screenshots/local-upload/upload-publish.png)

5. 在 **版本与部署** 确认新版本生效，再打开该环境的访问地址。更新站点时重新上传，历史版本可在 **版本与部署** 中回滚。

上传和构建需要目标环境的构建权限，确认发布需要发布权限。

## 使用 krun CLI 上传并部署

先[安装 krun CLI](/docs/guides/krun-cli.html#安装)，[配置控制台地址](/docs/guides/krun-cli.html#配置-endpoint)并[登录](/docs/guides/krun-cli.html#登录和退出)。CI 使用 [API Key 登录](/docs/guides/krun-cli.html#自动化与非交互登录)。按 [API Key 策略示例](/docs/guides/krun-cli.html#管理-api-key)将权限限定到目标 Worker，并授予 `worker.read`、`build.start`、`worker.version.read` 和 `worker.publish`。

把 `ORG_ID` 换成 `krun org list` 返回的组织 ID，把 `WORKER_ID` 换成 `krun workers list --org "$ORG_ID"` 返回的 Worker ID。以下两条命令按本地内容任选一条，并将 `./dist` 或 `./worker.zip` 换成实际路径。

```sh
ORG_ID=replace-with-organization-id
WORKER_ID=replace-with-worker-id

krun deploy --org "$ORG_ID" --worker "$WORKER_ID" --env production --path ./dist
krun deploy --org "$ORG_ID" --worker "$WORKER_ID" --env production --path ./worker.zip
```

CLI 等待构建成功并提交发布，默认继续等待运行时确认。使用 `--wait=false` 可在提交后返回。ZIP 包含多个项目时，可以加 `--project-root repo/apps/site`。构建失败时，用命令输出的 Build ID 查看日志。[构建并部署 Worker](/docs/guides/krun-cli.html#构建并部署-worker)

## 使用官方 Wrangler CLI

按 Wrangler 部署指南配置 krun 兼容 API 地址、组织和凭据，然后在带 Wrangler 配置的项目目录运行 `npx wrangler@4.155.0 deploy`。已有纯静态目录时，也可以用 `--assets` 指定产物目录。

```sh
npx wrangler@4.155.0 deploy --assets ./dist --name my-static-site --compatibility-date 2026-09-28
```

`--name` 是组织内的 Worker 定位符。命令会将产物上传到 krun，发布结果可以在控制台查看。[配置 Wrangler 并部署](/docs/guides/cloudflare-workers.html)

## 上传问题

| 提示 | 原因 | 处理 |
| --- | --- | --- |
| HTTP 422 `LOCAL_PROJECT_ROOT_REQUIRED`，消息以 `Multiple projects found.` 开头 | 上传内容包含多个候选项目目录 | 在页面上填写相对于上传根部的项目目录，或在 CLI 命令中加 `--project-root` |
| HTTP 422 `LOCAL_PROJECT_INVALID`，消息为 `The project directory must contain a Wrangler configuration or a built static index.html` | 所选目录没有 Wrangler 配置，也没有构建好的首页 | 上传包含 `index.html` 的静态产物目录，或上传含 Wrangler 配置的项目根目录 |

## 验证发布

在目标环境确认新版本生效，并访问业务路由。[项目部署概览中的验证命令](/docs/guides/deploy-project.html#验证发布)

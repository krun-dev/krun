---
title: Quick Start
description: 从登录控制台到连接 CLI，开始使用 krun。
sidebar:
  order: 0
---

:::tip[准备好开始了吗？]
一个控制台，连接你的团队与资源。跟随下面的步骤，几分钟内完成设置。
:::

## krun 是什么

krun 是一个自托管应用平台，帮助团队在自己的基础设施上部署和管理应用。你可以通过控制台或 CLI 管理组织和权限，构建、发布 Worker，并为应用配置对象存储和 PostgreSQL 等资源。

krun 的 Workers 运行时基于 [Cloudflare 开源的 workerd](https://github.com/cloudflare/workerd)：workerd 负责执行 Worker 代码，krun 提供构建、发布、环境、资源和权限管理。krun 支持部分 Cloudflare Workers API 与 Wrangler 工作流；具体支持范围见 [Workers 代码兼容范围](/docs/guides/workers-runtime-compatibility.html)和 [Wrangler 配置兼容范围](/docs/guides/wrangler-configuration.html)。

## 我们的目标

我们的目标是让团队在自己的基础设施上获得类似 Cloudflare Workers 的应用开发和交付体验：编写应用、连接资源、构建发布，再通过统一入口管理环境和访问权限。长期来看，krun 希望支持更多类型的应用运行时和计算、存储能力。当前提供的能力以本文档列出的功能和兼容范围为准。

## 开始之前

准备好团队提供的 krun 控制台地址和登录方式。登录后，如果没有看到组织或需要的资源，请联系组织管理员邀请你并分配权限。创建组织、加入团队和分配权限的操作见[组织与权限](/docs/guides/organizations.html)。

## 1. 登录控制台

打开 [krun 控制台](/)，使用团队配置的身份来源登录。在控制台中选择组织；组织是团队管理成员、权限和资源的边界。

## 2. 安装 CLI

macOS 和 Linux 安装需要 curl 和 Python 3。运行以下命令，自动识别系统和架构并安装到 `~/.local/bin/krun`：

```sh
(
  set -eu
  installer=$(mktemp)
  trap 'rm -f "$installer"' EXIT
  status=$(curl -q -fsS -o "$installer" -w '%{http_code}' "{{KRUN_ORIGIN}}/install.sh")
  [ "$status" = 200 ]
  sh "$installer" "{{KRUN_ORIGIN}}"
)
```

如提示 `~/.local/bin` 不在 PATH 中，请按提示配置。详细用法见 [命令行工具](/docs/guides/krun-cli.html)。也可以手动下载对应版本：

- [macOS Apple Silicon](/downloads/krun-darwin-arm64)
- [macOS Intel](/downloads/krun-darwin-amd64)
- [Linux x86-64](/downloads/krun-linux-amd64)
- [Linux ARM64](/downloads/krun-linux-arm64)
- [Windows x86-64](/downloads/krun-windows-amd64.exe)
- [Windows ARM64](/downloads/krun-windows-arm64.exe)

手动下载后，将文件设为可执行并放到 PATH 中；Windows 用户可在 PowerShell 中运行下载的 `krun-windows-amd64.exe help`。

安装后可用 `krun --version` 查看版本，运行 `krun upgrade --endpoint {{KRUN_ORIGIN}}` 升级到当前控制台发布的 CLI。

## 3. 连接你的账号

```sh
krun config set endpoint {{KRUN_ORIGIN}}
krun login
krun whoami
krun org list
```

保存 endpoint 后，登录、升级和资源命令都会使用该地址。可用 `krun config show` 查看配置；临时连接其他控制台时使用 `--endpoint URL`。

在浏览器中完成登录并批准 CLI 访问。`whoami` 查看当前身份，`org list` 查看你可以访问的组织。CLI 会保存登录信息；使用 `krun logout` 退出。

在远程终端或无法自动打开浏览器时，可以使用设备授权：

```sh
krun login --endpoint {{KRUN_ORIGIN}} --device --browser=false
```

按终端提示在浏览器中完成授权。CLI 接受 HTTPS 控制台地址；本地开发允许 loopback HTTP 地址。

## 4. 认识 krun

- **组织**：团队协作和资源管理的边界。成员的角色决定他们可以执行的操作。
- **Workers**：运行应用代码的服务。在控制台的 Workers 页面创建 Worker，上传源码或连接仓库，构建并发布到环境中。
- **构建与版本**：构建生成可发布的产物；发布将构建应用到目标环境，版本记录发布结果。
- **对象存储**：在有权限的情况下浏览桶、上传和下载文件，也可为 Worker 配置存储绑定。
- **API Key**：为自动化场景创建的凭据，权限受其策略和创建者权限限制。请妥善保管。

## 接下来

要发布 Worker，可以从 Git 仓库构建，也可以上传本地项目目录或 ZIP。[部署项目](/docs/guides/deploy-project.html)

不熟悉 Workers、Wrangler 或 krun CLI？先阅读[基础概念](/docs/guides/concepts.html)，了解工具的作用和发布路径。

从[示例总览](/docs/guides/worker-examples.html)选择一个项目，或直接查看 [Workers + R2](/docs/guides/examples-r2-workers.html)、[Workers + PG](/docs/guides/examples-postgres-todo.html)、[Hono](/docs/guides/examples-hono.html)、[Astro](/docs/guides/examples-astro.html)和[静态页面](/docs/guides/examples-static-site.html)的部署指南。

在控制台打开 Workers 或对象存储，探索你的组织有权使用的资源。运行 `krun --help` 查看 CLI 支持的命令；使用 `krun api` 可以调用受授权的 `/api/v1/` 接口。CLI 支持 Worker 构建、发布与回滚，以及组织权限和对象存储管理。

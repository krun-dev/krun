---
title: Quick Start
description: 从登录控制台到连接 CLI，开始使用 krun。
sidebar:
  order: 0
---

:::tip[准备好开始了吗？]
一个控制台，连接你的团队与资源。跟随下面的步骤，几分钟内完成设置。
:::

## 开始之前

准备好团队提供的 krun 控制台地址和登录方式。登录后，如果没有看到组织或需要的资源，请联系组织管理员邀请你并分配权限。

## 1. 登录控制台

打开 [krun 控制台](/)，使用团队配置的身份来源登录。在控制台中选择组织；组织是团队管理成员、权限和资源的边界。

## 2. 安装 CLI

macOS 和 Linux 可以运行以下命令，自动识别系统和架构并安装到 `~/.local/bin/krun`：

```sh
curl -fsSL {{KRUN_ORIGIN}}/install.sh | sh -s -- {{KRUN_ORIGIN}}
```

如提示 `~/.local/bin` 不在 PATH 中，请按提示配置。详细用法见 [命令行工具](/docs/guides/krun-cli.html)。也可以手动下载对应版本：

- [macOS Apple Silicon](/downloads/krun-darwin-arm64)
- [macOS Intel](/downloads/krun-darwin-amd64)
- [Linux x86-64](/downloads/krun-linux-amd64)
- [Linux ARM64](/downloads/krun-linux-arm64)
- [Windows x86-64](/downloads/krun-windows-amd64.exe)
- [Windows ARM64](/downloads/krun-windows-arm64.exe)

手动下载后，将文件设为可执行并放到 PATH 中；Windows 用户可在 PowerShell 中运行下载的 `krun-windows-amd64.exe help`。

安装后可用 `krun version` 查看版本，运行 `krun upgrade --endpoint {{KRUN_ORIGIN}}` 升级到当前控制台发布的 CLI。

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

在控制台打开 Workers 或对象存储，探索你的组织有权使用的资源。运行 `krun help` 查看 CLI 支持的命令；使用 `krun api` 可以调用受授权的 `/api/v1/` 接口。CLI 主要用于身份、组织和权限管理；Worker 的构建与发布可在控制台完成。

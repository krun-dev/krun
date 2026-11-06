---
title: krun CLI 使用指南
description: 安装和使用 krun 命令行工具，管理身份、组织与资源。
---

krun CLI 用于登录控制面、查看组织和管理有权限的资源。下面的地址会使用当前控制台地址。

## 安装

在 macOS 或 Linux 终端运行：

```sh
curl -fsSL {{KRUN_ORIGIN}}/install.sh | sh -s -- {{KRUN_ORIGIN}}
```

脚本自动识别 Intel/AMD 64 位和 ARM64，无需 root，安装到 `~/.local/bin/krun`；再次运行会更新该文件。脚本不会修改 shell 配置。如果安装目录不在 PATH 中，按提示运行下面的命令，即可在当前终端使用：

```sh
export PATH="$HOME/.local/bin:$PATH"
```

要让后续终端也能使用，将同一行加入 `~/.zshrc`（zsh）或 `~/.bashrc`（bash），然后重新打开终端。运行 `krun help` 检查安装。Windows 请从 [Quick Start](/docs/) 手动下载对应版本。

## 登录和退出

```sh
krun login --server {{KRUN_ORIGIN}}
krun whoami
krun logout
```

登录时在浏览器中完成授权；CLI 会保存登录信息并自动刷新授权。退出会撤销当前授权。

远程终端可以使用设备授权，按终端提示在浏览器完成登录：

```sh
krun login --server {{KRUN_ORIGIN}} --device --browser=false
```

CLI 接受 HTTPS 控制台地址，本地开发允许 loopback HTTP。可用 `--config FILE` 指定独立配置文件。

## 查看组织和资源

```sh
krun org list
krun members list --org ORGANIZATION_ID
krun groups list --org ORGANIZATION_ID
krun roles list --org ORGANIZATION_ID
krun keys list --org ORGANIZATION_ID
```

将 `ORGANIZATION_ID` 替换为 `krun org list` 输出中的组织 ID。还可以使用 `krun org create --name NAME`、`krun groups create --org ORGANIZATION_ID --name NAME` 等命令；操作受当前身份和组织权限限制。

查看审计记录：

```sh
krun audit --org ORGANIZATION_ID --limit 50
```

## 调用 API

对于尚无专用命令的操作，可调用有权限的 `/api/v1/` 接口：

```sh
krun api --method GET --path /api/v1/orgs
```

需要请求体时，用 `--file FILE` 提供 YAML 或 JSON 文件。自动化可设置 `KRUN_SERVER` 和 `KRUN_TOKEN`；Token 使用 krun API Key，权限仍受 Key 策略和创建者权限限制。不要把凭据提交到仓库。

运行 `krun help` 查看完整命令和参数。Workers 构建与发布可在控制台完成，也可参考 [Wrangler 部署指南](/docs/guides/cloudflare-workers.html)。

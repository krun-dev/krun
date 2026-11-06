---
title: krun CLI 使用指南
description: 安装、升级和配置 krun 命令行工具，管理身份、组织与资源。
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

## 配置 endpoint

保存控制台地址，后续命令无需重复传入 `--endpoint`：

```sh
krun config set endpoint {{KRUN_ORIGIN}}
krun config get endpoint
krun config show
krun login
```

endpoint 必须是控制台的 origin（如 `https://console.example.com`），不要加 `/api` 或页面路径。HTTPS 为必需，本地开发允许 `http://localhost:8080` 或 `http://127.0.0.1:8080`。`config show` 显示配置文件位置、已保存的 endpoint 和是否保存凭据，不输出 Token。

配置保存在系统用户配置目录的 `krun/config.yaml`，地址字段为 `endpoint`。可用 `--config FILE` 选择独立配置，例如 `krun config set endpoint {{KRUN_ORIGIN}} --config ./team.yaml`；后续操作同一配置时也要带上这个参数。

单次命令用 `--endpoint URL` 覆盖地址，也可用 `KRUN_ENDPOINT` 设置自动化环境的默认地址。优先级为：命令行参数 > `KRUN_ENDPOINT` > 保存的配置 > 本地默认地址 `http://127.0.0.1:8080`。`config get/show` 始终显示保存的配置。切换 endpoint 时会清除旧地址的登录凭据，需要重新登录；临时覆盖地址不会修改配置文件。

## 升级

```sh
krun version
krun upgrade --check
krun upgrade
```

升级会从配置的控制台下载当前系统和架构的已发布 CLI，支持 macOS、Linux、Windows 的 amd64/arm64。无需登录，也不会发送 API Token。`--check` 只检查，不替换文件；`krun --version` 也可查看当前版本。

未保存 endpoint 时，可直接运行：

```sh
krun upgrade --endpoint {{KRUN_ORIGIN}}
```

命令更新当前运行的可执行文件，保留配置与登录信息。下载完成并通过 SHA-256 校验后才替换文件；下载失败或校验失败会保留原文件。如果文件通过符号链接调用，更新的是链接目标。安装目录需要可写；Windows 可能保留一个 `.old` 备份，下次升级会清理，若提示被占用请先退出旧的 CLI 进程。

升级目标是**该控制台当前发布的版本**；切到较旧的控制台版本也会安装对应版本。旧 CLI 如果还没有 `upgrade` 命令，macOS/Linux 可重新运行上面的安装脚本，Windows 可重新下载后替换文件。

## 登录和退出

```sh
krun login --endpoint {{KRUN_ORIGIN}}
krun whoami
krun logout
```

登录时在浏览器中完成授权；CLI 会保存登录信息并自动刷新授权。退出会撤销当前授权。

远程终端可以使用设备授权，按终端提示在浏览器完成登录：

```sh
krun login --endpoint {{KRUN_ORIGIN}} --device --browser=false
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

需要请求体时，用 `--file FILE` 提供 YAML 或 JSON 文件。自动化可设置 `KRUN_ENDPOINT` 和 `KRUN_TOKEN`；Token 使用 krun API Key，权限仍受 Key 策略和创建者权限限制。不要把凭据提交到仓库。

运行 `krun help` 查看完整命令和参数。Workers 构建与发布可在控制台完成，也可参考 [Wrangler 部署指南](/docs/guides/cloudflare-workers.html)。

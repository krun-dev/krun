---
title: 项目部署概览
description: 选择 Git 仓库或上传部署，构建项目并发布到 krun Workers。
---

打开一个 Worker，进入 **构建 → 部署项目**。选择代码所在的位置，再把构建结果发布到目标环境。

| 部署方式 | 什么时候用 | 需要准备什么 |
| --- | --- | --- |
| [Git 仓库](/docs/guides/git-repositories.html) | 项目持续在 GitHub 或 GitLab 维护，需要按分支、Tag 或提交构建 | 已授权的仓库，项目目录中的 Wrangler 配置，以及依赖锁文件（如有） |
| [上传部署](/docs/guides/deploy-local-project.html) | 手头已经有静态站点产物或本地 Worker 项目，希望直接发布 | 含 `index.html` 的静态产物目录，或含 Wrangler 配置的 Worker 项目目录。两种目录都可打成 ZIP |

同一个 Worker 可以交替使用两种方式。两种方式共用环境、发布历史和回滚。上传部署无需授权或绑定 Git 仓库。[Markdown 原文](/docs/guides/deploy-project.md.txt)

## 验证发布

1. 在控制台 **版本与流量** 中确认目标环境显示新版本已生效。CLI 部署返回的 JSON 中，`state` 应为 `available`。
2. 检查部署状态。输出中的 `confirmed_version_id` 应等于本次部署的 `version_id`。
3. 调用业务路由。以下命令以 `/` 为例，换成你的应用预期返回 HTTP 200 的路由。静态首页可核对返回正文的标题。

先[安装 krun CLI](/docs/guides/krun-cli.html#安装)，[配置控制台地址](/docs/guides/krun-cli.html#配置-endpoint)并[登录](/docs/guides/krun-cli.html#登录和退出)。无人值守环境使用 [API Key 登录](/docs/guides/krun-cli.html#自动化与非交互登录)。把 `ORG_ID` 换成 `krun org list` 返回的组织 ID，把 `WORKER_ID` 换成 `krun workers list --org "$ORG_ID"` 返回的 Worker ID。验证预览环境时，把 `TARGET_ENV` 改成对应的预览环境名。

```sh
ORG_ID=replace-with-organization-id
WORKER_ID=replace-with-worker-id
TARGET_ENV=production

krun deployments status --org "$ORG_ID" --worker "$WORKER_ID" --env "$TARGET_ENV"
krun workers invoke --org "$ORG_ID" --worker "$WORKER_ID" --env "$TARGET_ENV" --path /
```

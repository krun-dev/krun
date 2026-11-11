---
title: 部署项目
description: 选择 Git 仓库或上传部署，构建项目并发布到 krun Workers。
---

打开一个 Worker，进入 **构建与发布 → 部署项目**。选择代码所在的位置，再把构建结果发布到目标环境。

| 部署方式 | 什么时候用 | 需要准备什么 |
| --- | --- | --- |
| **Git 仓库** | 项目持续在 GitHub 或 GitLab 维护，需要按分支、Tag 或提交构建 | 已授权的仓库，项目目录中的 Wrangler 配置，以及依赖锁文件（如有） |
| **上传部署** | 手头已经有静态站点产物或本地 Worker 项目，希望直接发布 | 含 `index.html` 的静态产物目录，或含 Wrangler 配置的 Worker 项目目录。两种目录都可打成 ZIP |

同一个 Worker 可以交替使用两种方式。两种方式共用环境、发布历史和回滚。上传部署无需授权或绑定 Git 仓库。[Markdown 原文](/docs/guides/deploy-project.md.txt)

## Git 仓库

1. 在 **部署项目 → Git 仓库** 点击 **绑定代码仓库**。在 **代码源** 选择平台、仓库和项目目录，保存配置。首次连接的授权步骤见 [Git 仓库集成](/docs/guides/git-repositories.html)。
2. 回到 **构建与发布 → 部署项目 → Git 仓库**，点击 **从仓库构建**。输入分支、Tag 或提交 SHA，使用 `HEAD` 时构建仓库默认分支。
3. 构建成功后，在构建记录中点击 **发布**，核对目标环境并确认。

若 **代码源** 显示「管理员尚未配置 Git 仓库来源」，请让管理员为当前组织配置 Git 平台，或直接使用 **上传部署**。已绑定仓库的自动化发布可以使用 [krun CLI](/docs/guides/krun-cli.html#已绑定仓库的-ci-发布)。

## 上传部署

1. 选好目标环境，在 **部署项目 → 上传部署** 拖入文件夹或 ZIP，也可以点击 **选择文件夹** 或 **选择 ZIP**。
2. 查看识别结果和构建状态。构建成功后点击 **发布**，确认目标环境。

静态框架项目先在本地生成 `dist/` 等产物目录。Worker 源码项目上传包含 `wrangler.jsonc`、`wrangler.json` 或 `wrangler.toml` 的项目目录。文件限制、ZIP 结构和 Wrangler CLI 示例见 [部署本地项目](/docs/guides/deploy-local-project.html)。

| 提示 | 原因 | 处理 |
| --- | --- | --- |
| HTTP 422 `LOCAL_PROJECT_ROOT_REQUIRED`，消息以 `Multiple projects found.` 开头 | 上传内容包含多个候选项目目录 | 在页面上填写相对于上传根部的项目目录，或在 CLI 命令中加 `--project-root` |
| HTTP 422 `LOCAL_PROJECT_INVALID`，消息为 `The project directory must contain a Wrangler configuration or a built static index.html` | 所选目录没有 Wrangler 配置，也没有构建好的首页 | 上传包含 `index.html` 的静态产物目录，或上传含 Wrangler 配置的项目根目录 |

## 用 krun CLI 部署

先[安装 krun CLI](/docs/guides/krun-cli.html#安装)并[配置控制台地址](/docs/guides/krun-cli.html#配置-endpoint)。无人值守环境使用 [API Key 登录](/docs/guides/krun-cli.html#自动化与非交互登录)，部署权限按[已绑定仓库的 CI 发布](/docs/guides/krun-cli.html#已绑定仓库的-ci-发布)限制到目标 Worker。

把 `ORG_ID` 换成 `krun org list` 返回的组织 ID，把 `WORKER_ID` 换成 `krun workers list --org "$ORG_ID"` 返回的 Worker ID。发布到预览环境时，把 `TARGET_ENV` 改成已创建的预览环境名。以下两条发布命令按项目来源任选一条。

```sh
ORG_ID=replace-with-organization-id
WORKER_ID=replace-with-worker-id
TARGET_ENV=production

# Git 仓库：使用已绑定仓库的默认 ref
krun deploy --org "$ORG_ID" --worker "$WORKER_ID" --env "$TARGET_ENV"

# 上传部署：将 ./dist 替换成实际目录或 ZIP 路径
krun deploy --org "$ORG_ID" --worker "$WORKER_ID" --env "$TARGET_ENV" --path ./dist
```

## 验证发布

1. 在控制台 **版本与部署** 中确认目标环境显示新版本已生效。CLI 部署返回的 JSON 中，`state` 应为 `available`。
2. 使用上节变量检查部署状态。输出中的 `confirmed_version_id` 应等于本次部署的 `version_id`。
3. 调用业务路由。以下命令以 `/` 为例，换成你的应用预期返回 HTTP 200 的路由。静态首页可核对返回正文的标题。

```sh
krun deployments status --org "$ORG_ID" --worker "$WORKER_ID" --env "$TARGET_ENV"
krun workers invoke --org "$ORG_ID" --worker "$WORKER_ID" --env "$TARGET_ENV" --path /
```

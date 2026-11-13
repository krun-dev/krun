---
title: Git 仓库
description: 连接 Git 平台并从仓库构建 Worker。
---

先按仓库所在平台连接账号。你也可以从 Worker 的 **代码源** 页发起连接，授权后会返回当前 Worker。

- [内部 GitLab](/docs/guides/gitlab-integration.html)
- [GitHub](/docs/guides/github-integration.html)

[![1 选择组织，2 进入 Git 仓库授权，3 连接 GitHub 或公司 GitLab](/docs/screenshots/git-repositories/personal-settings.png)](/docs/screenshots/git-repositories/personal-settings.png)

## 绑定仓库并构建

1. 在左侧打开 **Workers**，进入要绑定仓库的 Worker，再点 **代码源**。
2. 选择仓库平台，点 **选择已授权仓库**，选中目标仓库。
3. 填写 **项目目录**，点 **保存仓库配置**。Wrangler 配置在仓库根目录时保留 `.`，在子目录时填相对路径，例如 `apps/edge-api`。

[![1 打开代码源，2 选择已授权仓库，3 保存仓库配置](/docs/screenshots/git-repositories/worker-source.png)](/docs/screenshots/git-repositories/worker-source.png)

绑定后，进入 **构建 → 部署项目 → Git 仓库**，点击 **从仓库构建**，填写分支、Tag 或 Commit，再点击 **开始构建**。留空时使用绑定中的 Git ref。填写 `HEAD` 时使用仓库默认分支。构建成功后，在构建记录中点击 **发布** 并确认目标环境。

## 使用 krun CLI 部署

绑定仓库后，可以运行以下命令从默认 ref 构建并发布。先[安装 krun CLI](/docs/guides/krun-cli.html#安装)，[配置控制台地址](/docs/guides/krun-cli.html#配置-endpoint)并[登录](/docs/guides/krun-cli.html#登录和退出)。把 `ORG_ID` 换成 `krun org list` 返回的组织 ID，把 `WORKER_ID` 换成 `krun workers list --org "$ORG_ID"` 返回的 Worker ID。发布到预览环境时，将 `production` 换成已创建的预览环境名。

```sh
ORG_ID=replace-with-organization-id
WORKER_ID=replace-with-worker-id

krun deploy --org "$ORG_ID" --worker "$WORKER_ID" --env production
```

CI 使用 API Key 登录，权限可限制到目标 Worker。[已绑定仓库的 CI 发布](/docs/guides/krun-cli.html#已绑定仓库的-ci-发布)

## 未配置仓库来源

如果 **代码源** 显示「管理员尚未配置 Git 仓库来源。你仍可在「构建」上传本地项目。」，请让管理员为当前组织配置 Git 平台，或使用[上传部署](/docs/guides/deploy-local-project.html)。

## 授权失效

在 **个人设置 → Git 仓库授权** 点 **验证授权**。如果提示失效，点 **重新授权**。仍有仓库权限时，重新授权后可继续使用原绑定。

选库列表显示当前账号有权访问的仓库。绑定完成后，有该 Worker 构建权限的同事可以构建已绑定的仓库。授权人断开连接或失去仓库权限后，新的构建需要重新授权，或由有仓库权限的同事重新绑定。已有构建仍可发布或回滚。

## 验证发布

在目标环境确认新版本生效，并访问业务路由。[项目部署概览中的验证命令](/docs/guides/deploy-project.html#验证发布)

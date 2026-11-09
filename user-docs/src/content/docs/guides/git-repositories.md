---
title: Git 仓库集成
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

绑定后，点 Worker 页右上角的 **构建新版本**，填写分支、Tag 或 Commit，再点 **开始构建**。留空或使用 `HEAD` 会构建默认分支。

## 授权失效

在 **个人设置 → Git 仓库授权** 点 **验证授权**。如果提示失效，点 **重新授权**。仍有仓库权限时，重新授权后可继续使用原绑定。

选库列表显示当前账号有权访问的仓库。绑定完成后，有该 Worker 构建权限的同事可以构建已绑定的仓库。授权人断开连接或失去仓库权限后，新的构建需要重新授权，或由有仓库权限的同事重新绑定。已有构建仍可发布或回滚。

---
title: 内部 GitLab
description: 授权内部 GitLab 账号，选择私有仓库作为 Worker 的代码源。
---

## 连接内部 GitLab

1. 在 krun 左上角选择组织，打开 **个人设置 → Git 仓库授权**。
2. 在 **公司 GitLab** 卡片点 **连接账号**，登录 GitLab。
3. 在授权页确认读取仓库和 API 的权限，点 **Authorize**。回到 krun 后，公司 GitLab 卡片会显示 **已授权**。

<div class="krun-gitlab-consent-screenshot">
  <a href="/docs/screenshots/git-repositories/gitlab-consent.png">
    <img src="/docs/screenshots/git-repositories/gitlab-consent.png" alt="GitLab 授权确认页，标注 Authorize 按钮" />
    <span aria-hidden="true">1</span>
  </a>
</div>

[![回到 krun 后，公司 GitLab 卡片显示已授权](/docs/screenshots/git-repositories/gitlab-authorized.png)](/docs/screenshots/git-repositories/gitlab-authorized.png)

如果没看到公司 GitLab 卡片，检查当前组织，并联系管理员确认该实例是否已启用。内部 GitLab 和 GitLab.com 可以分别启用。

## 选择仓库

打开 Worker 的 **代码源** 页，将 **仓库平台** 设为 **公司 GitLab**，点 **选择已授权仓库**。如果找不到仓库，检查当前账号能否在内部 GitLab 打开该仓库。

[![krun 的公司 GitLab 选库列表，部分仓库名称已打码](/docs/screenshots/git-repositories/gitlab-repositories.png)](/docs/screenshots/git-repositories/gitlab-repositories.png)

选中仓库后，按[绑定仓库并构建](/docs/guides/git-repositories.html#绑定仓库并构建)保存配置并开始构建。

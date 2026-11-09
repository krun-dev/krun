---
title: GitHub
description: 连接 GitHub，检查 GitHub App 的安装范围，并选择 Worker 的代码仓库。
---

## 连接 GitHub

1. 在 krun 左上角选择组织，打开 **个人设置 → Git 仓库授权**。
2. 点 **连接 GitHub**。页面会跳到 GitHub，登录后按页面提示确认授权。
3. 回到 krun，确认 GitHub 卡片显示 **已授权**。

未登录 GitHub 时，会先看到 GitHub 的登录页。

[![GitHub 显示登录后继续到 KRun-dev](/docs/screenshots/git-repositories/github-sign-in.png)](/docs/screenshots/git-repositories/github-sign-in.png)

登录后，在 GitHub 授权页面点 **Authorize**。App 名称和 GitHub 账号以实际页面为准。

<div class="krun-consent-screenshot">
  <a href="/docs/screenshots/git-repositories/github-consent.png">
    <img src="/docs/screenshots/git-repositories/github-consent.png" alt="GitHub App 授权确认页，标注 Authorize 按钮" />
    <span aria-hidden="true">1</span>
  </a>
</div>

## 安装或管理 GitHub App

**连接 GitHub** 是你本人授权 krun 使用你的 GitHub 身份。**安装 GitHub App** 是在 GitHub 账号或组织中选择允许 App 访问的仓库。选库时，krun 只显示你的账号和 App 都有权访问的仓库。

首次使用尚未开放给 App 的仓库时，需要安装 App。选库窗口展开 **找不到仓库？**，点 **安装 GitHub App 或调整仓库范围**，在 GitHub 中选择仓库所属账号或组织。

1. 在 **Repository access** 中选择范围。**All repositories** 包含该账号当前和未来的仓库。**Only select repositories** 只开放选定的仓库，可选用于构建的仓库。下图当前选中的是 All repositories。
2. 确认 **Read access to code and metadata**，点 **Install**。组织仓库可能需要管理员批准。

<div class="krun-install-screenshot">
  <a href="/docs/screenshots/git-repositories/github-app-install.png">
    <img src="/docs/screenshots/git-repositories/github-app-install.png" alt="GitHub App 安装页面，标注仓库范围和 Install 按钮" />
    <span class="krun-install-scope" aria-hidden="true">1</span>
    <span class="krun-install-button" aria-hidden="true">2</span>
  </a>
</div>

App 已安装时，可在同一入口调整仓库范围并保存。回到 krun 点 **刷新仓库列表**，再选择仓库。

## 选择仓库

打开 Worker 的 **代码源** 页，将 **仓库平台** 设为 **GitHub**，点 **选择已授权仓库**。

如果找不到目标仓库，先确认你的 GitHub 账号能访问它，再检查该仓库是否在 App 的安装范围内。

[![krun 的 GitHub 选库窗口，展开后可调整 GitHub App 仓库范围并刷新列表](/docs/screenshots/git-repositories/github-repository-help.png)](/docs/screenshots/git-repositories/github-repository-help.png)

选中仓库后，按[绑定仓库并构建](/docs/guides/git-repositories.html#绑定仓库并构建)保存配置并开始构建。

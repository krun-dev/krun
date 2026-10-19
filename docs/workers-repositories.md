# Workers 服务列表与仓库构建

Workers 导航先展示当前组织中有权查看的服务列表。服务名称、关联仓库和可见环境状态来自后端；点击服务才进入构建 / 发布详情。返回服务列表和浏览器返回均可用，详情 URL 可以直接打开。创建服务后进入新服务详情，不再自动打开第一个已有服务。

“构建新版本”现在从 GitHub、GitLab.com 或管理员配置的自建 GitLab 拉取代码。用户填写仓库地址、分支 / Tag / Commit、项目目录和入口文件，保存为该服务的仓库配置，然后选择构建环境。后续打开会恢复已保存配置。历史构建展示仓库、请求的 ref 与实际 commit，发布与回滚继续选用已成功构建的不可变制品。

## 管理员配置

完整示例见 [workers.example.yaml](../config/workers.example.yaml)。仓库来源配置在 `workers.repository_providers`，与认证登录使用的顶层 `providers` 独立。

```yaml
workers:
  # 其余 Workers 运行、网关与 S3 配置见完整示例。
  repository_providers:
    - id: company-gitlab
      type: gitlab
      name: 公司 GitLab
      enabled: true
      priority: 10
      base_url: https://gitlab.example.internal
      token: ${KRUN_GITLAB_REPOSITORY_TOKEN:-}
      allowed_repositories: [team/worker-service]
      # organization_ids: [org_example]
    - id: github
      type: github
      name: GitHub
      enabled: true
      priority: 20
      base_url: https://github.com
    - id: gitlab
      type: gitlab
      name: GitLab.com
      enabled: false
      priority: 30
      base_url: https://gitlab.com
```

- `enabled` 必须显式为 true 才会展示与接受新仓库构建；未配置来源时页面显示尚未启用。
- `priority` 越小越靠前；相同值按 id 排序。新服务默认选择第一个启用来源。
- `type: gitlab` 同时支持 GitLab.com 和自建实例，`base_url` 必须为固定 HTTPS origin。GitHub 使用 github.com。
- `token` 可通过配置文件的环境变量展开注入；公开仓库可以省略。私有仓库使用管理员提供的只读凭证。配置了共享 Token 时，`allowed_repositories` 必须列出允许读取的完整 namespace/name，不支持通配符。
- `organization_ids` 可限定来源可用于哪些平台组织，省略时对有服务权限的组织用户开放。共享 Token 的访问范围由这些配置与服务权限共同限制。
- 修改后重启控制面生效。Token 不出现在来源列表、构建 API、日志、Kubernetes 构建 Job 输入或 workerd 中。页面会说明所选来源是否配置了凭证。

GitHub API 用 Bearer Token，GitLab 用 PRIVATE-TOKEN。GitHub 归档下载会跳转到 codeload.github.com，该请求不会携带 GitHub API 的 Token；其他跨来源跳转拒绝。接口依据 [GitHub 仓库归档 API](https://docs.github.com/en/rest/repos/contents#download-a-repository-archive-tar)、[GitLab Commit API](https://docs.gitlab.com/api/commits/#retrieve-a-commit) 和 [GitLab 归档 API](https://docs.gitlab.com/api/repositories/#get-file-archive) 实现。

## 后端流程

1. `PUT .../workers/repository` 使用 `service.update` 权限及 `expected_revision` 保存共享仓库配置。`GET .../workers/repository` 返回配置与修订号。
2. `POST .../workers/builds` 传 `source_type: repository`、`repository_revision`、构建环境及幂等键，要求目标环境 `build.start`。先检查仓库配置修订号，防止构建请求与另一个用户的配置修改串用。入队时复制服务的仓库配置，后续配置修改不影响该构建。
3. 控制器在数据库事务外请求配置平台的 API，将 ref 解析为完整 commit，并先持久化 commit。下载中断后的重试仍使用该 commit。
4. 下载该 commit 的 tar.gz，检查目录边界、文件类型、链接、UTF-8、大小与入口文件，持久化源码摘要和私有源码输入。仓库中任何脚本、hook 都不会在控制面执行。
5. 现有 Kubernetes Job 用固定 esbuild profile 构建这些源文件，上传至 S3；控制器验证制品后标记成功。Job 接收源码和临时上传 URL，不接收 Git 平台凭证。
6. 选历史制品发布到 test / staging / production，沿用候选验证 → Release 提交 → 副本确认流程。

`GET /api/v1/orgs/{org}/workers/services` 返回按用户环境权限过滤的列表；`GET /api/v1/orgs/{org}/workers/repository-providers` 返回当前组织启用的来源展示信息。旧源码上传构建 API 保留兼容，产品页面已使用仓库构建。

## 当前范围与网络

当前使用固定 `esbuild-esm-v1` profile，支持 JS / TS、JSON 与相对导入及已有 Cloudflare builtins；不执行 npm 安装、自定义 build command 或 Git submodule 拉取。项目目录内最多 32 个源码文件，总计 256 KiB；源码归档最多 16 MiB，解压最多 64 MiB、10,000 项。不改变原有发布语义。

kind 只为控制面增加已配置仓库 API / 下载域名解析所得 IP 的 TCP 443 出口；builder 仍只访问 MinIO，runtime 仍只访问平台代码接口。后端镜像安装系统 CA，TLS 校验保持启用。实验网络策略中的 IP 是部署时解析的快照，DNS 地址变化需要重新生成；生产环境应使用可持续管理域名访问的出口代理或网络策略。

本地 `npm run dev:kind` 继续连接 kind 后端。公司 GitLab URL 已放在 kind 配置首位；当前没有该实例私有仓库的凭证，不能将接口测试结果视为真实私有仓库验收。

## kind 与浏览器验收 · 2026-10-13

实际浏览器完成服务列表进入、侧边栏 Workers 返回列表、浏览器返回、新服务创建、来源选择、仓库配置保存、源码构建、日志和 MinIO 制品查看、发布 test，以及经 kgateway 调用 Worker。默认来源按配置选择公司 GitLab，重新打开表单能恢复已保存的 GitHub 仓库配置。返回列表后会重新获取仓库和环境状态。

- 服务：`github-repository-demo` / `service_RjTQNfLdyPFLgnF-`，组织为 Workers 体验。
- 仓库：`cloudflare/workers-sdk`，ref `main`，项目目录 `fixtures/worker-ts/src`，入口 `index.ts`。
- Commit：`97b2fb1632de1f02bb0d12639f8602cefb3de158`。
- 成功构建：`bld_U6NJm3nXeUXsyKVS`，编译 696 bytes，归档 645 bytes。
- 归档 SHA-256：`ba1af0c24152f808d4d761a897a528f458adb45e638b173ed6e13a337c1f4d78`，bucket `krun-artifacts`。
- test Deployment：`dep_a64372b5395c21729e28183a0dba06f9bf5d11e1e94a15647fcd008e2563b941`，发布已生效。
- 调用：HTTP 200，正文 `Hello World!`，响应 Pod UID `3b5cdbbd-c847-4e0b-9bba-7b9a6aacb73e`。

首次真实归档下载发现 GitHub 的全局 PAX metadata 被误认为归档根目录；已修复并增加回归测试，失败记录保留在历史中。九组新增后端回归测试覆盖来源排序与关闭、URL / 目录及仓库访问范围、仓库配置 CAS、幂等重试、commit 固定、GitLab 嵌套 namespace / ref、GitHub 重定向的凭证隔离、归档边界、列表环境权限和全局 PAX header。完整 `go test -race ./...`、`go vet ./...` 通过；前端类型 / 生产构建、4 项国际化与 5 项权限检查通过。

从 kind 控制面访问 `https://gitlab.example.internal/api/v4/version` 的 HTTPS 请求得到 HTTP 401，证明当前网络可达并要求认证。没有该实例 Token 和具体私有仓库的授权，当前仅完成 GitLab API 合约及凭证隔离测试，未声称真实私有仓库构建已验收。私有仓库接入需先在配置中填写允许的仓库，并向控制面注入只读 Token 环境变量。

最终 API 增加 `repository_revision` 后再次从保存配置发起构建并成功。该次 main 已移动到 `f3608290da40…`，新构建记录独立保存新 commit；已发布制品与上一构建的代码相同，因此复用相同内容哈希制品，已发布环境没有变化。

# Workers Workers 列表与仓库构建

Workers 导航先展示当前组织中有权查看的Workers 列表。服务名称、关联仓库和可见环境状态来自后端；点击服务才进入构建 / 发布详情。返回Workers 列表和浏览器返回均可用，详情 URL 可以直接打开。创建服务后进入新服务详情，不再自动打开第一个已有服务。

在 Worker 的「代码源」页签选择仓库平台和项目目录。授权后可打开「选择已授权仓库」，或填写仓库地址。发起仓库构建时选择分支、Tag 或 Commit。入口、静态资源与构建命令读取项目内的 Wrangler 配置。历史构建记录请求的 ref 与实际 commit。

「个人设置 → Git 仓库授权」展示当前用户在所选组织可用的仓库平台及自己的授权状态。页面加载和「刷新」只读取本地状态。「验证授权」、选择仓库和构建按需访问 Git 平台。过期但仍可续期的授权在使用时续期，并发请求共享现有刷新租约。无法续期或上游拒绝令牌时，页面提示重新授权。Git 平台暂时不可用时可以重试。

从授权列表选择仓库并保存后，绑定使用配置者的授权。有该 Worker 构建权限的成员可构建绑定的仓库，仓库选择器仍按当前用户的授权过滤。授权人断开或失去访问权后，依赖该授权的新构建需要重新授权或重新绑定。已生成的构建制品继续用于发布与回滚。

## 管理员配置

完整示例见 [config/example.yaml](../config/example.yaml) 的 `workers:` 段。仓库来源配置在 `workers.repository_providers`，与认证登录使用的顶层 `providers` 独立。

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
      allow_private_network: true
      token: ${KRUN_GITLAB_REPOSITORY_TOKEN:-}
      allowed_repositories: [team/worker-service]
      # organization_ids: [org-example]
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
- `type: gitlab` 同时支持 GitLab.com 和自建实例，`base_url` 必须为固定 HTTPS origin。GitHub 使用 github.com。OAuth endpoint 默认拒绝私网/loopback 解析；只有管理员确认目标是受信任内网实例后才可为该 GitLab 显式设置 `allow_private_network: true`，GitHub 禁止开启。
- `token` 可通过配置文件的环境变量展开注入；公开仓库可以省略。私有仓库使用管理员提供的只读凭证。配置了共享 Token 时，`allowed_repositories` 必须列出允许读取的完整 namespace/name，不支持通配符。
- `organization_ids` 可限定来源可用于哪些平台组织，省略时对有服务权限的组织用户开放。共享 Token 的访问范围由这些配置与服务权限共同限制。
- 修改后重启控制面生效。Token 不出现在来源列表、构建 API、日志、Kubernetes 构建 Job 输入或 workerd 中。页面会说明所选来源是否配置了凭证。

配置 `client_id` 和 `client_secret` 后，用户可连接自己的 GitHub 或 GitLab 账号。配置 GitHub App 时，再设置 `github_app_slug`，使用该 App 的 Client ID、Client Secret 和仓库权限。App 需要仓库的 Contents 只读权限，并安装到需要访问的仓库。用户授权的访问范围取用户权限与 App 安装范围的交集。改用 GitHub App 或更换 App 的 Client ID 后，需要重新授权。GitLab 的 OAuth 授权请求 `read_repository` 和 `read_api`，分别用于拉取代码和读取仓库列表。回调地址是 `<public_url>/api/v1/workers/repository/<provider_id>/callback`。

Access Token、Refresh Token 和到期时间保存在控制面的私有凭据表。列表请求只发送到配置的 Git 平台，分页按用户操作触发。API 响应和构建 Job 输入只包含所需的仓库元数据。OAuth 和仓库 API 请求固定到受信任的来源并拒绝重定向，受连接、响应体和总时限约束。可信内网 GitLab 通过 `allow_private_network` 显式放行。

仓库 ref 解析和浅克隆统一使用 [Git smart HTTP](https://git-scm.com/docs/http-protocol)，直接访问 provider 的固定 HTTPS origin（例如 `https://github.com/<owner>/<repo>.git`）。公开仓库匿名读取；私有仓库只在 Git 请求的 Basic Auth 中使用短期或管理员只读 Token，错误和构建日志会清理 URL 与凭据。控制面把 commit 固定后只拉取该 commit；服务端不支持 exact-SHA want 时最多回退到深度 32 的指定 ref，并在 checkout 前再次核对 commit。

## 后端流程

1. `PUT .../workers/{worker}/repository` 使用 `worker.update` 权限及 `expected_revision` 保存仓库配置。`use_authorization: true` 将当前会话用户的授权绑定到所选仓库。`GET .../workers/{worker}/repository` 返回配置与修订号。
2. `POST .../workers/{worker}/builds` 传 `source_type: repository`、`repository_revision`、构建环境及幂等键，要求目标环境 `build.start`。先检查仓库配置修订号，防止构建请求与另一个用户的配置修改串用。入队时复制 Worker 的仓库配置，后续配置修改不影响该构建。
3. 控制器在数据库事务外通过 Git smart HTTP 将 ref 解析为完整 commit，并先持久化 commit。下载中断后的重试仍使用该 commit。
4. 对固定 commit 做有界浅克隆，在控制面内生成项目归档并检查目录边界、链接、特殊文件、路径和大小；拒绝 `.env`、`.dev.vars`、`.npmrc` 等本地凭据，并保留 Wrangler 配置、锁文件、框架配置及 Text/Data/Wasm 等二进制构建输入。仓库中任何脚本、hook 都不会在控制面执行。
5. Kubernetes Job 根据唯一锁文件执行 frozen install，并由固定 Wrangler dry-run 构建已经配置好的 Workers 项目。安装和 custom build 在无上传凭据的 init container 中执行；可信 packer 在单独主容器校验输出、生成 `workerd-bundle-v2` 并上传。Job 不接收 Git 平台凭据或 S3 长期凭据。
6. 选历史制品发布到生产环境（`production`）或该 Worker 已创建的预览环境，沿用候选验证 → Version 提交 → 副本确认流程。

`GET /api/v1/orgs/{org}/workers` 返回按用户环境权限过滤的列表；`GET /api/v1/orgs/{org}/workers/repository-providers` 返回当前组织启用的来源和当前用户的授权状态。用户在 `POST /api/v1/orgs/{org}/worker-repositories/{provider}/repositories` 中传入 `page`，按需验证授权并读取自己可用的仓库。源码上传与仓库构建是两种独立输入模式，产品页面使用仓库构建。

## 当前范围与网络

当前使用固定 `workerd-bundle-v2` profile，支持模块 Workers、npm/pnpm/Yarn/Bun 锁定安装、Wrangler custom build、静态资产、Vite/已配置 adapter、`rules`、`find_additional_modules`、`no_bundle`、ESM/CommonJS/Text/Data/Wasm/source-map 和 `nodejs_compat`。仓库必须显式包含 Wrangler 配置；平台不会把普通框架仓库自动改造成 Workers 项目，也不执行 Cloudflare deploy/resource 命令。Python、Rust、legacy Service Worker 暂不支持，Git submodule 仍拒绝。项目目录最多 4096 文件、24 MiB、单文件 8 MiB；仓库归档解压最多 64 MiB、10,000 项。compatibility date/flags 优先取显式构建参数，否则取 Wrangler 配置，再否则使用平台锁定的 workerd 日期。

kind 为控制面增加已配置 Git provider origin 解析所得 IP 的 TCP 443 出口；独立 E2E 把 npm registry 指向集群内 TLS fixture，builder 除该 registry 与 MinIO 外没有业务出口，runtime 仍只访问控制面与 MinIO。kind 专用镜像注入 fixture CA，TLS 校验保持启用；tracked 示例 lockfile 保持标准 npmjs 地址，E2E 只在上传的临时源码包内替换为 fixture tarball 与现场计算的 integrity。生产环境应把 `npm_registry` 指向稳定的内部 proxy，并使用可持续管理域名访问的出口代理或网络策略。

日常本地开发使用 `make dev` 的 local-first 拓扑；完整集群验收才使用 `deploy/workers-kind`。私有仓库只有在 `.data/dev.yaml` 或独立 E2E 生成配置中提供了真实测试凭据后才算完成验收，不能把公开仓库接口测试当作私有仓库验收。

## 验证

仓库下载、TLS Git、Wrangler build/pack、finalize、发布和 Gateway 调用由独立 kind E2E 验证；
真实浏览器测试覆盖绑定仓库、默认 HEAD 构建、发布和调用。运行方式见 [验证说明](verification.md)。

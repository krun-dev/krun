# Workers Workers 列表与仓库构建

Workers 导航先展示当前组织中有权查看的Workers 列表。服务名称、关联仓库和可见环境状态来自后端；点击服务才进入构建 / 发布详情。返回Workers 列表和浏览器返回均可用，详情 URL 可以直接打开。创建服务后进入新服务详情，不再自动打开第一个已有服务。

“构建新版本”现在从 GitHub、GitLab.com 或管理员配置的自建 GitLab 拉取代码。用户填写仓库地址、分支 / Tag / Commit 和项目目录，保存为该服务的仓库配置；入口、静态资源与构建命令统一以项目内 Wrangler 配置为准，避免平台表单与项目配置出现两套真相。后续打开会恢复已保存配置。历史构建展示仓库、请求的 ref 与实际 commit，发布与回滚继续选用已成功构建的不可变制品。

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
- `type: gitlab` 同时支持 GitLab.com 和自建实例，`base_url` 必须为固定 HTTPS origin。GitHub 使用 github.com。OAuth endpoint 默认拒绝私网/loopback 解析；只有管理员确认目标是受信任内网实例后才可为该 GitLab 显式设置 `allow_private_network: true`，GitHub 禁止开启。
- `token` 可通过配置文件的环境变量展开注入；公开仓库可以省略。私有仓库使用管理员提供的只读凭证。配置了共享 Token 时，`allowed_repositories` 必须列出允许读取的完整 namespace/name，不支持通配符。
- `organization_ids` 可限定来源可用于哪些平台组织，省略时对有服务权限的组织用户开放。共享 Token 的访问范围由这些配置与服务权限共同限制。
- 修改后重启控制面生效。Token 不出现在来源列表、构建 API、日志、Kubernetes 构建 Job 输入或 workerd 中。页面会说明所选来源是否配置了凭证。

配置 `client_id` / `client_secret` 后，用户可连接自己的 GitHub/GitLab OAuth
授权。access token、refresh token、到期时间和轮换 revision 只保存在控制面的私有凭据
表；普通 Workers 快照和 API 不加载这些字段。控制面在构建前刷新即将到期的授权，
通过 CAS 租约保证多副本不会同时消费轮换型 refresh token。授权失效时必须由用户重连，
不会静默退回管理员共享 Token。Token endpoint 请求固定在配置的 provider host，拒绝
重定向并受连接、响应头、响应体和总时限约束；生产默认拒绝解析到 loopback、私网或
link-local 地址。可信内网 GitLab 可以逐 provider 显式放行 private/loopback，但
link-local/unspecified 永远拒绝，不能把普通 OAuth 配置变成任意内网请求入口。

仓库 ref 解析和浅克隆统一使用 [Git smart HTTP](https://git-scm.com/docs/http-protocol)，直接访问 provider 的固定 HTTPS origin（例如 `https://github.com/<owner>/<repo>.git`）。公开仓库匿名读取；私有仓库只在 Git 请求的 Basic Auth 中使用短期或管理员只读 Token，错误和构建日志会清理 URL 与凭据。控制面把 commit 固定后只拉取该 commit；服务端不支持 exact-SHA want 时最多回退到深度 32 的指定 ref，并在 checkout 前再次核对 commit。

## 后端流程

1. `PUT .../workers/{worker}/repository` 使用 `worker.update` 权限及 `expected_revision` 保存共享仓库配置。`GET .../workers/{worker}/repository` 返回配置与修订号。
2. `POST .../workers/{worker}/builds` 传 `source_type: repository`、`repository_revision`、构建环境及幂等键，要求目标环境 `build.start`。先检查仓库配置修订号，防止构建请求与另一个用户的配置修改串用。入队时复制 Worker 的仓库配置，后续配置修改不影响该构建。
3. 控制器在数据库事务外通过 Git smart HTTP 将 ref 解析为完整 commit，并先持久化 commit。下载中断后的重试仍使用该 commit。
4. 对固定 commit 做有界浅克隆，在控制面内生成项目归档并检查目录边界、链接、特殊文件、路径和大小；拒绝 `.env`、`.dev.vars`、`.npmrc` 等本地凭据，并保留 Wrangler 配置、锁文件、框架配置及 Text/Data/Wasm 等二进制构建输入。仓库中任何脚本、hook 都不会在控制面执行。
5. Kubernetes Job 根据唯一锁文件执行 frozen install，并由固定 Wrangler dry-run 构建已经配置好的 Workers 项目。安装和 custom build 在无上传凭据的 init container 中执行；可信 packer 在单独主容器校验输出、生成 `workerd-bundle-v2` 并上传。Job 不接收 Git 平台凭据或 S3 长期凭据。
6. 选历史制品发布到 test / staging / production，沿用候选验证 → Version 提交 → 副本确认流程。

`GET /api/v1/orgs/{org}/workers` 返回按用户环境权限过滤的列表；`GET /api/v1/orgs/{org}/workers/repository-providers` 返回当前组织启用的来源展示信息。源码上传与仓库构建是两种独立输入模式，产品页面使用仓库构建。

## 当前范围与网络

当前使用固定 `workerd-bundle-v2` profile，支持模块 Workers、npm/pnpm/Yarn/Bun 锁定安装、Wrangler custom build、静态资产、Vite/已配置 adapter、`rules`、`find_additional_modules`、`no_bundle`、ESM/CommonJS/Text/Data/Wasm/source-map 和 `nodejs_compat`。仓库必须显式包含 Wrangler 配置；平台不会把普通框架仓库自动改造成 Workers 项目，也不执行 Cloudflare deploy/resource 命令。Python、Rust、legacy Service Worker 暂不支持，Git submodule 仍拒绝。项目目录最多 4096 文件、24 MiB、单文件 8 MiB；仓库归档解压最多 64 MiB、10,000 项。compatibility date/flags 优先取显式构建参数，否则取 Wrangler 配置，再否则使用平台锁定的 workerd 日期。

kind 为控制面增加已配置 Git provider origin 解析所得 IP 的 TCP 443 出口；独立 E2E 把 npm registry 指向集群内 TLS fixture，builder 除该 registry 与 MinIO 外没有业务出口，runtime 仍只访问控制面与 MinIO。kind 专用镜像注入 fixture CA，TLS 校验保持启用；tracked 示例 lockfile 保持标准 npmjs 地址，E2E 只在上传的临时源码包内替换为 fixture tarball 与现场计算的 integrity。生产环境应把 `npm_registry` 指向稳定的内部 proxy，并使用可持续管理域名访问的出口代理或网络策略。

日常本地开发使用 `make dev` 的 local-first 拓扑；完整集群验收才使用 `deploy/workers-kind`。私有仓库只有在 `.data/dev.yaml` 或独立 E2E 生成配置中提供了真实测试凭据后才算完成验收，不能把公开仓库接口测试当作私有仓库验收。

## 验证

仓库下载、TLS Git、Wrangler build/pack、finalize、发布和 Gateway 调用由独立 kind E2E 验证；
真实浏览器测试覆盖绑定仓库、默认 HEAD 构建、发布和调用。运行方式见 [验证说明](verification.md)。

# 本地联调 GitHub、GitLab 与阿里 SSO

当前 Vite 前端为 `http://127.0.0.1:5173/console/`，Go 后端为 `127.0.0.1:8080`。Vite 代理 `/auth/` 和 `/api/`，所以 `public_url` 与第三方回调地址都使用 **5173**。浏览器始终用 `127.0.0.1`，不要混用 `localhost`、8080 和 5173。

开发环境从其他 Host / 端口打开后端 `/` 或 `/console/` 时，会自动跳转到配置的 `public_url`，保留路径与查询参数；认证与 API 端点仍可通过后端访问。登录写请求仍严格检查固定 Origin 和 CSRF。生产使用真实 HTTPS origin，如 `https://console.example.com`，回调对应 `https://console.example.com/auth/gitlab/callback`；生产配置不使用此开发跳转，也不启用 dev Provider。

GitHub、GitLab、通用 OAuth2 和 OIDC 已有实现。真实接入需要在身份提供商创建应用，拿到 Client ID 和 Client Secret；本地开发登录不能替代真实 SSO 验收。

## 1. 创建测试应用

| 来源 | 创建入口 | 回调地址 | Scope |
| --- | --- | --- | --- |
| GitHub | Settings → Developer settings → OAuth Apps → New OAuth App | `http://127.0.0.1:5173/auth/github/callback` | `read:user`（由 krun 请求） |
| GitLab | 头像 → Edit profile → Access → Applications → Add new application | `http://127.0.0.1:5173/auth/gitlab/callback` | 勾选 `read_user` |
| 阿里云 IDaaS OAuth2（旧版） | 应用 → 添加应用 → OAuth2（Web 应用） | `http://127.0.0.1:5173/auth/aliyun/callback` | `read` |
| 阿里云 IDaaS OIDC | IDaaS 控制台创建 OIDC 应用；使用总示例的 company 来源 | `http://127.0.0.1:5173/auth/company/callback` | `openid profile email` |

GitHub 应用名称可填 `krun-local`，Homepage URL 填 `http://127.0.0.1:5173/console/`。保存后取得 Client ID，再生成 Client Secret。参考 [GitHub 官方创建说明](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app)。

GitLab 保存后取得 Application ID（即 Client ID）和 Secret；如页面有 Confidential 选项，启用它，krun 在后端保存和使用 Secret。自建 GitLab 在对应实例创建应用，配置中的 `base_url` 填该实例的 HTTPS origin。参考 [GitLab 官方创建说明与 scopes](https://docs.gitlab.com/integration/oauth_provider/)。

krun 登录不需要申请仓库读写权限。两种提供商都使用授权码 + PKCE S256；GitHub 支持见 [官方授权流程](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps)。CLI 使用 krun 自己的浏览器确认流程，无需启用 GitHub 的 Device Flow。

如同一 GitLab 应用还用于仓库 / 项目 API 集成，可保留应用的 `api` 范围，并将 krun Provider 的 `scopes` 改为 `[api]`；`api` 包含 API 读写操作，仍受登录用户在 GitLab 中的实际权限约束。应用允许的 scope 与 krun 请求的 scope 名称需要匹配，允许 `api` 不代表接受单独申请的 `read_user`。只读 API 与 Git-over-HTTPS 拉取可按需要申请 `read_api`、`read_repository`，并在应用中勾选对应范围。此版本只在认证过程中读取用户资料，不持久保存 GitLab Access / Refresh Token，也未实现仓库列表或拉取；配置较大 scope 不会自动增加仓库功能。

## 2. 配置 GitHub / GitLab

本地开发只有一份配置：`.data/dev.yaml`（已被 `.gitignore` 忽略，不会进仓库）。GitHub / GitLab / 阿里来源已经在这份文件里，按需把对应 provider 的 `enabled` 设为 `true`，填写 Client ID 和 Secret。需要保留开发一键登录时启用 `local`；生产必须关闭它。不测试的来源可以保留禁用或删除。

示例省略默认 scopes，GitHub 默认申请 read:user，GitLab 默认 read_user。应用只允许 api 或另需项目 / 仓库 API 时，在 GitLab 来源中显式填写 scopes: [api]。

Secret 可直接写入 YAML，也可通过 `${KRUN_GITHUB_SECRET}` / `${KRUN_GITLAB_SECRET}` / `${KRUN_ALIYUN_SECRET}` 引用。`scripts/dev-server.sh` 自动读取 `.data/github-local.secret`、`.data/gitlab-local.secret` 和 `.data/aliyun-local.secret`，已有同名环境变量时优先使用环境变量。邮箱发信密码同样支持 `.data/smtp-local.secret`。Secret 文件权限应为 0600，位于忽略提交的 .data/。

停止当前占用 8080 的 krun 后端（在它的运行终端按 Ctrl+C），再在上述终端启动：

```sh
./scripts/dev-server.sh
```

已运行的 Vite 无需重启。尚未启动前端时，另开终端执行：

```sh
npm run dev -- --port 5173 --strictPort
```

打开 `http://127.0.0.1:5173/`，退出当前 krun 登录后，登录页会显示已配置的 GitHub / GitLab。可用 `curl http://127.0.0.1:5173/api/auth/providers` 检查公开来源列表；该接口不会返回 Secret。

此配置的 `db` 直接指向 PostgreSQL。测试时沿用现有开发数据库以保留组织和开发管理员，不需要加密密钥。省略 `registration` 时默认允许首次成功认证创建本地用户；**不会自动加入组织或授予管理员权限**。需要邀请注册时可显式配置 `registration: invite_only`，并按准确 `source_id + subject` 邀请。

## 3. 阿里的接法

OAuth2（Web 应用）使用授权码模式 authorization_code。在 [统一配置示例](../config/example.yaml) 的 aliyun 来源中启用登录、填写应用的 Client ID / Secret，并将三个端点的 `login.example.com` 替换为实际 IDaaS 登录域名。示例包含 read 范围、data 字段映射和 success=true 检查；端点与字段需按实例提供的文档确认。

示例中 Token 使用 POST 表单，用户信息使用 GET Bearer Token；返回数据位于 `data`，身份映射为 `data.sub` / `data.nickname` / `data.email`，同时要求顶层 `success: true`。用户信息端点使用 `/api/bff/v1.2/oauth2/userinfo`，授权范围为 `read`；不同实例可能需要调整。控制台复制的 Authorize URL 中已有 `state` 只是示例，配置仅填写基础端点；krun 每次生成自己的随机 state 与 PKCE S256。需要在 IDaaS 中给测试账号或其组织授权此应用。

将 Secret 放入权限为 `0600` 的 `.data/aliyun-local.secret`，启动脚本自动导出 `KRUN_ALIYUN_SECRET`。配置后重启后端，回调使用上面的 5173 地址。其他实例需根据实际协议文档确认端点与字段映射。

先确认阿里 SSO 产品和协议：

- **阿里云 IDaaS OIDC**：可用现有 OIDC Provider 接入，需取得应用 Client ID、Secret 和控制台提供的准确 Issuer。开启授权码模式、PKCE S256，为测试用户分配应用访问权限。参考 [IDaaS OIDC 配置](https://help.aliyun.com/zh/idaas/eiam/user-guide/oidc-sso-configurations/)。
- **公司内部阿里 SSO**：若为标准 OIDC，按 OIDC 配置；若为标准 OAuth2，按 [通用 OAuth2 映射](auth-api.md#provider-配置) 配置；若使用 ticket、签名或验票接口，需要正式协议和适配代码。
- **CloudSSO / RAM 的云控制台登录**：与让 krun 作为业务应用登录不同。当前 krun 没有 SAML 适配，不能把这类地址直接作为 OIDC Issuer。

确认是 IDaaS OIDC 后，启用总示例的 company 来源，填写实际应用提供的 issuer、Client ID 与 Secret；回调地址使用 `/auth/company/callback`。Issuer 必须复制实际应用提供的值，不是阿里云控制台地址；不同 IDaaS 应用类型的路径不同。krun 请求 `Issuer + /.well-known/openid-configuration` 自动发现端点，启动时需要可访问该端点。`token_client_auth` 默认 client_secret_basic，也支持 client_secret_post，应与 IDaaS 应用设置保持一致。Secret 可直接填入 YAML，或通过 KRUN_OIDC_SECRET 环境变量引用；配置后重启后端。

当前要求发现的授权、Token 端点与 Issuer 同主机，JWKS 也受配置主机限制；实际应用端点跨主机时需要扩展适配。如果提供商不接受 loopback HTTP 回调，应使用测试域名与 HTTPS 反向代理、生产配置及独立数据库，不公开暴露含 dev Provider 的本地配置。

## 4. 验收登录、授权与 CLI

1. 分别退出 krun 后，用 GitHub / GitLab / 已配置的阿里来源登录，确认第三方授权完成后返回概览，并复制本地用户 ID `usr_...`。
2. 再次用相同来源和账号登录，应得到同一个本地用户 ID。即使邮箱相同，不同来源也产生独立用户，不自动绑定或合并。
3. 用“本地开发登录”进入已有管理员账号，在目标组织添加该 `usr_...` 成员，再给它授予组织范围的 `viewer` 角色；若组织有身份来源白名单，把实际 `source_id` 加入允许列表。
4. 切回真实 SSO 身份，确认能读取该组织的服务和模型；创建 / 修改资源应被拒绝。改授 `developer` 后再试写入；撤销角色或移除成员后再次请求，应立即失去相应权限。
5. 测试 CLI：

   ```sh
   ./bin/krun --config .data/cli-sso-test.yaml login --server http://127.0.0.1:5173
   ./bin/krun --config .data/cli-sso-test.yaml whoami
   ./bin/krun --config .data/cli-sso-test.yaml org list
   ```

   打开 CLI 打印的确认链接，用目标 SSO 身份登录，核对终端确认码后批准。`whoami` 应与浏览器中的本地用户 ID 一致。使用独立 CLI 配置避免覆盖原有终端账号。
6. 在管理员审计页查看认证与操作记录；API Key 即使选择 `*`，也应受该 SSO 用户的实时角色权限约束。

## 排查

| 现象 | 检查 |
| --- | --- |
| 登录页没有第三方按钮 | 后端是否加载 `.data/sso-local.yaml`；退出当前登录，检查 `/auth/providers` |
| 后端报 `provider client_id and client_secret required` | 保留的每个来源均填写 ID，Secret 在启动后端的同一终端 export |
| 回调地址不匹配、CSRF / Origin 错误 | `public_url`、应用回调、浏览器都用 `http://127.0.0.1:5173`；回调路径中的 Provider ID 一致 |
| `OIDC discovery failed` | Issuer 是否准确、DNS / HTTPS 可达、端点是否符合主机限制 |
| 第三方认证后登录失败 | Secret、scope、Token 认证方式、PKCE、应用访问权限、JIT / 精确身份邀请；回调错误不会回显上游敏感正文 |
| 登录成功但组织为空 | 新身份尚未获授组织成员资格和角色，或者来源不在组织允许列表 |
| `existing identity source namespace cannot be replaced` | 已登记来源的 Client ID / Issuer / 实例不可替换；测试另一应用或实例时用新的 `id` 和 `source_id`，同步更新回调 |
| 浏览器可访问第三方，后端兑换 Token 失败 | 后端直接连接提供商，不读取 `HTTP_PROXY` / `HTTPS_PROXY`；检查本机 DNS、网络或内网 VPN |

回退本地开发配置时，保持 Vite 公共 origin：停止测试后端，直接运行 `./scripts/dev-server.sh` 重新读取 `.data/dev.yaml`。只回退配置，无需删除数据或重新 bootstrap。

# 本地联调 GitHub、GitLab 与阿里 SSO

本地 Vite 入口为 `http://127.0.0.1:5173/`，Go 控制面监听
`127.0.0.1:8080`。Vite 只把 `/api/*` 代理到 Go，因此浏览器、
`public_url` 和身份提供商回调必须统一使用 5173；不要混用 `localhost`、8080
和 5173。

生产回调同样在 API 命名空间内：
`https://console.example.com/api/auth/<provider-id>/callback`。生产关闭 dev
Provider，并使用真实 HTTPS 同源入口。

## 创建测试应用

| 来源 | 回调地址 | 默认 Scope |
| --- | --- | --- |
| GitHub OAuth App | `http://127.0.0.1:5173/api/auth/github/callback` | `read:user` |
| GitLab Application | `http://127.0.0.1:5173/api/auth/gitlab/callback` | `read_user` |
| 阿里云 IDaaS OAuth2 | `http://127.0.0.1:5173/api/auth/aliyun/callback` | `read` |
| 标准 OIDC | `http://127.0.0.1:5173/api/auth/company/callback` | `openid profile email` |

GitHub Homepage URL 填 `http://127.0.0.1:5173/`。GitLab 自建实例的
`base_url` 填其 HTTPS origin。krun 使用授权码和 PKCE S256；CLI 使用 krun
自己的浏览器确认流程，不要求启用上游 Device Flow。

若同一个 GitLab 应用还用于仓库读取，可按实际需要给应用和配置增加 `read_api`、
`read_repository` 或 `api`，两侧 scope 名称必须一致。登录本身不会持久保存上游
Access Token，也不会因为 scope 较大自动获得 krun 内权限。

## 唯一本地配置

所有本地运行期配置只写在 gitignored 的 `.data/dev.yaml`：

```bash
cp config/development.example.yaml .data/dev.yaml
```

按 [config/example.yaml](../config/example.yaml) 中的结构，把需要测试的 provider
合并进该文件并设为 `enabled: true`。Client Secret 可以直接放在这个被忽略的 YAML
里，也可以写 `${KRUN_GITHUB_SECRET}` 等环境变量引用；不要创建第二份 SSO YAML、
组件配置或独立 secret 文件。`scripts/dev-server.sh` 只读取传入的主配置，不会偷偷
拼接其他配置来源。

停止旧后端后启动：

```bash
./scripts/dev-server.sh .data/dev.yaml
npm run dev -- --port 5173 --strictPort
```

打开 `http://127.0.0.1:5173/`。可用下面的同源请求确认公开来源，返回中不会包含
Secret：

```bash
curl http://127.0.0.1:5173/api/auth/providers
```

默认 JIT 只创建本地用户，不自动加入组织或授予角色。需要邀请注册时显式设置
`registration: invite_only`，并按准确的 `source_id + subject` 邀请。

## 阿里与通用 OIDC

`config/example.yaml` 的 `aliyun` 示例展示了标准 OAuth2 授权码流程、POST token、
Bearer userinfo、字段映射和业务 `success` 条件。实际端点与字段必须以当前 IDaaS
应用文档为准；配置 URL 中不要预填 state，krun 每次会生成 state、nonce 和 PKCE。

标准 OIDC 使用 `company` 示例，Issuer 必须是应用提供的准确值。krun 从
`Issuer + /.well-known/openid-configuration` 发现端点，并要求发现的授权和 Token
端点与 Issuer 同主机。`client_secret_basic` 和 `client_secret_post` 均支持。

CloudSSO / RAM 控制台登录、SAML、公司自定义 ticket / 签名验票不等同于 OIDC；
没有正式协议和对应适配时不能直接填进现有 Provider。

## 验收

1. 分别用每个真实来源登录，记录本地 `usr_...`；同一来源再次登录应复用同一用户。
2. 相同邮箱但不同来源应保持为独立身份，不自动合并。
3. 管理员把该用户加入目标组织并授予 `viewer`；确认只能读。改授 `developer` 后写入
   生效，撤销角色或成员关系后权限立即失效。
4. 用独立 CLI 配置测试浏览器批准、API 调用和退出：

   ```bash
   ./bin/krun --config .data/cli-sso-test.yaml login --server http://127.0.0.1:5173
   ./bin/krun --config .data/cli-sso-test.yaml whoami
   ./bin/krun --config .data/cli-sso-test.yaml org list
   ./bin/krun --config .data/cli-sso-test.yaml logout
   ```

   终端应核对确认码；批准后的 token 只能领取一次，logout 后立即失效。
5. 查看审计页，确认登录与管理操作存在；API Key 仍受用户当前角色约束。

## 排查

| 现象 | 检查 |
| --- | --- |
| 登录页没有第三方按钮 | 服务是否读取 `.data/dev.yaml`；检查 `/api/auth/providers` |
| 回调不匹配或 Origin 错误 | `public_url`、浏览器和回调是否都使用 `127.0.0.1:5173` 与 `/api/auth/.../callback` |
| OIDC discovery 失败 | Issuer、DNS、HTTPS 和同主机端点约束 |
| Token 兑换失败 | Secret、scope、认证方式、PKCE、应用授权；后端不读取 HTTP(S)_PROXY |
| 登录成功但组织为空 | 用户尚无成员关系/角色，或来源不在组织 allowlist |
| identity source 不能替换 | 换应用或实例时使用新的 `id` 与 `source_id`，并同步回调 |

真实凭据不得进入 tracked 文件。提交前运行 `make check-secrets`。

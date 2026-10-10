# krun

Go 控制服务与 `krun` CLI，React + TypeScript + [shadcn/ui](https://ui.shadcn.com/) 管理控制台。认证身份、组织成员、角色与 API Key 共用一个服务端授权边界。

当前已实现：

- GitHub、GitLab、自建 GitLab、通用 OAuth2、OIDC 登录；默认认证成功后自动创建账号。不提供密码、账号绑定或合并。
- PKCE S256、OIDC nonce / issuer / audience / 签名 / 有效期校验、一次性登录事务与浏览器 generation、CSRF、不透明 Session 和退出撤销。
- 系统与组织管理员、developer / publisher / viewer、直接角色和用户组角色、服务 / 模型范围与环境条件、组织身份来源限制、最后一名系统管理员保护。
- Cedar Go 求值：用户权限与 Key 限制分别检查，取交集；默认拒绝、显式 deny 优先、资源真实归属检查、列表逐项过滤。
- API Key 创建、元数据查看、策略 revision 更新、撤销、一次性明文发放；组织精确范围、所有用户可选的组织 / 跨工作空间通配范围，支持 60 秒至 90 天或长期有效。
- 浏览器确认 CLI 登录、1 小时 Bearer Session、API Key 登录、组织 / 成员 / 组 / 角色 / Key / 服务与模型元数据 / 权限检查 / 审计命令。
- 开发与生产统一使用 PostgreSQL 共享事务存储。

## 本地运行

需要 Go 1.26+、Node.js 20.19+ 或 22.12+（建议 Node 24）、npm 和 PostgreSQL。无需 Docker；可直接使用本机 PG 二进制。

```sh
# pg_ctl / initdb 放入 PATH，或设置 KRUN_PG_BIN 指向其目录。
# 首次初始化会提示设置数据库用户 krun 的密码。
./scripts/dev-postgres.sh
createdb -h 127.0.0.1 -p 25432 -U krun krun_dev
# 在 config/development.yaml 中填写实际 db 连接串
make build
./scripts/dev-server.sh
```

服务端配置统一支持直接值和环境变量引用，字符串字段都使用同一种写法：

```yaml
db: ${KRUN_DATABASE_URL}
listen: ${KRUN_LISTEN:-127.0.0.1:8080}
public_url: ${KRUN_PUBLIC_URL:-http://127.0.0.1:5173}
providers:
  - id: github
    source_id: github-local-test
    type: github
    client_id: ${KRUN_GITHUB_CLIENT_ID}
    client_secret: ${KRUN_GITHUB_SECRET}
```

也可直接写 `db: postgres://krun:密码@127.0.0.1:25432/krun_dev?sslmode=disable` 或 `client_secret: 直接值`。`${NAME}` 要求环境变量存在且非空；`${NAME:-默认值}` 在未设置或为空时使用默认值；`$$` 表示字面的 `$`。引用在 YAML 解析后替换，变量里的换行、冒号和 `$` 按字符串原样保留，不再次展开。只替换服务端配置中的字符串值，不替换字段名；布尔值和数字仍按 YAML 类型填写。不再使用 `db_env`、`client_secret_env`、`database_url_env` 或 `data_file`。数据库密码里的特殊字符需要 URL 编码。

开发启动脚本默认优先读取本机 `.data/sso-local.yaml`，不存在时使用 `config/development.yaml`；也可传入配置路径。

打开 [控制台](http://127.0.0.1:8080/console/)，点击“本地开发登录”。默认开发身份为 `source_id=local-development, subject=developer`。在概览页复制准确的本地用户 ID，显式初始化一次系统管理员：

```sh
./bin/krun-server bootstrap --config config/development.yaml --user usr_你的用户ID
```

刷新控制台后创建组织。`bootstrap` 为本地运维命令，没有公开 HTTP 接口，只能成功执行一次。

开发配置只允许绑定 loopback；生产配置拒绝启用的 dev Provider。应用数据统一保存于 PostgreSQL，本地 PG 数据目录和联调凭据放在忽略提交的 `.data/`。**不支持将开发配置暴露为公共服务。**

登录来源可设置 `enabled: false` 禁用，包括本地开发入口；禁用后该来源的登录和已有凭据访问都会被拒绝，用户与授权数据保留。省略时默认启用。生产配置拒绝启用的 dev Provider。

本地邮箱验证码登录使用 `type: email`，支持生产环境，无需密码。通过根级 `smtp` 配置发送邮件，验证码有效 10 分钟、60 秒后可重发，验证成功后沿用现有用户和权限规则。配置见 [统一配置示例](config/example.yaml)，字段与接口见 [认证接口](docs/auth-api.md)。

## 中英文

控制台默认中文，登录页和控制台右上角都可以切换 English；语言选择保存在 `krun.language`，刷新、退出和再次登录后保留。浏览器的默认语言不会覆盖首次使用时的中文默认值。页面、表单、权限目录、审计分页、无障碍标签、日期和数字格式随语言切换；用户姓名、组织 / 资源名称及协议标识保持原值。

`krun` CLI 的帮助、提示和错误固定为英文，API 请求统一发送 `Accept-Language: en`，不读取 `KRUN_LANG`，不提供 `--lang` 参数。服务端 `krun-server` 的帮助、错误与运行提示默认中文，可以使用 `--lang` 参数或 `KRUN_LANG` 环境变量选择英文；参数优先于环境变量：

```sh
./bin/krun help
./bin/krun-server --lang en serve --config config/production.yaml
KRUN_LANG=en ./scripts/dev-server.sh
```

HTTP API 的 `error.message` 按 `Accept-Language` 返回中英文，缺省或不支持的语言使用中文；响应携带 `Content-Language`。`error.code`、JSON 字段、角色 / 动作 / 环境 ID 和 CLI JSON 数据输出保持稳定。前端使用 [i18next](https://www.i18next.com/translation-function/interpolation) / react-i18next；文案位于 `web/src/locales/`，后端文案位于 `internal/localize/zh-CN.json`，前端也复用其中的错误文案。

## CLI 使用

```sh
./bin/krun login --server http://127.0.0.1:8080
./bin/krun whoami
./bin/krun org create --name 'Platform Engineering'
./bin/krun org list
./bin/krun members add --org org_xxx --user usr_xxx
./bin/krun groups create --org org_xxx --name Developers
./bin/krun groups add-member --org org_xxx --group grp_xxx --user usr_xxx
./bin/krun roles grant --org org_xxx --subject-kind group --subject grp_xxx --role developer
./bin/krun services create --org org_xxx --name orders-api
./bin/krun check --kind service --id service_xxx --action service.read
./bin/krun audit --org org_xxx --limit 100
```

Key 策略示例见 `config/key-policy.example.yaml`，替换真实组织和资源 ID 后签发：

```sh
./bin/krun keys create --org org_xxx --name staging-ci --policy config/key-policy.example.yaml --expires 24h
./bin/krun keys create --system --name personal-automation --policy config/key-policy.wildcard.example.yaml --expires never
./bin/krun keys list --org org_xxx
./bin/krun keys revoke --id key_xxx
```

使用 `--expires never`（或 `--expires 0`）创建长期有效 Key，返回的 `expires_at` 为 null。所有用户可以用 `--system` 创建自己的跨工作空间 Key；策略里的资源通配与动作 `*` 始终受用户实时权限约束。创建命令只在该次响应中返回完整 Key。CI 推荐从 Secret 管理器注入 `KRUN_TOKEN`；也可以通过 `krun login --token-stdin` 写入本地配置，不把 Key 放进命令行参数。CLI 配置文件位于系统用户配置目录的 `krun/config.yaml`，权限为 `0600`；可用 `--config FILE` 指定独立配置。切换 `--server` 不会把旧服务器保存的凭据发送到新服务器。

浏览器确认的 CLI Session 可以执行 IAM 管理，使用 API Key 时这些接口仍被拒绝。CLI Session 绝对期限为 1 小时、闲置期限 30 分钟，不持久保存上游 Token；退出时撤销当前 CLI Session。API Key 退出只移除本地凭据，撤销需使用 `keys revoke`。

更多命令：`krun help`。可以执行 `go install ./cmd/krun` 把 `krun` 安装到 Go bin 目录。

## 生产配置

服务端配置、CLI 本地配置、Key 策略和 CLI `--file` 输入仅支持 `.yaml` / `.yml`，不读取 JSON 配置。YAML 支持注释；字段名沿用 API 命名，未知字段、重复键和多文档会被拒绝。HTTP API 与 CLI 输出仍使用 JSON。

`scopes` 可省略：GitHub 默认 `read:user`，GitLab 默认 `read_user`，OIDC 默认 `openid, profile, email`；通用 OAuth2 按提供商要求填写。需要仓库权限或应用只允许特定范围时显式覆盖，例如 GitLab `scopes: [api]`。应用允许的范围须覆盖实际请求的范围。

服务端示例统一在 [config/example.yaml](config/example.yaml)，涵盖本地开发登录、邮箱 / SMTP、GitHub、GitLab、阿里云 IDaaS OAuth2、OIDC 和自定义 OAuth2。`config/development.yaml` 保留为启动脚本使用的最小开发配置；两个 Key 策略示例是 CLI `--policy` 的输入。

复制总示例到自己的配置文件，修改公开 origin 与数据库，将需要的 Provider 设为 `enabled: true` 并填写凭据。总示例中的所有来源默认关闭，可选 Secret 使用空默认值；不启用的来源无需配置其 Secret。字符串值也可以直接填写：

```sh
cp config/example.yaml config/production.yaml
# 修改 public_url，并启用所需 Provider；例如 github / company。
export KRUN_DATABASE_URL='postgres://krun:密码@db.example.com:5432/krun?sslmode=require'
export KRUN_GITHUB_SECRET='来自 Secret 管理器的 client secret'
export KRUN_OIDC_SECRET='来自公司 SSO 的 client secret'
./bin/krun-server serve --config config/production.yaml
```

所有副本使用相同数据库、公开 origin 与 Provider 配置。数据库应为 krun 独立元数据库；应用角色首次启动需要建表权限，后续可用预建表并授予读写权限。服务端自动初始化 `krun_control_state` 表，初始化 SQL 见 `docs/storage.md`。

生产不支持子路径 origin；UI 为 `/console/`，认证 `/auth/`，API `/api/v1/`。反向代理在固定公开 origin 终止 TLS；API 不根据 Host / X-Forwarded-* 改写回调 URL，不开放跨 origin CORS。

前后端可以分离部署：省略 `static_dir` 或设置为空，Go 只提供 API；前端的 dist 独立部署在静态服务器，并在前端公开域名下将 `/auth/`、`/api/` 代理到 Go。`public_url` 仍填写前端 origin。合并部署显式设置 `static_dir: dist`。构建、Nginx 和回调配置见 [分离部署说明](docs/deployment.md)。

外部应用准确回调地址：`https://console.example.com/auth/<provider-id>/callback`。GitHub scope 默认 `read:user`，GitLab 默认 `read_user`，OIDC 默认 `openid profile email`。

本地真实 SSO 联调同样使用 [统一配置示例](config/example.yaml)，将 development 改为 true、listen 改为 loopback、public_url 改为 5173 的 origin；具体步骤与回调地址见 [操作说明](docs/sso-local-testing.md)。

默认认证成功后创建用户，不需要填写 `registration: jit`。首位管理员登录后，由本地运维执行 bootstrap。需要限制账号创建时可显式使用 `registration: invite_only`；邀请有效期 7 天且只消费一次：

```sh
./bin/krun-server invite --config config/production.yaml --source github-main --subject 真实GitHub数字ID
./bin/krun-server bootstrap --config config/production.yaml --user usr_真实本地ID
```

后续管理员可用 `POST /api/v1/system/invitations` 邀请；`source_id + subject` 唯一识别账号，邮箱不会触发注册或授权。

Provider 端点限定 HTTPS、配置 host allowlist，拒绝 loopback / link-local / metadata 目标，出站不跟随重定向。OIDC discovery/token/授权端点必须同 issuer 主机，JWKS 请求也受相同出站 host 限制；跨主机 discovery 需要后续明确扩展 allowlist。内网 SSO 可使用经管理员配置的固定内网主机。通用 OAuth2 配置详见 `docs/auth-api.md`。

## 验证

```sh
npm ci
npm run build
go vet ./...
go test -race ./...
```

PostgreSQL 集成测试必须使用**独立、可清空的测试数据库**，测试会重置自己的 `krun_control_state` 内容：

```sh
KRUN_TEST_POSTGRES_URL='postgres://krun:test@127.0.0.1:5432/krun_test?sslmode=disable' go test -race ./...
```

CI 启动独立 PostgreSQL，运行前端构建、Go vet、race 与集成测试。测试覆盖跨组织越权、组范围、环境条件、Key 策略上限与显式 deny、撤销、来源隔离、最后管理员、CSRF、回调重放/串浏览器/过期、PKCE 用户接口映射、签名 OIDC Token、CLI 配置权限，以及多客户端 PG 事务。

本地已验证开发登录 → 浏览器 CLI 确认 → Go CLI 创建组织 → 控制台读取 → 权限决策，详情见 `docs/verification.md`。自建 GitLab 和 GitHub 已完成真实登录与退出，阿里云 IDaaS OAuth2 已完成真实登录；OIDC 仍需实际应用凭据。

## 实现边界

此版本交付认证与授权，以及用于验证授权的服务 / 模型**元数据目录**。构建、版本制品、发布执行器、ServiceAccount、Worker 网关 SSO、公司非 OAuth2/OIDC 票据协议未实现，不存在假成功的部署接口。

PG 当前采用一行 JSONB 事务快照，以统一锁确保首次管理员、权限修改、撤销与审计原子一致；每次请求读取最新事实，不缓存 Allow。它适合首版与功能验证，**没有实现架构文档规划的逐表关系模型和高并发查询**；存储演进与具体限制见 `docs/storage.md`。

代码入口：`cmd/krun-server`、`cmd/krun`、`internal/control`、`internal/cli`、`web/src`。前端源组件通过 shadcn CLI 生成并保存在 `web/src/components/ui/`，视觉参考 [Supabase Studio 布局](https://supabase.com/design-system/docs/ui-patterns/layout)，采用中性色、绿色强调色、紧凑侧栏与深浅主题。API Key 权限按资源类型平铺，支持全部 API / 只读 / 读写快捷选择、逐类型资源范围、搜索、状态筛选与策略详情。

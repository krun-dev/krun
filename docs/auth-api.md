# 认证、授权与 CLI 接口

业务接口错误为 `{error:{code,message},request_id}`；OAuth form 端点使用标准 `{error,error_description}`。机器端返回 JSON，不自动转向第三方登录。认证与 API 响应 `Cache-Control: no-store`；回调错误只返回固定公共码，不回显上游正文。

错误 `message` 默认中文，`Accept-Language: en` 可请求英文，支持语言质量权重和 en-US / zh-CN 等语言标签；未知语言使用中文。响应返回 `Content-Language: zh-CN` 或 `en`，并设置 `Vary: Accept-Language`。错误 `code`、请求 ID、所有 JSON 字段和业务数据不会因语言改变。控制台自动发送所选语言，CLI 固定请求英文，程序应始终根据 `code` 判断错误。

## 认证

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | /api/auth/providers | 公共登录来源的 id/type/name；不含配置或 secret |
| POST | /api/auth/csrf | 受限地建立浏览器绑定，返回登录或 Session CSRF |
| POST | /api/auth/:provider/login | form body，CSRF 与 Origin 检查；`return_to` 仅限根页面的查询参数路径；303 或 Accept:application/json 时返回 redirect_url |
| GET | /api/auth/:provider/callback | 一次性 state、浏览器绑定、generation、过期与配置指纹，锁外兑换 Token，再原子签发本地 Session |
| POST | /api/auth/:provider/email/code | 仅 email 来源；JSON `{email,return_to}`，Origin + 浏览器 CSRF，发送邮箱验证码，返回 challenge_id / expires_in / retry_after |
| POST | /api/auth/:provider/email/verify | JSON `{challenge_id,code}`，Origin + 浏览器 CSRF，原子消费验证码并建立浏览器 Session，返回 redirect_url |
| GET | /api/auth/me | 当前主体、凭据类型、系统管理员状态及权限修订 |
| POST | /api/auth/logout | 浏览器或 CLI Session 撤销；清浏览器 Cookie |
| GET | /api/auth/oauth/authorize | 授权码流程；注册客户端、loopback 回调、state、PKCE S256 校验，转向静态授权页 |
| POST | /api/auth/oauth/device/auth | OAuth 设备码流程；5 分钟有效，返回确认码和浏览器地址 |
| GET | /api/auth/oauth/requests/:request | 授权页面所需的应用名称、实际 scopes、状态及到期时间；不返回凭据 |
| POST | /api/auth/oauth/requests/:request | 仅浏览器 Session + CSRF；允许或拒绝，授权码模式返回回调地址 |
| POST | /api/auth/oauth/token | form 编码；authorization_code、refresh_token、device_code 三种 grant |
| POST | /api/auth/oauth/revoke | form 编码；撤销 access 或 refresh 所属的整次授权；未知 token 也返回成功 |

krun CLI 和 Wrangler 共用 OAuth 授权与凭据存储。Access Token 有效 1 小时；携带
`offline_access` 时签发最长 30 天的刷新授权。刷新令牌每次使用后轮换，旧令牌重放会
撤销整次授权；主动退出也会立即使该授权的全部 access/refresh 失效。浏览器 Session
绝对期限 8 小时、闲置期限 30 分钟。OAuth access 只检查自己的绝对期限，不因闲置
30 分钟提前失效。用户禁用、来源禁用和 auth_version 变化仍即时生效。

授权码、设备码和 refresh token 在数据库中只保存摘要；授权码/设备码只能兑换一次。
请求记录保留至 5 分钟过期，access Session 保留至 1 小时到期，授权及 refresh 记录保留
至最长 30 天到期，由统一后台清理。刷新与撤销在同一授权行上 CAS 协调；不使用全局锁。

注册的公共客户端为 `krun-cli` 和 `krun-wrangler`。krun CLI 请求 `krun:control`，仍受
用户当前角色限制；Wrangler 请求的 scopes 只签发当前支持的 account/user/Workers
子集，不能访问 krun 的 IAM 管理入口，不授予未实现的 CF 存储或其他产品权限。
回调仅允许 `http://localhost:PORT/oauth/callback`、IPv4/IPv6 loopback 对应地址；不接受
外部回调、隐式 grant 或 plain PKCE。

CLI 使用：

```sh
krun login --endpoint https://console.example.com
krun login --device --browser=false --endpoint https://console.example.com
krun whoami
krun logout
```

默认启动本机 8976 端口接收回调，可用 `--callback-port 0` 选择空闲端口。SSH 等不便
接收本机回调的场景使用 `--device`。`--browser=false` / `--no-browser` 打印链接。
已有 API Key 仍可通过 `--token-stdin` 保存；不参与 OAuth 刷新。
CLI 配置文件权限为 0600，包含 access/refresh 和到期时间；请求前自动续期，认证返回
401 时最多刷新一次重试。切换 server 或使用显式 KRUN_TOKEN 时不会携带旧刷新凭据。

Session Cookie：生产 `__Host-krun_session`，Secure/HttpOnly/SameSite=Lax/Path=/、无 Domain；开发使用独立 `krun_dev_session`。Cookie 写操作要求固定 Origin 和 CSRF header，预登录 form 可用 csrf_token 字段。Bearer 不要求 CSRF，仍检查凭据、用户状态、auth_version 与启用来源。

生产前端是 OSS 上的静态文件，公开 origin 只把 `/api/*` 转发给 Go。`public_url`、SSO 回调与 CLI 浏览器确认 URL 都使用这个同源入口，详见 [部署说明](deployment.md)。

## Provider 配置

所有来源支持 `enabled: false`，省略时默认为启用。禁用后不展示登录入口，不接收登录 / 回调 / 邮箱验证码请求，已有该来源的 Session、CLI Session 与 API Key 也不能访问；不会删除用户、授权或凭据，重新启用后未过期且未撤销的凭据可继续使用。修改配置后重启服务生效。禁用来源仍需合法的 id / source_id / type，但不需要上游凭据或 SMTP；全局环境变量替换仍会校验 `${NAME}`，停用时可删掉不再需要的值或使用 `${NAME:-}`。

本地邮箱登录使用 `type: email`，见 [完整配置示例](../config/example.yaml)。与开发一键登录 `type: dev` 独立，支持生产使用；无需 OAuth 应用或 scopes。SMTP 配置位于根级 `smtp`：host、port、username、password、from、tls。`tls: starttls` 为默认值，默认端口 587；`tls: tls` 使用直接 TLS，默认端口 465。不跳过证书检查、不降级为明文；`tls: none` 仅允许开发环境的 loopback SMTP，供本地邮件模拟测试。username / password 可以同时省略，用于受信任的无密码邮件中继。

邮箱在去掉两端空白并转为小写后作为稳定 subject。默认验证邮箱后自动创建账号，不授予组织成员资格或角色；不会按同名邮箱合并其他 SSO 来源身份。若显式使用 invite_only，请以小写邮箱为 subject 提前邀请。禁用的用户无法登录。

验证码是 6 位随机数字，有效期 10 分钟，只能使用一次；每个邮箱 / 浏览器发送间隔至少 60 秒，IP 每分钟最多 10 封。连续输错 5 次后失效，新登录会使同浏览器旧验证码失效。验证码绑定来源、配置指纹、浏览器和登录代次；数据库保存验证码与 challenge 的摘要，API 和日志不返回验证码。邮件按请求语言提供中文或英文。持久化和计数使用 PostgreSQL 事务锁，支持副本间一次性消费与服务重启。发送失败返回 EMAIL_DELIVERY_FAILED；无效、过期或用过的验证码返回 EMAIL_CODE_INVALID。

本地开发、邮箱 / SMTP、GitHub、GitLab、阿里 OAuth2、OIDC 和自定义 OAuth2 的完整 YAML 都位于 [统一配置示例](../config/example.yaml)。按需启用来源；自定义 OAuth2 部分包含用户信息字段映射、POST / headers / query / body 与业务成功条件。

路径以点分隔对象字段；数组使用数字段，例如 `data.users.0.id`。subject 必须是非空字符串或整数，保持大整数原文，上限 512 bytes。不支持任意脚本、正则转换或远程用户指定 URL。配置可选 GET/POST、固定 headers/query/JSON body、业务 success 等值检查；默认 GET、Bearer token。Headers 禁止覆盖 Authorization/Cookie/Host；授权参数禁止覆盖 state/nonce/PKCE/client_id/scope/response_type/redirect_uri。token 支持 `client_secret_post` 与 `client_secret_basic`，PKCE 始终 S256，不自动降级。

身份源、Provider ID 和命名空间会持久登记、保留墓碑；已有 source 不能替换 subject 映射、实例、issuer 或客户端命名空间；重复登记命名空间失败。在途事务还绑定完整配置指纹。secret 环境变量内的值可轮换，不改变本地身份。

公司自定义 ticket / 签名 / 验票协议需取得正式协议再添加代码适配，不能假设它等于 OAuth2。当前未提供该适配。

## 管理 API

### 组织角色

组织提供三个固定角色，`GET /api/v1/roles` 同时返回独立的 `system_admin` 系统角色：

| 能力 | 组织管理员 `org_admin` | 成员 `member` | 只读 `viewer` |
| --- | --- | --- | --- |
| 查看资源、版本、源码与日志 | 是 | 是 | 是 |
| 创建、修改、删除 Worker 与模型元数据 | 是 | 是 | 否 |
| Workers 构建、创建版本、发布、回滚和调用 | 是 | 是 | 否 |
| 创建、启用与停用逻辑桶 | 是 | 是 | 否 |
| 浏览、下载存储对象 | 是 | 是 | 是 |
| 上传、覆盖、删除存储对象 | 是 | 是 | 否 |
| 管理组织设置、成员、用户组和角色授权 | 是 | 否 | 否 |
| 查看本组织审计 | 是 | 否 | 否 |
| 管理全平台用户、系统角色及全平台审计 | 否 | 否 | 否 |

`member` 同时具有开发与发布权限。组织授权只接受表中的三个角色，非法角色在授予或
更新时返回 `POLICY_INVALID`；未知角色不产生资源权限，不做启动回填或隐式角色映射。

`system_admin` 在全平台范围管理系统和组织；创建组织只会授予创建者该组织的
`org_admin`。手动添加成员与角色授权是独立接口；[组织链接邀请](org-invitation-links.md)
在明确接受时同时建立成员资格和 `member` / `viewer` 角色。
每个成员都可以管理自己的 API Key，实际访问始终取当前角色与 Key 策略的交集。

上表为整个组织、全部环境授权。`member` / `viewer` 也可限定到指定 Worker 或模型及环境；
`org_admin` 必须是整组织范围且不能附加环境条件。成员关系、账号状态、身份来源约束、
Key 范围和撤权检查逐请求生效。模型执行相关动作仍是预留目录，不代表存在执行接口。

### 接口目录

前缀 `/api/v1`。浏览器与浏览器批准的 CLI Session 都可以使用 S 接口；API Key 只可以使用 K 接口。

| 方法与路径 | 凭据 | 行为 |
| --- | --- | --- |
| GET /me、/roles | S K | 当前主体；公开固定角色和动作目录 |
| POST /authz/check | S K | 检查当前主体 action/resource_kind/resource_id/environment；不接受自定义 principal |
| GET /orgs | S K | 过滤未授权组织 |
| POST /orgs | S | 开启 organization_self_service 时所有已登录用户可创建组织，否则仅系统管理员；创建者获得该组织的组织管理员 |
| GET /orgs/:org | S K | organization.read |
| GET /orgs/:org/permissions | S K | 当前主体在该组织 global 环境下的实际权限，返回 organization_id/actions/revision；要求 organization.read，无权或不存在统一 404 |
| PATCH /orgs/:org | S | organization.update；name、allowed_sources |
| GET /system/users | S K | system.user.read |
| PATCH /system/users/:user | S | system.user.update；仅 active/disabled，增加 auth_version；最后管理员保护 |
| POST /system/invitations | S | system.user.update；准确 source_id/subject |
| GET /orgs/:org/members | S K | member.read |
| POST /orgs/:org/members | S | member.manage；准确本地 user_id |
| DELETE /orgs/:org/members/:user | S | member.manage；移除成员、组成员关系及本组织直接角色 |
| GET /orgs/:org/invitation-links | S | member.read；游标分页，不返回凭证 |
| POST /orgs/:org/invitation-links | S | member.manage + iam.grant；默认 member、1 人，可定向账号或已验证邮箱，固定 7 天有效；返回一次性链接 |
| DELETE /orgs/:org/invitation-links/:id | S | member.manage + iam.grant；幂等撤销 |
| POST /invitation-links/preview | 公开 | 请求 token，最小组织/角色/到期预览；限流、no-store |
| POST /invitation-links/accept | 仅浏览器 Session | CSRF、身份来源、定向对象和创建者权限校验；明确加入，重复接受不占名额 |
| GET /orgs/:org/groups | S K | iam.read |
| POST/PATCH/DELETE /orgs/:org/groups[/:group] | S | iam.grant/revoke |
| PUT/DELETE /orgs/:org/groups/:group/members/:user | S | iam.grant/revoke；必须是有效组织成员 |
| GET/POST/PUT/DELETE /orgs/:org/role-assignments[/:assignment] | GET S K，其余 S | iam.read/grant/revoke；组织与资源范围验证 |
| /system/groups、/system/role-assignments | GET S K，其余 S | 上述组/授权接口的系统范围；只允许 system_admin |
| POST /orgs/:org/api-keys、/system/api-keys | S | 当前用户所有；所有用户支持组织 / 跨工作空间 Key，始终受当前用户权限约束 |
| GET /orgs/:org/api-keys、/system/api-keys | S K | 逐 Key apiKey.read 后过滤 |
| GET/PATCH/DELETE /api-keys/:key | GET/DELETE S K，PATCH S | 读取、策略更新、撤销；更新验证选择器、动作兼容性与 Owner 状态，运行时取权限交集 |
| GET/POST /orgs/:org/workers、/models | S K | list/create，对列表逐资源 read 检查 |
| GET/PATCH/DELETE /orgs/:org/workers/:resource、/models/:resource | S K | read/update/delete；路径组织与实际归属一致 |
| GET /system/audit、/orgs/:org/audit | S K | audit.read，游标分页，仅最近 7 天；limit 默认 50、最大 500 |
| GET /system/audit/export、/orgs/:org/audit/export | S K | audit.read，下载当前范围的 CSV，默认脱敏 |

JSON DTO 拒绝未知字段和重复键，正文上限 1 MiB。资源/组/Key/Assignment 的更新需要 expected_revision，不匹配 409。创建返回稳定随机 ID；删除后不会复用。名单、组织成员和组写入读取最新事实，禁止跨组织组/角色；错误的用户或资源不会被隐式创建。

控制台依据组织权限接口显示菜单、概览快捷入口并限制页面访问；系统用户与全平台审计只在系统管理员的「平台管理」中显示。切换组织、完成修改或重新聚焦窗口时刷新权限，加载失败时关闭组织功能入口。页面权限仅用于界面显示，业务接口仍逐请求授权。

主配置的 `organization_self_service` 控制自助创建，示例配置显式开启；省略或设为 `false` 时仅系统管理员可创建。API Key 不允许创建组织。创建者不会获得系统权限，也不会获得其他组织的权限。

例：组织范围角色授予：

```json
{"subject_kind":"group","subject_id":"grp_xxx","role_id":"member","scope_kind":"organization","scope_id":"org_xxx","conditions":{}}
```

Key 创建：`{name, expires_in, policy}`。`expires_in` 必填，`0` 表示长期有效，返回 `expires_at: null`；正数为 60 秒至 90 天，有限期限到期后拒绝认证与策略更新。撤销、禁用用户、身份来源限制及实时角色检查同样适用于长期有效 Key。策略见 `config/key-policy.example.yaml`。只读资源、列表、版本、发布、删除分别授权，不存在 `write` 或隐式超级 Key。精确 Key 列表发现可显式加入组织选择器上的 worker.list / model.list；返回条目仍逐资源过滤。

所有用户可选择 `actions:["*"]`，按选择器兼容的动作目录展开。`resource.kind:"*"` 可配合 `all_in_organization:true` 与准确 `organization_id` 表示组织所有类型资源；`all_in_system:true` 仅用于跨工作空间 Key，允许所有用户创建自己的 Key，并不授予系统管理员权限。使用 worker/model 等具体 kind 时仍限制该类型。`kind:"system", all_in_system:true` 仅选择平台资源；全部资源类型统一使用 `kind:"*"`。

策略是访问上限，可以勾选用户当前尚未获授的操作；精确 ID 仍须存在且属于选择器指定组织。每次请求严格计算“用户当前权限 ∩ Key 策略”，deny 优先；角色、成员和身份来源变化立即生效。组织范围 Key 始终不能跨组织。普通用户只可列出、读取、修改或撤销自己的跨工作空间 Key；其他系统管理接口仍按各自权限检查。

```json
{"name":"personal-automation","expires_in":0,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["*"],"resource":{"kind":"*","all_in_system":true},"conditions":{}}]}}
```

前端“只读”选择 `.read` / `.list`；“读写”增加创建、更新、删除、管理、授权、撤销、配置及绑定操作。发布 / 回滚 / 调用 / 执行需要单独勾选，或选择“全部 API”。预留领域动作在界面注明，不代表执行接口已实现。

Workers 的构建、发布、回滚与调用接口见 [Workers 后端](workers-backend.md)。模型等未提供执行接口的资源只支持已注册的元数据操作；动作目录不代表执行能力。

## 审计分页与保留期

`GET /api/v1/system/audit?limit=50` 或 `GET /api/v1/orgs/:org/audit?limit=50`。`limit` 为 1–500 的整数；下一页传响应中的 `next_cursor`，例如 `?limit=50&cursor=...`。游标使用 Base64URL 编码的分页参数；所有页都重新检查当前用户与 Key 的 `audit.read` 权限，验证组织 / 系统范围和时间边界。游标本身不授予访问权限。

响应为 `{items,total,limit,has_more,cursor,next_cursor,as_of,retention_days}`；最后一页 `has_more:false, next_cursor:""`。按 time / id 倒序，首个请求固定 `as_of` 快照；翻页和返回之前的 `cursor` 不受新日志插入影响。刷新时不传 cursor 获取最新快照。`total` 统计该快照中仍处于保留期且属于请求范围的记录；若翻页期间记录跨过 7 天边界，过期记录仍会被删除。超过保留期的游标返回 409 `AUDIT_CURSOR_EXPIRED`，无效、篡改、跨范围游标及非法 limit 返回 400。

列表与 CSV 导出共用同一快照查询，最多包含范围内最近的 5,000 条记录。审计仅保留滚动最近 7 天。后台保留任务每分钟清理过期行，空闲时也生效；未提供长期归档。

在控制台审计页选择「导出 CSV」可下载当前组织或全平台的整个快照。导出默认启用「脱敏导出」，遮盖记录、主体、API Key、资源、组织和请求的标识，保留时间、凭据类型、动作、决策和 revision。取消勾选后可导出完整标识。

API 为 `GET /api/v1/system/audit/export` 或 `GET /api/v1/orgs/:org/audit/export`，返回带 UTF-8 BOM 的 `krun-audit.csv`。可传入列表响应的 `cursor` 复用其快照时间，CSV 不受该游标的页位置影响；不传则导出最新快照。`redact=true` 为默认值，显式传入 `redact=false` 可保留完整标识。导出与列表检查相同的 `audit.read` 权限。

CLI 同步支持 `krun audit --org ID --limit 100 --cursor CURSOR`；不指定 org 查询系统审计，仍要求系统审计权限。

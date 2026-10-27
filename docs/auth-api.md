# 认证、授权与 CLI 接口

所有错误为 `{error:{code,message},request_id}`。机器端返回 JSON，不自动转向第三方登录。认证与 API 响应 `Cache-Control: no-store`；回调错误只返回固定公共码，不回显上游正文。

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
| POST | /api/auth/cli/start | 公共接口，返回 5 分钟 device_code/user_code、浏览器确认 URL |
| POST | /api/auth/cli/approve | 仅浏览器 Session + CSRF；核对 user_code 后签发 1 小时 CLI Session |
| POST | /api/auth/cli/poll | 凭 256 bit device_code 领取一次 CLI Session；未批准 409，过期/重放 400 |

CLI Session audience 只限 krun Control API，凭据前缀 `krun_cli_`，作为短期人类会话使用；它不是 API Key，不能批准其他终端登录。浏览器 Session 的绝对期限 8 小时，CLI 1 小时，闲置期限均 30 分钟；无 refresh token。客户端必须明确核对终端显示的确认码。

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

前缀 `/api/v1`。浏览器与浏览器批准的 CLI Session 都可以使用 S 接口；API Key 只可以使用 K 接口。

| 方法与路径 | 凭据 | 行为 |
| --- | --- | --- |
| GET /me、/roles | S K | 当前主体；公开固定角色和动作目录 |
| POST /authz/check | S K | 检查当前主体 action/resource_kind/resource_id/environment；不接受自定义 principal |
| GET /orgs | S K | 过滤未授权组织 |
| POST /orgs | S | 系统管理员创建组织，创建者获得组织管理员 |
| GET /orgs/:org | S K | organization.read |
| PATCH /orgs/:org | S | organization.update；name、allowed_sources |
| GET /system/users | S K | system.user.read |
| PATCH /system/users/:user | S | system.user.update；仅 active/disabled，增加 auth_version；最后管理员保护 |
| POST /system/invitations | S | system.user.update；准确 source_id/subject |
| GET /orgs/:org/members | S K | member.read |
| POST /orgs/:org/members | S | member.manage；准确本地 user_id |
| DELETE /orgs/:org/members/:user | S | member.manage；移除成员、组成员关系及本组织直接角色 |
| GET /orgs/:org/groups | S K | iam.read |
| POST/PATCH/DELETE /orgs/:org/groups[/:group] | S | iam.grant/revoke |
| PUT/DELETE /orgs/:org/groups/:group/members/:user | S | iam.grant/revoke；必须是有效组织成员 |
| GET/POST/PUT/DELETE /orgs/:org/role-assignments[/:assignment] | GET S K，其余 S | iam.read/grant/revoke；组织与资源范围验证 |
| /system/groups、/system/role-assignments | GET S K，其余 S | 上述组/授权接口的系统范围；只允许 system_admin |
| POST /orgs/:org/api-keys、/system/api-keys | S | 当前用户所有；所有用户支持组织 / 跨工作空间 Key，始终受当前用户权限约束 |
| GET /orgs/:org/api-keys、/system/api-keys | S K | 逐 Key apiKey.read 后过滤 |
| GET/PATCH/DELETE /api-keys/:key | GET/DELETE S K，PATCH S | 读取、策略更新、撤销；更新验证选择器、动作兼容性与 Owner 状态，运行时取权限交集 |
| GET/POST /orgs/:org/services、/models | S K | list/create，对列表逐资源 read 检查 |
| GET/PATCH/DELETE /orgs/:org/services/:resource、/models/:resource | S K | read/update/delete；路径组织与实际归属一致 |
| GET /system/audit、/orgs/:org/audit | S K | audit.read，游标分页，仅最近 7 天；limit 默认 50、最大 500 |

JSON DTO 拒绝未知字段和重复键，正文上限 1 MiB。资源/组/Key/Assignment 的更新需要 expected_revision，不匹配 409。创建返回稳定随机 ID；删除后不会复用。名单、组织成员和组写入读取最新事实，禁止跨组织组/角色；错误的用户或资源不会被隐式创建。

例：组织范围角色授予：

```json
{"subject_kind":"group","subject_id":"grp_xxx","role_id":"developer","scope_kind":"organization","scope_id":"org_xxx","conditions":{}}
```

Key 创建：`{name, expires_in, policy}`。`expires_in` 必填，`0` 表示长期有效，返回 `expires_at: null`；正数为 60 秒至 90 天，有限期限到期后拒绝认证与策略更新。撤销、禁用用户、身份来源限制及实时角色检查同样适用于长期有效 Key。策略见 `config/key-policy.example.yaml`。只读资源、列表、版本、发布、删除分别授权，不存在 `write` 或隐式超级 Key。精确 Key 列表发现可显式加入组织选择器上的 service.list / model.list；返回条目仍逐资源过滤。

所有用户可选择 `actions:["*"]`，按选择器兼容的动作目录展开。`resource.kind:"*"` 可配合 `all_in_organization:true` 与准确 `organization_id` 表示组织所有类型资源；`all_in_system:true` 仅用于跨工作空间 Key，允许所有用户创建自己的 Key，并不授予系统管理员权限。使用 service/model 等具体 kind 时仍限制该类型。为兼容初版，`kind:"system", all_in_system:true` 仍表示全部类型范围；新全类型策略推荐 `kind:"*"`。

策略是访问上限，可以勾选用户当前尚未获授的操作；精确 ID 仍须存在且属于选择器指定组织。每次请求严格计算“用户当前权限 ∩ Key 策略”，deny 优先；角色、成员和身份来源变化立即生效。组织范围 Key 始终不能跨组织。普通用户只可列出、读取、修改或撤销自己的跨工作空间 Key；其他系统管理接口仍按各自权限检查。

```json
{"name":"personal-automation","expires_in":0,"policy":{"schema_version":1,"statements":[{"effect":"allow","actions":["*"],"resource":{"kind":"*","all_in_system":true},"conditions":{}}]}}
```

前端“只读”选择 `.read` / `.list`；“读写”增加创建、更新、删除、管理、授权、撤销、配置及绑定操作。发布 / 回滚 / 调用 / 执行需要单独勾选，或选择“全部 API”。预留领域动作在界面注明，不代表执行接口已实现。

未实现版本上传/构建/发布/回滚执行 API；相应动作在固定注册表中为后续领域门面保留，权限检查可以验证这些动作，但不会执行工作负载。

## 审计分页与保留期

`GET /api/v1/system/audit?limit=50` 或 `GET /api/v1/orgs/:org/audit?limit=50`。`limit` 为 1–500 的整数；下一页传响应中的 `next_cursor`，例如 `?limit=50&cursor=...`。游标使用 Base64URL 编码的分页参数；所有页都重新检查当前用户与 Key 的 `audit.read` 权限，验证组织 / 系统范围和时间边界。游标本身不授予访问权限。

响应为 `{items,total,limit,has_more,cursor,next_cursor,as_of,retention_days}`；最后一页 `has_more:false, next_cursor:""`。按 time / id 倒序，首个请求固定 `as_of` 快照；翻页和返回之前的 `cursor` 不受新日志插入影响。刷新时不传 cursor 获取最新快照。`total` 统计该快照中仍处于保留期且属于请求范围的记录；若翻页期间记录跨过 7 天边界，过期记录仍会被删除。超过保留期的游标返回 409 `AUDIT_CURSOR_EXPIRED`，无效、篡改、跨范围游标及非法 limit 返回 400。

审计仅保留滚动最近 7 天。后台保留任务每分钟清理过期行，空闲时也生效；未提供长期归档。

CLI 同步支持 `krun audit --org ID --limit 100 --cursor CURSOR`；不指定 org 查询系统审计，仍要求系统审计权限。

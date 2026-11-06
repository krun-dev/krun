# 组织链接邀请

管理员在「组织成员 → 邀请成员」生成链接，同事登录后明确确认加入，同时获得组织成员资格和指定角色。打开链接和登录均不会自动加入。

## 创建与管理

默认角色为成员 `member`，可选只读 `viewer`，作用于整个组织和全部环境；不提供管理员角色、用户组或资源级授权。权限见[组织角色](auth-api.md#组织角色)。

链接固定 7 天有效，默认最多加入 **1 个新成员**。「更多设置」支持：

| 设置 | 行为 |
| --- | --- |
| 最多加入人数 | 整数 1–100；仅成功加入的新成员占名额，重复点击和已有成员不计数 |
| 任何持有链接的人 | 默认；符合组织身份来源限制的账号可接受 |
| 指定用户 ID | 仅准确的现有活跃用户 ID 可接受，不使用显示名 |
| 指定已验证邮箱 | 仅当前账号已验证邮箱匹配时可接受，忽略邮箱大小写；不能同时指定用户 ID |

邮箱验证码登录会验证邮箱。OIDC 仅信任通过签名、issuer、audience、nonce 和有效期校验的 ID Token 中 `email_verified=true` 的邮箱。普通 OAuth 用户资料、开发登录或自定义 profile 的邮箱不视为已验证。相同邮箱不会合并不同来源账号；若多个身份均持有同一已验证邮箱且来源被允许，它们各自可以消耗一个名额。只邀请一个准确账号时使用用户 ID。

完整链接仅在创建响应和弹窗中展示一次，关闭后无法恢复；列表不返回 token 或摘要。需要再次分享时重新生成，新链接不自动撤销其他链接。每组织最多 10 条有效且未满额的链接。

「成员 / 邀请链接」页签支持列表、游标翻页和确认撤销。列表显示角色、创建时间、邀请对象、已加入人数/上限、到期时间和状态（有效、已满额、已过期、已撤销）。链接生成后不能修改角色、对象、人数或有效期。撤销只阻止后续加入，不影响已有成员和角色；移除成员不退还名额。

## 接受流程

链接为 `/?page=join#invite=<opaque-token>`，origin 来自主配置 `public_url`。

1. 页面将 token 存到当前标签页的 `sessionStorage`，然后清除 fragment。公开预览只显示组织名、邀请角色、到期时间和是否定向；不显示指定邮箱、账号或创建人。
2. 未登录时点击「登录后加入」，使用现有登录入口。`return_to` 仅为 `/?page=join`，token 不进入登录 URL、SSO state 或回调；同一标签页登录后恢复邀请。
3. 登录后展示当前账号与来源，点击「加入组织」时重新校验邀请、来源与定向对象。
4. 成功后清除临时 token，显示结果；点击「进入组织」刷新控制台并选择该组织。可以先切换账号再接受。

| 情况 | 结果 |
| --- | --- |
| 不存在、已过期、已撤销、满额，或创建者已无权邀请 | 410 `INVITATION_UNAVAILABLE`；提示联系管理员获取新链接 |
| 身份来源不被组织允许 | 403 `INVITATION_SOURCE_DENIED`，可以切换账号 |
| 指定用户不匹配，或邮箱不匹配/未经验证 | 403 `INVITATION_RECIPIENT_MISMATCH`，不消耗名额 |
| 已是成员 | 返回 `already_member`，不新增或替换角色、不消耗名额；有组织读取权限才允许进入，否则联系管理员授权 |
| 曾用同一链接加入，之后被移除 | 409 `INVITATION_ALREADY_USED`，需新邀请 |
| 网络失败 | 可重试；未确认成功时不清除凭证 |
| 浏览器阻止当前标签页存储 | 明确提示，不继续登录或丢弃原始链接 |

已有成员也必须通过有效期、撤销、来源、定向对象及创建者权限校验；仅人数满额不影响已有成员重复接受的幂等结果。匿名预览满额链接仍返回 410。链接不绕过 provider 注册策略：`invite_only` 下新身份仍需系统注册邀请。现有 `POST /api/v1/system/invitations` 按准确 source/subject 开放注册，与组织链接邀请职责独立。

## API

全部接口位于同源 `/api/*`；控制台是根路径静态 SPA，不需要前端运行期服务或新增根路由。

| 方法与路径 | 凭据和行为 |
| --- | --- |
| `GET /api/v1/orgs/{org}/invitation-links` | Session，`member.read`；`limit` 默认 20、最大 100，`cursor` 使用上页 `next_cursor`；返回 `{items,next_cursor}` |
| `POST /api/v1/orgs/{org}/invitation-links` | Session，`member.manage` + `iam.grant`；返回 `{invitation,invite_url}`，201 |
| `DELETE /api/v1/orgs/{org}/invitation-links/{id}` | Session，同创建权限；幂等撤销，204；跨组织 ID 为 404 |
| `POST /api/v1/invitation-links/preview` | 公开只读，请求 `{token}`；最小预览，不写业务数据，沿用认证入口有界限流 |
| `POST /api/v1/invitation-links/accept` | 仅浏览器 Session + CSRF，请求 `{token}`；返回 `{organization_id,status,can_enter}`，status 为 `joined` 或 `already_member` |

管理接口支持浏览器和浏览器批准的 CLI Session，均拒绝 API Key；接受接口同时拒绝 CLI Session。写请求遵循既有同源与 CSRF 校验。

创建参数：

```json
{
  "role_id": "member",
  "max_uses": 1,
  "target_email": "teammate@example.com"
}
```

所有字段可省略；`target_user_id` 与 `target_email` 互斥。未知字段和重复键被拒绝。非法参数返回 422 `INVALID_REQUEST`；有效链接数量超限返回 409 `INVITATION_LIMIT_REACHED`；并发修改有界重试后仍冲突时返回 409 `REVISION_CONFLICT`。

元数据字段为 `id`、`organization_id`、`creator_user_id`、`role_id`、可选 `target_user_id`/`target_email`、`max_uses`、`use_count`、`created_at`、`expires_at`、可选 `revoked_at` 和 `status`。所有邀请响应均为 `Cache-Control: no-store`。

## 凭证与一致性

token 使用 `internal/securetoken` 生成 256 bit 随机值，数据库只保存 SHA-256 摘要。明文仅存在于创建响应、链接 fragment、当前标签页临时存储与预览/接受请求体，不写入 API 路径、登录 URL、审计、错误消息或 `localStorage`。

接受时重新验证创建者账号活跃、来源启用且符合组织限制，并仍具备目标组织的 `member.manage` 和 `iam.grant`。受邀者也必须账号活跃且来源允许。

| 表 | 内容 |
| --- | --- |
| `krun_org_invitation_links` | 一链接一行；角色、定向对象、唯一 token 摘要、人数上限/计数、生命周期、revision；角色 CHECK 仅 member/viewer，人数 CHECK 1–100，定向对象互斥 |
| `krun_org_invitation_redemptions` | 一次成功加入一行；主键 `(invitation_id,user_id)`，加入时间；无额外身份资料或 token |
| `krun_users.email_verified` | 当前登录所确认的邮箱验证标记；默认 false |

创建以组织 revision CAS 协调 10 条有效链接上限。接受用有界小事务，以邀请 revision、未撤销、未过期、未满额为条件认领一个名额；校验当前账号，插入 redemption、成员和直接角色授权，同时写成功审计。任何一步失败全部回滚。不使用外键、全局锁或 `SELECT FOR UPDATE`。

同账号重复请求只占一个名额；不同链接竞争同一组织成员时，由唯一成员键决定谁建立资格，另一方回滚名额和授权后返回已有成员。撤销与接受竞争由邀请条件更新确定先后，撤销提交后不能新加入。移除成员保留 redemption，防止复用旧链接。

邀请不进入全局 `State` 或快照。HTTP 层负责会话、CSRF、解码、授权及审计装配；纯业务校验位于 `internal/iam`；SQL、CAS、事务和清理位于 `internal/storage`。预览和接受仅加载当前组织与相关账号的权限事实。

## 保留与复现

链接在 `expires_at + 7 天` 后清理，redemption 随链接一并删除；删除组织也显式删除关联行。后台每分钟运行 retention，每批最多 1000 条，同时清理孤儿 redemption。创建、撤销、成功接受的审计沿用 7 天保留，只记录邀请 ID、组织和操作者。

完整建表定义与生成模型已更新。沿用 schema fingerprint，不提供自动迁移、回填或数据库重建；旧开发/测试库需要开发者明确重建专用可丢弃实例。

验证入口见[自动化验证](verification.md)。`make test` 包含业务/HTTP、真实 PostgreSQL 并发、事务回滚、数量限制、分页与清理测试。`bash scripts/test-object-browser.sh` 覆盖真实静态 SPA、同源 Go API、定向创建、登录回跳、明确接受、已有成员、撤销和名额竞争。

# 验证说明

项目使用 Go 自动化测试、前端权限与国际化测试，以及浏览器联调验证认证和授权流程。测试数据使用独立 PostgreSQL 数据库，不使用生产数据。

Workers 后端另有独立 kind 端到端验收，覆盖真实构建 Job、S3 制品、workerd 加载、发布与回滚。运行入口见 [kind E2E](../deploy/workers-kind/README.md)，规模验证见 [性能验证](performance-scaling.md)。

## 当前基线验收

2026-11-03 在专用 PostgreSQL 数据库、独立 kind 对象桶及私有 S3/TOS 上验证：

- `make test` 全部通过，包含真实 PostgreSQL、Go race/vet、前端权限和国际化、builder、真实 workerd / Wrangler 制品 smoke。
- 完整 kind E2E 35/35，通过真实构建、发布、回滚、重启恢复、网络与权限隔离、共享路由、shard checkpoint、资产 GC、未部署 Version 与固定部署地址。
- Playwright UI 15/15：控制台 10 项覆盖桌面与移动端、106 站点分页、页外深链接、仓库构建、发布调用、回滚、JSON 变量与版本元数据；OAuth 5 项覆盖 CLI、设备授权、刷新、撤销与官方 Wrangler 登录。浏览器 JavaScript 异常检查通过。
- 官方 Wrangler 兼容 E2E 29/29，覆盖资产、规则文件、`--keep-vars`、Preview、Secret、版本与回滚。
- 私有存储 E2E 5/5，覆盖上传、本地构建、发布、ASSETS、静态域、浏览器调用与业务 Worker 的 HTTP/TCP 出站隔离；控制面暂停后替换 Runtime Pod，私有 TOS 冷启动通过。
- MinIO 的 path / virtual / auto 寻址实测通过，SDK 预签名上传、下载及 Runtime SigV4 读取一致；AWS 寻址和签名单测通过，未使用真实 AWS 账号联调。
- Runtime 10 万路由增量更新、并发回源合并和有界缓存回归通过。
- 静态产物、密钥检查及文档链接检查通过。

基线校验覆盖空库初始化、重复启动，以及 fingerprint 不匹配或缺失时保留原数据并拒绝启动。
旧 manifest schema 被拒绝；平台 API Key 的 `kind:"system"` 只匹配平台资源，不扩展为其他类型。
专用测试栈结束后恢复 local-first 拓扑；现有开发数据不做隐式迁移。

## 运行检查

在仓库根目录运行：

```sh
go test -race ./...
go vet ./...
npm run check
npm run test:permissions
npm run test:i18n
npm run build
```

通过 `KRUN_TEST_POSTGRES_URL` 提供专用 PostgreSQL 测试数据库后，Go 测试会执行数据库集成用例。测试会修改该数据库，请仅使用可清空的测试实例。未提供连接串时，数据库集成测试跳过；CI 配置了专用 PostgreSQL service。

## 已覆盖的行为

| 范围 | 验证内容 |
| --- | --- |
| 认证 | 开发登录、OAuth2 授权码与 PKCE、OIDC 签名及 issuer / audience / nonce / expiry 校验、Session 与 CSRF、受信任跳转 |
| 来源隔离 | 身份来源命名空间不可替换；相同邮箱不自动合并不同来源身份；禁用来源后现有凭据失效 |
| 邮箱登录 | SMTP 协议、中文与英文邮件、验证码过期、错误次数、单次消费、浏览器绑定、发送限流、数据库重启与并发消费 |
| 授权 | 组织成员与角色、用户组、来源白名单、Cedar allow / deny、权限撤销及时生效 |
| API Key | 用户权限与 Key 范围取交集、普通用户通配符、跨组织隔离、永久有效与到期 / 撤销、可视化权限选择 |
| CLI | 浏览器确认、确认码与凭据隔离、组织及资源操作、英文输出、YAML profile / 输入 |
| 审计 | 最近 7 天保留、后台清理、稳定游标分页、排序、每页权限检查、非法与跨范围游标拒绝 |
| 配置 | YAML 严格解析、未知字段与重复键拒绝、字符串环境变量引用、禁用来源校验、统一示例加载 |
| 存储 | PostgreSQL 持久化、并发 bootstrap、事务回滚、授权与审计同事务提交 |
| 国际化 | 前端中文默认与英文切换、语言持久化、翻译键及占位符完整性、后端错误语言 |
| 部署 | 合并静态部署、API-only 路由、独立静态前端通过代理完成登录 / 读取身份 / 退出 |

浏览器联调已覆盖开发登录、CLI 授权、组织及资源操作、API Key 权限选择、审计分页、语言切换，以及 GitHub、GitLab 和 IDaaS OAuth2 登录。真实应用凭据、账号信息、联调日志和截图保存在忽略提交的本地目录中。

## 验证边界

OIDC 使用受控服务与真实签名 JWT 测试，接入实际身份提供商仍需验证其应用设置与端点。邮箱登录通过隔离 SMTP 服务验收，真实邮件投递需配置可用 SMTP 并联调。PostgreSQL 使用关系行存储；百万站点元数据验证和配置应用微基准见 [性能改造与验证](performance-scaling.md)，它们不代表生产高并发容量结论。

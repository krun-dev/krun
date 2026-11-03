# 控制面代码架构

控制面的 HTTP、业务、基础设施调用和数据库实现按 Go 包隔开；实体类型在领域包中共享。

| 模块 | 职责 |
| --- | --- |
| `internal/control` | HTTP 路由、请求解析、Cookie、CSRF、入口权限门禁、响应格式和状态码、监听器生命周期与依赖装配 |
| `internal/auth` | 身份源注册、会话认证、登录提交、验证码、CLI 设备授权、CSRF 凭据和退出流程 |
| `internal/iam` | 组织、成员、用户组、角色分配、资源目录、API Key、邀请和初始化 |
| `internal/authorization` | Cedar 编译和评估、用户与 Key 权限交集、策略校验、目标解析和只读请求缓存 |
| `internal/workers` | 服务、资源池放置、构建、环境、发布、回滚、密钥、制品生命周期、配置投影和后台收敛 |
| `internal/repository` | 仓库配置、用户连接、OAuth 状态消费、凭据刷新租约与提交 |
| `internal/runtimeconfig` | Runtime 增量 feed、制品和资产描述、密钥交付、路由查询与 ACK 校验 |
| `internal/audit` | 审计记录、范围过滤、稳定游标和分页 |
| `internal/artifact` | 制品 manifest、归档、源码路径、资产和 Wrangler 配置校验 |
| `internal/config` | 唯一应用配置的加载、默认值、校验和部署目录解析 |
| `internal/adapters/*` | 身份提供商、仓库 OAuth、Git、Kubernetes、S3、本地 builder、邮件、Runtime 管理与控制台调用 |
| `internal/storage` | Bun 行模型与常规 CRUD、专用 SQL、连接池、事务、CAS、租约、保留和清理 |
| `internal/domain` | 共享实体、请求工作副本、待提交命令与不可变发布数据 |
| `internal/securetoken` | 随机凭据、ID 和 SHA-256 摘要 |

## 一次业务请求

1. HTTP 入口验证凭据、来源、Cookie 和 CSRF，通过存储层加载本次请求需要的工作副本。
2. handler 保持原有校验顺序，解析资源范围及类型化输入。IAM 和 Workers 的状态修改交给业务服务。
3. 业务服务校验 revision、成员关系、发布状态和幂等键，修改工作副本；权限门禁与审计通过明确接口接入。
4. 回调成功后，存储层只持久化差异并执行数据库 CAS；失败时丢弃业务改动，按原有规则保留拒绝审计。IAM 与 Workers 不共享写事务。
5. HTTP 层把业务结果映射为已有 API 响应和状态码。Workers 的 `Outcome` 不依赖 HTTP。

登录提交、CLI 初始化及邀请同样使用业务模块。仓库 OAuth、Git 获取、本地 builder 和 Runtime 管理调用由适配器执行；凭据刷新与构建任务的数据库认领、网络调用和条件提交分开执行，不把网络等待放进数据库事务。

`authorization.ForReadSnapshot` 的缓存只绑定单次只读请求的工作副本及 revision，不进入 `State`、数据库或 JSON。写请求实时计算权限。

## 文件定位

- Workers 按服务、构建、环境、发布、授权、制品、配置分发及清理分文件；后台构建和发布分别位于 `build_controller.go`、`deployment_controller.go`。
- 基础设施实现按系统放在 `internal/adapters/`；Kubernetes 的连接、构建 Job、Pod 发现和 HTTPRoute 各有文件。S3 端口允许注入客户端，SDK 连接构造只在适配器内。
- 身份提供商、制品和本地 builder 的纯模块测试与模块放在一起；HTTP 集成测试保留在 `internal/control`。
- 表结构及查询定位见 [存储说明](storage.md) 和 [storage 包目录](../internal/storage/README.md)。

## 自动化约束

`internal/storage/architecture_test.go` 递归检查全部模块：SQL、数据库驱动和 ORM 仅属于存储层；业务包不依赖 HTTP 或外部客户端 SDK；下层不能反向导入 HTTP 包。检查还禁止 HTTP handler 直接修改实体和 revision。

Bun 模型从 DDL 生成，ORM 和驱动只存在于存储层。业务对象不携带 ORM 标签，模型不负责自动建表或关联保存。

数据库 schema 仍只有 `internal/storage/schema.sql` 一份，不通过拆包增加迁移、回填、外键或自动级联。前端保持静态 SPA，所有请求仍使用同源 `/api/*`。

# 存储层

HTTP 与业务入口调用本包，所有层共享 `domain`。本包不依赖 HTTP、控制器、部署配置或对象存储客户端。业务代码只调用有明确用途的存储方法；连接池、SQL、行映射与事务留在包内。

| 查找内容 | 入口 |
| --- | --- |
| 完整表结构、索引与触发器 | `schema.sql` |
| 初始化及 schema fingerprint | `schema.go` |
| 连接池、工作副本与分区提交 | `store.go` |
| IAM 读取 / 事务编排 | `iam_read.go` / `iam.go` |
| IAM 按实体的 ORM 写入 | `iam_users.go` / `iam_organizations.go` / `iam_groups.go` / `iam_permissions.go` / `iam_keys.go` / `iam_identity.go` |
| 数据库行模型 / 生成入口 | `models/*_gen.go` / `models/generate.go` |
| ORM 连接、事务及 CAS | `orm.go` |
| 会话 / 仓库的 ORM 映射与查询 | `orm_rows.go` / `orm_repository.go` |
| Session、Browser、API Key 摘要查找 | `auth_session.go` |
| OAuth 登录与邮件挑战 | `auth_login.go` |
| CLI 设备授权 | `auth_device.go` |
| 仓库 OAuth 凭据与刷新租约 | `repository_connections.go` |
| Workers 行读取 / 差异写入 | `workers_read.go` / `workers.go` |
| Cloudflare Script 名称映射 / 已打包模块入库 | `scripts.go` / `uploaded_build.go` |
| 构建认领、制品提交 | `worker_builds.go` |
| 发布操作的状态迁移 | `worker_operations.go` |
| 有界工作队列与专用行解码 | `worker_queue.go` |
| 密文与环境 / Version 绑定 | `secrets.go` |
| 来源上传与资产对象清单 | `source_upload.go` / `asset_inventory.go` |
| 配置投影的数据库读取 | `config_projection.go` |
| 配置 revision 事务与变更写入 | `config_delta.go` / `config_write.go` |
| Runtime 配置读取、checkpoint、增量 feed 与 ACK | `config_checkpoint.go` / `config_feed.go` / `runtime.go` |
| 审计与限流 | `audit.go` / `rate_limit.go` |
| 保留期与清理 | `policy.go` / `retention.go` / `retention_scoped.go` |

共享实体定义在 `internal/domain`，数据库行模型在 `models`。改表时修改 `schema.sql`，运行 `go generate ./internal/storage/models`，再检查对应 domain 类型、读写方法和数据库测试。`State` 是请求工作副本，数据库仍按一实体一行保存。

跨表不变量由存储方法持有事务：调用方不能传入连接或自行拼事务。IAM 与 Workers 分区独立提交；HTTP、OAuth、Kubernetes 和对象存储调用不进入数据库事务。CAS、dirty generation 条件确认、保留期和幂等清理也由本层实现。

`Open` 必须连接 PostgreSQL。`NewMemory` 是显式创建的测试后端，复用相同的数据访问契约，不会作为数据库失败时的降级路径。`config_memory.go` 仅为该后端保存合成快照；生产配置仍走 normalized delta。

验证：

```sh
go test ./internal/domain ./internal/storage
KRUN_TEST_POSTGRES_URL="$TEST_DATABASE_URL" go test -race ./...
go vet ./...
```

数据库测试需要专用、可写的测试数据库；存储包为每个数据库用例创建并清理独立 schema。`architecture_test.go` 阻止 SQL/驱动进入业务层以及下层导入控制器或 HTTP/对象存储依赖。

Bun 负责常规实体映射和读写，专用 pgx 批量/CAS 语句保留。ORM 复用原连接池；业务层接触不到 Bun、`database/sql`、pgx 或连接。没有 ORM 建表、迁移、关联保存或级联。模型用法参考 [Bun models](https://bun.uptrace.dev/guide/models.html) 和 [transactions](https://bun.uptrace.dev/guide/transactions.html)。
